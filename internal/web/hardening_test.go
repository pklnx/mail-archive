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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keepia/archive/internal/auth"
	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/store/storetest"
	"github.com/keepia/archive/internal/testwait"
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

// The grouping itself is tested with auth.LimitKey (TestLimitKey there).
func TestClientAddr(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[2001:db8:1:2:3:4:5:6]:4711"
	if got := s.clientAddr(r); got != "2001:db8:1:2::/64" {
		t.Errorf("IPv6: %q", got)
	}
	r.RemoteAddr = "pipe"
	if got := s.clientAddr(r); got != "pipe" {
		t.Errorf("no address: %q", got)
	}
}

// The passkey-begin caps count IPv6 per /64 and per /48, IPv4 per address;
// without an address, per RemoteAddr.
func TestPasskeyOpenKeys(t *testing.T) {
	for _, c := range []struct {
		addr string
		want []auth.QuotaKey
	}{
		{"2001:db8:1:2:3:4:5:6", []auth.QuotaKey{{Key: "2001:db8:1:2::/64", Cap: passkeyOpenPerAddr}, {Key: "2001:db8:1::/48", Cap: passkeyOpenPer48}}},
		{"fe80::1%eth0", []auth.QuotaKey{{Key: "fe80::/64", Cap: passkeyOpenPerAddr}, {Key: "fe80::/48", Cap: passkeyOpenPer48}}},
		{"::ffff:192.0.2.1", []auth.QuotaKey{{Key: "192.0.2.1", Cap: passkeyOpenPerAddr}}},
		{"pipe", []auth.QuotaKey{{Key: "pipe", Cap: passkeyOpenPerAddr}}},
	} {
		s := &Server{}
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = c.addr
		if got := s.passkeyOpenKeys(r); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.addr, got, c.want)
		}
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

// plainHTTPMsg is the start of warnPlainHTTP's line.
const plainHTTPMsg = "the web UI is used over plain HTTP under a host name other than localhost or a loopback address"

// browserAgent is a User-Agent of a current browser.
const browserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"

// plainBrowserRequest is a GET of /healthz from a browser that sends no
// Fetch Metadata, over plain HTTP, with hostport as Host, from remote.
func plainBrowserRequest(hostport, remote string) *http.Request {
	req := httptest.NewRequest("GET", "http://"+hostport+"/healthz", nil)
	req.RemoteAddr = remote
	req.TLS = nil
	req.Header.Set("User-Agent", browserAgent)
	return req
}

// plainHTTPServer is a server with the given allowed hosts and trusted
// proxies that logs to buf.
func plainHTTPServer(buf *bytes.Buffer, hosts []string, trusted ...netip.Prefix) *Server {
	return New(nil, nil, slog.New(slog.NewTextHandler(buf, nil)), Options{AllowedHosts: hosts, TrustedProxies: trusted})
}

// serveOK sends req through h and fails unless it is answered with want.
func serveOK(t *testing.T, h http.Handler, req *http.Request, want int) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", req.Method, req.Host, rec.Code, want, rec.Body)
	}
}

// A browser over plain HTTP under a LAN name gets one warning per allowed
// host, the public URL's included, whatever port or case the Host header or
// the setting has. The line names the configured host and the fixes.
func TestWarnPlainHTTPOncePerHost(t *testing.T) {
	var buf bytes.Buffer
	h := New(nil, nil, slog.New(slog.NewTextHandler(&buf, nil)), Options{
		AllowedHosts: []string{" Mail.LAN", "192.168.1.10"}, PublicURL: "https://archive.example",
	}).Handler()
	for _, host := range []string{"mail.lan:8080", "MAIL.LAN", "192.168.1.10", "192.168.1.10:80", "archive.example", "archive.example:443"} {
		serveOK(t, h, plainBrowserRequest(host, "192.168.1.20:5000"), 200)
	}
	logged := buf.String()
	lines := strings.Split(strings.TrimSpace(logged), "\n")
	if strings.Count(logged, plainHTTPMsg) != 3 || len(lines) != 3 {
		t.Fatalf("want three warnings, got: %s", logged)
	}
	for i, host := range []string{"mail.lan", "192.168.1.10", "archive.example"} {
		line := lines[i]
		if !strings.Contains(line, "level=WARN") || !strings.Contains(line, plainHTTPMsg) || !strings.HasSuffix(line, " host="+host) {
			t.Errorf("line %d: %s, want a warning for %s", i, line, host)
		}
		for _, s := range []string{"use HTTPS", "http://localhost", config.EnvTrustedProxies, "Plain LAN access"} {
			if !strings.Contains(line, s) {
				t.Errorf("line %d does not name %q: %s", i, s, line)
			}
		}
	}
	for _, s := range []string{"8080", "MAIL.LAN", "Mail.LAN", ":443", "192.168.1.20", "Firefox"} {
		if strings.Contains(logged, s) {
			t.Errorf("the log contains %q from the request: %s", s, logged)
		}
	}
}

