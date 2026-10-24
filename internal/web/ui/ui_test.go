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
// each), hashed assets and a file that is never served by name.
func rootBuild() fstest.MapFS {
	files := fstest.MapFS{
		"index.html":       {Data: []byte(`<!doctype html><div id="root"></div>`)},
		".keep":            {Data: []byte("keep")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"assets/sub/a.css": {Data: []byte("a{color:red}")},
		"assets/x.mjs":     {Data: []byte("export default 1")},
	}
	for _, path := range RootPaths() {
		files[strings.TrimPrefix(path, "/")] = &fstest.MapFile{Data: []byte("root file " + path)}
	}
	return files
}

// checkUI checks the headers every UI answer carries: assets, root files,
// 404s and the hint page alike.
func checkUI(t *testing.T, rec *httptest.ResponseRecorder) {
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
	if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Errorf("Cross-Origin-Resource-Policy = %q", got)
	}
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
			if rec.Code != http.StatusNotFound {
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

// TestAssetsFilesOnly checks /assets/ on a fake build: the regular files
// under it are served, cached for a year; everything else answers 404 with
// no listing and no redirect, and is not cached (#250).
func TestAssetsFilesOnly(t *testing.T) {
	files := rootBuild()
	h := handler(files)
	do := func(method, path string, header ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	const immutable = "public, max-age=31536000, immutable"

	t.Run("no listing, no redirect", func(t *testing.T) {
		for _, path := range []string{
			"/assets/", "/assets/sub/", "/assets/sub", "/assets/.", "/assets/sub/.",
			"/assets/%2e%2e/", "/assets/sub/index.html", "/assets/%2e%2e/index.html",
			"/assets/app-1.js/", "/assets/nope.js", "/assets/sub/nope.css",
		} {
			rec := do("GET", path)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: status %d, want 404", path, rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Errorf("%s: redirects to %q", path, loc)
			}
			body := rec.Body.String()
			for _, leak := range []string{"app-1.js", "a.css", "x.mjs", "sub/", "<a href"} {
				if strings.Contains(body, leak) {
					t.Errorf("%s: body names %q: %q", path, leak, body)
				}
			}
			// Only a file is immutable, so a missing one is not cached.
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("%s: Cache-Control %q, want no-cache", path, got)
			}
			checkUI(t, rec)
		}
	})

	t.Run("files are served", func(t *testing.T) {
		for _, tc := range []struct{ path, name string }{
			{"/assets/app-1.js", "assets/app-1.js"},
			{"/assets/sub/a.css", "assets/sub/a.css"},
			{"/assets/x.mjs", "assets/x.mjs"},
			// An encoded spelling of the folder is the same file (#250).
			{"/%61ssets/app-1.js", "assets/app-1.js"},
			{"/assets/%61pp-1.js", "assets/app-1.js"},
		} {
			rec := do("GET", tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status %d", tc.path, rec.Code)
			}
			if want := files[tc.name].Data; !bytes.Equal(rec.Body.Bytes(), want) {
				t.Errorf("%s: body %q, want %q", tc.path, rec.Body.Bytes(), want)
			}
			// The type comes from the extension; the host's table decides
			// the exact string.
			want := mime.TypeByExtension(path.Ext(tc.path))
			if want == "" {
				t.Fatalf("%s: the host has no type for %s", tc.path, path.Ext(tc.path))
			}
			if got := rec.Header().Get("Content-Type"); got != want {
				t.Errorf("%s: Content-Type %q, want %q", tc.path, got, want)
			}
			if got := rec.Header().Get("Cache-Control"); got != immutable {
				t.Errorf("%s: Cache-Control %q", tc.path, got)
			}
			checkUI(t, rec)
		}
	})

	t.Run("head and range", func(t *testing.T) {
		rec := do("HEAD", "/assets/app-1.js")
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Errorf("HEAD: %d %q", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != immutable {
			t.Errorf("HEAD: Cache-Control %q", got)
		}

		// A part of the same file is as immutable as the file.
		rec = do("GET", "/assets/app-1.js", "Range", "bytes=0-6")
		if rec.Code != http.StatusPartialContent || rec.Body.String() != "console" {
			t.Errorf("range: %d %q", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Range"); got != "bytes 0-6/14" {
			t.Errorf("range: Content-Range %q", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != immutable {
			t.Errorf("range: Cache-Control %q", got)
		}
		checkUI(t, rec)

		// The exceptions to "a non-200 answer is no-cache", all from
		// http.ServeContent after the file was found: a 416 has no
		// Cache-Control, a 304 and a 412 keep immutable (documented on
		// serveAsset).
		rec = do("GET", "/assets/app-1.js", "Range", "bytes=100-")
		if rec.Code != http.StatusRequestedRangeNotSatisfiable {
			t.Errorf("unsatisfiable range: status %d", rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "" {
			t.Errorf("unsatisfiable range: Cache-Control %q, want none", got)
		}
		rec = do("GET", "/assets/app-1.js", "If-None-Match", "*")
		if rec.Code != http.StatusNotModified || rec.Header().Get("Cache-Control") != immutable {
			t.Errorf("If-None-Match: %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
		}
		rec = do("GET", "/assets/app-1.js", "If-Match", `"x"`)
		if rec.Code != http.StatusPreconditionFailed || rec.Header().Get("Cache-Control") != immutable {
			t.Errorf("If-Match: %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
		}
	})
}
