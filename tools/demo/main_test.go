package main

import (
	"strings"
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

// The server gives a mail with inline SVG or MathML no remote images (the
// bytes "<svg" or "<math", any case; foreignContent in internal/web). The
// mails whose remote images the e2e specs load (referrer.spec.ts,
// css-images.spec.ts' controls, reading.spec.ts' long mail) must not have
// them; the stereoscope mail must.
func TestRemoteImageMailsHaveNoSVG(t *testing.T) {
	d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	foreign := func(raw string) bool {
		l := strings.ToLower(raw)
		return strings.Contains(l, "<svg") || strings.Contains(l, "<math")
	}
	mails := map[string]message{"periscope": hintCheck(d), "semaphore": referrerCheck(d), "zoetrope": cssCheck(d)}
	for i, m := range offsite(func(days, hours int) time.Time { return d }) {
		mails["offsite "+string(rune('0'+i))] = m
	}
	for name, m := range mails {
		if foreign(m.raw) {
			t.Errorf("%s has inline SVG or MathML: its remote images would be refused", name)
		}
	}
	if !foreign(cssSVGCheck(d).raw) {
		t.Error("stereoscope has no inline SVG or MathML")
	}
}
