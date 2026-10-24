package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/web/ui"
)

// TestUIAssetsStayInTheirFolder sends the escaped spellings of ".." to
// /assets/ through the real route table, protect and authenticate (#242).
// The mux does not clean "%2e%2e", so only here can the request reach the UI
// handler undecoded, as a client that sends the path as it is (curl
// --path-as-is) makes it. Nothing outside the build's assets folder may be
// served, cached for a year. The embedded build holds at least dist/.keep
// (a UI build adds the root files), so the test needs neither a build nor a
// database.
func TestUIAssetsStayInTheirFolder(t *testing.T) {
	h := (&Server{allowedHosts: []string{"mail.example"}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "https://mail.example"+path, nil))
		return rec
	}
	outside := append([]string{"/.keep"}, ui.RootPaths()...)
	for _, up := range []string{"../", "%2e%2e/", "%2E%2E/", ".%2e/", "%2e./", "..%2f", "%2e%2e%2f", "%2E%2E%2F"} {
		for _, name := range outside {
			path := "/assets/" + up + strings.TrimPrefix(name, "/")
			rec := get(path)
			// The mux redirects a plain ".." to the cleaned path, which is
			// no file under /assets/ either.
			if rec.Code == http.StatusOK || strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
				t.Errorf("%s: status %d, Cache-Control %q, body %q", path, rec.Code,
					rec.Header().Get("Cache-Control"), rec.Body.String())
			}
		}
	}
	// The build root is no listing under /assets/%2e%2e/ either.
	for _, path := range []string{"/assets/%2e%2e/", "/assets/%2E%2E/", "/assets/.%2e/"} {
		rec := get(path)
		if body := rec.Body.String(); strings.Contains(body, ".keep") || strings.Contains(body, "index.html") {
			t.Errorf("%s: served the build root: %d %q", path, rec.Code, body)
		}
	}
}

// TestUIAssetsAnswers sends the paths that are no asset file to /assets/
// through the real route table, protect and authenticate (#250). Each answers
// 404 and is not cached, immutable included; the UI's policies are on every
// answer, also those of the hint page or index at "/". The embedded build is
// whatever `make web` left (CI: only dist/.keep), so only headers are
// compared at "/", and the files themselves are tested in internal/web/ui.
func TestUIAssetsAnswers(t *testing.T) {
	h := (&Server{allowedHosts: []string{"mail.example"}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "https://mail.example"+path, nil))
		return rec
	}
	// "/%61ssets/" is the folder under another spelling: the mux routes on
	// the decoded path, so it reaches the UI handler.
	for _, path := range []string{
		"/%61ssets/nope.js", "/assets/nope.js", "/assets/x/index.html", "/assets/%2e%2e/index.html",
		"/assets/", "/assets/sub/",
	} {
		rec := get(path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control %q, want no-cache", path, got)
		}
		if rec.Header().Get("Location") != "" {
			t.Errorf("%s: redirects to %q", path, rec.Header().Get("Location"))
		}
		checkUIPolicies(t, path, rec)
	}
	// "/" is the index or, without a build, the hint page (status not pinned).
	rec := get("/")
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("/: Cache-Control %q", got)
	}
	checkUIPolicies(t, "/", rec)
	// The mux sends "/assets" to "/assets/" itself, never as immutable.
	rec = get("/assets")
	if rec.Code != http.StatusTemporaryRedirect || strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("/assets: status %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

// checkUIPolicies checks the headers the UI handler puts on every answer.
func checkUIPolicies(t *testing.T, path string, rec *httptest.ResponseRecorder) {
	t.Helper()
	for name, want := range map[string]string{
		"Cross-Origin-Resource-Policy": "same-origin",
		"X-Frame-Options":              "DENY",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s: %s = %q, want %q", path, name, got, want)
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("%s: Content-Security-Policy = %q", path, rec.Header().Get("Content-Security-Policy"))
	}
}
