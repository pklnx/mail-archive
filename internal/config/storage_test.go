package config

import (
	"strings"
	"testing"
)

func TestParseByteSize(t *testing.T) {
	for in, want := range map[string]int64{
		"0": 0, "1024": 1024, "1KiB": 1 << 10, "500GiB": 500 << 30, "2TiB": 2 << 40, "2 tib": 2 << 40,
		"1GB": 1e9, "10G": 10 << 30, "3T": 3 << 40, "1PiB": 1 << 50, "100B": 100, " 7M ": 7 << 20, "1024PiB": 1 << 60,
	} {
		if got, err := ParseByteSize(in); err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "-1", "1.5GiB", "GiB", "1XB", "1025PiB", "9223372036854775807K", "abc"} {
		if n, err := ParseByteSize(bad); err == nil {
			t.Errorf("ParseByteSize(%q) = %d, want an error", bad, n)
		}
	}
}

// The storage settings have their defaults, accept their range ends and
// refuse what lies outside, with an error naming the variable.
func TestParseStorage(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	got, err := ParseStorage(env(nil))
	want := Storage{Quota: 0, WarnPercent: 80, OverflowPercent: 10, MinFreeDisk: 1 << 30}
	if err != nil || got != want {
		t.Fatalf("defaults = %+v, %v; want %+v", got, err, want)
	}
	got, err = ParseStorage(env(map[string]string{
		EnvStorageQuota: "2TiB", EnvStorageWarnPercent: "99", EnvStorageSMTPOverflowPercent: "0", EnvMinFreeDisk: "0",
	}))
	want = Storage{Quota: 2 << 40, WarnPercent: 99, OverflowPercent: 0, MinFreeDisk: 0}
	if err != nil || got != want {
		t.Fatalf("set = %+v, %v; want %+v", got, err, want)
	}
	for env1, bads := range map[string][]string{
		EnvStorageQuota:               {"-5", "lots", "1.5T"},
		EnvStorageWarnPercent:         {"0", "100", "80%", "x"},
		EnvStorageSMTPOverflowPercent: {"-1", "101", "ten"},
		EnvMinFreeDisk:                {"-1", "1 GiBs"},
	} {
		for _, v := range bads {
			if _, err := ParseStorage(env(map[string]string{env1: v})); err == nil || !strings.HasPrefix(err.Error(), env1+": ") {
				t.Errorf("%s=%q: err = %v, want an error naming the variable", env1, v, err)
			}
		}
	}
}
