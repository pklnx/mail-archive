package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/imaptest"
)

// Tests of the limits and answers of IMAP login checks. Dials are counted
// by a stub or go to scripted servers on the loopback interface; no test
// waits for time.

// upstreamText is text only the IMAP server or the network writes: it may
// be logged, never answered.
const upstreamText = "UPSTREAM-91c4"

// checkReply is the answer to a login check.
type checkReply struct {
	status     int
	Error      string `json:"error"`
	Code       string `json:"code"`
	RetryAfter int    `json:"retryAfter"`
	header     http.Header
	raw        string
}

// check sends a request that runs a login check, as the session token
// (empty: the fixture's own user).
func (f *manageFixture) check(token, method, path string, body any) checkReply {
	f.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, f.srv.URL+path, bytes.NewReader(b))
	if body == nil {
		req.Body = http.NoBody
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.srv.URL)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Error(err)
		return checkReply{}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	r := checkReply{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &r)
	return r
}

// addAccount runs the login check of adding an IMAP account for user name
// on host and port, as token.
func (f *manageFixture) addAccount(token, host string, port int, user, password string) checkReply {
	f.t.Helper()
	return f.check(token, "POST", "/api/accounts", map[string]any{
		"name": "new", "host": host, "port": port, "tls": "none", "username": user, "password": password,
	})
}

// wantRefused checks a 429 of the login-check limits.
func wantRefused(t *testing.T, r checkReply, retryAfter int) {
	t.Helper()
	if r.status != http.StatusTooManyRequests || r.Code != errTooManyChecks {
		t.Fatalf("answer %d %s, want 429 %s", r.status, r.raw, errTooManyChecks)
	}
	h, err := strconv.Atoi(r.header.Get("Retry-After"))
	if err != nil || h < 1 || h != r.RetryAfter || retryAfter > 0 && h != retryAfter {
		t.Errorf("Retry-After %q, retryAfter %d, want %d", r.header.Get("Retry-After"), r.RetryAfter, retryAfter)
	}
}

// wantFailed checks a failed check's answer.
func wantFailed(t *testing.T, r checkReply, status int, code string) {
	t.Helper()
	if r.status != status || r.Code != code {
		t.Fatalf("answer %d %s, want %d %s", r.status, r.raw, status, code)
	}
}

// dialStub replaces the syncer's Dial: it counts the dials and fails each
// with fail's error.
func dialStub(f *manageFixture, fail func(cfg imapsync.Config) error) *atomic.Int32 {
	n := &atomic.Int32{}
	f.server.syncer.Dial = func(_ context.Context, cfg imapsync.Config) (*imapsync.Conn, error) {
		n.Add(1)
		return nil, fail(cfg)
	}
	return n
}

// refused fails like a closed port: before LOGIN was sent.
func refused(cfg imapsync.Config) error {
	return &imapsync.Error{Category: imapsync.CategoryConnect, Op: "connect to " + cfg.Host,
		Err: errors.New("connection refused")}
}

// refusedLogin fails like a server that answers NO to LOGIN.
func refusedLogin(imapsync.Config) error {
	return &imapsync.Error{Category: imapsync.CategoryLogin, Op: "login", LoginSent: true,
		Err: errors.New("NO [AUTHENTICATIONFAILED] " + upstreamText)}
}

// users makes n users with sessions and returns their tokens.
func (f *manageFixture) users(n int) []string {
	f.t.Helper()
	tokens := make([]string, n)
	for i := range tokens {
		_, tokens[i] = sessionFor(f.t, f.st, fmt.Sprintf("checker%d", i))
	}
	return tokens
}

// The 21st check of a user within 10 minutes is refused before it
// connects; another user still checks. The targets vary, so that only the
// user's limit applies.
func TestLoginCheckUserLimit(t *testing.T) {
	f := newManageFixture(t)
	dials := dialStub(f, refused)
	for i := range loginCheckUserLimit {
		wantFailed(t, f.addAccount("", "imap.example", 993, fmt.Sprintf("user%d", i), "pw"), http.StatusUnprocessableEntity, "imap_connect")
	}
	wantRefused(t, f.addAccount("", "imap.example", 993, "one-more", "pw"), 0)
	if n := dials.Load(); n != loginCheckUserLimit {
		t.Errorf("%d dials, want %d", n, loginCheckUserLimit)
	}
	other := f.users(1)[0]
	wantFailed(t, f.addAccount(other, "imap.example", 993, "one-more", "pw"), http.StatusUnprocessableEntity, "imap_connect")
}

