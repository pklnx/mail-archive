// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
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
	// EnvReconcileInterval is how often each folder is compared with the
	// server to find deleted messages and changed flags ("0" turns it off).
	EnvReconcileInterval = "MAIL_ARCHIVE_RECONCILE_INTERVAL"
	// EnvPublicURL is the address users open in the browser, like
	// https://archive.example.ts.net. Passkeys need it: they are bound to
	// its host name.
	EnvPublicURL = "MAIL_ARCHIVE_PUBLIC_URL"
	// EnvAlertAfterFailures is how many syncs of an account in a row must
	// fail before it counts as failing (alert, banner, /healthz/sync).
	EnvAlertAfterFailures = "MAIL_ARCHIVE_ALERT_AFTER_FAILURES"
	// EnvNotifyWebhookURL is where alerts are posted; empty turns them off.
	EnvNotifyWebhookURL = "MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL"
	// EnvNotifyWebhookFormat is "json" (default) or "ntfy".
	EnvNotifyWebhookFormat = "MAIL_ARCHIVE_NOTIFY_WEBHOOK_FORMAT"
	// EnvNotifyWebhookAuthorization is sent as the Authorization header.
	EnvNotifyWebhookAuthorization = "MAIL_ARCHIVE_NOTIFY_WEBHOOK_AUTHORIZATION"
)

// DefaultAlertAfterFailures is used when EnvAlertAfterFailures is not set.
const DefaultAlertAfterFailures = 3

// MaxAlertAfterFailures is the largest accepted threshold.
const MaxAlertAfterFailures = 100

// Webhook formats.
const (
	WebhookFormatJSON = "json"
	WebhookFormatNtfy = "ntfy"
)

// DefaultSyncInterval is used when EnvSyncInterval is not set.
const DefaultSyncInterval = 6 * time.Hour

// MinSyncInterval protects mail servers from being polled too often.
const MinSyncInterval = 5 * time.Minute

// DefaultReconcileInterval is used when EnvReconcileInterval is not set.
const DefaultReconcileInterval = 24 * time.Hour

// MinReconcileInterval: a reconcile lists every UID of a folder.
const MinReconcileInterval = time.Hour

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
	// ReconcileInterval: how old a folder's last reconcile may get before a
	// sync reconciles it again; zero means only on request.
	ReconcileInterval time.Duration
	Require2FA        bool
	// PublicURL is the web UI's origin (scheme, host and port, no path);
	// empty turns passkeys off.
	PublicURL string
	// AlertAfterFailures: failed syncs in a row that make an account failing.
	AlertAfterFailures int
	// Webhook for alerts; nil turns them off.
	Webhook *Webhook
}

// Webhook is where alerts are sent. The URL and the authorization value
// are secrets: never log them.
type Webhook struct {
	URL           *url.URL
	Format        string
	Authorization string
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
	cfg.ReconcileInterval, err = parseInterval(EnvReconcileInterval, os.Getenv(EnvReconcileInterval),
		DefaultReconcileInterval, MinReconcileInterval, "24h")
	if err != nil {
		return nil, err
	}
	cfg.Require2FA = strings.EqualFold(strings.TrimSpace(os.Getenv(EnvRequire2FA)), "true")
	if v := strings.TrimSpace(os.Getenv(EnvPublicURL)); v != "" {
		origin, _, err := ParsePublicURL(v)
		if err != nil {
			return nil, err
		}
		cfg.PublicURL = origin
	}
	cfg.AlertAfterFailures, err = parseAlertAfter(os.Getenv(EnvAlertAfterFailures))
	if err != nil {
		return nil, err
	}
	cfg.Webhook, err = ParseWebhook(os.Getenv(EnvNotifyWebhookURL), os.Getenv(EnvNotifyWebhookFormat),
		os.Getenv(EnvNotifyWebhookAuthorization))
	if err != nil {
		return nil, err
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
	return parseInterval(EnvSyncInterval, v, DefaultSyncInterval, MinSyncInterval, "6h")
}

// parseInterval reads a duration of at least minimum, or "0" for off.
func parseInterval(env, v string, def, minimum time.Duration, example string) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	if v == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < minimum {
		return 0, fmt.Errorf("%s: want a duration of at least %s (like %s) or 0 to turn it off, got %q",
			env, minimum, example, v)
	}
	return d, nil
}

func parseAlertAfter(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultAlertAfterFailures, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > MaxAlertAfterFailures {
		return 0, fmt.Errorf("%s: want a number from 1 to %d, got %q", EnvAlertAfterFailures, MaxAlertAfterFailures, v)
	}
	return n, nil
}

// ParseWebhook checks the webhook settings. An empty URL means no webhook
// (Compose always passes a format); an authorization without a URL is an
// error. Errors never contain the URL or the authorization value: both may
// hold tokens.
func ParseWebhook(rawURL, format, authorization string) (*Webhook, error) {
	rawURL, format = strings.TrimSpace(rawURL), strings.ToLower(strings.TrimSpace(format))
	authorization = strings.TrimSpace(authorization)
	if rawURL == "" {
		if authorization != "" {
			return nil, fmt.Errorf("%s is set, but %s is not", EnvNotifyWebhookAuthorization, EnvNotifyWebhookURL)
		}
		return nil, nil
	}
	bad := func(why string) (*Webhook, error) {
		return nil, fmt.Errorf("%s: %s (the value is not shown, it may contain a token)", EnvNotifyWebhookURL, why)
	}
	u, err := url.Parse(rawURL)
	switch {
	case err != nil:
		return bad("not a URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return bad("want an http:// or https:// URL")
	case u.Host == "" || u.Hostname() == "":
		return bad("no host name")
	case u.Fragment != "" || strings.Contains(rawURL, "#"):
		return bad("a URL with a #fragment is not allowed")
	}
	switch format {
	case "":
		format = WebhookFormatJSON
	case WebhookFormatJSON, WebhookFormatNtfy:
	default:
		return nil, fmt.Errorf("%s: want %s or %s, got %q", EnvNotifyWebhookFormat, WebhookFormatJSON, WebhookFormatNtfy, format)
	}
	for _, r := range authorization {
		if r < 0x20 || r == 0x7f {
			return nil, fmt.Errorf("%s: contains a control character", EnvNotifyWebhookAuthorization)
		}
	}
	return &Webhook{URL: u, Format: format, Authorization: authorization}, nil
}

// Target names the webhook's scheme and host, for logs. The path, query and
// user info are left out: they may hold tokens.
func (w *Webhook) Target() string {
	return w.URL.Scheme + "://" + w.URL.Host
}

// PlainHTTP reports whether the webhook sends alerts unencrypted to a host
// other than this machine.
func (w *Webhook) PlainHTTP() bool {
	if w.URL.Scheme != "http" {
		return false
	}
	host := w.URL.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// Sealer returns a Sealer for the configured secret key.
func (c *Config) Sealer() (*crypto.Sealer, error) {
	if c.SecretKey == nil {
		return nil, errors.New(EnvSecretKey + " is not set (generate one with `mail-archive keygen`)")
	}
	return crypto.NewSealer(c.SecretKey)
}
