package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// hardeningServer is a server for mail.example whose requests are built
// by hand: their peer address, TLS and X-Forwarded-Proto are the test's.
func hardeningServer(t *testing.T, log io.Writer, trusted ...netip.Prefix) (*Server, *store.Store) {
	t.Helper()
	st := storetest.New(t)
	s := New(st, nil, slog.New(slog.NewTextHandler(log, nil)), Options{
		AllowedHosts: []string{"mail.example"}, Hasher: cheapHasher, TrustedProxies: trusted,
	})
	h, err := cheapHasher.Hash(context.Background(), "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(context.Background(), "alice", h, false); err != nil {
		t.Fatal(err)
	}
	return s, st
}

// hardeningRequest is a request to mail.example from remote, without TLS
// unless tls is set, with X-Forwarded-Proto proto if not empty.
func hardeningRequest(method, path, body, remote, proto string, tlsOn bool, cookies ...*http.Cookie) *http.Request {
	req := httptest.NewRequest(method, "https://mail.example"+path, strings.NewReader(body))
	req.RemoteAddr = remote
	req.Header.Set("Origin", "https://mail.example")
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}
	if proto != "" {
		req.Header.Set("X-Forwarded-Proto", proto)
	}
	req.TLS = nil
	if tlsOn {
		req.TLS = &tls.ConnectionState{}
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

const aliceLogin = `{"username":"alice","password":"correct horse battery"}`

// newSession starts a session of the user and returns its token.
func newSession(t *testing.T, st *store.Store, userID int64) string {
	t.Helper()
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(context.Background(), hash, userID, time.Now().Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	return token
}

// sessionExists reports whether the session of token is still stored.
func sessionExists(t *testing.T, st *store.Store, token string) bool {
	t.Helper()
	_, err := st.GetSession(context.Background(), auth.HashSessionToken(token), time.Now().Add(-auth.IdleTimeout))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

// X-Forwarded-Proto counts only from loopback or a trusted proxy; HSTS
// goes with the secure cookie.
func TestSecureCookie(t *testing.T) {
	s, _ := hardeningServer(t, io.Discard, netip.MustParsePrefix("10.0.0.0/8"))
	for _, c := range []struct {
		name   string
		remote string
		header string
		tls    bool
		secure bool
	}{
		{"trusted proxy", "10.1.2.3:4000", "https", false, true},
		{"IPv4-mapped trusted proxy", "[::ffff:10.1.2.3]:4000", "https", false, true},
		{"loopback proxy", "127.0.0.1:4000", "https", false, true},
		{"IPv6 loopback proxy", "[::1]:4000", "https", false, true},
		{"IPv4-mapped loopback proxy", "[::ffff:127.0.0.1]:4000", "https", false, true},
		{"direct tls", "192.0.2.1:1234", "", true, true},
		// Anybody else could set the header: it does not count.
		{"untrusted peer", "192.0.2.1:1234", "https", false, false},
		{"IPv4-mapped untrusted peer", "[::ffff:192.0.2.1]:1234", "https", false, false},
		{"plain http", "10.1.2.3:4000", "", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, hardeningRequest("POST", "/api/session", aliceLogin, c.remote, c.header, c.tls))
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			cookies := rec.Result().Cookies()
			want := cookieName
			if c.secure {
				want = secureCookieName
			}
			if len(cookies) == 0 || cookies[0].Name != want || cookies[0].Secure != c.secure || cookies[0].MaxAge <= 0 {
				t.Fatalf("cookies: %+v, want %s secure=%v", cookies, want, c.secure)
			}
			// Without HTTPS a __Host- cookie from before is expired.
			if c.secure && len(cookies) != 1 {
				t.Fatalf("cookies: %+v", cookies)
			}
			if !c.secure && (len(cookies) != 2 || cookies[1].Name != secureCookieName || cookies[1].MaxAge >= 0 || !cookies[1].Secure || cookies[1].Path != "/") {
				t.Fatalf("cookies: %+v, want an expired %s", cookies, secureCookieName)
			}
			hsts := rec.Header().Get("Strict-Transport-Security")
			if c.secure && hsts != "max-age=31536000" || !c.secure && hsts != "" {
				t.Fatalf("Strict-Transport-Security %q, secure=%v", hsts, c.secure)
			}
		})
	}
}

// The ignored X-Forwarded-Proto is logged once, with the peer and the
// setting, and only where it is ignored.
func TestWarnUntrustedForwardedProto(t *testing.T) {
	const msg = "X-Forwarded-Proto from a peer that is neither loopback nor a trusted proxy is ignored"
	run := func(trusted []netip.Prefix, remote, proto string, tlsOn bool) (warnings int, logged string) {
		t.Helper()
		var buf bytes.Buffer
		s := New(nil, nil, slog.New(slog.NewTextHandler(&buf, nil)), Options{AllowedHosts: []string{"mail.example"}, TrustedProxies: trusted})
		h := s.Handler()
		for range 2 {
			req := hardeningRequest("GET", "/healthz", "", remote, proto, tlsOn)
			req.Header.Set("X-Forwarded-For", "198.51.100.7")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("status %d", rec.Code)
			}
		}
		return strings.Count(buf.String(), msg), buf.String()
	}
	for _, remote := range []string{"192.0.2.1:1234", "[::ffff:192.0.2.1]:1234"} {
		n, logged := run(nil, remote, "https", false)
		if n != 1 || !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, "peer=192.0.2.1") ||
			!strings.Contains(logged, config.EnvTrustedProxies) || strings.Contains(logged, "198.51.100.7") {
			t.Fatalf("untrusted peer %s: %d warnings: %s", remote, n, logged)
		}
	}
	for _, c := range []struct {
		name    string
		trusted []netip.Prefix
		remote  string
		proto   string
		tls     bool
	}{
		{"loopback", nil, "127.0.0.1:1234", "https", false},
		{"IPv6 loopback", nil, "[::1]:1234", "https", false},
		{"IPv4-mapped loopback", nil, "[::ffff:127.0.0.1]:1234", "https", false},
		{"trusted proxy", []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, "192.0.2.1:1234", "https", false},
		{"without the header", nil, "192.0.2.1:1234", "", false},
		{"http", nil, "192.0.2.1:1234", "http", false},
		{"direct tls", nil, "192.0.2.1:1234", "https", true},
	} {
		if n, logged := run(c.trusted, c.remote, c.proto, c.tls); n != 0 {
			t.Errorf("%s: %s", c.name, logged)
		}
	}
}

