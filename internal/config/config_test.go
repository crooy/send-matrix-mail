package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("/nonexistent/path/no/such/file")
	if err != nil {
		t.Fatalf("Load with nonexistent path should not error: %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("default LogLevel: got %q, want %q", cfg.LogLevel, "info")
	}
}

func TestSearchPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/home/user/.config")
	paths := searchPaths()
	if len(paths) < 2 {
		t.Fatalf("expected at least 2 paths, got %d", len(paths))
	}
	if !strings.HasSuffix(paths[0], filepath.Join("send-matrix-mail", "sendmailrc.toml")) {
		t.Errorf("first path: got %q, expected XDG path", paths[0])
	}
	if paths[len(paths)-1] != "/etc/send-matrix-mail/sendmailrc.toml" {
		t.Errorf("last path: got %q, expected /etc path", paths[len(paths)-1])
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "test.toml")

	// Root-level keys must come BEFORE table headers in TOML.
	content := `spool_dir = "/tmp/spool"
log_level = "debug"

[matrix]
homeserver = "https://matrix.example.com"
user_id = "@bot:example.com"
default_room = "#alerts:example.com"

[author]
default_from = "cron@example.com"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Matrix.Homeserver != "https://matrix.example.com" {
		t.Errorf("homeserver: got %q", cfg.Matrix.Homeserver)
	}
	if cfg.Matrix.UserID != "@bot:example.com" {
		t.Errorf("user_id: got %q", cfg.Matrix.UserID)
	}
	if cfg.Matrix.DefaultRoom != "#alerts:example.com" {
		t.Errorf("default_room: got %q", cfg.Matrix.DefaultRoom)
	}
	if cfg.Author.DefaultFrom != "cron@example.com" {
		t.Errorf("default_from: got %q", cfg.Author.DefaultFrom)
	}
	if cfg.SpoolDir != "/tmp/spool" {
		t.Errorf("spool_dir: got %q", cfg.SpoolDir)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log_level: got %q", cfg.LogLevel)
	}
}
