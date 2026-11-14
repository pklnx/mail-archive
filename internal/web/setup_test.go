package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/testwait"
)

// setupToken is a fresh setup token, made at run time.
func setupToken(t *testing.T) string {
	t.Helper()
	tok, err := config.NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// syncBuffer is a log the server writes from its request goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// newSetupFixture is an auth fixture on a new instance whose setup token
// tok was recorded at the fixture clock's now, as a start would; its log
// goes to logs.
func newSetupFixture(t *testing.T, tok string) (*authFixture, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	f := newAuthFixtureLog(t, Options{}, logs)
	if _, _, err := f.st.RecordSetupToken(context.Background(), store.HashSetupToken(tok), f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	return f, logs
}

func setupBody(tok, name, password string) map[string]string {
	return map[string]string{"token": tok, "username": name, "password": password}
}

// TestSetupSessionStates (#396, part 3): GET /api/session without a
// session says how the first admin is created: cli without a token, token
// with one, expired after 7 days, complete once a user exists.
func TestSetupSessionStates(t *testing.T) {
	f := newAuthFixture(t)
	state := func(want string, required bool) {
		t.Helper()
		out := f.expect("GET", "/api/session", nil, http.StatusUnauthorized)
		if out["setup"] != want || out["setupRequired"] != required {
			t.Errorf("session: %v, want setup %q, setupRequired %v", out, want, required)
		}
	}
	state(store.SetupCLI, true)
	tok := setupToken(t)
	if _, _, err := f.st.RecordSetupToken(context.Background(), store.HashSetupToken(tok), f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	state(store.SetupToken, true)
	f.clock.Advance(store.SetupTokenValidity)
	state(store.SetupExpired, true)
	// A start without a token revokes it.
	if _, err := f.st.ClearSetupToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	state(store.SetupCLI, true)
	f.addUser("alice", "correct horse battery", false)
	state(store.SetupComplete, false)
}

// TestSetup (#396, part 3): the setup link's request. A wrong or expired
// token answers 401, a bad name or password 400, and none of them creates
// a user. The right token creates the admin and logs it in (201 with the
// session cookie): the session must set up 2FA first. Both steps are
// audited with method setup_token, and the token is in no log line and no
// entry. A second attempt answers 404.
func TestSetup(t *testing.T) {
	tok := setupToken(t)
	f, logs := newSetupFixture(t, tok)
	pw := "correct horse battery"

	if out := f.expect("POST", setupPath, setupBody(setupToken(t), "root", pw), http.StatusUnauthorized); out["error"] != errSetupToken {
		t.Errorf("wrong token: %v", out)
	}
	if out := f.expect("POST", setupPath, setupBody(tok, "Not Valid!", pw), http.StatusBadRequest); !strings.Contains(fmt.Sprint(out["error"]), "user names") {
		t.Errorf("bad name: %v", out)
	}
	if out := f.expect("POST", setupPath, setupBody(tok, "root", "short"), http.StatusBadRequest); !strings.Contains(fmt.Sprint(out["error"]), "12 characters") {
		t.Errorf("short password: %v", out)
	}
	f.clock.Advance(store.SetupTokenValidity)
	f.expect("POST", setupPath, setupBody(tok, "root", pw), http.StatusUnauthorized)
	f.clock.Advance(-time.Second)
	if n, _ := f.st.CountUsers(context.Background()); n != 0 {
		t.Fatalf("%d users after refused attempts", n)
	}

	before := len(auditLog(t, f.st))
	resp, out := f.do("POST", setupPath, setupBody(tok, " Root ", pw))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %v", resp.StatusCode, out)
	}
	user, _ := out["user"].(map[string]any)
	if user["name"] != "root" || user["admin"] != true || user["twoFactorRequired"] != true || user["twoFactorEnabled"] != false {
		t.Errorf("setup answer: %v", out)
	}
	if len(resp.Cookies()) == 0 {
		t.Error("no session cookie")
	}
	// Logged in, and held at the 2FA setup like every new admin.
	if out := f.expect("GET", "/api/session", nil, http.StatusOK); out["user"].(map[string]any)["name"] != "root" {
		t.Errorf("session after setup: %v", out)
	}
	if out := f.expect("GET", "/api/accounts", nil, http.StatusForbidden); out["twoFactorSetupRequired"] != true {
		t.Errorf("before the 2FA setup: %v", out)
	}
	f.expect("POST", "/api/profile/2fa/setup", map[string]any{}, http.StatusOK)

	u, err := f.st.GetUserByName(context.Background(), "root")
	if err != nil || !u.IsAdmin || u.MustChangePassword {
		t.Fatalf("the admin: %+v %v", u, err)
	}
	entries := newEntries(t, f.st, before)
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
		if e.Action == "user.setup" || e.Action == "auth.login" {
			if d := details(t, e); d["method"] != "setup_token" {
				t.Errorf("%s details: %v", e.Action, d)
			}
			if e.UserID == nil || *e.UserID != u.ID {
				t.Errorf("%s user: %v", e.Action, e.UserID)
			}
		}
		if e.Action == "user.setup" && (e.TargetType != store.TargetUser || e.TargetID != fmt.Sprint(u.ID)) {
			t.Errorf("user.setup target: %s %s", e.TargetType, e.TargetID)
		}
		if strings.Contains(string(e.Details), tok) {
			t.Errorf("%s holds the token", e.Action)
		}
	}
	if len(actions) < 2 || actions[0] != "user.setup" || actions[1] != "auth.login" {
		t.Errorf("entries: %v", actions)
	}

	if out := f.expect("POST", setupPath, setupBody(tok, "second", pw), http.StatusNotFound); out["error"] != errSetupUnavailable {
		t.Errorf("second attempt: %v", out)
	}
	if out := f.expect("GET", "/api/session", nil, http.StatusOK); out["user"] == nil {
		t.Errorf("session lost: %v", out)
	}
	if strings.Contains(logs.String(), tok) {
		t.Errorf("the log holds the setup token:\n%s", logs)
	}
	if !strings.Contains(logs.String(), "first admin created with the setup link") {
		t.Errorf("the log lacks the setup:\n%s", logs)
	}
}

// TestSetupUnavailable (#396, part 3): without a stored token, and on an
// instance that has a user (set up from the command line, or migrated with
// users), the setup answers 404 whatever the token: the two cases look the
// same.
func TestSetupUnavailable(t *testing.T) {
	tok := setupToken(t)
	f := newAuthFixture(t)
	if out := f.expect("POST", setupPath, setupBody(tok, "root", "correct horse battery"), http.StatusNotFound); out["error"] != errSetupUnavailable {
		t.Errorf("no token: %v", out)
	}
	f.addUser("alice", "correct horse battery", true)
	// A start with a token after the setup stores nothing.
	if _, _, err := f.st.RecordSetupToken(context.Background(), store.HashSetupToken(tok), f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	for _, try := range []string{tok, setupToken(t)} {
		if out := f.expect("POST", setupPath, setupBody(try, "root", "correct horse battery"), http.StatusNotFound); out["error"] != errSetupUnavailable {
			t.Errorf("with a user: %v", out)
		}
	}
	if _, err := f.st.GetUserByName(context.Background(), "root"); err == nil {
		t.Error("a user was created")
	}
}

// TestSetupLimiter (#396, part 3): wrong setup tokens count as failed
// logins of the address. After 20 of them the address is blocked, also for
// the right token (429 with Retry-After).
func TestSetupLimiter(t *testing.T) {
	tok := setupToken(t)
	f, _ := newSetupFixture(t, tok)
	for range 20 {
		f.expect("POST", setupPath, setupBody(setupToken(t), "root", "correct horse battery"), http.StatusUnauthorized)
	}
	resp, out := f.do("POST", setupPath, setupBody(tok, "root", "correct horse battery"))
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("blocked: %d %v", resp.StatusCode, out)
	}
	if n, _ := f.st.CountUsers(context.Background()); n != 0 {
		t.Errorf("%d users while blocked", n)
	}
}

// TestSetupConcurrent (#396, part 3): two setups with the right token at
// once. Both pass the first check; the test holds the row of
// instance_state until both wait for it (the signal), then exactly one
// creates the admin (201) and the other answers 404.
func TestSetupConcurrent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	tok := setupToken(t)
	f, _ := newSetupFixture(t, tok)
	holder, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close(context.Background()) }()
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM instance_state FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		status int
		name   string
	}
	done := make(chan result, 2)
	for _, name := range []string{"first", "second"} {
		go func() {
			b := strings.NewReader(fmt.Sprintf(`{"token":%q,"username":%q,"password":"correct horse battery"}`, tok, name))
			req, _ := http.NewRequestWithContext(ctx, "POST", f.srv.URL+setupPath, b)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", f.srv.URL)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				done <- result{status: -1, name: name}
				return
			}
			_ = resp.Body.Close()
			done <- result{status: resp.StatusCode, name: name}
		}()
	}
	observer, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observer.Close(context.Background()) }()
	for {
		var n int
		if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n); err != nil {
			t.Fatalf("waiting for both setups: %v", err)
		}
		if n == 2 {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("a setup ended before the row was released: %+v", r)
		default:
		}
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	got := map[int]int{}
	for range 2 {
		r := <-done
		got[r.status]++
	}
	if got[http.StatusCreated] != 1 || got[http.StatusNotFound] != 1 {
		t.Errorf("answers %v, want one 201 and one 404", got)
	}
	if n, _ := f.st.CountUsers(context.Background()); n != 1 {
		t.Errorf("%d users, want 1", n)
	}
}
