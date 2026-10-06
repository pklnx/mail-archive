package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProtect(t *testing.T) {
	s := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := s.protect(ok)

	cases := []struct {
		name    string
		method  string
		host    string
		headers map[string]string
		want    int
	}{
		{"get localhost", "GET", "localhost:8080", nil, 204},
		{"get 127.0.0.1", "GET", "127.0.0.1:8080", nil, 204},
		{"get ipv6", "GET", "[::1]:8080", nil, 204},
		{"get uppercase host", "GET", "LOCALHOST:8080", nil, 204},
		{"dns rebinding host", "GET", "evil.example:8080", nil, 403},
		{"post without origin", "POST", "localhost:8080", map[string]string{"Content-Type": "application/json"}, 403},
		{"post cross origin", "POST", "localhost:8080", map[string]string{"Origin": "http://evil.example", "Content-Type": "application/json"}, 403},
		{"post other port", "POST", "localhost:8080", map[string]string{"Origin": "http://localhost:3000", "Content-Type": "application/json"}, 403},
		{"post form from same origin", "POST", "localhost:8080", map[string]string{"Origin": "http://localhost:8080", "Content-Type": "application/x-www-form-urlencoded"}, 415},
		{"post json same origin", "POST", "localhost:8080", map[string]string{"Origin": "http://localhost:8080", "Content-Type": "application/json; charset=utf-8"}, 204},
		{"post json sec-fetch-site", "POST", "localhost:8080", map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}, 204},
		{"post json cross-site fetch", "POST", "localhost:8080", map[string]string{"Sec-Fetch-Site": "cross-site", "Content-Type": "application/json"}, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, "http://x/api/test", strings.NewReader("{}"))
			req.Host = c.host
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d", rec.Code, c.want)
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Error("missing nosniff header")
			}
		})
	}
}

func TestCursorRoundTrip(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	at, got, err := decodeCursor(encodeCursor(parseTime(t, "2026-10-05T10:00:00.123456Z"), sha))
	if err != nil || got != sha || !at.Equal(parseTime(t, "2026-10-05T10:00:00.123456Z")) {
		t.Fatalf("round trip: %v %q %v", at, got, err)
	}
	for _, bad := range []string{"!!", "bm9wZQ", encodeCursor(parseTime(t, "2026-01-01T00:00:00Z"), "../etc")} {
		if _, _, err := decodeCursor(bad); err == nil {
			t.Errorf("decodeCursor(%q) succeeded", bad)
		}
	}
}
