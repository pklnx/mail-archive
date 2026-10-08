package config

import (
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
