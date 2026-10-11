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
	c, err := ParseSMTP("", "", "", "", "", "")
	if c != nil || err != nil {
		t.Fatalf("off: %+v, %v", c, err)
	}
	c, err = ParseSMTP(":2525", "172.16.0.0/12, 192.0.2.10,::ffff:198.51.100.0/120, 2001:db8::/32", "", "", "", "archive.example")
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
	if c.MaxSize != DefaultSMTPMaxSize || c.Hostname != "archive.example" || c.Listen != ":2525" {
		t.Errorf("config %+v", c)
	}
	for in, want := range map[string]int64{"50M": 50 << 20, "52428800": 52428800, "1g": 1 << 30, "20MiB": 20 << 20, "2048K": 2 << 20} {
		c, err := ParseSMTP(":2525", "127.0.0.1", in, "", "", "")
		if err != nil || c.MaxSize != want {
			t.Errorf("max size %q: %+v, %v", in, c, err)
		}
	}
	for _, bad := range [][6]string{
		{"", "127.0.0.1", "", "", "", ""},              // settings without listen
		{"2525", "127.0.0.1", "", "", "", ""},          // no host:port
		{":2525", "", "", "", "", ""},                  // no allowlist
		{":2525", "10.0.0.0/33", "", "", "", ""},       // bad network
		{":2525", "127.0.0.1", "100K", "", "", ""},     // too small
		{":2525", "127.0.0.1", "2G", "", "", ""},       // too large
		{":2525", "127.0.0.1", "fifty", "", "", ""},    // not a size
		{":2525", "127.0.0.1", "", "cert.pem", "", ""}, // cert without key
	} {
		if _, err := ParseSMTP(bad[0], bad[1], bad[2], bad[3], bad[4], bad[5]); err == nil {
			t.Errorf("accepted %q", bad)
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
