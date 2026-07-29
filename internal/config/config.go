// Package config loads TOML configuration for send-matrix-mail.
// Search order: -C <path> → $XDG_CONFIG_HOME/send-matrix-mail/send-matrix-mail.toml → /etc/send-matrix-mail/send-matrix-mail.toml.
package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// MatrixConfig holds Matrix homeserver and authentication settings.
type MatrixConfig struct {
	Homeserver  string `toml:"homeserver"`
	UserID      string `toml:"user_id"`
	AccessToken string `toml:"access_token"`
	Password    string `toml:"password"`
	DefaultRoom string `toml:"default_room"`
	StateDir    string // not set by toml; set by config.Load
}

// AuthorConfig provides fallbacks for resolving the envelope author address.
type AuthorConfig struct {
	DefaultFrom string `toml:"default_from"`
	DefaultHost string `toml:"default_host"`
}

// Config is the top-level configuration.
type Config struct {
	Matrix   MatrixConfig `toml:"matrix"`
	Author   AuthorConfig `toml:"author"`
	SpoolDir string       `toml:"spool_dir"`
	LogLevel string       `toml:"log_level"`
}

// Defaults returns a Config with safe defaults.
func Defaults() *Config {
	return &Config{
		LogLevel: "info",
	}
}

// Load reads configuration from the standard search paths.
// If configPath is non-empty, only that file is tried.
func Load(configPath string) (*Config, error) {
	cfg := Defaults()

	var paths []string
	if configPath != "" {
		paths = []string{configPath}
	} else {
		paths = searchPaths()
	}

	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		defer f.Close()
		if _, err := toml.NewDecoder(f).Decode(cfg); err != nil {
			return nil, err
		}
		break
	}

	// Set runtime state dir
	cfg.Matrix.StateDir = DefaultStateDir()

	// Set default spool dir
	if cfg.SpoolDir == "" {
		cfg.SpoolDir = defaultSpoolDir()
	}

	return cfg, nil
}

func searchPaths() []string {
	var dirs []string
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		dirs = append(dirs, filepath.Join(d, "send-matrix-mail", "send-matrix-mail.toml"))
	} else {
		home, err := os.UserHomeDir()
		if err == nil {
			dirs = append(dirs, filepath.Join(home, ".config", "send-matrix-mail", "send-matrix-mail.toml"))
		}
	}
	dirs = append(dirs, "/etc/send-matrix-mail/send-matrix-mail.toml")
	return dirs
}

// DefaultStateDir returns the default state directory for send-matrix-mail.
func DefaultStateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "send-matrix-mail")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir() + "/send-matrix-mail"
	}
	return filepath.Join(home, ".local", "state", "send-matrix-mail")
}

func defaultSpoolDir() string {
	return filepath.Join(DefaultStateDir(), "spool")
}