// Hosts reached only with a port, an IPv6 one in brackets, still warn once
// each, under the configured name.
func TestWarnPlainHTTPHostWithPortOnly(t *testing.T) {
	var buf bytes.Buffer
	h := plainHTTPServer(&buf, []string{"mail.lan", "fd00::10"}).Handler()
	for range 2 {
		for _, host := range []string{"mail.lan:8080", "[fd00::10]:8080"} {
			serveOK(t, h, plainBrowserRequest(host, "192.168.1.20:5000"), 200)
		}
	}
	logged := buf.String()
	if strings.Count(logged, plainHTTPMsg) != 2 || strings.Count(logged, "host=mail.lan\n") != 1 ||
		strings.Count(logged, "host=fd00::10\n") != 1 || strings.Contains(logged, "8080") {
		t.Fatalf("want one warning each for mail.lan and fd00::10: %s", logged)
	}
}

// No warning where the guard works: under a loopback name, over HTTPS
// (directly or through a proxy that counts), for a browser that sends Fetch
// Metadata, and for a client that is no browser. X-Forwarded-Proto: https
// from a peer that does not count gets only its own warning.
func TestWarnPlainHTTPNot(t *testing.T) {
	const forwardedMsg = "X-Forwarded-Proto from a peer that is neither loopback nor a trusted proxy is ignored"
	trusted := netip.MustParsePrefix("192.0.2.0/24")
	hosts := []string{"localhost", "app.localhost", "127.0.0.1", "127.0.0.2", "::1", "mail.lan"}
	for _, c := range []struct {
		name, host, remote string
		tls                bool
		header             map[string]string
		forwarded          bool
	}{
		{"localhost", "localhost:8080", "127.0.0.1:5000", false, nil, false},
		{"name under localhost", "app.localhost", "127.0.0.1:5000", false, nil, false},
		{"127.0.0.1", "127.0.0.1:8080", "127.0.0.1:5000", false, nil, false},
		{"127.0.0.2", "127.0.0.2", "127.0.0.1:5000", false, nil, false},
		{"::1", "[::1]:8080", "[::1]:5000", false, nil, false},
		{"tls", "mail.lan", "198.51.100.7:5000", true, nil, false},
		{"https from a loopback proxy", "mail.lan", "127.0.0.1:5000", false, map[string]string{"X-Forwarded-Proto": "https"}, false},
		{"https from a trusted proxy", "mail.lan", "192.0.2.1:5000", false, map[string]string{"X-Forwarded-Proto": "https"}, false},
		{"https from an untrusted peer", "mail.lan", "198.51.100.7:5000", false, map[string]string{"X-Forwarded-Proto": "https"}, true},
		{"fetch metadata", "mail.lan", "198.51.100.7:5000", false, map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
		{"fetch mode only", "mail.lan", "198.51.100.7:5000", false, map[string]string{"Sec-Fetch-Mode": "navigate"}, false},
		{"api key", "mail.lan", "198.51.100.7:5000", false, map[string]string{"Authorization": "Bearer " + auth.APIKeyPrefix + "abc"}, false},
		{"curl", "mail.lan", "198.51.100.7:5000", false, map[string]string{"User-Agent": "curl/8.5.0"}, false},
		{"no user agent", "mail.lan", "198.51.100.7:5000", false, map[string]string{"User-Agent": ""}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := plainHTTPServer(&buf, hosts, trusted).Handler()
			for range 2 {
				req := plainBrowserRequest(c.host, c.remote)
				if c.tls {
					req.TLS = &tls.ConnectionState{}
				}
				for k, v := range c.header {
					if v == "" {
						req.Header.Del(k)
					} else {
						req.Header.Set(k, v)
					}
				}
				serveOK(t, h, req, 200)
			}
			logged := buf.String()
			if strings.Contains(logged, plainHTTPMsg) {
				t.Errorf("warned: %s", logged)
			}
			if c.forwarded != strings.Contains(logged, forwardedMsg) {
				t.Errorf("X-Forwarded-Proto warning %v, want %v: %s", !c.forwarded, c.forwarded, logged)
			}
		})
	}
}

