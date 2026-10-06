package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// cheapHasher keeps the tests fast.
var cheapHasher = auth.NewHasher(auth.Params{Memory: 64, Time: 1, Threads: 1})

// loggedIn creates a user with a session and adds its cookie to every
// request that has none, so that tests of other endpoints need no login.
func loggedIn(t *testing.T, st *store.Store, h http.Handler) http.Handler {
	t.Helper()
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "tester", "unused", true)
	if err != nil {
		t.Fatal(err)
	}
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, hash, u.ID, time.Now().Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessionToken(r) == "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		}
		h.ServeHTTP(w, r)
	})
}

type authFixture struct {
	t      *testing.T
	st     *store.Store
	srv    *httptest.Server
	client *http.Client
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	st := storetest.New(t)
	s := New(st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{AllowedHosts: []string{"127.0.0.1"}, Hasher: cheapHasher})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &authFixture{t: t, st: st, srv: srv, client: &http.Client{Jar: jar}}
}

func (f *authFixture) addUser(name, password string, admin bool) *store.User {
	f.t.Helper()
	h, err := cheapHasher.Hash(context.Background(), password)
	if err != nil {
		f.t.Fatal(err)
	}
	u, err := f.st.CreateUser(context.Background(), name, h, admin)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *authFixture) do(method, path string, body any) (*http.Response, map[string]any) {
	f.t.Helper()
	var r io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.srv.URL)
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func (f *authFixture) expect(method, path string, body any, want int) map[string]any {
	f.t.Helper()
	resp, out := f.do(method, path, body)
	if resp.StatusCode != want {
		f.t.Fatalf("%s %s: status %d, want %d: %v", method, path, resp.StatusCode, want, out)
	}
	return out
}

func (f *authFixture) login(name, password string) *http.Response {
	f.t.Helper()
	resp, _ := f.do("POST", "/api/session", map[string]string{"username": name, "password": password})
	return resp
}

func TestLoginRequired(t *testing.T) {
	f := newAuthFixture(t)

	// Without any user, the UI is told to explain the setup.
	out := f.expect("GET", "/api/session", nil, 401)
	if out["setupRequired"] != true {
		t.Fatalf("no users: %v", out)
	}
	f.addUser("alice", "correct horse battery", false)
	if out := f.expect("GET", "/api/session", nil, 401); out["setupRequired"] != false {
		t.Fatalf("with a user: %v", out)
	}

	// Every API endpoint needs a session, also unknown and write ones.
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/accounts"}, {"GET", "/api/status"}, {"GET", "/api/messages"},
		{"GET", "/api/messages/" + strings.Repeat("a", 64) + "/raw"},
		{"POST", "/api/sync"}, {"DELETE", "/api/accounts/x"}, {"GET", "/api/does-not-exist"},
	} {
		if out := f.expect(c.method, c.path, nil, 401); out["error"] != "login required" {
			t.Errorf("%s %s: %v", c.method, c.path, out)
		}
	}
	// The UI code and the health check are public.
	for _, p := range []string{"/", "/healthz"} {
		resp, err := f.client.Get(f.srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == 401 {
			t.Errorf("%s needs a login", p)
		}
	}
}