// Logout expires both session cookies with a Max-Age, over plain HTTP and
// over HTTPS through a proxy. A cookie without Max-Age would stay in the
// browser until it closes.
func TestLogoutExpiresBothCookies(t *testing.T) {
	s, st := hardeningServer(t, io.Discard)
	u, err := st.GetUserByName(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, remote, proto, cookie string
	}{
		{"http", "192.0.2.1:1234", "", cookieName},
		{"https through a loopback proxy", "127.0.0.1:4000", "https", secureCookieName},
	} {
		t.Run(c.name, func(t *testing.T) {
			token := newSession(t, st, u.ID)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, hardeningRequest("DELETE", "/api/session", "", c.remote, c.proto, false, &http.Cookie{Name: c.cookie, Value: token}))
			if rec.Code != 204 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			seen := map[string]bool{}
			for _, k := range rec.Result().Cookies() {
				if k.Name != cookieName && k.Name != secureCookieName {
					continue
				}
				seen[k.Name] = true
				if k.MaxAge >= 0 || k.Value != "" || k.Path != "/" {
					t.Errorf("not expired: %+v", k)
				}
			}
			if !seen[cookieName] || !seen[secureCookieName] {
				t.Errorf("cookies %v, want both names expired: %q", seen, rec.Header().Values("Set-Cookie"))
			}
			if sessionExists(t, st, token) {
				t.Error("the session outlived the logout")
			}
		})
	}
}

