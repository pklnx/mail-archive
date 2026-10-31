package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Fetch Metadata guard (#203): the HTML mail frame shares the app's
// origin, so its images and links reach /api with the session cookie. A
// navigation passes only to the routes a browser opens, other destinations
// than a fetch never, and requests without the headers as before.
func TestFetchMetadataGuard(t *testing.T) {
	s := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{AllowedHosts: []string{"x"}})
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := s.protect(ok)
	sha := strings.Repeat("0a", 32)
	msg := "/api/messages/" + sha

	type headers = map[string]string
	nav := func(dest string) headers {
		return headers{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": dest}
	}
	sub := func(dest string) headers {
		return headers{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": dest}
	}
	fetch := headers{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}
	download := headers{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "empty"}

	type c struct {
		method, path string
		headers      headers
		want         int
	}
	cases := []c{
		// No Fetch Metadata: API clients and old browsers, as before.
		{"GET", "/api/status", nil, 204},
		{"GET", msg + "/conversation", nil, 204},
		// The app's own fetch calls.
		{"GET", "/api/messages?q=x", fetch, 204},
		{"GET", msg + "/html", fetch, 204},
		{"GET", "/api/status", headers{"Sec-Fetch-Dest": "empty"}, 204},
		// The routes a browser navigates to: the frame, the frame opened on
		// its own, the .eml download, attachments with and without a name,
		// and the API description.
		{"GET", msg + "/html", nav("iframe"), 204},
		{"GET", msg + "/html?images=1", nav("iframe"), 204},
		{"GET", msg + "/html", nav("document"), 204},
		{"HEAD", msg + "/html", nav("document"), 204},
		{"GET", msg + "/raw", download, 204},
		{"GET", msg + "/raw", nav("document"), 204},
		{"GET", msg + "/parts/1", download, 204},
		{"GET", msg + "/parts/12", nav("document"), 204},
		{"GET", "/api/openapi.json", nav("document"), 204},
		{"GET", msg + "/html", headers{"Sec-Fetch-Dest": "iframe"}, 204}, // dest without mode
		// Navigations to any other route: a link in a mail.
		{"GET", "/api/status", nav("document"), 403},
		{"GET", "/api/status", nav("iframe"), 403},
		{"GET", "/api/messages?q=x", nav("document"), 403},
		{"GET", "/api/accounts/a/server-folders", nav("document"), 403},
		{"GET", msg, nav("document"), 403},
		{"GET", msg + "/conversation", nav("iframe"), 403},
		{"GET", "/api/status", download, 403},
		{"GET", "/api/status", headers{"Sec-Fetch-Mode": "navigate"}, 403},
		{"GET", "/api/status", headers{"Sec-Fetch-Dest": "document"}, 403},
		{"GET", "/api", nav("document"), 403},
		{"GET", "/api/", nav("document"), 403},
		// The exact route shape: no prefix, suffix, dot segments or escapes.
		{"GET", msg + "/html/", nav("document"), 403},
		{"GET", msg + "/htmlx", nav("document"), 403},
		{"GET", msg + "/html/../../../status", nav("document"), 403},
		{"GET", msg + "/../status", nav("document"), 403},
		{"GET", "/api/messages/" + strings.ToUpper(sha) + "/html", nav("document"), 403},
		{"GET", "/api/messages/" + sha[:63] + "/html", nav("document"), 403},
		{"GET", "/api/messages/" + sha + "0/html", nav("document"), 403},
		{"GET", msg + "/parts/", nav("document"), 403},
		{"GET", msg + "/parts/-1", nav("document"), 403},
		{"GET", msg + "/parts/1/x", nav("document"), 403},
		{"GET", msg + "/%68tml", nav("document"), 403},
		{"GET", "/api/openapi.json/x", nav("document"), 403},
		{"GET", "/x/api/openapi.json", nav("document"), 204}, // not /api at all
		// Only GET and HEAD navigate to the allowed routes.
		{"POST", msg + "/html", nav("document"), 403},
		// Any other destination than a fetch, on any /api route.
		{"GET", "/api/messages?q=x", sub("image"), 403},
		{"GET", "/api/status", sub("image"), 403},
		{"GET", msg + "/raw", sub("image"), 403},
		{"GET", msg + "/parts/1", sub("image"), 403},
		{"GET", msg + "/html", sub("image"), 403},
		{"GET", "/api/openapi.json", sub("image"), 403},
		// Prefetch, also of an allowed route and as a fetch.
		{"GET", msg + "/raw", headers{"Sec-Purpose": "prefetch", "Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "no-cors"}, 403},
		{"GET", "/api/status", headers{"Sec-Purpose": "prefetch;prerender"}, 403},
		{"GET", msg + "/html", headers{"Sec-Purpose": "prefetch", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}, 403},
		// Paths the mux routes or redirects to /api are guarded too.
		{"GET", "/%61pi/status", sub("image"), 403},
		{"GET", "/assets/../api/status", sub("image"), 403},
		{"GET", "//api/status", sub("image"), 403},
		// The UI is not affected.
		{"GET", "/", nav("document"), 204},
		{"GET", "/assets/app.css", sub("style"), 204},
		{"GET", "/icon.svg", sub("image"), 204},
		{"GET", "/apikeys", sub("image"), 204},
	}
	// A navigation to a route a browser opens counts only from the app's own
	// origin, typed or bookmarked (none), or without the header: a link in a
	// mail to another site that redirects back is cross-site (#459).
	for _, p := range []string{msg + "/html", msg + "/html?images=1", msg + "/raw", msg + "/parts/1", "/api/openapi.json"} {
		for _, d := range []string{"document", "iframe", "empty"} {
			for site, want := range map[string]int{"cross-site": 403, "same-site": 403, "CROSS-SITE": 403, "bogus": 403, "same-origin": 204, "none": 204, "": 204} {
				h := headers{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": d}
				if site != "" {
					h["Sec-Fetch-Site"] = site
				}
				cases = append(cases, c{"GET", p, h, want})
			}
		}
		// A fetch from another site is not a navigation; CORS keeps its answer from the page.
		cases = append(cases, c{"GET", p, headers{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}, 204})
	}
	for _, d := range []string{"image", "style", "font", "script", "audio", "video", "track", "object", "embed", "manifest", "worker", "sharedworker", "serviceworker", "report", "xslt", "audioworklet", "paintworklet", "frame"} {
		for _, p := range []string{msg + "/raw", "/api/messages?q=x", "/api/status"} {
			want := 403
			if navigationDests[d] && p == msg+"/raw" {
				// A navigation (into an object, embed or frame) to a route a
				// browser opens. The frame's CSP (default-src 'none') does
				// not load these elements at all.
				want = 204
			}
			cases = append(cases, c{"GET", p, sub(d), want})
		}
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://x"+c.path, nil)
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		if c.method == "POST" {
			req.Header.Set("Origin", "http://x")
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s %v: status %d, want %d", c.method, c.path, c.headers, rec.Code, c.want)
		}
		if rec.Code == 403 && rec.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
			t.Errorf("%s %s: 403 without CORP", c.method, c.path)
		}
	}
}

// With a session cookie, as the browser sends it for a same-origin link or
// image in the mail frame: refused before the handler, so no audit entry is
// written; the allowed routes still work.
func TestFetchMetadataGuardWithSession(t *testing.T) {
	f := newAPIFixture(t)
	msg := "/api/messages/" + f.ids["rich"]
	do := func(path string, h map[string]string) int {
		t.Helper()
		req, _ := http.NewRequest("GET", f.srv.URL+path, nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req) // f.srv adds the session cookie
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	nav := func(dest string) map[string]string {
		return map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": dest, "Sec-Fetch-User": "?1"}
	}
	image := map[string]string{"Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image"}

	n := len(auditLog(t, f.st))
	for _, p := range []string{"/api/messages?q=x", "/api/status", "/api/accounts/alice/server-folders", msg, msg + "/conversation"} {
		for _, d := range []string{"document", "iframe"} {
			if got := do(p, nav(d)); got != http.StatusForbidden {
				t.Errorf("navigation (%s) to %s: %d", d, p, got)
			}
		}
	}
	for _, p := range []string{"/api/messages?q=x", "/api/status", msg + "/raw", msg + "/parts/1", msg + "/html"} {
		if got := do(p, image); got != http.StatusForbidden {
			t.Errorf("image %s: %d", p, got)
		}
	}
	if got := do(msg+"/raw", map[string]string{"Sec-Purpose": "prefetch", "Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "empty"}); got != http.StatusForbidden {
		t.Errorf("prefetch: %d", got)
	}
	if got := newEntries(t, f.st, n); len(got) != 0 {
		t.Errorf("refused requests wrote audit entries: %+v", got)
	}

	// The routes a browser opens pass, and are recorded as usual.
	for _, c := range []struct {
		path string
		h    map[string]string
	}{
		{msg + "/html", nav("iframe")},
		{msg + "/html", nav("document")},
		{msg + "/raw", nav("document")},
		{msg + "/raw", map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "empty"}},
		{msg + "/parts/1", nav("document")},
		{msg + "/parts/1", map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "empty"}},
		{openAPIPath, nav("document")},
		{"/api/status", map[string]string{"Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}},
		{"/api/status", nil},
	} {
		if got := do(c.path, c.h); got != http.StatusOK {
			t.Errorf("%s %v: %d", c.path, c.h, got)
		}
	}
	if got := newEntries(t, f.st, n); len(got) != 6 {
		t.Errorf("allowed requests: %d audit entries, want 6 (html twice, raw twice, part twice)", len(got))
	}
}

// A navigation to a route a browser opens that the browser marks as coming
// from another site is refused through the real route table, before
// anything is logged (#459): an external link in a mail that redirects back
// to the archive. Chromium sends the session cookie with it, because the
// navigation began in the mail frame, which has the app's origin. The same
// routes still open from the app (same-origin), typed or bookmarked (none)
// and without the header.
func TestFetchMetadataGuardCrossSiteNavigation(t *testing.T) {
	f := newAPIFixture(t)
	msg := "/api/messages/" + f.ids["rich"]
	do := func(path, site, dest string) int {
		t.Helper()
		req, _ := http.NewRequest("GET", f.srv.URL+path, nil)
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Dest", dest)
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		resp, err := http.DefaultClient.Do(req) // f.srv adds the session cookie
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	routes := []string{msg + "/html", msg + "/raw", msg + "/parts/1", openAPIPath}
	n := len(auditLog(t, f.st))
	for _, p := range routes {
		for _, site := range []string{"cross-site", "same-site"} {
			for _, dest := range []string{"document", "iframe", "empty"} {
				if got := do(p, site, dest); got != http.StatusForbidden {
					t.Errorf("%s navigation (%s) to %s: %d", site, dest, p, got)
				}
			}
		}
	}
	if got := newEntries(t, f.st, n); len(got) != 0 {
		t.Errorf("refused navigations wrote audit entries: %+v", got)
	}
	for _, p := range routes {
		for _, site := range []string{"same-origin", "none", ""} {
			if got := do(p, site, "document"); got != http.StatusOK {
				t.Errorf("%q navigation to %s: %d", site, p, got)
			}
		}
	}
	// html, raw and the part, three times each.
	if got := newEntries(t, f.st, n); len(got) != 9 {
		t.Errorf("allowed navigations: %d audit entries, want 9", len(got))
	}
}
