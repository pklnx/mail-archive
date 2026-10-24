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
