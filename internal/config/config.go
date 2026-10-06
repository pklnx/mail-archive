// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/crypto"
)

// Environment variable names.
const (
	EnvDatabaseURL = "MAIL_ARCHIVE_DATABASE_URL"
	EnvDataDir     = "MAIL_ARCHIVE_DATA_DIR"
	EnvSecretKey   = "MAIL_ARCHIVE_SECRET_KEY" //nolint:gosec // variable name, not a credential
	EnvLogLevel    = "MAIL_ARCHIVE_LOG_LEVEL"
	// EnvAllowedHosts is a comma-separated list of host names the web server
	// accepts in the Host header (protection against DNS rebinding).
	EnvAllowedHosts = "MAIL_ARCHIVE_ALLOWED_HOSTS"
	// EnvSyncInterval is how often the web server syncs each enabled account
	// (Go duration like "6h"; "0" turns the schedule off).
	EnvSyncInterval = "MAIL_ARCHIVE_SYNC_INTERVAL"
	EnvRequire2FA   = "MAIL_ARCHIVE_REQUIRE_2FA"
)

// DefaultSyncInterval is used when EnvSyncInterval is not set.
const DefaultSyncInterval = 6 * time.Hour

// MinSyncInterval protects mail servers from being polled too often.
const MinSyncInterval = 5 * time.Minute

// Config holds the application configuration.
type Config struct {
	DatabaseURL string
	DataDir     string
	SecretKey   []byte // nil if not configured
	LogLevel    string
	// AllowedHosts for the web server; empty means the server's defaults.
	AllowedHosts []string
	// SyncInterval of the web server's schedule; zero means off.
	SyncInterval time.Duration
	Require2FA    bool
}

// Load reads the configuration from the environment.
func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL: os.Getenv(EnvDatabaseURL),
		DataDir:     os.Getenv(EnvDataDir),
		LogLevel:    os.Getenv(EnvLogLevel),
	}
	if cfg.DatabaseURL == "" {
		return nil, errors.New(EnvDatabaseURL + " is not set")
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "./data"
	}
	for _, h := range strings.Split(os.Getenv(EnvAllowedHosts), ",") {
		if h = strings.TrimSpace(h); h != "" {
			cfg.AllowedHosts = append(cfg.AllowedHosts, h)
		}
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	interval, err := parseSyncInterval(os.Getenv(EnvSyncInterval))
	if err != nil {
		return nil, err
	}
	cfg.SyncInterval = interval
	cfg.Require2FA = strings.EqualFold(strings.TrimSpace(os.Getenv(EnvRequire2FA)), "true")
	if raw := os.Getenv(EnvSecretKey); raw != "" {
		key, err := crypto.ParseKey(raw)
		if err != nil {
			return nil, errors.New(EnvSecretKey + ": " + err.Error())
		}
		cfg.SecretKey = key
	}
	return cfg, nil
}

func parseSyncInterval(v string) (time.Duration, error) {
	if v == "" {
		return DefaultSyncInterval, nil
	}
	if v == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < MinSyncInterval {
		return 0, fmt.Errorf("%s: want a duration of at least %s (like 6h) or 0 to turn the schedule off, got %q",
			EnvSyncInterval, MinSyncInterval, v)
	}
	return d, nil
}

// Sealer returns a Sealer for the configured secret key.
func (c *Config) Sealer() (*crypto.Sealer, error) {
	if c.SecretKey == nil {
		return nil, errors.New(EnvSecretKey + " is not set (generate one with `mail-archive keygen`)")
	}
	return crypto.NewSealer(c.SecretKey)
}
