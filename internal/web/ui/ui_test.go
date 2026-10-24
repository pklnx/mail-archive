package ui

import (
	"bytes"
	"maps"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
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

// rootBuild is a build root with index.html, every root file (distinct bytes
// each), a hashed asset and a file that is never served by name.
func rootBuild() fstest.MapFS {
	files := fstest.MapFS{
		"index.html":      {Data: []byte(`<!doctype html><div id="root"></div>`)},
		".keep":           {Data: []byte("keep")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
	}
	for _, path := range RootPaths() {
		files[strings.TrimPrefix(path, "/")] = &fstest.MapFile{Data: []byte("root file " + path)}
	}
	return files
}

// TestRootFilesServed checks the web app manifest and its icons through the
// handler on a fake build, so it runs without a UI build (the Go test job has
// none): each file is served at its exact path only, with its type and the
// UI's headers (#242).
func TestRootFilesServed(t *testing.T) {
	files := rootBuild()
	h := handler(files)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	checkUI := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if got := rec.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
			t.Errorf("CSP = %q", got)
		}
		if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("X-Frame-Options = %q", got)
		}
		if got := rec.Header().Get("Permissions-Policy"); got != permissionsPolicy {
			t.Errorf("Permissions-Policy = %q", got)
		}
	}
	index := files["index.html"].Data

	if len(RootPaths()) != 6 {
		t.Fatalf("root paths: %v", RootPaths())
	}
	for _, path := range RootPaths() {
		rec := get(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if want := files[strings.TrimPrefix(path, "/")].Data; !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("%s: body %q, want %q", path, rec.Body.Bytes(), want)
		}
		if got := rec.Header().Get("Content-Type"); got != rootFiles[path] {
			t.Errorf("%s: Content-Type %q, want %q", path, got, rootFiles[path])
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control %q", path, got)
		}
		checkUI(t, rec)
	}

	// Near misses never return a root file, neither its bytes nor its type.
	for _, path := range []string{
		"/.keep", "/icon-192.png/", "/assets/../icon-192.png", "/assets/%2e%2e/icon-192.png",
		"/Icon-192.png", "/icon-192.PNG", "/manifest.webmanifest/",
	} {
		rec := get(path)
		body := rec.Body.String()
		if strings.HasPrefix(body, "root file ") || body == "keep" {
			t.Errorf("%s: served a root file: %d %q", path, rec.Code, body)
		}
		if typ := rec.Header().Get("Content-Type"); typ == "image/png" || typ == "application/manifest+json" {
			t.Errorf("%s: Content-Type %q", path, typ)
		}
		checkUI(t, rec)
	}

	// No path under /assets/ reaches a file outside the assets folder, in
	// any spelling of "..": not a root file, not .keep, not index.html. The
	// escaped spellings reach the handler undecoded through the server's
	// mux (TestUIAssetsStayInTheirFolder in internal/web); here they are
	// the same request as the plain one.
	var outside []string
	for name := range files {
		if !strings.HasPrefix(name, "assets/") {
			outside = append(outside, name)
		}
	}
	for _, up := range []string{"../", "%2e%2e/", "%2E%2E/", ".%2e/", "%2e./", "..%2f", "%2e%2e%2f", "%2E%2E%2F"} {
		for _, name := range outside {
			path := "/assets/" + up + name
			rec := get(path)
			// The file server sends a request for index.html to its folder.
			if rec.Code != http.StatusNotFound && (name != "index.html" || rec.Code != http.StatusMovedPermanently) {
				t.Errorf("%s: status %d, want 404", path, rec.Code)
			}
			body := rec.Body.Bytes()
			if strings.HasPrefix(string(body), "root file ") || string(body) == "keep" || bytes.Equal(body, index) {
				t.Errorf("%s: served a file outside assets/: %q", path, body)
			}
			if typ := rec.Header().Get("Content-Type"); typ == "image/png" || typ == "application/manifest+json" {
				t.Errorf("%s: Content-Type %q", path, typ)
			}
			checkUI(t, rec)
		}
	}
	// The folder above assets/ is no listing of the build either.
	for _, path := range []string{"/assets/../", "/assets/%2e%2e/", "/assets/%2E%2E/", "/assets/.%2e/"} {
		rec := get(path)
		if body := rec.Body.Bytes(); bytes.Equal(body, index) || strings.Contains(string(body), "index.html") {
			t.Errorf("%s: served the build root: %d %q", path, rec.Code, body)
		}
		checkUI(t, rec)
	}

	// /assets/ still serves the build's assets, immutable.
	rec := get("/assets/app-1.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Errorf("asset: %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset: Cache-Control %q", got)
	}
	checkUI(t, rec)

	// The index runs here too, which a test without a UI build never sees.
	rec = get("/")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), index) {
		t.Errorf("index: %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("index: Content-Type %q", got)
	}
	checkUI(t, rec)

	// A root file missing from the build answers 404, not index.html.
	delete(files, "favicon-32.png")
	h = handler(files)
	rec = get("/favicon-32.png")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing root file: status %d", rec.Code)
	}
	if bytes.Equal(rec.Body.Bytes(), index) || strings.Contains(rec.Body.String(), `<div id="root">`) {
		t.Errorf("missing root file: index.html body %q", rec.Body.String())
	}
	if typ := rec.Header().Get("Content-Type"); strings.HasPrefix(typ, "text/html") || typ == "image/png" {
		t.Errorf("missing root file: Content-Type %q", typ)
	}
	checkUI(t, rec)
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
