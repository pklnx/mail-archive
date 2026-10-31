package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// The Log page sends its local days as RFC 3339 instants of local midnight,
// with milliseconds and Z (auditQuery in web/src/api.ts, #402). A plain date
// stays a UTC day, for the CLI and the documented API.
func TestParseAuditBound(t *testing.T) {
	for _, c := range []struct {
		in   string
		end  bool
		want string
	}{
		{"2026-10-31", false, "2026-10-31T00:00:00Z"},
		{"2026-10-31", true, "2026-11-01T00:00:00Z"},
		{"2026-10-30T23:00:00.000Z", false, "2026-10-30T23:00:00Z"},
		{"2026-10-31T23:00:00.000Z", true, "2026-10-31T23:00:00Z"},
		{"2026-10-31T00:00:00+01:00", false, "2026-10-30T23:00:00Z"},
	} {
		got, err := parseAuditBound(c.in, c.end)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if s := got.UTC().Format(time.RFC3339); s != c.want {
			t.Errorf("%s (end %v): got %s, want %s", c.in, c.end, s, c.want)
		}
	}
	for _, in := range []string{"31.10.2026", "2026-10-31T23:00", ""} {
		if _, err := parseAuditBound(in, false); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}

// At 00:03 on 31 October in Berlin (23:03 UTC), the UI's bounds for that day
// are accepted; the plain day the UI sent before #402 is read as UTC midnight,
// after the default until, and refused.
func TestAuditBoundsJustAfterBerlinMidnight(t *testing.T) {
	st := storetest.New(t)
	now := time.Date(2026, 10, 30, 23, 3, 0, 0, time.UTC)
	s := New(st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{AllowedHosts: []string{"127.0.0.1"}, Now: func() time.Time { return now }})
	srv := httptest.NewServer(loggedIn(t, st, s.Handler()))
	t.Cleanup(srv.Close)
	for _, c := range []struct {
		query string
		want  int
	}{
		{"since=2026-10-30T23:00:00.000Z", http.StatusOK},
		{"since=2026-10-30T23:00:00.000Z&until=2026-10-31T23:00:00.000Z", http.StatusOK},
		{"since=2026-10-31", http.StatusBadRequest},
	} {
		resp, err := http.Get(srv.URL + "/api/audit?" + c.query)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d: %s", c.query, resp.StatusCode, c.want, body)
		}
	}
}