// The 61st check from one client address within 10 minutes is refused,
// whichever user sends it. Users and targets vary.
func TestLoginCheckAddressLimit(t *testing.T) {
	f := newManageFixture(t)
	dials := dialStub(f, refused)
	tokens := f.users(loginCheckAddrLimit/loginCheckUserLimit + 1)
	for i := range loginCheckAddrLimit {
		token := tokens[i/loginCheckUserLimit]
		wantFailed(t, f.addAccount(token, "imap.example", 993, fmt.Sprintf("user%d", i), "pw"), http.StatusUnprocessableEntity, "imap_connect")
	}
	wantRefused(t, f.addAccount(tokens[len(tokens)-1], "imap.example", 993, "one-more", "pw"), 0)
	if n := dials.Load(); n != loginCheckAddrLimit {
		t.Errorf("%d dials, want %d", n, loginCheckAddrLimit)
	}
}

// dropAfterLogin is a scripted server that closes the connection when it
// receives LOGIN, instead of answering NO.
func dropAfterLogin(t *testing.T) *imaptest.Script {
	return imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Send("* OK [CAPABILITY IMAP4rev1] ready"); err != nil {
			return err
		}
		_, err := c.Expect("LOGIN")
		return err
	})
}

// Failed logins count against the IMAP login (host, port, user name),
// whoever makes them, also when the server drops the connection instead of
// answering NO. The 11th check is refused before it connects; the user
// name's case and the host's spelling make no difference. Another user
// name on the same server is not affected.
func TestLoginCheckTargetLimit(t *testing.T) {
	f := newManageFixture(t)
	s := dropAfterLogin(t)
	var dials atomic.Int32
	f.server.syncer.Dial = func(ctx context.Context, cfg imapsync.Config) (*imapsync.Conn, error) {
		dials.Add(1)
		return imapsync.Dial(ctx, cfg)
	}
	tokens := f.users(2)
	for i := range loginCheckTargetLimit {
		wantFailed(t, f.addAccount(tokens[i%2], s.Host, s.Port, "Victim", "guess"+strconv.Itoa(i)), http.StatusUnprocessableEntity, "imap_protocol")
	}
	host := s.Host
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		host = "::ffff:" + host // the same address, written differently
	}
	wantRefused(t, f.addAccount("", host, s.Port, "VICTIM", "guess"), 0)
	if n := dials.Load(); n != loginCheckTargetLimit {
		t.Errorf("%d dials, want %d", n, loginCheckTargetLimit)
	}
	wantFailed(t, f.addAccount("", s.Host, s.Port, "someone-else", "pw"), http.StatusUnprocessableEntity, "imap_protocol")
}

// A failure before LOGIN was sent (here: nothing listens) does not count
// against the IMAP login.
func TestLoginCheckTargetCountsOnlyAfterLogin(t *testing.T) {
	f := newManageFixture(t)
	dials := dialStub(f, refused)
	tokens := f.users(2)
	for i := range loginCheckTargetLimit + 5 {
		wantFailed(t, f.addAccount(tokens[i%2], "imap.example", 993, "victim", "pw"), http.StatusUnprocessableEntity, "imap_connect")
	}
	if n := dials.Load(); n != loginCheckTargetLimit+5 {
		t.Errorf("%d dials", n)
	}
}

// An account's folder list with its saved password does not count against
// the IMAP login, nor is it refused by that limit: nobody can block another
// user's folder list by failing logins to the same mailbox.
func TestServerFoldersSkipTargetLimit(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	f.do("POST", "/api/accounts", f.newAccount("work", "alice", "secret"), http.StatusCreated)
	f.waitIdle("work")
	other := f.users(1)[0]
	for range loginCheckTargetLimit {
		wantFailed(t, f.addAccount(other, f.host, f.port, "alice", "wrong"), http.StatusUnprocessableEntity, "imap_login")
	}
	wantRefused(t, f.addAccount(other, f.host, f.port, "alice", "wrong"), 0)
	var out struct {
		Folders []serverFolderJSON `json:"folders"`
	}
	if err := json.Unmarshal(f.do("GET", "/api/accounts/work/server-folders", nil, http.StatusOK), &out); err != nil || len(out.Folders) != 2 {
		t.Fatalf("folders %+v, %v", out, err)
	}
}

