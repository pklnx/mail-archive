package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keepia/archive/internal/testwait"
)

// TestStartingHandler (#396): while serve starts, /healthz answers 503
// {"status":"starting"} and every other path 503 with Retry-After, behind
// the same Host check and headers as the app; the public URL's host is
// allowed as in the app.
func TestStartingHandler(t *testing.T) {
	h := Starting(slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		AllowedHosts: []string{"archive.lan"}, PublicURL: "https://mail.example.org",
		// Only these count: a starting server has no store or sealer.
		Require2FA: true,
	})
	for _, c := range []struct {
		name, method, path, host string
		want                     int
		status                   string // the JSON status, for /healthz
	}{
		{"healthz", "GET", "/healthz", "archive.lan:8080", 503, "starting"},
		{"healthz head", "HEAD", "/healthz", "archive.lan", 503, ""},
		{"healthz public url host", "GET", "/healthz", "mail.example.org", 503, "starting"},
		{"api", "GET", "/api/session", "archive.lan", 503, ""},
		{"ui", "GET", "/", "archive.lan", 503, ""},
		{"healthz sync", "GET", "/healthz/sync", "archive.lan", 503, ""},
		{"wrong host healthz", "GET", "/healthz", "evil.example", 403, ""},
		{"wrong host api", "GET", "/api/session", "evil.example", 403, ""},
		{"cross-site post", "POST", "/api/session", "archive.lan", 403, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, "http://x"+c.path, nil)
			req.Host = c.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.want, rec.Body)
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("headers %v", rec.Header())
			}
			if c.want != 503 {
				return
			}
			if rec.Header().Get("Retry-After") != "5" || rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("headers %v", rec.Header())
			}
			if c.method == "HEAD" {
				return
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", rec.Body, err)
			}
			if c.status != "" && (body["status"] != c.status || len(body) != 1) {
				t.Errorf("body %v", body)
			}
			if c.status == "" && !strings.Contains(body["error"], "starting") {
				t.Errorf("body %v", body)
			}
		})
	}
}

// TestSwap (#396): serve's one HTTP server answers with the starting
// handler, then with the app after Set, on the same listener. A request
// that began before Set completes with the handler it began with.
func TestSwap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	first := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(entered)
			<-release
		}
		_, _ = io.WriteString(w, "first")
	})
	second := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "second") })
	sw := NewSwap(first)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, stop := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- Serve(serveCtx, ln, sw) }()
	base := "http://" + ln.Addr().String()
	get := func(path string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
		if err != nil {
			return "", err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close() //nolint:errcheck // test
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}

	if got, err := get("/"); err != nil || got != "first" {
		t.Fatalf("before the swap: %q %v", got, err)
	}
	slow := make(chan string, 1)
	go func() {
		got, err := get("/slow")
		if err != nil {
			got = err.Error()
		}
		slow <- got
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the slow request never arrived")
	}
	sw.Set(second)
	if got, err := get("/"); err != nil || got != "second" {
		t.Fatalf("after the swap: %q %v", got, err)
	}
	close(release)
	if got := <-slow; got != "first" {
		t.Errorf("the request in flight during the swap: %q", got)
	}
	stop()
	if err := <-served; err != nil {
		t.Errorf("serve: %v", err)
	}
}
