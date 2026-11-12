package config

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// Each IMAP limit has its default, accepts its range ends and refuses
// what lies outside, with an error naming the variable.
func TestParseIMAPLimits(t *testing.T) {
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}
	got, err := ParseIMAPLimits(env(nil))
	want := IMAPLimits{FillMaxFolderMessages: 500_000, ReconcileMaxMessages: 2_000_000, MaxFolders: 10_000, IdleTimeout: 2 * time.Minute}
	if err != nil || got != want {
		t.Fatalf("defaults = %+v, %v; want %+v", got, err, want)
	}
	for _, tc := range []struct {
		env       string
		good, bad []string
		read      func(IMAPLimits) string
	}{
		{EnvFillMaxFolderMessages, []string{"10000", " 10000000 ", "750000"}, []string{"9999", "10000001", "-1", "5e5", "500k", "abc"},
			func(l IMAPLimits) string { return itoa(int64(l.FillMaxFolderMessages)) }},
		{EnvReconcileMaxMessages, []string{"10000", "20000000"}, []string{"9999", "20000001", "0"},
			func(l IMAPLimits) string { return itoa(int64(l.ReconcileMaxMessages)) }},
		{EnvIMAPMaxFolders, []string{"100", "200000"}, []string{"99", "200001", "1.5"},
			func(l IMAPLimits) string { return itoa(int64(l.MaxFolders)) }},
		{EnvIMAPIdleTimeout, []string{"30s", "30m", "5m"}, []string{"29s", "31m", "0", "120", "abc"},
			func(l IMAPLimits) string { return l.IdleTimeout.String() }},
	} {
		for _, v := range tc.good {
			l, err := ParseIMAPLimits(env(map[string]string{tc.env: v}))
			if err != nil {
				t.Errorf("%s=%q: %v", tc.env, v, err)
				continue
			}
			wantV := strings.TrimSpace(v)
			if d, derr := time.ParseDuration(wantV); derr == nil {
				wantV = d.String()
			}
			if got := tc.read(l); got != wantV {
				t.Errorf("%s=%q read as %s", tc.env, v, got)
			}
		}
		for _, v := range tc.bad {
			if _, err := ParseIMAPLimits(env(map[string]string{tc.env: v})); err == nil || !strings.HasPrefix(err.Error(), tc.env+": ") {
				t.Errorf("%s=%q: err = %v, want an error naming the variable", tc.env, v, err)
			}
		}
	}
}

// Load reads the IMAP limits and refuses a value out of range at start.
func TestLoadIMAPLimits(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://x")
	t.Setenv(EnvIMAPMaxFolders, "50000")
	t.Setenv(EnvIMAPIdleTimeout, "10m")
	cfg, err := Load()
	if err != nil || cfg.IMAP.MaxFolders != 50_000 || cfg.IMAP.IdleTimeout != 10*time.Minute ||
		cfg.IMAP.FillMaxFolderMessages != DefaultFillMaxFolderMessages || cfg.IMAP.ReconcileMaxMessages != DefaultReconcileMaxMessages {
		t.Fatalf("Load = %+v, %v", cfg, err)
	}
	t.Setenv(EnvFillMaxFolderMessages, "20000000")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), EnvFillMaxFolderMessages) {
		t.Fatalf("out of range: %v", err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
