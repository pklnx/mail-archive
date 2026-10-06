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
