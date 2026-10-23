package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

const keyPassword = "correct horse battery"

// syncWriter is a log sink that is safe for the server's goroutines.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// newKeyFixture is an auth fixture that can sync (the runner is not
// started, so syncs are only queued).
func newKeyFixture(t *testing.T) (*authFixture, *syncWriter) {
	t.Helper()
	logs := &syncWriter{}
	f := newAuthFixtureLog(t, Options{Syncer: &archive.Syncer{}, Runner: &archive.Runner{}}, logs)
	return f, logs
}

// createKey creates an API key for the logged-in user of f and returns it.
func (f *authFixture) createKey(name string, scopes []string, code string) string {
	f.t.Helper()
	body := map[string]any{"name": name, "scopes": scopes, "currentPassword": keyPassword}
	if code != "" {
		body["code"] = code
	}
	out := f.expect("POST", "/api/profile/api-keys", body, 201)
	key, _ := out["key"].(string)
	if !strings.HasPrefix(key, "ma_") || len(key) != 55 {
		f.t.Fatalf("created key: %v", out)
	}
	return key
}

// keyRequest sends a request with only an API key: no cookie, no Origin
// and no Content-Type, like curl.
func (f *authFixture) keyRequest(key, method, path string, body any, cookie *http.Cookie) (*http.Response, map[string]any) {
	f.t.Helper()
	var r io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, r)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func (f *authFixture) keyStatus(key, method, path string) int {
	f.t.Helper()
	resp, _ := f.keyRequest(key, method, path, nil, nil)
	return resp.StatusCode
}

// sessionCookie returns the session cookie of f's browser.
func (f *authFixture) sessionCookie() *http.Cookie {
	f.t.Helper()
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, c := range f.client.Jar.Cookies(u) {
		if c.Name == cookieName {
			return c
		}
	}
	f.t.Fatal("no session cookie")
	return nil
}

func (f *authFixture) exec(sql string, args ...any) {
	f.t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

// TestAPIKeyAdminWithTwoFactor is the case the keys are for: an admin with
// 2FA reads messages and starts a sync from curl with a key and nothing
// else.
func TestAPIKeyAdminWithTwoFactor(t *testing.T) {
	f, _ := newKeyFixture(t)
	admin := f.addUser("admin", keyPassword, true)
	f.imapAccount("mine", admin)
	f.loginOK("admin", keyPassword)

	// The second factor is needed to create a key.
	f.expect("POST", "/api/profile/api-keys", map[string]any{"name": "cron", "scopes": []string{"read"}, "currentPassword": keyPassword}, 401)
	f.expect("POST", "/api/profile/api-keys", map[string]any{"name": "cron", "scopes": []string{"read"}, "currentPassword": "wrong wrong wrong", "code": f.code(f.adminSecrets["admin"])}, 401)
	n := len(auditLog(t, f.st))
	key := f.createKey("cron", []string{"read", "sync"}, f.code(f.adminSecrets["admin"]))

	if s := f.keyStatus(key, "GET", "/api/messages?q=invoice"); s != 200 {
		t.Fatalf("search: %d", s)
	}
	resp, out := f.keyRequest(key, "POST", "/api/sync", nil, nil)
	if resp.StatusCode != 202 || out["queued"] != float64(1) {
		t.Fatalf("sync: %d %v", resp.StatusCode, out)
	}
	if s := f.keyStatus(key, "GET", "/api/status"); s != 200 {
		t.Fatalf("status: %d", s)
	}

	// The audit log names the key: create, search and sync, nothing for
	// the plain read of /api/status.
	var actions []string
	for _, r := range newEntries(t, f.st, n) {
		actions = append(actions, r.Action)
		switch r.Action {
		case "api_key.create":
			if r.Actor != store.ActorSession || r.UserName != "admin" || details(t, r)["key_name"] != "cron" {
				t.Errorf("create entry %+v %v", r, details(t, r))
			}
		case "message.search", "sync.start":
			if r.Actor != store.ActorAPIKey || r.APIKeyName != "cron" || r.UserName != "admin" {
				t.Errorf("%s entry %+v", r.Action, r)
			}
		}
	}
	if strings.Join(actions, " ") != "api_key.create message.search sync.start" {
		t.Errorf("entries %v", actions)
	}
}

// TestAPIKeyRouteAccess calls every route of the table with a read key, a
// sync key and a key with both. Driven by routes(), so new routes are
// covered as they come.
func TestAPIKeyRouteAccess(t *testing.T) {
	f, _ := newKeyFixture(t)
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	keys := map[string]string{
		"read": f.createKey("r", []string{"read"}, ""),
		"sync": f.createKey("s", []string{"sync"}, ""),
		"both": f.createKey("b", []string{"read", "sync"}, ""),
	}
	params := strings.NewReplacer("{sha}", strings.Repeat("0", 64), "{name}", "nothing", "{id}", "999999", "{n}", "1")
	for _, rt := range (&Server{}).routes() {
		if rt.ui || rt.access == accessPublic {
			continue
		}
		method, path, ok := strings.Cut(rt.pattern, " ")
		if !ok {
			// A pattern without a method (the /api/ catch-all) takes any
			// method and any path below it.
			method, path = "GET", rt.pattern+"nope"
		}
		path = params.Replace(path)
		for kind, key := range keys {
			got := f.keyStatus(key, method, path)
			scope := requiredScope(rt.access)
			allowed := scope != "" && (kind == "both" || kind == scope)
			switch {
			case allowed && (got == 401 || got == 403):
				t.Errorf("%s with a %s key: %d, want access", rt.pattern, kind, got)
			case !allowed && got != 403:
				t.Errorf("%s with a %s key: %d, want 403", rt.pattern, kind, got)
			}
		}
	}
	// An unknown path is session-only: with a key it answers like a
	// session-only route, so a key cannot tell which paths exist.
	for kind, key := range keys {
		if s := f.keyStatus(key, "GET", "/api/nope"); s != 403 {
			t.Errorf("unknown path with a %s key: %d, want 403", kind, s)
		}
	}
	// Public routes work with a key too.
	if s := f.keyStatus(keys["read"], "GET", openAPIPath); s != 200 {
		t.Errorf("openapi.json: %d", s)
	}
	// The scope is named.
	resp, out := f.keyRequest(keys["read"], "POST", "/api/sync", nil, nil)
	if resp.StatusCode != 403 || out["scope"] != "sync" || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `scope="sync"`) {
		t.Errorf("scope answer: %d %v %q", resp.StatusCode, out, resp.Header.Get("WWW-Authenticate"))
	}
}