// X-Forwarded-Proto: http from a trusted proxy means the browser used plain
// HTTP to the proxy: that warns too.
func TestWarnPlainHTTPThroughTrustedProxy(t *testing.T) {
	var buf bytes.Buffer
	h := plainHTTPServer(&buf, []string{"mail.lan"}, netip.MustParsePrefix("192.0.2.0/24")).Handler()
	for range 2 {
		req := plainBrowserRequest("mail.lan", "192.0.2.1:5000")
		req.Header.Set("X-Forwarded-Proto", "http")
		serveOK(t, h, req, 200)
	}
	if logged := buf.String(); strings.Count(logged, plainHTTPMsg) != 1 || !strings.Contains(logged, "host=mail.lan") {
		t.Fatalf("want one warning for mail.lan: %s", logged)
	}
}

// A request with a Host that is not allowed gets its 403 and neither warns
// nor adds a host: the warnings stay one per configured host, however many
// names clients send. The allowed host still warns afterwards.
func TestWarnPlainHTTPAfterHostCheck(t *testing.T) {
	var buf bytes.Buffer
	s := plainHTTPServer(&buf, []string{"localhost", "mail.lan"})
	h := s.Handler()
	serveOK(t, h, plainBrowserRequest("scanner.example", "198.51.100.9:5000"), 403)
	for i := range 1000 {
		serveOK(t, h, plainBrowserRequest(fmt.Sprintf("h%d.example", i), "198.51.100.9:5000"), 403)
	}
	if strings.Contains(buf.String(), plainHTTPMsg) || len(s.plainHTTPWarned) != 1 {
		t.Fatalf("%d hosts after the refused requests, log: %s", len(s.plainHTTPWarned), buf.String())
	}
	serveOK(t, h, plainBrowserRequest("mail.lan", "198.51.100.9:5000"), 200)
	if logged := buf.String(); strings.Count(logged, plainHTTPMsg) != 1 || !strings.Contains(logged, "host=mail.lan\n") {
		t.Fatalf("want one warning for mail.lan: %s", logged)
	}
}

// Concurrent first requests to one host log one line.
func TestWarnPlainHTTPConcurrent(t *testing.T) {
	for round := range 100 {
		var buf bytes.Buffer
		h := plainHTTPServer(&buf, []string{"mail.lan"}).Handler()
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range 50 {
			wg.Go(func() {
				<-start
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, plainBrowserRequest("mail.lan", "198.51.100.9:5000"))
			})
		}
		close(start)
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(testwait.Limit(t)):
			t.Fatal("the requests did not finish")
		}
		if n := strings.Count(buf.String(), plainHTTPMsg); n != 1 {
			t.Fatalf("round %d: %d warnings: %s", round, n, buf.String())
		}
	}
}

func TestIsLoopbackName(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "localhost.": true, "app.localhost": true, "a.b.localhost": true, "a.b.localhost.": true,
		"127.0.0.1": true, "127.0.0.2": true, "::1": true, "::ffff:127.0.0.1": true,
		"localhost.example": false, "notlocalhost": false, "192.168.1.1": false, "10.0.0.1": false, "::": false,
		"fe80::1": false, "mail.lan": false, "": false,
	} {
		if got := isLoopbackName(host); got != want {
			t.Errorf("isLoopbackName(%q) = %v, want %v", host, got, want)
		}
	}
}