// A request with a Host that is not allowed does not use up the one
// warning: a scanner or a LAN client would otherwise get its address named
// as the proxy, and the real misconfiguration would log nothing.
func TestWarnUntrustedForwardedProtoAfterHostCheck(t *testing.T) {
	const msg = "X-Forwarded-Proto from a peer that is neither loopback nor a trusted proxy is ignored"
	var buf bytes.Buffer
	h := New(nil, nil, slog.New(slog.NewTextHandler(&buf, nil)), Options{AllowedHosts: []string{"mail.example"}}).Handler()
	wrong := httptest.NewRequest("GET", "https://scanner.example/healthz", nil)
	wrong.TLS, wrong.RemoteAddr = nil, "198.51.100.9:1234"
	wrong.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, wrong)
	if rec.Code != 403 || strings.Contains(buf.String(), msg) {
		t.Fatalf("wrong host: %d, log: %s", rec.Code, buf.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, hardeningRequest("GET", "/healthz", "", "192.0.2.1:1234", "https", false))
	if rec.Code != 200 || strings.Count(buf.String(), msg) != 1 || !strings.Contains(buf.String(), "peer=192.0.2.1") ||
		strings.Contains(buf.String(), "peer=198.51.100.9") {
		t.Fatalf("allowed host: %d, log: %s", rec.Code, buf.String())
	}
}

// On a request over HTTPS only the __Host- cookie logs in, on a plain
// request only the plain one. A plain cookie planted over plain HTTP or
// from a sibling host name next to a __Host- cookie of an ended session
// must not log the user into the planter's session (login CSRF).
func TestSessionCookieOfTheScheme(t *testing.T) {
	s, st := hardeningServer(t, io.Discard)
	h := s.Handler()
	ctx := context.Background()
	alice, err := st.GetUserByName(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	mallory, err := st.CreateUser(ctx, "mallory", "unused", false)
	if err != nil {
		t.Fatal(err)
	}
	planted := &http.Cookie{Name: cookieName, Value: newSession(t, st, mallory.ID)}
	stale := &http.Cookie{Name: secureCookieName, Value: "stale-token-of-an-ended-session"}
	own := newSession(t, st, alice.ID)
	const loopback, direct = "127.0.0.1:4000", "192.0.2.1:1234"
	for _, c := range []struct {
		name    string
		remote  string
		proto   string
		cookies []*http.Cookie
		want    int
	}{
		{"secure: planted plain next to a stale __Host-", loopback, "https", []*http.Cookie{stale, planted}, 401},
		{"secure: planted plain alone", loopback, "https", []*http.Cookie{planted}, 401},
		{"secure: own session in a plain cookie", loopback, "https", []*http.Cookie{{Name: cookieName, Value: own}}, 401},
		{"secure: own session in __Host- next to a planted plain", loopback, "https", []*http.Cookie{planted, {Name: secureCookieName, Value: own}}, 200},
		{"plain: own session in a __Host- cookie", direct, "", []*http.Cookie{{Name: secureCookieName, Value: own}}, 401},
		{"plain: own session in a plain cookie next to a stale __Host-", direct, "", []*http.Cookie{stale, {Name: cookieName, Value: own}}, 200},
	} {
		for _, path := range []string{"/api/session", "/api/status"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, hardeningRequest("GET", path, "", c.remote, c.proto, false, c.cookies...))
			if rec.Code != c.want {
				t.Errorf("%s, %s: %d, want %d: %s", c.name, path, rec.Code, c.want, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "mallory") {
				t.Errorf("%s, %s: logged in as the planter: %s", c.name, path, rec.Body)
			}
		}
	}
}

// A browser behind a proxy that no longer counts (not trusted, not
// loopback, such as the Compose gateway) still holds a __Host- cookie of an
// ended session next to the plain one: the plain one counts there, and
// logout expires both.
func TestStaleSecureCookie(t *testing.T) {
	s, _ := hardeningServer(t, io.Discard)
	h := s.Handler()
	const gateway = "172.18.0.1:40000"
	stale := &http.Cookie{Name: secureCookieName, Value: "stale-token-of-an-ended-session"}
	send := func(method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, hardeningRequest(method, path, body, gateway, "https", false, cookies...))
		return rec
	}
	rec := send("POST", "/api/session", aliceLogin, stale)
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	var plain *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && c.MaxAge > 0 {
			plain = c
		}
	}
	if plain == nil {
		t.Fatalf("no session cookie: %+v", rec.Result().Cookies())
	}
	session := &http.Cookie{Name: cookieName, Value: plain.Value}
	if rec := send("GET", "/api/session", "", stale, session); rec.Code != 200 {
		t.Fatalf("session with a stale cookie: %d %s", rec.Code, rec.Body)
	}
	if rec := send("GET", "/api/status", "", stale, session); rec.Code != 200 {
		t.Fatalf("API with a stale cookie: %d %s", rec.Code, rec.Body)
	}
	if rec := send("DELETE", "/api/session", "", stale, session); rec.Code != 204 {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	if rec := send("GET", "/api/session", "", session); rec.Code != 401 {
		t.Fatalf("session after logout: %d", rec.Code)
	}
}