func TestAPIKeyNoCookieFallback(t *testing.T) {
	f, _ := newKeyFixture(t)
	alice := f.addUser("alice", keyPassword, false)
	bob := f.addUser("bob", keyPassword, false)
	f.imapAccount("alice-mail", alice)
	f.imapAccount("bob-mail", bob)
	f.loginOK("alice", keyPassword)
	key := f.createKey("k", []string{"read"}, "")
	b := f.as()
	b.loginOK("bob", keyPassword)
	bobCookie := b.sessionCookie()

	// The key's user counts, not the cookie's.
	resp, out := f.keyRequest(key, "GET", "/api/accounts", nil, bobCookie)
	if resp.StatusCode != 200 || !strings.Contains(toJSON(out), "alice-mail") || strings.Contains(toJSON(out), "bob-mail") {
		t.Fatalf("key with another cookie: %d %v", resp.StatusCode, out)
	}
	// An invalid key does not fall back to the cookie.
	resp, out = f.keyRequest("ma_"+strings.Repeat("a", 52), "GET", "/api/accounts", nil, bobCookie)
	if resp.StatusCode != 401 || out["error"] != "invalid API key" || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "invalid_token") {
		t.Fatalf("invalid key with a cookie: %d %v", resp.StatusCode, out)
	}
	resp, _ = f.keyRequest("ma_short", "GET", "/api/accounts", nil, bobCookie)
	if resp.StatusCode != 401 {
		t.Fatalf("malformed key with a cookie: %d", resp.StatusCode)
	}

	// Other Authorization headers are not key requests: the cookie works
	// and writes still need the Origin.
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/accounts", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwdw==")
	req.AddCookie(bobCookie)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("Basic with a cookie: %v %v", resp, err)
	}
	req, _ = http.NewRequest("POST", f.srv.URL+"/api/sync", nil)
	req.Header.Set("Authorization", "Bearer some-proxy-token")
	req.AddCookie(bobCookie)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("other Bearer without Origin: %v %v", resp, err)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	f, _ := newKeyFixture(t)
	f.addUser("admin", keyPassword, true)
	f.addUser("kim", keyPassword, false)
	f.loginOK("admin", keyPassword)
	k := f.as()
	k.loginOK("kim", keyPassword)

	// Revoking an own key needs no password; another user's id is 404.
	key := k.createKey("one", []string{"read"}, "")
	list := k.expect("GET", "/api/profile/api-keys", nil, 200)
	keys, _ := list["keys"].([]any)
	if len(keys) != 1 || list["max"] != float64(store.MaxAPIKeys) {
		t.Fatalf("list %v", list)
	}
	first, _ := keys[0].(map[string]any)
	id := int64(first["id"].(float64))
	if first["hint"] != key[51:] || first["state"] != "active" || first["key"] != nil {
		t.Fatalf("listed %v", first)
	}
	adminKey := f.createKey("admin key", []string{"read"}, f.code(f.adminSecrets["admin"]))
	f.expect("DELETE", "/api/profile/api-keys/"+itoa(id), nil, 404)
	k.expect("DELETE", "/api/profile/api-keys/"+itoa(id), nil, 204)
	if s := k.keyStatus(key, "GET", "/api/status"); s != 401 {
		t.Fatalf("revoked: %d", s)
	}
	k.expect("DELETE", "/api/profile/api-keys/"+itoa(id), nil, 404)

	// Names are unique among keys that are not revoked; scopes are checked.
	key = k.createKey("one", []string{"read"}, "")
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "one", "scopes": []string{"read"}, "currentPassword": keyPassword}, 409)
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "two", "scopes": []string{"admin"}, "currentPassword": keyPassword}, 400)
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "two", "scopes": []string{"read"}, "expiresInDays": 0, "currentPassword": keyPassword}, 400)
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "", "scopes": []string{"read"}, "currentPassword": keyPassword}, 400)

	// Admins list and revoke every key; users may not.
	k.expect("GET", "/api/api-keys", nil, 403)
	all := f.expect("GET", "/api/api-keys", nil, 200)
	if s := toJSON(all); !strings.Contains(s, `"user":"kim"`) || !strings.Contains(s, `"user":"admin"`) || strings.Contains(s, key) {
		t.Fatalf("admin list %s", s)
	}
	users := toJSON(f.expect("GET", "/api/users", nil, 200))
	if !strings.Contains(users, `"name":"kim"`) || !strings.Contains(users, `"apiKeys":1`) {
		t.Fatalf("users %s", users)
	}
	// An expired key answers 401.
	expiring := k.createKey("expiring", []string{"read"}, "")
	f.exec("UPDATE api_keys SET created_at = now() - interval '2 days', expires_at = now() - interval '1 second' WHERE name = 'expiring'")
	if s := k.keyStatus(expiring, "GET", "/api/status"); s != 401 {
		t.Fatalf("expired: %d", s)
	}

	// Locking suspends, unlocking restores.
	f.expect("PATCH", "/api/users/kim", map[string]any{"locked": true}, 204)
	if s := k.keyStatus(key, "GET", "/api/status"); s != 401 {
		t.Fatalf("locked: %d", s)
	}
	f.expect("PATCH", "/api/users/kim", map[string]any{"locked": false}, 204)
	if s := k.keyStatus(key, "GET", "/api/status"); s != 200 {
		t.Fatalf("unlocked: %d", s)
	}

	// A password reset with the opt-out keeps the key, but the owner must
	// choose a password first; a plain reset revokes it.
	f.expect("POST", "/api/users/kim/password", map[string]any{"revokeApiKeys": false}, 200)
	resp, out := k.keyRequest(key, "GET", "/api/status", nil, nil)
	if resp.StatusCode != 403 || out["passwordChangeRequired"] != true {
		t.Fatalf("must change password: %d %v", resp.StatusCode, out)
	}
	f.expect("POST", "/api/users/kim/password", nil, 200)
	if s := k.keyStatus(key, "GET", "/api/status"); s != 401 {
		t.Fatalf("after reset: %d", s)
	}

	// An admin's 2FA reset keeps the keys, which wait for 2FA again.
	f.addUser("other", keyPassword, true)
	o := f.as()
	o.loginOK("other", keyPassword)
	otherKey := o.createKey("k", []string{"read"}, o.code(o.adminSecrets["other"]))
	f.expect("POST", "/api/users/other/2fa/reset", nil, 204)
	resp, out = o.keyRequest(otherKey, "GET", "/api/status", nil, nil)
	if resp.StatusCode != 403 || out["twoFactorSetupRequired"] != true {
		t.Fatalf("2FA reset: %d %v", resp.StatusCode, out)
	}

	// An admin revokes any key, also their own; removal deletes the keys.
	f.expect("DELETE", "/api/api-keys/999999", nil, 404)
	var adminKeyID int64
	for _, item := range all["keys"].([]any) {
		m := item.(map[string]any)
		if m["user"] == "admin" {
			adminKeyID = int64(m["id"].(float64))
		}
	}
	f.expect("DELETE", "/api/api-keys/"+itoa(adminKeyID), nil, 204)
	if s := f.keyStatus(adminKey, "GET", "/api/status"); s != 401 {
		t.Fatalf("admin revoked own key: %d", s)
	}
	f.expect("DELETE", "/api/users/other", nil, 204)
	if s := o.keyStatus(otherKey, "GET", "/api/status"); s != 401 {
		t.Fatalf("removed owner: %d", s)
	}
}

