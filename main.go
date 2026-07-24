package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	// Subcommand dispatch:
	//   herdr-whistle start -> probe the single-instance lock, detach a worker, return
	//   herdr-whistle stop  -> SIGTERM the running instance via its pidfile
	//   herdr-whistle run   -> run the bot in the foreground (also the default)
	//
	// herdr's manifest invokes "start" from the autostart action and the
	// workspace event hooks; "run" is the long-lived Telegram bot. A bare
	// invocation (manual/debug) defaults to "run" so it stays in the foreground.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "start":
			if err := runStart(); err != nil {
				log.Fatalf("start: %v", err)
			}
			return
		case "stop":
			if err := runStop(); err != nil {
				log.Fatalf("stop: %v", err)
			}
			return
		case "run":
			// fall through to the foreground run path below
		default:
			log.Fatalf("unknown subcommand %q (expected start|stop|run)", os.Args[1])
		}
	}

	if err := runBot(); err != nil {
		log.Fatalf("%v", err)
	}
}

// runBot loads config, takes the single-instance lock, writes the pidfile, and
// blocks on the Telegram bot until SIGINT/SIGTERM.
func runBot() error {
	// Determine config path from environment or use default.
	configPath := "./config.toml"
	if envDir := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); envDir != "" {
		configPath = filepath.Join(envDir, "config.toml")
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config from %s: %w", configPath, err)
	}
	if cfg.Token == "" {
		return errors.New("bot token is empty in configuration")
	}
	if cfg.OwnerID <= 0 {
		return errors.New("owner_id must be a positive Telegram user ID")
	}
	if cfg.ChatID <= 0 {
		return errors.New("chat_id must be a positive private-chat ID")
	}
	if cfg.ChatID != cfg.OwnerID {
		return errors.New("chat_id must be the owner's private Telegram chat ID (the same value as owner_id)")
	}

	// Single-instance: refuse to poll Telegram if another instance holds the
	// canonical lock. Telegram allows only one getUpdates poller per token, so a
	// second instance would cause 409 conflicts.
	stateDir, err := canonicalStateDir()
	if err != nil {
		return fmt.Errorf("resolving state dir: %w", err)
	}
	if err := initUXState(stateDir); err != nil {
		return fmt.Errorf("loading UX state: %w", err)
	}
	lock, err := acquireInstanceLock(stateDir)
	if err != nil {
		if errors.Is(err, errLockHeld) {
			log.Printf("another herdr-whistle instance is running (lock %s); exiting", filepath.Join(stateDir, "instance.lock"))
			// A concurrent start parent is waiting on the inherited readiness
			// descriptor. The existing lock holder is authoritative and healthy
			// enough for this start request to succeed.
			signalParentReady()
			return nil
		}
		return fmt.Errorf("acquiring instance lock: %w", err)
	}
	// Hold the lock for the whole process lifetime; closing releases it.
	defer lock.Close()
	if err := writePidFile(stateDir, os.Getpid()); err != nil {
		log.Printf("WARN writing pidfile: %v", err)
	} else {
		defer func() { _ = os.Remove(filepath.Join(stateDir, "bot.pid")) }()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Fprintf(os.Stderr, "Starting herdr-whistle bot...\n")
	// startBot blocks on b.Start(ctx), which runs until ctx is cancelled by
	// SIGINT/SIGTERM. If it ever returns early we exit rather than hang.
	if err := startBot(ctx, cfg); err != nil {
		return fmt.Errorf("starting Telegram bot: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Shutting down...\n")
	return nil
}
