package config

import (
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