// heldDials holds every dial until the test releases it, and counts them.
type heldDials struct {
	n       atomic.Int32
	release chan struct{}
}

func holdDials(f *manageFixture) *heldDials {
	h := &heldDials{release: make(chan struct{})}
	f.server.syncer.Dial = func(ctx context.Context, cfg imapsync.Config) (*imapsync.Conn, error) {
		h.n.Add(1)
		select {
		case <-h.release:
		case <-ctx.Done():
		}
		return nil, refusedLogin(cfg)
	}
	return h
}

// waits records the bounded waits for a turn, so that a test can end one
// as if its 15 seconds had passed.
type waits struct {
	mu      sync.Mutex
	cancels []context.CancelCauseFunc
	expired bool // new waits end at once
}

func recordWaits(f *manageFixture) *waits {
	w := &waits{}
	f.server.checks.waitContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancelCause(parent)
		w.mu.Lock()
		defer w.mu.Unlock()
		w.cancels = append(w.cancels, cancel)
		if w.expired {
			cancel(errCheckWait)
		}
		return ctx, func() { cancel(context.Canceled) }
	}
	return w
}

// expireLast ends the latest wait as if its time were up.
func (w *waits) expireLast() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cancels[len(w.cancels)-1](errCheckWait)
}

// Two checks run at a time, and one per user: a third waits for a turn
// and gets one when a check ends. A check that waits too long is refused
// with Retry-After: 15, without connecting, and gives back its place in
// the IMAP login's limit.
func TestLoginCheckTurns(t *testing.T) {
	f := newManageFixture(t)
	held := holdDials(f)
	w := recordWaits(f)
	tokens := f.users(3)
	replies := make(chan checkReply, 8)
	send := func(token, user string) {
		go func() { replies <- f.addAccount(token, "imap.example", 993, user, "pw") }()
	}
	waiting := func(n int) {
		spinUntil(t, fmt.Sprintf("%d checks waiting", n), func() bool { return f.server.checks.Waiting() == n })
	}
	dialed := func(n int32) {
		spinUntil(t, fmt.Sprintf("%d dials", n), func() bool { return held.n.Load() == n })
	}

	// One per user: the second check of user 0 waits, though a place is
	// free.
	send(tokens[0], "a")
	dialed(1)
	send(tokens[0], "b")
	waiting(1)
	// Two in all: user 1 runs, user 2 waits.
	send(tokens[1], "c")
	dialed(2)
	send(tokens[2], "d")
	waiting(2)
	if n := held.n.Load(); n != 2 {
		t.Fatalf("%d dials while two checks run", n)
	}
	// Each ended check lets one waiting check run.
	held.release <- struct{}{}
	wantFailed(t, <-replies, http.StatusUnprocessableEntity, "imap_login")
	dialed(3)
	waiting(1)
	held.release <- struct{}{}
	wantFailed(t, <-replies, http.StatusUnprocessableEntity, "imap_login")
	dialed(4)
	waiting(0)

	// Both places taken: a check for the IMAP login "victim" waits, and is
	// refused when its wait is up.
	send(tokens[2], "victim")
	waiting(1)
	w.expireLast()
	wantRefused(t, <-replies, int(loginCheckWait.Seconds()))
	if n := held.n.Load(); n != 4 {
		t.Errorf("%d dials, want 4: the refused check connected", n)
	}
	close(held.release)
	for range 2 {
		wantFailed(t, <-replies, http.StatusUnprocessableEntity, "imap_login")
	}

	// The refused check gave back its place in the IMAP login's limit: all
	// 10 failures are still allowed. A place still held would make the
	// 10th wait, and with every wait ended at once, be refused.
	w.mu.Lock()
	w.expired = true
	w.mu.Unlock()
	for i := range loginCheckTargetLimit {
		wantFailed(t, f.addAccount(tokens[i%3], "imap.example", 993, "victim", "pw"), http.StatusUnprocessableEntity, "imap_login")
	}
	wantRefused(t, f.addAccount(tokens[0], "imap.example", 993, "victim", "pw"), 0)
}

// syncWriterLog makes the server log into a buffer.
func syncWriterLog(f *manageFixture) *syncWriter {
	logs := &syncWriter{}
	f.server.log = slog.New(slog.NewTextHandler(logs, nil))
	return logs
}