func TestLoginLogout(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", true)

	if resp := f.login("alice", "wrong horse battery"); resp.StatusCode != 401 {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	if resp := f.login("nobody", "correct horse battery"); resp.StatusCode != 401 {
		t.Fatalf("unknown user: %d", resp.StatusCode)
	}
	_, wrongPw := f.do("POST", "/api/session", map[string]string{"username": "alice", "password": "x"})
	_, unknown := f.do("POST", "/api/session", map[string]string{"username": "nobody", "password": "x"})
	if wrongPw["error"] != unknown["error"] {
		t.Fatalf("answers differ: %v / %v", wrongPw, unknown)
	}

	// Names are case-insensitive.
	resp := f.login(" Alice ", "correct horse battery")
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	var c *http.Cookie
	for _, k := range resp.Cookies() {
		if k.Name == cookieName {
			c = k
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure || c.MaxAge != int(auth.MaxSessionAge.Seconds()) {
		t.Fatalf("cookie: %+v", c)
	}
	out := f.expect("GET", "/api/session", nil, 200)
	if user, _ := out["user"].(map[string]any); user["name"] != "alice" || user["admin"] != true {
		t.Fatalf("session: %v", out)
	}
	f.expect("GET", "/api/status", nil, 200)

	u, _ := f.st.GetUserByName(context.Background(), "alice")
	if u.LastLoginAt == nil {
		t.Error("last login not recorded")
	}

	f.expect("DELETE", "/api/session", nil, 204)
	f.expect("GET", "/api/status", nil, 401)
}

func TestLoginWithStolenCookieAfterLogout(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.login("alice", "correct horse battery")
	u, _ := url.Parse(f.srv.URL)
	old := f.client.Jar.Cookies(u)

	f.expect("DELETE", "/api/session", nil, 204)
	f.client.Jar.SetCookies(u, old) // the old token is dead on the server
	f.expect("GET", "/api/status", nil, 401)
}

func TestSessionEnds(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	alice := f.addUser("alice", "correct horse battery", false)
	f.addUser("admin", "correct horse battery", true)

	// A new password ends existing sessions.
	f.login("alice", "correct horse battery")
	f.expect("GET", "/api/status", nil, 200)
	h, _ := cheapHasher.Hash(ctx, "another horse battery")
	if err := f.st.SetUserPassword(ctx, alice.ID, h); err != nil {
		t.Fatal(err)
	}
	f.expect("GET", "/api/status", nil, 401)

	// So does locking, and a locked user cannot log in again.
	f.login("alice", "another horse battery")
	f.expect("GET", "/api/status", nil, 200)
	if err := f.st.SetUserLocked(ctx, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	f.expect("GET", "/api/status", nil, 401)
	if resp := f.login("alice", "another horse battery"); resp.StatusCode != 403 {
		t.Fatalf("locked login: %d", resp.StatusCode)
	}
	// A wrong password for a locked user gets the usual answer.
	if resp := f.login("alice", "wrong horse battery"); resp.StatusCode != 401 {
		t.Fatalf("locked, wrong password: %d", resp.StatusCode)
	}
}

func TestSessionExpiry(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	u, _ := st.CreateUser(ctx, "alice", "x", false)
	now := time.Now()

	_, valid, _ := auth.NewSessionToken()
	_, expired, _ := auth.NewSessionToken()
	_ = st.CreateSession(ctx, valid, u.ID, now.Add(time.Hour), "")
	_ = st.CreateSession(ctx, expired, u.ID, now.Add(-time.Second), "")

	if _, err := st.GetSession(ctx, valid, now.Add(-auth.IdleTimeout)); err != nil {
		t.Fatalf("valid session: %v", err)
	}
	if _, err := st.GetSession(ctx, expired, now.Add(-auth.IdleTimeout)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("past absolute expiry: %v", err)
	}
	// Idle: last used before the cutoff.
	if _, err := st.GetSession(ctx, valid, now.Add(time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("idle session: %v", err)
	}
	n, err := st.DeleteExpiredSessions(ctx, now.Add(-auth.IdleTimeout))
	if err != nil || n != 1 {
		t.Fatalf("cleanup: %d, %v", n, err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	for range 5 {
		if resp := f.login("alice", "wrong horse battery"); resp.StatusCode != 401 {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}
	// Blocked: even the right password waits, and the block is announced.
	resp, out := f.do("POST", "/api/session", map[string]string{"username": "alice", "password": "correct horse battery"})
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "60" || out["retryAfter"] != float64(60) {
		t.Fatalf("blocked login: %d %q %v", resp.StatusCode, resp.Header.Get("Retry-After"), out)
	}
	// Other names are not affected.
	f.addUser("bob", "correct horse battery", false)
	if resp := f.login("bob", "correct horse battery"); resp.StatusCode != 200 {
		t.Fatalf("other user: %d", resp.StatusCode)
	}
}

func TestSecureCookie(t *testing.T) {
	st := storetest.New(t)
	s := New(st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{AllowedHosts: []string{"mail.example"}, Hasher: cheapHasher})
	h, _ := cheapHasher.Hash(context.Background(), "correct horse battery")
	if _, err := st.CreateUser(context.Background(), "alice", h, false); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		header string
		tls    bool
	}{{"proxy", "https", false}, {"direct tls", "", true}} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "https://mail.example/api/session", strings.NewReader(`{"username":"alice","password":"correct horse battery"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "https://mail.example")
			if c.header != "" {
				req.Header.Set("X-Forwarded-Proto", c.header)
			}
			if !c.tls {
				req.TLS = nil
			} else {
				req.TLS = &tls.ConnectionState{}
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != secureCookieName || !cookies[0].Secure {
				t.Fatalf("cookies: %+v", cookies)
			}
		})
	}
}
