// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/attest"
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
	// EnvSyncIntervalMin is the shortest interval the schedule syncs an
	// account at, also for accounts with a shorter own interval.
	EnvSyncIntervalMin = "MAIL_ARCHIVE_SYNC_INTERVAL_MIN"
	EnvRequire2FA      = "MAIL_ARCHIVE_REQUIRE_2FA"
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
	// EnvTrustedProxies lists the reverse proxies (CIDR or single addresses)
	// whose X-Forwarded-For names the client; empty trusts none.
	EnvTrustedProxies = "MAIL_ARCHIVE_TRUSTED_PROXIES"
	// EnvOperator names who runs a command, for the audit log. The ./ma
	// wrapper sets it to the host user; it is not verified.
	EnvOperator = "MAIL_ARCHIVE_OPERATOR"
	// EnvAttestKeyFile is the Ed25519 key that signs attestations; default
	// <data dir>/attest/signing.key.
	EnvAttestKeyFile = "MAIL_ARCHIVE_ATTEST_KEY_FILE"
	// EnvAttestInterval is how often the web server writes an attestation
	// ("0" turns the schedule off).
	EnvAttestInterval = "MAIL_ARCHIVE_ATTEST_INTERVAL"
	// EnvRetentionFileGrace is how long the files of messages deleted by
	// retention outlive their rows ("0" removes them at the next sweep).
	EnvRetentionFileGrace = "MAIL_ARCHIVE_RETENTION_FILE_GRACE"
	// EnvJobWorkers is how many background jobs (attachment text) a
	// process runs at a time: `serve` (0 = none, they run in a separate
	// `worker`) or `worker`.
	EnvJobWorkers = "MAIL_ARCHIVE_JOB_WORKERS"
)

// DefaultJobWorkers is used when EnvJobWorkers is not set.
const DefaultJobWorkers = 1

// MaxJobWorkers is the largest accepted EnvJobWorkers.
const MaxJobWorkers = 8

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

// DefaultAttestInterval is used when EnvAttestInterval is not set.
const DefaultAttestInterval = 24 * time.Hour

// MinAttestInterval keeps the chain short: each attestation adds a line
// and a leaves file.
const MinAttestInterval = time.Hour

// DefaultRetentionFileGrace keeps the files of deleted messages for a week:
// a backup made meanwhile (dump first, then the files) is complete, and
// restoring the last dump undoes a wrong deletion.
const DefaultRetentionFileGrace = 7 * 24 * time.Hour

// Config holds the application configuration.
type Config struct {
	DatabaseURL string
	DataDir     string
	SecretKey   []byte // nil if not configured
	LogLevel    string
	// AllowedHosts for the web server; empty means the server's defaults.
	AllowedHosts []string
	// SyncInterval of the web server's schedule, the default for accounts
	// without their own; zero turns the schedule off.
	SyncInterval time.Duration
	// SyncIntervalMin is the floor for every account's interval.
	SyncIntervalMin time.Duration
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
	// TrustedProxies may name the client in X-Forwarded-For.
	TrustedProxies []netip.Prefix
	// AttestKeyFile signs attestations.
	AttestKeyFile string
	// AttestInterval of the web server's attestations; zero means off.
	AttestInterval time.Duration
	// RetentionFileGrace is how long the files of deleted messages stay.
	RetentionFileGrace time.Duration
	// JobWorkers is the size of the process's job pool; 0 runs no jobs.
	JobWorkers int
}

// AttestDir holds the attestation file, the leaves and the public keys:
// always <data dir>/attest, also when the key file is elsewhere.
func (c *Config) AttestDir() string { return filepath.Join(c.DataDir, "attest") }

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

// DataDirFromEnv returns the data directory, ./data unless set.
func DataDirFromEnv() string {
	if d := os.Getenv(EnvDataDir); d != "" {
		return d
	}
	return "./data"
}

