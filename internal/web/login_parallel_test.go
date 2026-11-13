package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// Parallel attempts against the login limits (GHSA-w6jg-ffvw-q4f5, H1):
// each test sends n requests at once, holds those that reached the
// expensive check, waits until every request is held, waits in the
// limiter or has its answer, and only then lets them go on. Without the
// reservation in the limiter, all n reach the check.

type attemptReply struct {
	status int
	msg    string
	err    error
}

// try sends one request like f.do, from any goroutine: it reports errors
// instead of failing the test. A non-empty bearer sends an API key instead
// of the browser's headers.
func (f *authFixture) try(method, path string, body any, bearer string) attemptReply {
	var r io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, r)
	if err != nil {
		return attemptReply{err: err}
	}
	client := f.client
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
		client = http.DefaultClient
	} else {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", f.srv.URL)
	}
	resp, err := client.Do(req)
	if err != nil {
		return attemptReply{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return attemptReply{status: resp.StatusCode, msg: out.Error}
}

// spinUntil returns once cond holds. Requests that wait in the limiter or
// on a lock are a state nothing signals, so it checks again right away, as
// waitForRow does in the store tests, bounded by testwait.Limit.
func spinUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testwait.Limit(t))
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
	}
}

// parallel sends n requests at once. Once held() (requests stopped at the
// check), waiting() (requests waiting in the limiter) and the finished
// requests add up to n, it calls release and returns all replies.
func parallel(t *testing.T, n int, send func(i int) attemptReply, held, waiting func() int, release func()) []attemptReply {
	t.Helper()
	var finished atomic.Int32
	replies := make(chan attemptReply, n)
	for i := range n {
		go func() {
			r := send(i)
			finished.Add(1)
			replies <- r
		}()
	}
	spinUntil(t, "every request held, waiting or answered", func() bool {
		return held()+waiting()+int(finished.Load()) == n
	})
	release()
	out := make([]attemptReply, 0, n)
	for range n {
		r := <-replies
		if r.err != nil {
			t.Fatal(r.err)
		}
		out = append(out, r)
	}
	return out
}

// holdVerify holds every password check at the hasher until release is
// called, and counts the checks.
func holdVerify(f *authFixture) (checks *atomic.Int32, release func()) {
	checks = &atomic.Int32{}
	gate := make(chan struct{})
	f.server.beforeVerify = func() {
		checks.Add(1)
		<-gate
	}
	return checks, func() { close(gate) }
}

func count(replies []attemptReply, status int, msg string) int {
	n := 0
	for _, r := range replies {
		if r.status == status && strings.Contains(r.msg, msg) {
			n++
		}
	}
	return n
}

const wrongPassword = "wrong horse battery"

// Endpoints that check a password: parallel wrong passwords reach the
// hasher at most as often as failures are left before the block.
func TestParallelPasswordAttempts(t *testing.T) {
	const n = 50
	const password = "correct horse battery"
	cases := []struct {
		name  string
		setup func(f *authFixture)
		send  func(f *authFixture, i int) attemptReply
		limit int
	}{
		{"login", nil, func(f *authFixture, _ int) attemptReply {
			return f.try("POST", "/api/session", map[string]string{"username": "alice", "password": wrongPassword}, "")
		}, 5},
		{"login of an unknown name", nil, func(f *authFixture, _ int) attemptReply {
			return f.try("POST", "/api/session", map[string]string{"username": "nobody", "password": wrongPassword}, "")
		}, 5},
		{"login of an invalid name", nil, func(f *authFixture, i int) attemptReply {
			return f.try("POST", "/api/session", map[string]string{"username": fmt.Sprintf("x %d", i), "password": wrongPassword}, "")
		}, 5},
		{"logins from one address", nil, func(f *authFixture, i int) attemptReply {
			return f.try("POST", "/api/session", map[string]string{"username": fmt.Sprintf("user%d", i), "password": wrongPassword}, "")
		}, 20},
		{"password change", loginAlice, func(f *authFixture, _ int) attemptReply {
			return f.try("PUT", profilePasswordPath, map[string]string{"current": wrongPassword, "new": "another horse battery"}, "")
		}, 5},
		{"API key creation", loginAlice, func(f *authFixture, i int) attemptReply {
			return f.try("POST", "/api/profile/api-keys", map[string]any{"name": fmt.Sprintf("k%d", i), "scopes": []string{"read"}, "currentPassword": wrongPassword}, "")
		}, 5},
		{"turning 2FA off", func(f *authFixture) {
			u, err := f.st.GetUserByName(context.Background(), "alice")
			if err != nil {
				t.Fatal(err)
			}
			f.enableTOTP(u)
			f.loginOK("alice", password)
		}, func(f *authFixture, _ int) attemptReply {
			return f.try("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": wrongPassword, "code": "000000"}, "")
		}, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newAuthFixture(t)
			f.addUser("alice", password, false)
			if c.setup != nil {
				c.setup(f)
			}
			checks, release := holdVerify(f)
			replies := parallel(t, n, func(i int) attemptReply { return c.send(f, i) },
				func() int { return int(checks.Load()) }, f.server.limiter.Waiting, release)
			if got := int(checks.Load()); got > c.limit {
				t.Errorf("%d password checks, want at most %d", got, c.limit)
			}
			if got := count(replies, 401, ""); got != c.limit {
				t.Errorf("%d answers 401, want %d", got, c.limit)
			}
			if got := count(replies, 429, "too many failed"); got != n-c.limit {
				t.Errorf("%d answers 429, want %d", got, n-c.limit)
			}
		})
	}
}

