package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseSyncInterval(t *testing.T) {
	for in, want := range map[string]time.Duration{"": DefaultSyncInterval, "0": 0, "6h": 6 * time.Hour, "5m": 5 * time.Minute} {
		got, err := parseSyncInterval(in)
		if err != nil || got != want {
			t.Errorf("parseSyncInterval(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"1m", "-1h", "abc", "6"} {
		if _, err := parseSyncInterval(in); err == nil {
			t.Errorf("parseSyncInterval(%q) succeeded, want error", in)
		}
	}
}

func TestSyncIntervalMin(t *testing.T) {
	for in, want := range map[string]time.Duration{"": MinSyncInterval, "5m": 5 * time.Minute, "1h": time.Hour, "720h": 720 * time.Hour} {
		if got, err := parseSyncIntervalMin(in); err != nil || got != want {
			t.Errorf("parseSyncIntervalMin(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"4m", "0", "721h", "abc"} {
		if _, err := parseSyncIntervalMin(in); err == nil || !strings.Contains(err.Error(), EnvSyncIntervalMin) {
			t.Errorf("parseSyncIntervalMin(%q) = %v, want an error naming the variable", in, err)
		}
	}
	load := func(interval, floor string) (*Config, error) {
		t.Setenv(EnvDatabaseURL, "postgres://x")
		t.Setenv(EnvSyncInterval, interval)
		t.Setenv(EnvSyncIntervalMin, floor)
		return Load()
	}
	if _, err := load("30m", "1h"); err == nil || err.Error() != "MAIL_ARCHIVE_SYNC_INTERVAL: 30m is below MAIL_ARCHIVE_SYNC_INTERVAL_MIN (1h)" {
		t.Errorf("default below the floor: %v", err)
	}
	if cfg, err := load("0", "1h"); err != nil || cfg.SyncInterval != 0 || cfg.SyncIntervalMin != time.Hour {
		t.Errorf("schedule off with a floor: %+v, %v", cfg, err)
	}
	if cfg, err := load("", ""); err != nil || cfg.SyncIntervalMin != MinSyncInterval {
		t.Errorf("defaults: %+v, %v", cfg, err)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Minute: "30m", time.Hour: "1h", 90 * time.Minute: "1h30m", 720 * time.Hour: "720h",
		10 * time.Second: "10s", 5*time.Minute + 30*time.Second: "5m30s", 10 * time.Hour: "10h", 20 * time.Minute: "20m",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestParseReconcileInterval(t *testing.T) {
	parse := func(v string) (time.Duration, error) {
		return parseInterval(EnvReconcileInterval, v, DefaultReconcileInterval, MinReconcileInterval, "24h")
	}
	for in, want := range map[string]time.Duration{"": 24 * time.Hour, "0": 0, "1h": time.Hour, "72h": 72 * time.Hour} {
		if got, err := parse(in); err != nil || got != want {
			t.Errorf("parse(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"30m", "-1h", "daily"} {
		if _, err := parse(in); err == nil || !strings.Contains(err.Error(), EnvReconcileInterval) {
			t.Errorf("parse(%q) = %v, want an error naming the variable", in, err)
		}
	}
}

func TestParsePublicURL(t *testing.T) {
	for _, c := range []struct{ in, origin, rpID string }{
		{"https://archive.example.ts.net", "https://archive.example.ts.net", "archive.example.ts.net"},
		{"https://Archive.Example.ts.net:8443/", "https://archive.example.ts.net:8443", "archive.example.ts.net"},
		{"https://archive.example.ts.net:443", "https://archive.example.ts.net", "archive.example.ts.net"},
		{"http://localhost:8080", "http://localhost:8080", "localhost"},
	} {
		origin, rpID, err := ParsePublicURL(c.in)
		if err != nil || origin != c.origin || rpID != c.rpID {
			t.Errorf("%s: %q %q %v", c.in, origin, rpID, err)
		}
	}
	for _, in := range []string{
		"http://archive.example.ts.net", "https://192.168.1.10", "https://[::1]:8080", "http://127.0.0.1:8080",
		"https://archive.example.ts.net/mail", "https://archive.example.ts.net?x=1", "https://user@archive.example.ts.net",
		"archive.example.ts.net", "ftp://localhost",
	} {
		if _, _, err := ParsePublicURL(in); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
}

func TestParseAlertAfter(t *testing.T) {
	for in, want := range map[string]int{"": DefaultAlertAfterFailures, "1": 1, " 5 ": 5, "100": 100} {
		if got, err := parseAlertAfter(in); err != nil || got != want {
			t.Errorf("parseAlertAfter(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "101", "-1", "three", "2.5"} {
		if _, err := parseAlertAfter(in); err == nil {
			t.Errorf("parseAlertAfter(%q) succeeded", in)
		}
	}
}

func TestParseJobWorkers(t *testing.T) {
	for in, want := range map[string]int{"": DefaultJobWorkers, "0": 0, " 2 ": 2, "8": 8} {
		if got, err := parseJobWorkers(in); err != nil || got != want {
			t.Errorf("parseJobWorkers(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"9", "-1", "one", "1.5"} {
		if _, err := parseJobWorkers(in); err == nil || !strings.Contains(err.Error(), EnvJobWorkers) {
			t.Errorf("parseJobWorkers(%q): %v", in, err)
		}
	}
}

// TestMigrateOnStart (#396): on or off in any case, off when empty;
// anything else is refused, naming the variable.
func TestMigrateOnStart(t *testing.T) {
	for in, want := range map[string]bool{"": false, "off": false, "OFF": false, "on": true, " On ": true} {
		if got, err := parseOnOff(EnvMigrateOnStart, in); err != nil || got != want {
			t.Errorf("parseOnOff(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"true", "1", "yes", "onn"} {
		if _, err := parseOnOff(EnvMigrateOnStart, in); err == nil || !strings.Contains(err.Error(), EnvMigrateOnStart) {
			t.Errorf("parseOnOff(%q): %v", in, err)
		}
	}
	t.Setenv(EnvDatabaseURL, "postgres://localhost/x")
	t.Setenv(EnvMigrateOnStart, "on")
	if cfg, err := Load(); err != nil || !cfg.MigrateOnStart {
		t.Errorf("Load with on: %v", err)
	}
	t.Setenv(EnvMigrateOnStart, "maybe")
	if _, err := Load(); err == nil {
		t.Error("Load accepted maybe")
	}
}

func TestParseWebhook(t *testing.T) {
	for _, format := range []string{"", "json", "ntfy"} {
		// Compose passes the default format without a URL.
		if w, err := ParseWebhook("", format, ""); w != nil || err != nil {
			t.Fatalf("no URL, format %q: %v %v", format, w, err)
		}
	}
	w, err := ParseWebhook("https://ntfy.example.org/secret-topic", "", "")
	if err != nil || w.Format != WebhookFormatJSON || w.Target() != "https://ntfy.example.org" || w.PlainHTTP() {
		t.Fatalf("https: %+v %v", w, err)
	}
	w, err = ParseWebhook("http://user:pw@192.168.1.5:8080/hook?token=x", "NTFY", "Bearer tk_abc")
	if err != nil || w.Format != WebhookFormatNtfy || w.Authorization != "Bearer tk_abc" || !w.PlainHTTP() {
		t.Fatalf("ntfy: %+v %v", w, err)
	}
	if strings.Contains(w.Target(), "pw") || strings.Contains(w.Target(), "token") {
		t.Fatalf("target leaks secrets: %s", w.Target())
	}
	for _, local := range []string{"http://localhost/x", "http://127.0.0.1:2586/x", "http://[::1]/x"} {
		if w, err := ParseWebhook(local, "", ""); err != nil || w.PlainHTTP() {
			t.Errorf("%s: %v %v", local, w, err)
		}
	}
	for _, c := range []struct{ url, format, auth string }{
		{"ftp://example.org/x", "", ""},
		{"https:///x", "", ""},
		{"https://example.org/x#frag", "", ""},
		{"https://example.org/x#", "", ""},
		{"example.org/x", "", ""},
		{"https://example.org/x", "slack", ""},
		{"https://example.org/x", "", "Bearer a\nX-Evil: 1"},
		{"", "", "Bearer x"},
	} {
		_, err := ParseWebhook(c.url, c.format, c.auth)
		if err == nil {
			t.Errorf("%+v accepted", c)
			continue
		}
		if c.url != "" && strings.Contains(err.Error(), "example.org") {
			t.Errorf("error shows the URL: %v", err)
		}
	}
}

func TestParseSMTP(t *testing.T) {
	c, err := ParseSMTP(SMTPEnv{})
	if c != nil || err != nil {
		t.Fatalf("off: %+v, %v", c, err)
	}
	c, err = ParseSMTP(SMTPEnv{Listen: ":2525", AllowedNetworks: "172.16.0.0/12, 192.0.2.10,::ffff:198.51.100.0/120, 2001:db8::/32",
		Hostname: "archive.example", Auth: "off"})
	if err != nil {
		t.Fatal(err)
	}
	var nets []string
	for _, p := range c.AllowedNetworks {
		nets = append(nets, p.String())
	}
	if got := strings.Join(nets, " "); got != "172.16.0.0/12 192.0.2.10/32 198.51.100.0/24 2001:db8::/32" {
		t.Errorf("networks %s", got)
	}
	if c.MaxSize != DefaultSMTPMaxSize || c.Hostname != "archive.example" || c.Listen != ":2525" || c.Auth || c.MaxConnsPerAddr != 0 {
		t.Errorf("config %+v", c)
	}
	for in, want := range map[string]int64{"50M": 50 << 20, "52428800": 52428800, "1g": 1 << 30, "20MiB": 20 << 20, "2048K": 2 << 20} {
		c, err := ParseSMTP(SMTPEnv{Listen: ":2525", AllowedNetworks: "127.0.0.1", MaxSize: in, Auth: "off"})
		if err != nil || c.MaxSize != want {
			t.Errorf("max size %q: %+v, %v", in, c, err)
		}
	}

	// required, the default: a certificate, no allowlist needed.
	for _, mode := range []string{"", "required", " required "} {
		c, err := ParseSMTP(SMTPEnv{Listen: ":587", TLSCert: "/tls/cert.pem", TLSKey: "/tls/key.pem", Auth: mode})
		if err != nil || !c.Auth || len(c.AllowedNetworks) != 0 || c.MaxConnsPerAddr != DefaultSMTPMaxConnsPerAddr {
			t.Errorf("auth %q: %+v, %v", mode, c, err)
		}
	}
	c, err = ParseSMTP(SMTPEnv{Listen: ":587", AllowedNetworks: "192.0.2.0/24", TLSCert: "c", TLSKey: "k", MaxConnsPerAddr: "64"})
	if err != nil || !c.Auth || len(c.AllowedNetworks) != 1 || c.MaxConnsPerAddr != 64 {
		t.Errorf("required with an allowlist: %+v, %v", c, err)
	}
	// The default without a certificate names the way out.
	_, err = ParseSMTP(SMTPEnv{Listen: ":2525", AllowedNetworks: "127.0.0.1"})
	if err == nil || !strings.Contains(err.Error(), "MAIL_ARCHIVE_SMTP_AUTH=off") || !strings.Contains(err.Error(), EnvSMTPTLSCert) {
		t.Errorf("required without a certificate: %v", err)
	}

	for _, bad := range []SMTPEnv{
		{AllowedNetworks: "127.0.0.1"}, // settings without listen
		{Auth: "off"},                  // auth without listen
		{MaxConnsPerAddr: "8"},         // cap without listen
		{Listen: "2525", AllowedNetworks: "127.0.0.1", Auth: "off"},    // no host:port
		{Listen: ":2525", Auth: "off"},                                 // off without an allowlist
		{Listen: ":2525", AllowedNetworks: "10.0.0.0/33", Auth: "off"}, // bad network
		{Listen: ":2525", AllowedNetworks: "127.0.0.1", MaxSize: "100K", Auth: "off"},
		{Listen: ":2525", AllowedNetworks: "127.0.0.1", MaxSize: "2G", Auth: "off"},
		{Listen: ":2525", AllowedNetworks: "127.0.0.1", MaxSize: "fifty", Auth: "off"},
		{Listen: ":2525", AllowedNetworks: "127.0.0.1", TLSCert: "cert.pem", Auth: "off"}, // cert without key
		{Listen: ":2525", TLSCert: "c", TLSKey: "k", Auth: "on"},                          // not a mode
		{Listen: ":2525", TLSCert: "c", TLSKey: "k", Auth: "Required"},                    // case matters
		{Listen: ":2525", TLSCert: "c"},                                                   // required, key missing
		{Listen: ":2525", TLSCert: "c", TLSKey: "k", MaxConnsPerAddr: "0"},
		{Listen: ":2525", TLSCert: "c", TLSKey: "k", MaxConnsPerAddr: "65"},
		{Listen: ":2525", TLSCert: "c", TLSKey: "k", MaxConnsPerAddr: "eight"},
		{Listen: ":2525", AllowedNetworks: "127.0.0.1", Auth: "off", MaxConnsPerAddr: "8"}, // the cap needs auth
	} {
		if c, err := ParseSMTP(bad); err == nil {
			t.Errorf("accepted %+v: %+v", bad, c)
		}
	}
}

func TestParseNetworks(t *testing.T) {
	got, err := ParseNetworks(EnvTrustedProxies, " 172.16.0.0/12, 192.0.2.10 ,::ffff:10.0.0.1, fd00::/8,")
	if err != nil {
		t.Fatal(err)
	}
	want := "[172.16.0.0/12 192.0.2.10/32 10.0.0.1/32 fd00::/8]"
	if fmt.Sprint(got) != want {
		t.Errorf("got %v, want %s", got, want)
	}
	if got, err := ParseNetworks(EnvTrustedProxies, ""); err != nil || got != nil {
		t.Errorf("empty: %v, %v", got, err)
	}
	if _, err := ParseNetworks(EnvTrustedProxies, "proxy.local"); err == nil || !strings.Contains(err.Error(), EnvTrustedProxies) {
		t.Errorf("host name: %v", err)
	}
}

func TestLoadAttest(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://localhost/x")
	t.Setenv(EnvDataDir, "/data")
	t.Setenv(EnvAttestKeyFile, "")
	t.Setenv(EnvAttestInterval, "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AttestKeyFile != "/data/attest/signing.key" || cfg.AttestDir() != "/data/attest" || cfg.AttestInterval != 24*time.Hour {
		t.Errorf("defaults: %q %q %v", cfg.AttestKeyFile, cfg.AttestDir(), cfg.AttestInterval)
	}

	t.Setenv(EnvAttestKeyFile, "/keys/attest.key")
	t.Setenv(EnvAttestInterval, "0")
	if cfg, err = Load(); err != nil {
		t.Fatal(err)
	}
	if cfg.AttestKeyFile != "/keys/attest.key" || cfg.AttestDir() != "/data/attest" || cfg.AttestInterval != 0 {
		t.Errorf("set: %q %q %v", cfg.AttestKeyFile, cfg.AttestDir(), cfg.AttestInterval)
	}

	for _, v := range []string{"30m", "daily"} {
		t.Setenv(EnvAttestInterval, v)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), EnvAttestInterval) {
			t.Errorf("%s: %v, want an error naming the variable", v, err)
		}
	}
}