func TestAPIKeyRates(t *testing.T) {
	f, logs := newKeyFixture(t)
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	key := f.createKey("busy", []string{"read"}, "")
	for i := range apiKeyLimit {
		if s := f.keyStatus(key, "GET", "/api/status"); s != 200 {
			t.Fatalf("request %d: %d", i+1, s)
		}
	}
	resp, _ := f.keyRequest(key, "GET", "/api/status", nil, nil)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("over the key rate: %d", resp.StatusCode)
	}

	n := len(auditLog(t, f.st))
	bad := "ma_" + strings.Repeat("b", 52)
	for i := range badAPIKeyLimit {
		if s := f.keyStatus(bad, "GET", "/api/status"); s != 401 {
			t.Fatalf("invalid key %d: %d", i+1, s)
		}
	}
	if s := f.keyStatus(bad, "GET", "/api/status"); s != 429 {
		t.Fatalf("over the invalid-key rate: %d", s)
	}
	// Password logins are not affected.
	f.as().loginOK("kim", keyPassword)

	entries := newEntries(t, f.st, n)
	if len(entries) < badAPIKeyLimit {
		t.Fatalf("%d entries for refused keys", len(entries))
	}
	if r := entries[0]; r.Action != "auth.login_failed" || details(t, r)["method"] != "api_key" || details(t, r)["reason"] != "invalid" || r.UserID != nil {
		t.Errorf("refused key entry %+v %v", r, details(t, r))
	}
	// Neither key appears in the log.
	if l := logs.String(); strings.Contains(l, key) || strings.Contains(l, bad) {
		t.Error("an API key is in the log")
	}
	for _, r := range auditLog(t, f.st) {
		if strings.Contains(string(r.Details), key) || strings.Contains(string(r.Details), bad) {
			t.Error("an API key is in the audit log")
		}
	}
}