// Load reads the configuration from the environment.
func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL: os.Getenv(EnvDatabaseURL),
		DataDir:     DataDirFromEnv(),
		LogLevel:    os.Getenv(EnvLogLevel),
	}
	if cfg.DatabaseURL == "" {
		return nil, errors.New(EnvDatabaseURL + " is not set")
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
	if cfg.SyncIntervalMin, err = parseSyncIntervalMin(os.Getenv(EnvSyncIntervalMin)); err != nil {
		return nil, err
	}
	if cfg.SyncInterval > 0 && cfg.SyncInterval < cfg.SyncIntervalMin {
		return nil, fmt.Errorf("%s: %s is below %s (%s)", EnvSyncInterval,
			FormatDuration(cfg.SyncInterval), EnvSyncIntervalMin, FormatDuration(cfg.SyncIntervalMin))
	}
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
	cfg.TrustedProxies, err = ParseNetworks(EnvTrustedProxies, os.Getenv(EnvTrustedProxies))
	if err != nil {
		return nil, err
	}
	cfg.AttestKeyFile = strings.TrimSpace(os.Getenv(EnvAttestKeyFile))
	if cfg.AttestKeyFile == "" {
		cfg.AttestKeyFile = filepath.Join(cfg.AttestDir(), attest.DefaultKeyFile)
	}
	cfg.AttestInterval, err = parseInterval(EnvAttestInterval, strings.TrimSpace(os.Getenv(EnvAttestInterval)),
		DefaultAttestInterval, MinAttestInterval, "24h")
	if err != nil {
		return nil, err
	}
	cfg.RetentionFileGrace, err = parseInterval(EnvRetentionFileGrace, strings.TrimSpace(os.Getenv(EnvRetentionFileGrace)),
		DefaultRetentionFileGrace, time.Hour, "168h")
	if err != nil {
		return nil, err
	}
	if cfg.JobWorkers, err = parseJobWorkers(os.Getenv(EnvJobWorkers)); err != nil {
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

// FormatDuration shows d without trailing zero units: "30m", "1h30m", "6h".
func FormatDuration(d time.Duration) string {
	s := d.String()
	for _, suffix := range []string{"0s", "0m"} {
		if len(s) > len(suffix)+1 && s[len(s)-len(suffix):] == suffix && s[len(s)-len(suffix)-1] >= 'a' {
			s = s[:len(s)-len(suffix)]
		}
	}
	return s
}

// MaxSyncIntervalMin is the largest floor: the longest own interval.
const MaxSyncIntervalMin = 720 * time.Hour

func parseSyncIntervalMin(v string) (time.Duration, error) {
	if v == "" {
		return MinSyncInterval, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < MinSyncInterval || d > MaxSyncIntervalMin {
		return 0, fmt.Errorf("%s: want a duration from %s to %s (like 15m), got %q",
			EnvSyncIntervalMin, MinSyncInterval, MaxSyncIntervalMin, v)
	}
	return d, nil
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

func parseJobWorkers(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultJobWorkers, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > MaxJobWorkers {
		return 0, fmt.Errorf("%s: want a number from 0 to %d, got %q", EnvJobWorkers, MaxJobWorkers, v)
	}
	return n, nil
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
	var err error
	if c.AllowedNetworks, err = ParseNetworks(EnvSMTPAllowedNetworks, networks); err != nil {
		return nil, err
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

// ParseNetworks parses a comma-separated list of CIDR networks and single
// addresses of the variable env. IPv4-mapped IPv6 forms become IPv4.
func ParseNetworks(env, v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, n := range strings.Split(v, ",") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		p, err := netip.ParsePrefix(n)
		if err != nil {
			a, aerr := netip.ParseAddr(n)
			if aerr != nil {
				return nil, fmt.Errorf("%s: want CIDR networks or addresses like 172.16.0.0/12 or 192.0.2.10, got %q", env, n)
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		out = append(out, netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-unmapBits(p)).Masked())
	}
	return out, nil
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
