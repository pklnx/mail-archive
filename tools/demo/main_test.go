package main

import (
	"testing"
	"time"
)

func TestParseNow(t *testing.T) {
	wall := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := func() time.Time { return wall }

	got, err := parseNow("", clock)
	if err != nil || !got.Equal(wall) {
		t.Fatalf("empty: got %v, %v; want the wall clock %v", got, err, wall)
	}

	got, err = parseNow("2026-10-23T08:41:00Z", clock)
	want := time.Date(2026, 10, 23, 8, 41, 0, 0, time.UTC)
	if err != nil || !got.Equal(want) {
		t.Fatalf("RFC 3339: got %v, %v; want %v", got, err, want)
	}

	got, err = parseNow("2026-10-23T10:41:00+02:00", clock)
	if err != nil || !got.Equal(want) {
		t.Fatalf("RFC 3339 with offset: got %v, %v; want %v", got, err, want)
	}

	for _, bad := range []string{"2026-10-23", "yesterday", "2026-10-23 08:41:00"} {
		if _, err := parseNow(bad, clock); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
