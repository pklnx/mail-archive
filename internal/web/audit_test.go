package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
)

// auditLog returns all audit entries, oldest first.
func auditLog(t *testing.T, st *store.Store) []store.AuditRecord {
	t.Helper()
	var out []store.AuditRecord
	f := store.AuditFilter{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour)}
	if err := st.ExportAudit(context.Background(), f, func(r store.AuditRecord) error {
		out = append(out, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func details(t *testing.T, r store.AuditRecord) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(r.Details, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// newEntries returns the entries written since the log had n of them.
func newEntries(t *testing.T, st *store.Store, n int) []store.AuditRecord {
	t.Helper()
	return auditLog(t, st)[n:]
}

// breakAudit makes every audit insert fail: the current month's partition
// is detached and keeps its name, so it cannot be created again.
func breakAudit(t *testing.T, dbURL string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	now := time.Now().UTC()
	part := fmt.Sprintf("audit_log_%04d_%02d", now.Year(), int(now.Month()))
	if _, err := conn.Exec(ctx, "ALTER TABLE audit_log DETACH PARTITION "+part); err != nil {
		t.Fatal(err)
	}
}

func TestAuditReads(t *testing.T) {
	f := newAPIFixture(t)
	inv, rich := f.ids["invoice"], f.ids["rich"]
	cursor := encodeCursor(time.Now(), inv)
	for _, c := range []struct {
		path, action string
		target       string
		details      map[string]any
	}{
		{"/api/messages?q=Rechnung&account=alice&limit=5", "message.search", "", map[string]any{"q": "Rechnung", "account": "alice"}},
		{"/api/messages?from=bob&group=1", "message.search", "", map[string]any{"from": "bob", "group": "1"}},
		{"/api/messages?after=2026-01-01", "message.search", "", map[string]any{"after": "2026-01-01"}},
		{"/api/messages?after=2026-01-01T00:00:00%2B01:00&before=2026-01-02T00:00:00%2B01:00", "message.search", "",
			map[string]any{"after": "2026-01-01T00:00:00+01:00", "before": "2026-01-02T00:00:00+01:00"}}, // as sent
		{"/api/messages/" + inv, "message.open", inv, map[string]any{}},
		{"/api/messages/" + rich + "/html", "message.html", rich, map[string]any{"inline_images": float64(1)}},
		{"/api/messages/" + rich + "/html?images=1", "message.html", rich, map[string]any{"images": true, "inline_images": float64(1)}},
		{"/api/messages/" + inv + "/raw", "message.raw", inv, map[string]any{}},
		{"/api/messages/" + rich + "/parts/1", "message.part", rich,
			map[string]any{"index": float64(1), "content_type": "image/png", "inline": true}},
		// Page views: no entry.
		{"/api/messages", "", "", nil},
		{"/api/messages?account=alice&folder=INBOX", "", "", nil},
		{"/api/messages?gone=1", "", "", nil},
		{"/api/messages?q=Rechnung&cursor=" + cursor, "", "", nil},
		{"/api/messages?q=Rechnung&thread=x", "", "", nil},
		{"/api/messages/" + inv + "/conversation", "", "", nil},
		{"/api/status", "", "", nil},
		{"/api/accounts", "", "", nil},
	} {
		n := len(auditLog(t, f.st))
		if resp := f.get(c.path); resp.StatusCode != 200 {
			t.Fatalf("%s: status %d", c.path, resp.StatusCode)
		}
		got := newEntries(t, f.st, n)
		if c.action == "" {
			if len(got) != 0 {
				t.Errorf("%s: logged %+v", c.path, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%s: %d entries", c.path, len(got))
		}
		e := got[0]
		if e.Action != c.action || e.Actor != store.ActorSession || e.UserName != "tester" || e.UserID == nil ||
			e.ClientIP != "127.0.0.1" || !strings.HasPrefix(e.UserAgent, "Go-http-client/") {
			t.Errorf("%s: %+v", c.path, e)
		}
		if c.target != "" && (e.TargetType != store.TargetMessage || e.TargetID != c.target) {
			t.Errorf("%s: target %s %s", c.path, e.TargetType, e.TargetID)
		}
		if d := details(t, e); fmt.Sprint(d) != fmt.Sprint(c.details) {
			t.Errorf("%s: details %v, want %v", c.path, d, c.details)
		}
	}
}

func TestAuditReadsFailClosed(t *testing.T) {
	f := newAPIFixture(t)
	breakAudit(t, f.dbURL)
	for _, path := range []string{
		"/api/messages/" + f.ids["invoice"],
		"/api/messages/" + f.ids["rich"] + "/html",
		"/api/messages/" + f.ids["invoice"] + "/raw",
		"/api/messages/" + f.ids["rich"] + "/parts/2",
		"/api/messages?q=Rechnung",
	} {
		resp := f.get(path)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), errAuditUnavailable) ||
			strings.Contains(string(body), "Rechnung liegt") || strings.Contains(string(body), "script") {
			t.Errorf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	// Page views work without the log.
	if resp := f.get("/api/messages"); resp.StatusCode != 200 {
		t.Errorf("list: %d", resp.StatusCode)
	}
}

func TestAuditLogins(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", "correct horse battery", false)
	admin := f.addUser("root", "correct horse battery", true)
	type want struct {
		action string
		user   *store.User
		detail map[string]any
	}
	step := func(name string, do func(), w *want) {
		t.Helper()
		n := len(auditLog(t, f.st))
		do()
		got := newEntries(t, f.st, n)
		if w == nil {
			if len(got) != 0 {
				t.Errorf("%s: logged %+v", name, got)
			}
			return
		}
		if len(got) != 1 {
			t.Fatalf("%s: %d entries: %+v", name, len(got), got)
		}
		e := got[0]
		if e.Action != w.action || e.Actor != store.ActorSession || e.ClientIP != "127.0.0.1" || e.UserAgent == "" {
			t.Errorf("%s: %+v", name, e)
		}
		if w.user != nil && (e.UserID == nil || *e.UserID != w.user.ID || e.UserName != w.user.Name) {
			t.Errorf("%s: user %v %q", name, e.UserID, e.UserName)
		}
		if w.user == nil && e.UserID != nil {
			t.Errorf("%s: user %v", name, *e.UserID)
		}
		d := details(t, e)
		for k, v := range w.detail {
			if d[k] != v {
				t.Errorf("%s: %s = %v, want %v (%v)", name, k, d[k], v, d)
			}
		}
		if _, ok := w.detail["name"]; !ok {
			if _, has := d["name"]; has {
				t.Errorf("%s: name recorded: %v", name, d)
			}
		}
	}
	login := func(name, pw string, status int) func() {
		return func() {
			if resp := f.login(name, pw); resp.StatusCode != status {
				t.Fatalf("login %s: %d", name, resp.StatusCode)
			}
		}
	}
	step("password", login("alice", "correct horse battery", 200),
		&want{"auth.login", alice, map[string]any{"method": "password"}})
	step("logout", func() { f.expect("DELETE", "/api/session", nil, 204) },
		&want{"auth.logout", alice, nil})
	step("logout without session", func() { f.expect("DELETE", "/api/session", nil, 204) }, nil)
	step("wrong password", login("alice", "wrong horse battery", 401),
		&want{"auth.login_failed", alice, map[string]any{"method": "password", "reason": "wrong_password"}})
	step("unknown user", login("nobody", "wrong horse battery", 401),
		&want{"auth.login_failed", nil, map[string]any{"reason": "unknown_user", "name": "nobody"}})
	step("password as name", login("my secret password", "x", 401),
		&want{"auth.login_failed", nil, map[string]any{"reason": "unknown_user"}})
	step("2fa", login("root", "correct horse battery", 200),
		&want{"auth.login", admin, map[string]any{"method": "2fa"}})
	f.expect("DELETE", "/api/session", nil, 204)
	step("wrong second factor", func() {
		out := f.expect("POST", "/api/session", map[string]string{"username": "root", "password": "correct horse battery"}, 200)
		f.expect("POST", "/api/session/2fa", map[string]string{"challenge": out["challenge"].(string), "code": "000000"}, 401)
	}, &want{"auth.login_failed", admin, map[string]any{"method": "2fa", "reason": "second_factor"}})

	if err := f.st.SetUserLocked(context.Background(), alice.ID, true); err != nil {
		t.Fatal(err)
	}
	step("locked", login("alice", "correct horse battery", 403),
		&want{"auth.login_failed", alice, map[string]any{"reason": "locked"}})
	// Over the limit: refused without an entry, so a flood cannot grow the log.
	for range 5 {
		f.login("bob", "wrong horse battery")
	}
	step("limited", login("bob", "wrong horse battery", 429), nil)
}

// TestAuditChanges runs every change of users and the own profile once and
// checks that each writes exactly one entry.
func TestAuditChanges(t *testing.T) {
	f := newAuthFixture(t)
	admin := f.addUser("root", "correct horse battery", true)
	alice := f.addUser("alice", "correct horse battery", false)
	if resp := f.login("root", "correct horse battery"); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	aliceID := strconv.FormatInt(alice.ID, 10)
	var carolID string
	for _, c := range []struct {
		method, path string
		body         any
		status       int
		action       string
		target       *string
		detail       map[string]any
	}{
		{"POST", "/api/users", map[string]any{"name": "carol"}, 201, "user.add", nil, map[string]any{"admin": false}},
		{"PATCH", "/api/users/alice", map[string]any{"locked": true}, 204, "user.change", &aliceID, map[string]any{"locked": true}},
		{"PATCH", "/api/users/alice", map[string]any{"admin": true}, 204, "user.change", &aliceID, map[string]any{"admin": true}},
		{"POST", "/api/users/alice/password", map[string]any{"removePasskeys": false}, 200, "user.password_reset", &aliceID, map[string]any{"passkeys_removed": false}},
		{"POST", "/api/users/alice/2fa/reset", nil, 204, "user.2fa_reset", &aliceID, nil},
		{"DELETE", "/api/users/alice/passkeys", nil, 204, "user.passkeys_removed", &aliceID, map[string]any{"count": float64(0)}},
		{"DELETE", "/api/users/carol", nil, 204, "user.remove", &carolID, nil},
		{"PUT", "/api/profile/password", map[string]any{"current": "correct horse battery", "new": "another horse battery"}, 204, "profile.password_change", nil, nil},
	} {
		n := len(auditLog(t, f.st))
		f.expect(c.method, c.path, c.body, c.status)
		got := newEntries(t, f.st, n)
		if len(got) != 1 {
			t.Fatalf("%s %s: %d entries", c.method, c.path, len(got))
		}
		e := got[0]
		if e.Action != c.action || e.Actor != store.ActorSession || e.UserID == nil || *e.UserID != admin.ID {
			t.Errorf("%s %s: %+v", c.method, c.path, e)
		}
		if c.action == "user.add" {
			if e.TargetType != store.TargetUser || e.TargetName != "carol" || e.TargetID == "" {
				t.Errorf("user.add target: %+v", e)
			}
			carolID = e.TargetID
		} else if c.target != nil && (e.TargetType != store.TargetUser || e.TargetID != *c.target) {
			t.Errorf("%s %s: target %q %q", c.method, c.path, e.TargetType, e.TargetID)
		}
		d := details(t, e)
		for k, v := range c.detail {
			if d[k] != v {
				t.Errorf("%s %s: %s = %v, want %v", c.method, c.path, k, d[k], v)
			}
		}
	}
	// A refused change writes nothing.
	n := len(auditLog(t, f.st))
	f.expect("POST", "/api/users", map[string]any{"name": "root"}, 409)
	if got := newEntries(t, f.st, n); len(got) != 0 {
		t.Errorf("refused change logged: %+v", got)
	}
}

func TestAuditChangesFailClosed(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("root", "correct horse battery", true)
	alice := f.addUser("alice", "correct horse battery", false)
	if resp := f.login("root", "correct horse battery"); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	breakAudit(t, f.dbURL)
	out := f.expect("PATCH", "/api/users/alice", map[string]any{"locked": true}, 503)
	if out["error"] != errAuditUnavailable {
		t.Errorf("answer %v", out)
	}
	if u, err := f.st.GetUserByID(context.Background(), alice.ID); err != nil || u.LockedAt != nil {
		t.Fatalf("alice was locked without an entry: %v", err)
	}
	// A logout still logs out.
	f.expect("DELETE", "/api/session", nil, 204)
	f.expect("GET", "/api/users", nil, 401)
	// A login creates no session.
	resp, _ := f.do("POST", "/api/session", map[string]string{"username": "alice", "password": "correct horse battery"})
	if resp.StatusCode != 503 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	f.expect("GET", "/api/session", nil, 401)
}

func TestAuditAccounts(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	type check struct {
		method, path string
		body         any
		status       int
		action       string
		detail       map[string]any
	}
	run := func(c check) store.AuditRecord {
		t.Helper()
		n := len(auditLog(t, f.st))
		f.do(c.method, c.path, c.body, c.status)
		got := newEntries(t, f.st, n)
		if len(got) != 1 {
			t.Fatalf("%s %s: %d entries: %+v", c.method, c.path, len(got), got)
		}
		e := got[0]
		if e.Action != c.action || e.UserName != "tester" {
			t.Errorf("%s %s: %+v", c.method, c.path, e)
		}
		d := details(t, e)
		for k, v := range c.detail {
			if fmt.Sprint(d[k]) != fmt.Sprint(v) {
				t.Errorf("%s %s: %s = %v, want %v", c.method, c.path, k, d[k], v)
			}
		}
		return e
	}
	e := run(check{"POST", "/api/accounts", f.newAccount("private", "alice", "secret"), 201, "account.add", map[string]any{"kind": "imap"}})
	if e.TargetType != store.TargetAccount || e.TargetName != "private" || e.TargetID == "" {
		t.Fatalf("account.add target %+v", e)
	}
	f.waitIdle("private")
	e = run(check{"PATCH", "/api/accounts/private", map[string]any{"name": "mine", "password": "secret", "enabled": false}, 204, "account.change",
		map[string]any{"fields": []any{"name", "password", "enabled"}, "name": map[string]any{"from": "private", "to": "mine"}, "enabled": false}})
	if strings.Contains(string(e.Details), "secret") {
		t.Errorf("password recorded: %s", e.Details)
	}
	run(check{"POST", "/api/accounts/mine/sync", map[string]any{"reconcile": true}, 202, "sync.start", map[string]any{"reconcile": true}})
	f.waitIdle("mine")
	run(check{"POST", "/api/sync", nil, 202, "sync.start", map[string]any{"accounts": 0}})
	run(check{"DELETE", "/api/accounts/mine", nil, 200, "account.remove", map[string]any{"result": "removed"}})
}

func TestClientIP(t *testing.T) {
	s := &Server{trustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}}
	for _, c := range []struct {
		remote string
		xff    []string
		want   string
	}{
		{"192.0.2.1:1234", nil, "192.0.2.1"},
		// An untrusted peer cannot choose its address.
		{"192.0.2.1:1234", []string{"198.51.100.7"}, "192.0.2.1"},
		{"10.0.0.2:1234", []string{"198.51.100.7"}, "198.51.100.7"},
		// Trusted proxies in the chain are skipped; the left part is the
		// client's own claim and not trusted.
		{"10.0.0.2:1234", []string{"6.6.6.6, 198.51.100.7, 10.0.0.9"}, "198.51.100.7"},
		{"[::1]:1234", []string{"198.51.100.7", "10.1.1.1"}, "198.51.100.7"},
		{"10.0.0.2:1234", []string{"not an address"}, "10.0.0.2"},
		{"10.0.0.2:1234", nil, "10.0.0.2"},
		{"10.0.0.2:1234", []string{"10.0.0.3"}, "10.0.0.2"},
		{"[::ffff:192.0.2.1]:1234", nil, "192.0.2.1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		for _, h := range c.xff {
			r.Header.Add("X-Forwarded-For", h)
		}
		if got := s.clientIP(r).String(); got != c.want {
			t.Errorf("%s %v: %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestAuditProfile(t *testing.T) {
	f := newPasskeyFixture(t)
	alice := f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	one := func(name, action string, do func()) map[string]any {
		t.Helper()
		n := len(auditLog(t, f.st))
		do()
		got := newEntries(t, f.st, n)
		if len(got) != 1 || got[0].Action != action || got[0].UserID == nil || *got[0].UserID != alice.ID {
			t.Fatalf("%s: %+v", name, got)
		}
		return details(t, got[0])
	}
	var secret string
	one("2fa on", "profile.2fa_enable", func() {
		secret = f.expect("POST", "/api/profile/2fa/setup", nil, 200)["secret"].(string)
		f.expect("POST", "/api/profile/2fa/confirm", map[string]string{"code": f.code(secret)}, 200)
	})
	one("recovery codes", "profile.recovery_codes", func() {
		f.expect("POST", "/api/profile/2fa/recovery-codes", map[string]string{"code": f.code(secret)}, 200)
	})
	one("2fa off", "profile.2fa_disable", func() {
		f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "correct horse battery", "code": f.code(secret)}, 204)
	})
	d := one("settings", "profile.change", func() {
		f.expect("PATCH", "/api/profile", map[string]string{"language": "de"}, 204)
	})
	if fmt.Sprint(d["fields"]) != "[language]" || fmt.Sprint(d["language"]) != "map[from:auto to:de]" || d["locale"] != nil {
		t.Errorf("profile.change details %v", d)
	}
	n := len(auditLog(t, f.st))
	f.expect("PATCH", "/api/profile", map[string]string{"language": "de"}, 204)
	if got := newEntries(t, f.st, n); len(got) != 0 {
		t.Errorf("unchanged settings logged: %+v", got)
	}
	k := newSoftKey(t)
	if d := one("passkey added", "profile.passkey_add", func() { f.addPasskey(k, "Phone", "correct horse battery", "") }); d["name"] != "Phone" {
		t.Errorf("passkey_add details %v", d)
	}
	other := f.as()
	if d := one("passkey login", "auth.login", func() { other.passkeyLogin(k, 200) }); d["method"] != "passkey" {
		t.Errorf("login details %v", d)
	}
	n = len(auditLog(t, f.st))
	f.as().passkeyLogin(newSoftKey(t), 401)
	if got := newEntries(t, f.st, n); len(got) != 1 || got[0].Action != "auth.login_failed" || got[0].UserID != nil ||
		details(t, got[0])["reason"] != "unknown_passkey" {
		t.Errorf("unknown passkey: %+v", got)
	}
	list := f.expect("GET", "/api/profile/passkeys", nil, 200)["passkeys"].([]any)
	id := int64(list[0].(map[string]any)["id"].(float64))
	if d := one("passkey removed", "profile.passkey_remove", func() {
		f.expect("DELETE", fmt.Sprintf("/api/profile/passkeys/%d", id), nil, 200)
	}); d["name"] != "Phone" {
		t.Errorf("passkey_remove details %v", d)
	}
}

func TestTruncateUserAgent(t *testing.T) {
	ua := truncateUserAgent(strings.Repeat("a", 255) + "é\x00\xff")
	if ua != strings.Repeat("a", 255) {
		t.Errorf("cut inside a character: %q", ua)
	}
	if got := truncateUserAgent("x\x00\xffy"); got != "x��y" {
		t.Errorf("cleaned %q", got)
	}
}