// addStaleCookie adds a plain session cookie of an ended session for the
// path /api to f's browser. The browser sends it before its session cookie
// for /, which has the same name.
func (f *authFixture) addStaleCookie() {
	f.t.Helper()
	u, err := url.Parse(f.srv.URL + "/api/status")
	if err != nil {
		f.t.Fatal(err)
	}
	f.client.Jar.SetCookies(u, []*http.Cookie{{Name: cookieName, Value: "stale-token-of-an-ended-session", Path: "/api"}})
	if cookies := f.client.Jar.Cookies(u); len(cookies) != 2 || cookies[0].Value != "stale-token-of-an-ended-session" {
		f.t.Fatalf("cookies in the browser: %+v, want the stale one first", cookies)
	}
}

// The endpoints that end a user's other sessions keep the valid one, not
// the session of the first cookie.
func TestStaleCookieKeepsValidSession(t *testing.T) {
	t.Run("password change", func(t *testing.T) {
		f := newAuthFixture(t)
		f.addUser("kim", "kims own passphrase", false)
		f.loginOK("kim", "kims own passphrase")
		other := f.as()
		other.loginOK("kim", "kims own passphrase")
		f.addStaleCookie()
		f.expect("PUT", "/api/profile/password", map[string]string{"current": "kims own passphrase", "new": "a brand new passphrase"}, 204)
		f.expect("GET", "/api/status", nil, 200)
		other.expect("GET", "/api/status", nil, 401)
	})
	t.Run("2fa disable", func(t *testing.T) {
		f := newAuthFixture(t)
		u := f.addUser("kim", "correct horse battery", false)
		secret, _ := f.enableTOTP(u)
		f.loginOK("kim", "correct horse battery")
		f.addStaleCookie()
		f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "correct horse battery", "code": f.code(secret)}, 204)
		f.expect("GET", "/api/status", nil, 200)
	})
	t.Run("passkey removal", func(t *testing.T) {
		f := newPasskeyFixture(t)
		f.addUser("kim", "correct horse battery", false)
		f.loginOK("kim", "correct horse battery")
		f.addPasskey(newSoftKey(t), "Phone", "correct horse battery", "")
		f.addStaleCookie()
		id := f.expect("GET", "/api/profile/passkeys", nil, 200)["passkeys"].([]any)[0].(map[string]any)["id"].(float64)
		f.expect("DELETE", fmt.Sprintf("/api/profile/passkeys/%d", int64(id)), nil, 200)
		f.expect("GET", "/api/status", nil, 200)
	})
}

