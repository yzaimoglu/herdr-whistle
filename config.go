package main

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/BurntSushi/toml"
)

// Config holds the plugin configuration loaded from a TOML file.
type Config struct {
	Token   string `toml:"token"`
	OwnerID int64  `toml:"owner_id"`
	ChatID  int64  `toml:"chat_id"`
}

// loadConfig reads and parses a TOML configuration file at the given path.
func loadConfig(path string) (*Config, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("checking config file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config file %s must be a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("config file %s must not be accessible by group or other users (use chmod 600)", path)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	return &cfg, nil
}