// A login the server refuses answers 422 with code imap_login and a fixed
// text: the server's text is in the log only.
func TestLoginCheckAnswerHidesServerText(t *testing.T) {
	f := newManageFixture(t)
	logs := syncWriterLog(f)
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Send("* OK [CAPABILITY IMAP4rev1] ready"); err != nil {
			return err
		}
		tag, err := c.Expect("LOGIN")
		if err != nil {
			return err
		}
		return c.Send(tag + " NO [AUTHENTICATIONFAILED] " + upstreamText)
	})
	r := f.addAccount("", s.Host, s.Port, "u", "pw")
	wantFailed(t, r, http.StatusUnprocessableEntity, "imap_login")
	if r.Error != "the IMAP server refused the login" || strings.Contains(r.raw, upstreamText) {
		t.Errorf("answer %s", r.raw)
	}
	if l := logs.String(); !strings.Contains(l, "imap login check failed") || !strings.Contains(l, upstreamText) {
		t.Errorf("log lacks the server's text:\n%s", l)
	}
}

// A dial error names the address and the system's error text; neither is
// answered.
func TestLoginCheckAnswerHidesDialError(t *testing.T) {
	f := newManageFixture(t)
	logs := syncWriterLog(f)
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // nothing listens there now
	r := f.addAccount("", "127.0.0.1", port, "u", "pw")
	wantFailed(t, r, http.StatusUnprocessableEntity, "imap_connect")
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if r.Error != "cannot connect to the IMAP server" || strings.Contains(r.raw, addr) || strings.Contains(r.raw, "refused") {
		t.Errorf("answer %s", r.raw)
	}
	if !strings.Contains(logs.String(), addr) {
		t.Errorf("log lacks the address:\n%s", logs)
	}
}

// Every category has its code and fixed text, also the check's minute
// (a context deadline) and its read limit; the error's own text is never
// answered.
func TestLoginCheckCodes(t *testing.T) {
	f := newManageFixture(t)
	var next error
	dialStub(f, func(imapsync.Config) error { return next })
	for _, c := range []struct {
		err  error
		code string
		text string
	}{
		{&imapsync.Error{Category: imapsync.CategoryConnect, Err: errors.New(upstreamText)}, "imap_connect", "cannot connect to the IMAP server"},
		{&imapsync.Error{Category: imapsync.CategoryTLS, Err: errors.New(upstreamText)}, "imap_tls", "the secure connection to the IMAP server failed"},
		{&imapsync.Error{Category: imapsync.CategoryLogin, Err: errors.New(upstreamText)}, "imap_login", "the IMAP server refused the login"},
		{&imapsync.Error{Category: imapsync.CategoryTimeout, Err: fmt.Errorf("%w: %s", context.DeadlineExceeded, upstreamText)}, "imap_timeout", "the IMAP server did not answer in time"},
		{&imapsync.Error{Category: imapsync.CategoryServer, Err: errors.New(upstreamText)}, "imap_server", "the IMAP server refused a command"},
		{&imapsync.Error{Category: imapsync.CategoryProtocol, Err: errors.New(upstreamText)}, "imap_protocol", "the IMAP server sent an answer that cannot be read"},
		{&imapsync.Error{Category: imapsync.CategoryLimit, Err: &imapsync.LimitError{What: imapsync.LimitRead, Limit: 16 << 20}},
			"imap_limit", "the IMAP server exceeded a limit (total read: 16777216)"},
	} {
		next = c.err
		r := f.addAccount("", "imap.example", 993, c.code, "pw")
		wantFailed(t, r, http.StatusUnprocessableEntity, c.code)
		if r.Error != c.text || strings.Contains(r.raw, upstreamText) {
			t.Errorf("%s: answer %s", c.code, r.raw)
		}
	}
}

// The folder list of a saved account keeps 502 for a failure upstream,
// now with the code.
func TestServerFoldersFailureCode(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	f.do("POST", "/api/accounts", f.newAccount("work", "alice", "secret"), http.StatusCreated)
	f.waitIdle("work")
	dialStub(f, refusedLogin)
	r := f.check("", "GET", "/api/accounts/work/server-folders", nil)
	wantFailed(t, r, http.StatusBadGateway, "imap_login")
	if strings.Contains(r.raw, upstreamText) {
		t.Errorf("answer %s", r.raw)
	}
}