func TestAPIKeyRefusedAudit(t *testing.T) {
	f, _ := newKeyFixture(t)
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	key := f.createKey("old", []string{"read"}, "")
	list := f.expect("GET", "/api/profile/api-keys", nil, 200)
	id := int64(list["keys"].([]any)[0].(map[string]any)["id"].(float64))
	n := len(auditLog(t, f.st))
	f.expect("DELETE", "/api/profile/api-keys/"+itoa(id), nil, 204)
	f.keyStatus(key, "GET", "/api/status")
	entries := newEntries(t, f.st, n)
	if len(entries) != 2 {
		t.Fatalf("entries %v", entries)
	}
	if r := entries[0]; r.Action != "api_key.revoke" || r.TargetName != "kim" || details(t, r)["key_name"] != "old" || details(t, r)["reason"] != "user" {
		t.Errorf("revoke entry %+v %v", r, details(t, r))
	}
	if r := entries[1]; r.Action != "auth.login_failed" || r.UserName != "kim" || details(t, r)["reason"] != "revoked" || details(t, r)["key_name"] != "old" {
		t.Errorf("refused entry %+v %v", r, details(t, r))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Moving an account to another user takes effect for keys issued before:
// authorization follows the current owner on each request, with no key
// revoked or issued again.
func TestAPIKeyAfterAccountTransfer(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	tester, err := f.st.GetUserByName(ctx, "tester")
	if err != nil {
		t.Fatal(err)
	}
	newOwner, _ := sessionFor(t, f.st, "newowner")
	old := credential{name: "tester's key", key: readKey(t, f.st, tester)}
	gaining := credential{name: "newowner's key", key: readKey(t, f.st, newOwner)}
	rich := "/api/messages/" + f.ids["rich"]
	objects := []string{"", "/html", "/raw", "/parts/3", "/conversation"}
	accounts := func(c credential) []string {
		t.Helper()
		var a accountsResponse
		f.fetchJSON(c, "/api/accounts", &a)
		out := []string{}
		for _, acc := range a.Accounts {
			out = append(out, acc.Name)
		}
		return out
	}
	status := func(c credential, path string) int {
		t.Helper()
		s, _ := f.fetch(c, path)
		return s
	}

	// Before: tester's key reads rich; newowner's finds nothing.
	if got := f.listAs(old, "to=alice%40example.com", false); !equal(got, []string{"rich"}) {
		t.Fatalf("old owner's search before: %v", got)
	}
	for _, p := range objects {
		if s := status(old, rich+p); s != http.StatusOK {
			t.Fatalf("GET rich%s with the old owner's key before: %d", p, s)
		}
		if s := status(gaining, rich+p); s != http.StatusNotFound {
			t.Fatalf("GET rich%s with the new owner's key before: %d", p, s)
		}
	}
	if got := f.listAs(gaining, "", false); len(got) != 0 {
		t.Fatalf("new owner's list before: %v", got)
	}

	alice, err := accountByName(f.st, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetAccountOwner(ctx, alice.Ref(), newOwner.ID); err != nil {
		t.Fatal(err)
	}

	// After: the old key still works for what tester keeps (bob: invoice,
	// newsletter), and has lost alice with rich and meeting.
	if s := status(old, "/api/status"); s != http.StatusOK {
		t.Errorf("status with the old owner's key: %d", s)
	}
	if got := accounts(old); !equal(got, []string{"bob"}) {
		t.Errorf("old owner's accounts: %v", got)
	}
	if got := f.listAs(old, "", false); !equal(got, []string{"newsletter", "invoice"}) {
		t.Errorf("old owner's list: %v", got)
	}
	for _, name := range []string{"invoice", "newsletter"} {
		var d messageJSON
		f.fetchJSON(old, "/api/messages/"+f.ids[name], &d)
		if len(d.Locations) != 1 || d.Locations[0].Account != "bob" {
			t.Errorf("old owner's locations of %s: %+v", name, d.Locations)
		}
	}
	for _, q := range []string{"to=alice%40example.com", "q=Vertrag", "q=roadmap", "account=alice"} {
		for _, grouped := range []bool{false, true} {
			if got := f.listAs(old, q, grouped); len(got) != 0 {
				t.Errorf("old owner's search %s (grouped %v): %v", q, grouped, got)
			}
		}
	}
	for _, p := range objects {
		if s := status(old, rich+p); s != http.StatusNotFound {
			t.Errorf("GET rich%s with the old owner's key: %d, want 404", p, s)
		}
	}
	if s := status(old, "/api/messages/"+f.ids["meeting"]); s != http.StatusNotFound {
		t.Errorf("GET meeting with the old owner's key: %d, want 404", s)
	}

	// The new owner's key, issued before, gains alice/INBOX and the bytes.
	if got := accounts(gaining); !equal(got, []string{"alice"}) {
		t.Errorf("new owner's accounts: %v", got)
	}
	if got := f.listAs(gaining, "", false); !equal(got, []string{"rich", "meeting", "invoice"}) {
		t.Errorf("new owner's list: %v", got)
	}
	var d messageJSON
	f.fetchJSON(gaining, rich, &d)
	if len(d.Locations) != 1 || d.Locations[0].Account != "alice" || d.Locations[0].Folder != "INBOX" {
		t.Errorf("new owner's locations: %+v", d.Locations)
	}
	for _, p := range objects {
		s, body := f.fetch(gaining, rich+p)
		if s != http.StatusOK {
			t.Errorf("GET rich%s with the new owner's key: %d", p, s)
		}
		if (p == "/raw" && string(body) != msgRich) || (p == "/parts/3" && string(body) != "%PDF-1.4\n") {
			t.Errorf("GET rich%s with the new owner's key: %q", p, body)
		}
	}
}
