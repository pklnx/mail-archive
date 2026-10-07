// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
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
	// EnvPublicURL is the address users open in the browser, like
	// https://archive.example.ts.net. Passkeys need it: they are bound to
	// its host name.
	EnvPublicURL = "MAIL_ARCHIVE_PUBLIC_URL"
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
	Require2FA   bool
	// PublicURL is the web UI's origin (scheme, host and port, no path);
	// empty turns passkeys off.
	PublicURL string
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
	if v := strings.TrimSpace(os.Getenv(EnvPublicURL)); v != "" {
		origin, _, err := ParsePublicURL(v)
		if err != nil {
			return nil, err
		}
		cfg.PublicURL = origin
	}
	if raw := os.Getenv(EnvSecretKey); raw != "" {
		key, err := crypto.ParseKey(raw)
		if err != nil {
			return nil, errors.New(EnvSecretKey + ": " + err.Error())
		}
		cfg.SecretKey = key
	}
	return cfg, nil
}

// ParsePublicURL checks a public URL and returns its origin (scheme, host
// and port) and the WebAuthn relying party ID (the host name alone). Only
// https is allowed, or http on localhost: browsers offer passkeys nowhere
// else. Paths, queries and IP addresses are refused.
func ParsePublicURL(v string) (origin, rpID string, err error) {
	bad := func(why string) (string, string, error) {
		return "", "", fmt.Errorf("%s: %s, got %q (example: https://archive.example.ts.net)", EnvPublicURL, why, v)
	}
	u, err := url.Parse(v)
	if err != nil {
		return bad("not a URL")
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case u.Scheme != "https" && (u.Scheme != "http" || host != "localhost"):
		return bad("want https://, or http://localhost")
	case host == "":
		return bad("no host name")
	case net.ParseIP(host) != nil:
		return bad("passkeys need a host name, not an IP address")
	case u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "":
		return bad("want only scheme, host and port")
	}
	origin = u.Scheme + "://" + host
	if port := u.Port(); port != "" {
		defaultPort := u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80"
		if !defaultPort {
			origin += ":" + port
		}
	}
	return origin, host, nil
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