// A login, also the password step of a 2FA login and a passkey login,
// ends the sessions of both session cookies, and so does logout.
func TestLoginAndLogoutEndAllCookieSessions(t *testing.T) {
	// withTwoSessions puts two valid sessions of u into f's browser, one
	// per cookie name, and returns their tokens.
	withTwoSessions := func(f *authFixture, u *store.User) []string {
		f.t.Helper()
		su, err := url.Parse(f.srv.URL)
		if err != nil {
			f.t.Fatal(err)
		}
		tokens := []string{newSession(f.t, f.st, u.ID), newSession(f.t, f.st, u.ID)}
		f.client.Jar.SetCookies(su, []*http.Cookie{
			{Name: secureCookieName, Value: tokens[0], Path: "/"},
			{Name: cookieName, Value: tokens[1], Path: "/"},
		})
		return tokens
	}
	ended := func(t *testing.T, f *authFixture, tokens []string) {
		t.Helper()
		for i, token := range tokens {
			if sessionExists(t, f.st, token) {
				t.Errorf("session %d outlived the change", i)
			}
		}
	}
	t.Run("password login", func(t *testing.T) {
		f := newAuthFixture(t)
		u := f.addUser("kim", "correct horse battery", false)
		tokens := withTwoSessions(f, u)
		f.loginOK("kim", "correct horse battery")
		ended(t, f, tokens)
	})
	t.Run("2fa password step", func(t *testing.T) {
		f := newAuthFixture(t)
		u := f.addUser("kim", "correct horse battery", false)
		f.enableTOTP(u)
		tokens := withTwoSessions(f, u)
		if challenge := f.passwordStep("kim", "correct horse battery"); challenge == "" {
			t.Fatal("no challenge")
		}
		ended(t, f, tokens)
	})
	t.Run("passkey login", func(t *testing.T) {
		f := newPasskeyFixture(t)
		u := f.addUser("kim", "correct horse battery", false)
		f.loginOK("kim", "correct horse battery")
		k := newSoftKey(t)
		f.addPasskey(k, "Phone", "correct horse battery", "")
		g := f.as()
		tokens := withTwoSessions(g, u)
		g.passkeyLogin(k, 200)
		ended(t, f, tokens)
	})
	t.Run("logout", func(t *testing.T) {
		f := newAuthFixture(t)
		u := f.addUser("kim", "correct horse battery", false)
		tokens := withTwoSessions(f, u)
		f.expect("DELETE", "/api/session", nil, 204)
		ended(t, f, tokens)
	})
}

// Unknown paths and methods under /api/ answer JSON 404 after the login.
func TestUnknownAPIPath(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("kim", "correct horse battery", false)
	if out := f.expect("GET", "/api/nope", nil, 401); out["error"] != "login required" {
		t.Fatalf("without a login: %v", out)
	}
	f.loginOK("kim", "correct horse battery")
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/nope"},
		{"GET", "/api/messages/x/y/z"},
		// A method no route of a known path takes.
		{"DELETE", "/api/status"},
	} {
		resp, out := f.do(c.method, c.path, nil)
		if resp.StatusCode != 404 || out["error"] != "not found" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Errorf("%s %s: %d %q %v", c.method, c.path, resp.StatusCode, resp.Header.Get("Content-Type"), out)
		}
	}
}

