package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	h := Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP = %q", csp)
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options")
	}
	// Without a UI build (plain `go test`), a hint page is served instead.
	switch rec.Code {
	case http.StatusOK:
		if !strings.Contains(rec.Body.String(), `<div id="root">`) {
			t.Errorf("unexpected index: %s", rec.Body.String())
		}
	case http.StatusServiceUnavailable:
		if !strings.Contains(rec.Body.String(), "make web") {
			t.Errorf("unexpected hint: %s", rec.Body.String())
		}
	default:
		t.Fatalf("status %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/does-not-exist.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing asset: status %d", rec.Code)
	}
}

func TestRootFiles(t *testing.T) {
	h := Handler()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	// With a UI build the manifest is served with its type; without one
	// (plain `go test`) the hint page answers instead.
	rec := get("/manifest.webmanifest")
	switch rec.Code {
	case http.StatusOK:
		if ct := rec.Header().Get("Content-Type"); ct != "application/manifest+json" {
			t.Errorf("manifest Content-Type = %q", ct)
		}
		if !strings.Contains(rec.Body.String(), `"start_url"`) {
			t.Errorf("unexpected manifest: %s", rec.Body.String())
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
			t.Error("manifest without the UI's CSP")
		}
	case http.StatusServiceUnavailable:
	default:
		t.Fatalf("manifest: status %d", rec.Code)
	}
	// Other files of the build root are not served by name.
	if rec := get("/.keep"); strings.HasPrefix(rec.Header().Get("Content-Type"), "image/") ||
		rec.Header().Get("Content-Type") == "application/manifest+json" || rec.Body.Len() == 0 {
		t.Errorf("/.keep: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
