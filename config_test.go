package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "token = \"bot123\"\nowner_id = 42\nchat_id = 42\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Token != "bot123" {
		t.Errorf("Token = %q, want \"bot123\"", cfg.Token)
	}
	if cfg.OwnerID != 42 {
		t.Errorf("OwnerID = %d, want 42", cfg.OwnerID)
	}
	if cfg.ChatID != 42 {
		t.Errorf("ChatID = %d, want 42", cfg.ChatID)
	}

	if _, err := loadConfig(filepath.Join(dir, "does-not-exist.toml")); err == nil {
		t.Error("expected error for missing config file")
	}
}

func TestLoadConfigRejectsLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("token = \"bot123\"\nowner_id = 42\nchat_id = 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected insecure config permissions to be rejected")
	}
}

func TestLoadConfigRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.toml")
	link := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(target, []byte("token = \"bot123\"\nowner_id = 42\nchat_id = 42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(link); err == nil {
		t.Fatal("expected symlink config to be rejected")
	}
}

func TestLoadConfigRejectsFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected FIFO config to be rejected")
	}
}