// failStore answers fixed texts and codes, never the store's error text.
func TestFailStoreHidesStoreText(t *testing.T) {
	s := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	const marker = "secret store detail"
	for _, c := range []struct {
		name  string
		err   error
		error string
		code  string
		ids   string
	}{
		{"hold with IDs", fmt.Errorf("%s: %w", marker, newTestHoldError(3, 5)), "named by a legal hold", "on_hold", "[3,5]"},
		{"hold", fmt.Errorf("%s: %w", marker, store.ErrOnHold), "named by a legal hold", "on_hold", ""},
		{"conflict", fmt.Errorf("%s: %w", marker, store.ErrConflict), "already exists", "conflict", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.failStore(rec, httptest.NewRequest("DELETE", "/api/users/kim", nil), c.err)
			if rec.Code != 409 || strings.Contains(rec.Body.String(), marker) || strings.Contains(rec.Body.String(), "lock the user instead") {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			var out struct {
				Error   string           `json:"error"`
				Code    string           `json:"code"`
				HoldIDs *json.RawMessage `json:"holdIds"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			ids := ""
			if out.HoldIDs != nil {
				ids = string(*out.HoldIDs)
			}
			if out.Error != c.error || out.Code != c.code || ids != c.ids {
				t.Fatalf("%s", rec.Body)
			}
		})
	}
}

// newTestHoldError is the store's HoldError of a deleted user named by the
// holds, with the store's text.
func newTestHoldError(ids ...int64) error {
	st := &store.HoldError{IDs: ids}
	if !errors.Is(st, store.ErrOnHold) {
		panic("HoldError is no ErrOnHold")
	}
	return fmt.Errorf("%w; lock the user instead", st)
}

func TestLimitKey(t *testing.T) {
	for _, c := range []struct{ addr, want string }{
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"fe80::1%eth0", "fe80::/64"},
		{"192.0.2.1", "192.0.2.1"},
		{"::ffff:192.0.2.1", "192.0.2.1"},
	} {
		if got := limitKey(netip.MustParseAddr(c.addr)); got != c.want {
			t.Errorf("%s: %s, want %s", c.addr, got, c.want)
		}
	}
	s := &Server{}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "pipe"
	if got := s.clientAddr(r); got != "pipe" {
		t.Errorf("no address: %q", got)
	}
}

// Failed logins from one IPv6 /64 count together, other /64s are not
// blocked by them; the log line and the audit log keep the full address.
func TestLoginLimitPerIPv6Prefix(t *testing.T) {
	var buf bytes.Buffer
	s, st := hardeningServer(t, &buf)
	h := s.Handler()
	login := func(remote, name string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, hardeningRequest("POST", "/api/session", `{"username":"`+name+`","password":"wrong horse battery"}`, remote, "", true))
		return rec.Code
	}
	// Other names each time, so that only the address limit counts.
	for i := range 20 {
		if code := login("[2001:db8:0:1::1]:1234", fmt.Sprintf("user%d", i)); code != 401 {
			t.Fatalf("failure %d: status %d", i, code)
		}
	}
	if code := login("[2001:db8:0:1::2]:1234", "another"); code != 429 {
		t.Errorf("same /64: status %d, want 429", code)
	}
	if code := login("[2001:db8:0:2::1]:1234", "another"); code != 401 {
		t.Errorf("other /64: status %d, want 401", code)
	}
	if !strings.Contains(buf.String(), `msg="login failed" user=user0 addr=2001:db8:0:1::1`) {
		t.Errorf("log without the full address: %s", buf.String())
	}
	entries, err := st.ListAudit(context.Background(), store.AuditPage{
		All: true, Actions: []string{"auth.login_failed"}, Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Limit: 50,
	})
	if err != nil || len(entries) == 0 {
		t.Fatalf("audit %+v %v", entries, err)
	}
	for _, e := range entries {
		if e.ClientIP != "2001:db8:0:1::1" && e.ClientIP != "2001:db8:0:2::1" {
			t.Errorf("audit address %q", e.ClientIP)
		}
	}
}

func TestNewHTTPServer(t *testing.T) {
	if srv := newHTTPServer(":0", http.NotFoundHandler()); srv.MaxHeaderBytes != 64<<10 || srv.ReadHeaderTimeout == 0 {
		t.Fatalf("server %+v", srv)
	}
}

// A login and logout end the sessions of both session cookie names, the
// __Host- one first, without empty values or duplicates and at most
// maxSessionCookies per name: each token costs a database lookup. Only the
// name of the request's scheme logs in.
func TestSessionTokens(t *testing.T) {
	plain := func(v string) *http.Cookie { return &http.Cookie{Name: cookieName, Value: v} }
	secure := func(v string) *http.Cookie { return &http.Cookie{Name: secureCookieName, Value: v} }
	for _, c := range []struct {
		name    string
		cookies []*http.Cookie
		want    string
	}{
		{"none", nil, ""},
		{"__Host- first", []*http.Cookie{plain("a"), secure("s")}, "s,a"},
		{"empty and duplicates", []*http.Cookie{plain(""), plain("a"), secure("a"), plain("b")}, "a,b"},
		{"capped per name", []*http.Cookie{plain("1"), plain("2"), plain("3"), secure("s1"), secure("s2"), secure("s3")}, "s1,s2,1,2"},
		{"other names", []*http.Cookie{{Name: "other", Value: "x"}, plain("a")}, "a"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		for _, k := range c.cookies {
			r.AddCookie(k)
		}
		if got := strings.Join(allSessionTokens(r), ","); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	s := &Server{}
	for _, c := range []struct {
		name   string
		remote string
		proto  string
		want   string
	}{
		{"secure", "127.0.0.1:4000", "https", "s1,s2"},
		{"plain", "192.0.2.1:1234", "", "1,2"},
		{"untrusted proxy", "192.0.2.1:1234", "https", "1,2"},
	} {
		r := hardeningRequest("GET", "/", "", c.remote, c.proto, false,
			plain("1"), plain("2"), plain("3"), secure("s1"), secure("s2"), secure("s3"))
		if got := strings.Join(s.sessionTokens(r), ","); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
