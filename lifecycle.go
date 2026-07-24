package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// errLockHeld is returned by acquireInstanceLock when another process already
// holds the instance lock.
var errLockHeld = errors.New("herdr-whistle instance lock already held")

// canonicalStateDir returns the machine-global state directory for herdr-whistle
// (creating it if needed), independent of how the process was launched. The
// single-instance lock must resolve to the same path whether the bot is started
// by a herdr event hook, the plugin action, or a manual shell invocation, so the
// path is derived from the OS user config dir rather than herdr's per-invocation
// HERDR_PLUGIN_STATE_DIR (which differs between herdr-injected and manual runs).
func canonicalStateDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolving user config dir: %w", err)
	}
	dir := filepath.Join(base, "herdr-whistle")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating state dir %s: %w", dir, err)
	}
	return dir, nil
}

// acquireInstanceLock opens instance.lock in dir and takes a non-blocking
// exclusive flock. The returned file must stay open for the process lifetime;
// closing it (or the process exiting for any reason) releases the lock. If
// another process holds the lock it returns errLockHeld.
func acquireInstanceLock(dir string) (*os.File, error) {
	p := filepath.Join(dir, "instance.lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock %s: %w", p, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errLockHeld
		}
		return nil, fmt.Errorf("locking %s: %w", p, err)
	}
	return f, nil
}

// instanceLocked reports whether the instance lock is currently held. It
// acquires-and-releases, so a false result has a TOCTOU window; runStart
// tolerates this because the spawned "run" worker re-checks the lock
// authoritatively before polling Telegram.
func instanceLocked(dir string) (bool, error) {
	f, err := acquireInstanceLock(dir)
	if err == nil {
		// Acquired momentarily; release and report free.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return false, nil
	}
	if errors.Is(err, errLockHeld) {
		return true, nil
	}
	return false, err
}

// runStart implements the "start" subcommand: if no instance holds the lock, it
// spawns a detached "run" worker (new session, stdio to bot.log) and waits for
// its readiness signal. If an instance is already running it is a no-op.
func runStart() error {
	dir, err := canonicalStateDir()
	if err != nil {
		return err
	}
	held, err := instanceLocked(dir)
	if err != nil {
		return err
	}
	if held {
		log.Printf("another herdr-whistle instance is running; not starting another")
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating executable: %w", err)
	}
	logPath := filepath.Join(dir, "bot.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening log %s: %w", logPath, err)
	}

	// Detached worker. Setsid puts it in a new session so it survives the
	// short-lived start process (and herdr's command lifecycle). Env is inherited
	// so HERDR_PLUGIN_CONFIG_DIR / HERDR_BIN_PATH reach the bot.
	cmd := exec.Command(exe, "run")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		_ = logFile.Close()
		return fmt.Errorf("creating readiness pipe: %w", err)
	}
	defer readyReader.Close()
	cmd.ExtraFiles = []*os.File{readyWriter}
	cmd.Env = append(os.Environ(), "HERDR_WHISTLE_READY_FD=3")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = readyWriter.Close()
		_ = logFile.Close()
		return fmt.Errorf("starting worker: %w", err)
	}
	pid := cmd.Process.Pid
	_ = readyWriter.Close()
	_ = logFile.Close()
	if err := readyReader.SetReadDeadline(time.Now().Add(40 * time.Second)); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("setting worker readiness deadline: %w", err)
	}
	var ready [1]byte
	if _, err := readyReader.Read(ready[:]); err != nil || ready[0] != 1 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("worker failed before becoming ready; inspect %s", logPath)
	}
	// Release the handle and intentionally do not Wait. When this process exits
	// the worker is reparented to init and will not become a zombie.
	_ = cmd.Process.Release()
	log.Printf("started herdr-whistle worker (pid %d); log: %s", pid, logPath)
	return nil
}

// runStop implements the "stop" subcommand: SIGTERM the running instance via its
// pidfile. The bot's signal handler cancels its context and shuts down cleanly.
// Idempotent: a missing or stale pidfile is logged and treated as "not running".
func runStop() error {
	dir, err := canonicalStateDir()
	if err != nil {
		return err
	}
	pid, err := readPidFile(dir)
	if err != nil {
		log.Printf("herdr-whistle not running (no pidfile: %v)", err)
		return nil
	}
	if !processAlive(pid) {
		_ = os.Remove(filepath.Join(dir, "bot.pid"))
		log.Printf("herdr-whistle not running (removed stale pidfile for pid %d)", pid)
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signalling pid %d: %w", pid, err)
	}
	log.Printf("sent SIGTERM to herdr-whistle (pid %d)", pid)
	return nil
}

// writePidFile records the bot's pid so the stop subcommand can signal it.
func writePidFile(dir string, pid int) error {
	return os.WriteFile(filepath.Join(dir, "bot.pid"), []byte(strconv.Itoa(pid)), 0o600)
}

// readPidFile reads the pid previously written by writePidFile.
func readPidFile(dir string) (int, error) {
	b, err := os.ReadFile(filepath.Join(dir, "bot.pid"))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("parsing pid %q: %w", strings.TrimSpace(string(b)), err)
	}
	return pid, nil
}

// processAlive reports whether pid names a living process via a signal-0 probe:
// nil = exists, ESRCH = gone, anything else (e.g. EPERM) = exists but not
// signalable, treated as alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return true
	case errors.Is(err, syscall.ESRCH):
		return false
	default:
		return true
	}
}