func loginAlice(f *authFixture) { f.loginOK("alice", "correct horse battery") }

// Endpoints that check a TOTP code: the requests that got past the limiter
// are held on the user's row lock, which every code check takes first.
func TestParallelSecondFactorAttempts(t *testing.T) {
	const n = 30
	const password = "correct horse battery"
	cases := []struct {
		name string
		// most is the most codes that may be checked: the name's limit of
		// 5, or the 3 codes a login challenge accepts (#6).
		most  int
		setup func(f *authFixture) func(i int) attemptReply
	}{
		{"2FA login", 3, func(f *authFixture) func(int) attemptReply {
			u, _ := f.st.GetUserByName(context.Background(), "alice")
			f.enableTOTP(u)
			_, out := f.do("POST", "/api/session", map[string]string{"username": "alice", "password": password})
			challenge, _ := out["challenge"].(string)
			if challenge == "" {
				t.Fatalf("no challenge: %v", out)
			}
			return func(int) attemptReply {
				return f.try("POST", "/api/session/2fa", map[string]string{"challenge": challenge, "code": "000000"}, "")
			}
		}},
		{"confirming 2FA", 5, func(f *authFixture) func(int) attemptReply {
			loginAlice(f)
			f.expect("POST", "/api/profile/2fa/setup", nil, 200)
			return func(int) attemptReply {
				return f.try("POST", "/api/profile/2fa/confirm", map[string]string{"code": "000000"}, "")
			}
		}},
		{"new recovery codes", 5, func(f *authFixture) func(int) attemptReply {
			u, _ := f.st.GetUserByName(context.Background(), "alice")
			f.enableTOTP(u)
			loginAlice(f)
			return func(int) attemptReply {
				return f.try("POST", "/api/profile/2fa/recovery-codes", map[string]string{"code": "000000"}, "")
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			// A pool for every request: held requests each keep a
			// connection while they wait for the row lock.
			_, dbURL := storetest.NewWithURL(t)
			st, err := store.Open(ctx, storetest.WithMaxConns(t, dbURL, n+10))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			f := newAuthFixtureOn(t, Options{}, io.Discard, st, dbURL)
			u := f.addUser("alice", password, false)
			send := c.setup(f)

			holder, err := pgx.Connect(ctx, dbURL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = holder.Close(ctx) })
			tx, err := holder.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, u.ID); err != nil {
				t.Fatal(err)
			}
			observer, err := pgx.Connect(ctx, dbURL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = observer.Close(ctx) })
			lockWaiters := func() int {
				var k int
				if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
					WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&k); err != nil {
					t.Fatal(err)
				}
				return k
			}
			replies := parallel(t, n, send, lockWaiters, f.server.limiter.Waiting, func() {
				if err := tx.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
			})
			// Each code checked and found wrong answers this, except on a
			// login the one that used the challenge's last attempt (#6): it
			// answers errTwoFactorExhausted, as the requests that found no
			// attempt left do. The audit log records only checked codes.
			got := count(replies, 401, errInvalidSecondFactor)
			if c.name == "2FA login" {
				got = 0
				for _, e := range auditLog(t, f.st) {
					if e.Action == "auth.login_failed" && details(t, e)["reason"] == reasonSecondFactor {
						got++
					}
				}
			}
			if got > c.most || got == 0 {
				t.Errorf("%d codes checked, want 1 to %d", got, c.most)
			}
			for _, r := range replies {
				if r.msg != errInvalidSecondFactor && r.status != 429 && (r.status != 401 || !strings.Contains(r.msg, "expired")) {
					t.Errorf("answer %d %q", r.status, r.msg)
				}
			}
		})
	}
}

