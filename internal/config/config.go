// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
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
	// EnvSMTPListen is the address the SMTP server for smtp accounts
	// listens on, like ":2525"; empty turns it off.
	EnvSMTPListen = "MAIL_ARCHIVE_SMTP_LISTEN"
	// EnvSMTPAllowedNetworks lists the networks (CIDR or single addresses,
	// comma-separated) the SMTP server accepts clients from.
	EnvSMTPAllowedNetworks = "MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS"
	// EnvSMTPMaxSize is the largest message the SMTP server accepts, in
	// bytes or with a K, M or G suffix.
	EnvSMTPMaxSize = "MAIL_ARCHIVE_SMTP_MAX_SIZE"
	// EnvSMTPTLSCert and EnvSMTPTLSKey are PEM files that turn STARTTLS on.
	EnvSMTPTLSCert = "MAIL_ARCHIVE_SMTP_TLS_CERT"
	EnvSMTPTLSKey  = "MAIL_ARCHIVE_SMTP_TLS_KEY"
	// EnvSMTPHostname is the name in the SMTP greeting; default: the
	// machine's host name.
	EnvSMTPHostname = "MAIL_ARCHIVE_SMTP_HOSTNAME"
)

// DefaultSMTPMaxSize is used when EnvSMTPMaxSize is not set.
const DefaultSMTPMaxSize = 50 << 20

// MaxSMTPMaxSize is the largest accepted EnvSMTPMaxSize.
const MaxSMTPMaxSize = 1 << 30

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

// MinReconcileInterval keeps reconciles rare: each lists every UID of a folder.
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
	// SMTP is the server for smtp accounts; nil turns it off.
	SMTP *SMTP
}

// SMTP configures the SMTP server for smtp accounts.
type SMTP struct {
	Listen          string
	AllowedNetworks []netip.Prefix
	MaxSize         int64
	TLSCert         string // empty: no STARTTLS
	TLSKey          string
	Hostname        string
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
	cfg.SMTP, err = ParseSMTP(os.Getenv(EnvSMTPListen), os.Getenv(EnvSMTPAllowedNetworks), os.Getenv(EnvSMTPMaxSize),
		os.Getenv(EnvSMTPTLSCert), os.Getenv(EnvSMTPTLSKey), os.Getenv(EnvSMTPHostname))
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

// ParseSMTP checks the SMTP server settings. An empty listen address
// turns the server off; then the other settings must be empty too, so that
// a typo in the listen variable does not go unnoticed.
func ParseSMTP(listen, networks, maxSize, cert, key, hostname string) (*SMTP, error) {
	listen, networks, maxSize = strings.TrimSpace(listen), strings.TrimSpace(networks), strings.TrimSpace(maxSize)
	cert, key, hostname = strings.TrimSpace(cert), strings.TrimSpace(key), strings.TrimSpace(hostname)
	if listen == "" {
		if networks != "" || maxSize != "" || cert != "" || key != "" || hostname != "" {
			return nil, fmt.Errorf("SMTP settings are set, but %s is not", EnvSMTPListen)
		}
		return nil, nil
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return nil, fmt.Errorf("%s: want host:port or :port, got %q", EnvSMTPListen, listen)
	}
	c := &SMTP{Listen: listen, MaxSize: DefaultSMTPMaxSize, TLSCert: cert, TLSKey: key, Hostname: hostname}
	for _, n := range strings.Split(networks, ",") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		p, err := netip.ParsePrefix(n)
		if err != nil {
			a, aerr := netip.ParseAddr(n)
			if aerr != nil {
				return nil, fmt.Errorf("%s: want CIDR networks or addresses like 172.16.0.0/12 or 192.0.2.10, got %q", EnvSMTPAllowedNetworks, n)
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		c.AllowedNetworks = append(c.AllowedNetworks, netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-unmapBits(p)).Masked())
	}
	if len(c.AllowedNetworks) == 0 {
		return nil, fmt.Errorf("%s is set, so %s is required: the SMTP server accepts mail only from these networks", EnvSMTPListen, EnvSMTPAllowedNetworks)
	}
	if maxSize != "" {
		n, err := parseSize(maxSize)
		if err != nil || n < 1<<20 || n > MaxSMTPMaxSize {
			return nil, fmt.Errorf("%s: want a size from 1M to 1G (like 50M or 52428800), got %q", EnvSMTPMaxSize, maxSize)
		}
		c.MaxSize = n
	}
	if (cert == "") != (key == "") {
		return nil, fmt.Errorf("set both %s and %s, or neither", EnvSMTPTLSCert, EnvSMTPTLSKey)
	}
	if c.Hostname == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "localhost"
		}
		c.Hostname = h
	}
	return c, nil
}

// unmapBits is how many prefix bits an IPv4-mapped IPv6 prefix loses when
// it is turned into an IPv4 prefix.
func unmapBits(p netip.Prefix) int {
	if p.Addr().Is4In6() {
		return 96
	}
	return 0
}

// parseSize reads a byte count with an optional K, M or G suffix (powers
// of 1024; a trailing "B" or "iB" is allowed).
func parseSize(v string) (int64, error) {
	s := strings.ToUpper(v)
	s = strings.TrimSuffix(strings.TrimSuffix(s, "B"), "I")
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 || n > MaxSMTPMaxSize/mult+1 {
		return 0, fmt.Errorf("invalid size %q", v)
	}
	return n * mult, nil
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
