package ui

import (
	"maps"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"slices"
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
	if got := rec.Header().Get("Permissions-Policy"); got != "camera=(), microphone=(), geolocation=()" {
		t.Errorf("Permissions-Policy = %q", got)
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
	// With a UI build every root file is served by name with its type, the
	// PNG favicon (#237) among them; without one the hint page answers.
	for _, p := range RootPaths() {
		rec := get(p)
		switch rec.Code {
		case http.StatusOK:
			if ct := rec.Header().Get("Content-Type"); ct != rootFiles[p] {
				t.Errorf("%s Content-Type = %q, want %q", p, ct, rootFiles[p])
			}
			if rec.Body.Len() == 0 {
				t.Errorf("%s: empty body", p)
			}
		case http.StatusServiceUnavailable:
		default:
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}
	if _, ok := rootFiles["/favicon-32.png"]; !ok {
		t.Error("/favicon-32.png is not a root file")
	}
	// Other files of the build root are not served by name.
	if rec := get("/.keep"); strings.HasPrefix(rec.Header().Get("Content-Type"), "image/") ||
		rec.Header().Get("Content-Type") == "application/manifest+json" || rec.Body.Len() == 0 {
		t.Errorf("/.keep: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// TestRootFilesMatchPublic guards the next file added to web/public: the
// server routes only the rootFiles by name, so a file in web/public without
// an entry here would answer 404 (#237: favicon-32.png).
func TestRootFilesMatchPublic(t *testing.T) {
	entries, err := os.ReadDir("../../../web/public")
	if err != nil {
		t.Fatal(err)
	}
	var public []string
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("web/public/%s: subdirectories are not served", e.Name())
			continue
		}
		public = append(public, "/"+e.Name())
	}
	slices.Sort(public)
	if got := slices.Sorted(maps.Keys(rootFiles)); !slices.Equal(got, public) {
		t.Errorf("rootFiles = %v, web/public = %v", got, public)
	}
	for _, p := range public {
		want := mime.TypeByExtension(path.Ext(p))
		if path.Ext(p) == ".webmanifest" {
			want = "application/manifest+json"
		}
		if got := rootFiles[p]; got == "" || got != want {
			t.Errorf("%s: type %q, want %q", p, got, want)
		}
	}
	if !slices.Contains(public, "/favicon-32.png") {
		t.Error("web/public has no favicon-32.png")
	}
}