// Parallel invalid API keys reach the lookup at most as often as the
// invalid-key limit allows.
func TestParallelInvalidAPIKeys(t *testing.T) {
	const n = 100
	f, _ := newKeyFixture(t)
	lookups := &atomic.Int32{}
	gate := make(chan struct{})
	f.server.beforeKeyLookup = func() {
		lookups.Add(1)
		<-gate
	}
	replies := parallel(t, n, func(int) attemptReply {
		key, _, _, err := auth.NewAPIKey()
		if err != nil {
			return attemptReply{err: err}
		}
		return f.try("GET", "/api/status", nil, key)
	}, func() int { return int(lookups.Load()) }, f.server.badKeyRate.Waiting, func() { close(gate) })
	if got := int(lookups.Load()); got > badAPIKeyLimit {
		t.Errorf("%d key lookups, want at most %d", got, badAPIKeyLimit)
	}
	if got := count(replies, 401, "invalid API key"); got != badAPIKeyLimit {
		t.Errorf("%d answers 401, want %d", got, badAPIKeyLimit)
	}
	if got := count(replies, 429, ""); got != n-badAPIKeyLimit {
		t.Errorf("%d answers 429, want %d", got, n-badAPIKeyLimit)
	}
}

// Valid keys in flight hold a place in the invalid-key limit only while
// they run: afterwards the full limit of invalid keys is still allowed.
func TestParallelValidAPIKeysKeepBudget(t *testing.T) {
	const n = 100
	f, _ := newKeyFixture(t)
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	key := f.createKey("busy", []string{"read"}, "")
	lookups := &atomic.Int32{}
	gate := make(chan struct{})
	f.server.beforeKeyLookup = func() {
		lookups.Add(1)
		<-gate
	}
	replies := parallel(t, n, func(int) attemptReply { return f.try("GET", "/api/status", nil, key) },
		func() int { return int(lookups.Load()) }, f.server.badKeyRate.Waiting, func() { close(gate) })
	if got := count(replies, 200, ""); got != n {
		t.Fatalf("%d of %d valid key requests answered 200", got, n)
	}
	f.server.beforeKeyLookup = nil
	bad := "ma_" + strings.Repeat("b", 52)
	for i := range badAPIKeyLimit {
		if s := f.keyStatus(bad, "GET", "/api/status"); s != 401 {
			t.Fatalf("invalid key %d after valid ones: %d", i+1, s)
		}
	}
	if s := f.keyStatus(bad, "GET", "/api/status"); s != 429 {
		t.Fatalf("over the invalid-key limit: %d", s)
	}
}

// A correct password after failures still logs in and forgets the name's
// failures, as before attempts were reserved.
func TestLoginAfterFailures(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	for range 2 {
		for range 4 {
			if resp := f.login("alice", wrongPassword); resp.StatusCode != 401 {
				t.Fatalf("wrong password: %d", resp.StatusCode)
			}
		}
		f.as().loginOK("alice", "correct horse battery")
	}
}

// The server log names a failed login only by a valid user name: a
// password typed into the name field stays out of it (L1).
func TestLoginLogOnlyValidNames(t *testing.T) {
	logs := &syncWriter{}
	f := newAuthFixtureLog(t, Options{}, logs)
	f.expect("POST", "/api/session", map[string]string{"username": "hunter2 Secret!", "password": wrongPassword}, 401)
	f.expect("POST", "/api/session", map[string]string{"username": "nobody", "password": wrongPassword}, 401)
	l := logs.String()
	if strings.Contains(strings.ToLower(l), "hunter2") {
		t.Errorf("an invalid name is in the log: %s", l)
	}
	if !strings.Contains(l, `msg="login failed" user_valid=false`) || !strings.Contains(l, `msg="login failed" user=nobody`) {
		t.Errorf("failed logins not logged: %s", l)
	}
}

// A refused attempt answers 429 for a block and for a wait past
// auth.MaxWait, and nothing when the client is gone.
func TestAttemptRefused(t *testing.T) {
	s := newAuthFixture(t).server
	req := httptest.NewRequest("POST", "/api/session", nil)
	for _, c := range []struct {
		wait  time.Duration
		err   error
		code  int
		retry string
	}{
		{0, nil, 0, ""},
		{90 * time.Second, nil, 429, "90"},
		{0, auth.ErrBusy, 429, "60"},
		{0, context.Canceled, 0, ""},
	} {
		rec := httptest.NewRecorder()
		refused := s.attemptRefused(rec, req, c.wait, c.err, errTooManyAttempts)
		if refused != (c.code != 0 || c.err != nil) {
			t.Errorf("%v %v: refused %v", c.wait, c.err, refused)
		}
		if c.code == 0 {
			if rec.Body.Len() != 0 {
				t.Errorf("%v %v: answered %s", c.wait, c.err, rec.Body)
			}
			continue
		}
		if rec.Code != c.code || rec.Header().Get("Retry-After") != c.retry {
			t.Errorf("%v %v: %d %q", c.wait, c.err, rec.Code, rec.Header().Get("Retry-After"))
		}
	}
}
