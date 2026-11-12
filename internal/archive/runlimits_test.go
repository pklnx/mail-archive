package archive_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// Tests of the run limits: a fresh dial after a broken connection,
// the run time limit, a message over the size cap. The IMAP servers are
// scripted (imaptest.StartScript); no test waits for time.

// scriptAccount adds an IMAP account on the scripted server s.
func (f *fixture) scriptAccount(name string, s *imaptest.Script) *store.Account {
	f.t.Helper()
	acc := &store.Account{Name: name, Host: s.Host, Port: s.Port, TLSMode: store.TLSModeNone, Username: "u", Enabled: true}
	seal := func(id int64) ([]byte, error) { return archive.SealPassword(f.sealer, id, "pw") }
	if err := f.store.CreateAccountSealed(f.ctx, acc, seal); err != nil {
		f.t.Fatal(err)
	}
	got, err := f.account(name)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

// health returns the account's health row.
func (f *fixture) health(id int64) store.SyncHealth {
	f.t.Helper()
	h, err := f.store.SyncHealth(f.ctx, nil)
	if err != nil || h[id] == nil {
		f.t.Fatalf("health: %v", err)
	}
	return *h[id]
}

// lastUID returns a folder's last UID.
func (f *fixture) lastUID(acc *store.Account, folder string) uint32 {
	f.t.Helper()
	fo, err := f.store.GetOrCreateFolder(f.ctx, acc.ID, folder)
	if err != nil {
		f.t.Fatal(err)
	}
	return fo.LastUID
}

// sendList answers LIST with the folders.
func sendList(c *imaptest.ScriptConn, folders ...string) error {
	tag, err := c.Expect("LIST")
	if err != nil {
		return err
	}
	var lines []string
	for _, name := range folders {
		lines = append(lines, fmt.Sprintf(`* LIST () "/" %q`, name))
	}
	return c.Send(append(lines, tag+" OK done")...)
}

// expectExamine answers EXAMINE of folder with exists messages and
// UIDNEXT uidNext.
func expectExamine(c *imaptest.ScriptConn, folder string, exists, uidNext int) error {
	tag, line, err := c.Next()
	if err != nil {
		return err
	}
	if want := fmt.Sprintf("EXAMINE %q", folder); !strings.EqualFold(line, want) && line != "EXAMINE "+folder {
		return fmt.Errorf("got %q, want %s", line, want)
	}
	return c.Send(
		fmt.Sprintf("* %d EXISTS", exists),
		"* OK [UIDVALIDITY 1] ok",
		fmt.Sprintf("* OK [UIDNEXT %d] ok", uidNext),
		tag+" OK [READ-ONLY] done",
	)
}

// scriptBody is a message body for UID uid.
func scriptBody(uid uint32) string {
	return fmt.Sprintf("Message-ID: <%d@script.example>\r\nFrom: a@example.com\r\nSubject: m%d\r\n"+
		"Date: Fri, 5 Mar 2010 10:00:00 +0000\r\n\r\nBody %d\r\n", uid, uid, uid)
}

// fetchLine is the FETCH response of a message with a body: a normal one,
// or with big set one over the size cap whose literal never follows (the
// client trips on its size first).
func fetchLine(seq int, uid uint32, big bool) string {
	if big {
		return fmt.Sprintf("* %d FETCH (UID %d FLAGS () INTERNALDATE \"05-Mar-2010 10:00:00 +0000\" BODY[] {%d}\r\n",
			seq, uid, imapsync.MaxMessageSize+1)
	}
	b := scriptBody(uid)
	return fmt.Sprintf("* %d FETCH (UID %d FLAGS () INTERNALDATE \"05-Mar-2010 10:00:00 +0000\" BODY[] {%d}\r\n%s)\r\n",
		seq, uid, len(b), b)
}

// tripFetch answers the next UID FETCH with a message of 1,001 flags, which
// trips the flag cap and breaks the connection, and reads until the client
// has closed it.
func tripFetch(c *imaptest.ScriptConn) error {
	if _, err := c.Expect("UID FETCH"); err != nil {
		return err
	}
	flags := make([]string, imapsync.MaxFlags+1)
	for i := range flags {
		flags[i] = fmt.Sprintf("f%d", i)
	}
	if err := c.Send(fmt.Sprintf("* 1 FETCH (UID 1 FLAGS (%s))", strings.Join(flags, " "))); err != nil {
		return err
	}
	return drainScript(c)
}

// serveFetch answers the next UID FETCH with the given response lines.
func serveFetch(c *imaptest.ScriptConn, lines ...string) error {
	tag, err := c.Expect("UID FETCH")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(c, strings.Join(lines, "")); err != nil {
		return err
	}
	return c.Send(tag + " OK done")
}

// drainScript reads until the client closes the connection.
func drainScript(c *imaptest.ScriptConn) error {
	for {
		if _, _, err := c.Next(); err != nil {
			return nil //nolint:nilerr // the close is what the script waits for
		}
	}
}

// runLimit is a limitRun seam: the run's context ends with
// archive.ErrRunTimeLimit when the test calls end.
type runLimit struct {
	mu     sync.Mutex
	cancel context.CancelCauseFunc
}

func (l *runLimit) seam(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	l.mu.Lock()
	l.cancel = cancel
	l.mu.Unlock()
	return ctx, func() { cancel(context.Canceled) }
}

// end makes the run reach its time limit.
func (l *runLimit) end() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cancel(archive.ErrRunTimeLimit)
}

// A cap trip breaks the connection in folder A; folder B is synced over a
// new connection (before, one bad folder failed every folder after it).
func TestSyncRedialAfterBrokenConnection(t *testing.T) {
	var conns atomic.Int32
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if conns.Add(1) == 1 {
			if err := sendList(c, "A", "B"); err != nil {
				return err
			}
			if err := expectExamine(c, "A", 1, 2); err != nil {
				return err
			}
			return tripFetch(c)
		}
		if err := expectExamine(c, "B", 1, 2); err != nil {
			return err
		}
		if err := serveFetch(c, fetchLine(1, 1, false)); err != nil {
			return err
		}
		return c.Serve()
	})
	f := newFixture(t)
	acc := f.scriptAccount("mail", s)
	r := f.syncer.SyncAccount(f.ctx, acc)
	if r.New != 1 || r.Err == nil || !strings.Contains(r.Err.Error(), "A: ") || strings.Contains(r.Err.Error(), "B: ") {
		t.Fatalf("result %+v", r)
	}
	if n := conns.Load(); n != 2 {
		t.Errorf("%d connections, want 2", n)
	}
	runs, _ := f.store.LastRuns(f.ctx)
	if st := runs[acc.ID].Status; st != "partial" {
		t.Errorf("status %s", st)
	}
	if f.lastUID(acc, "B") != 1 {
		t.Errorf("B not synced")
	}
}

// Each folder breaks the connection: after 5 new connections the run
// stops; the 7th folder is never opened, and with no folder synced the
// run failed.
func TestSyncRedialCap(t *testing.T) {
	var conns, examines atomic.Int32
	folders := []string{"F1", "F2", "F3", "F4", "F5", "F6", "F7"}
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if conns.Add(1) == 1 {
			if err := sendList(c, folders...); err != nil {
				return err
			}
		}
		n := examines.Add(1)
		if err := expectExamine(c, folders[n-1], 1, 2); err != nil {
			return err
		}
		return tripFetch(c)
	})
	f := newFixture(t)
	acc := f.scriptAccount("mail", s)
	r := f.syncer.SyncAccount(f.ctx, acc)
	if r.Err == nil || !strings.Contains(r.Err.Error(), "after 5 new connections") || strings.Contains(r.Err.Error(), "F7") {
		t.Fatalf("err = %v", r.Err)
	}
	if c, e := conns.Load(), examines.Load(); c != 1+archive.MaxRedials || e != 1+archive.MaxRedials {
		t.Errorf("%d connections, %d folders opened; want %d each", c, e, 1+archive.MaxRedials)
	}
	runs, _ := f.store.LastRuns(f.ctx)
	if st := runs[acc.ID].Status; st != "failed" {
		t.Errorf("status %s, want failed", st)
	}
	if h := f.health(acc.ID); h.FailureStreak != 1 {
		t.Errorf("health %+v", h)
	}
}

// When the new connection fails, the remaining folders are left: the run
// is partial, as folder A synced.
func TestSyncRedialFails(t *testing.T) {
	var conns atomic.Int32
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if conns.Add(1) > 1 {
			if err := c.Send("* OK [CAPABILITY IMAP4rev1] ready"); err != nil {
				return err
			}
			tag, err := c.Expect("LOGIN")
			if err != nil {
				return err
			}
			if err := c.Send(tag + " NO [AUTHENTICATIONFAILED] no"); err != nil {
				return err
			}
			return drainScript(c)
		}
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := sendList(c, "A", "B", "C"); err != nil {
			return err
		}
		if err := expectExamine(c, "A", 0, 1); err != nil {
			return err
		}
		if err := expectExamine(c, "B", 1, 2); err != nil {
			return err
		}
		return tripFetch(c)
	})
	f := newFixture(t)
	acc := f.scriptAccount("mail", s)
	r := f.syncer.SyncAccount(f.ctx, acc)
	if r.Err == nil || !strings.Contains(r.Err.Error(), "connect again") || strings.Contains(r.Err.Error(), "C: ") {
		t.Fatalf("err = %v", r.Err)
	}
	if n := conns.Load(); n != 2 {
		t.Errorf("%d connections, want 2", n)
	}
	runs, _ := f.store.LastRuns(f.ctx)
	if st := runs[acc.ID].Status; st != "partial" {
		t.Errorf("status %s, want partial", st)
	}
}

// A message over the size cap: the folder's last UID stops right before
// it, queued messages are saved only if all come before it, the other
// folder still syncs, and the partial run counts as a failure in the
// account's health (maintainer decision: option A with an alert).
func TestSyncStopsBeforeLargeMessage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		batch     int
		lines     []string
		wantUID   uint32
		wantSaved int
		wantLog   string
	}{
		// 1 and 2 are queued and saved; the folder stops at 2.
		{"in order", 10, []string{fetchLine(1, 1, false), fetchLine(2, 2, false), fetchLine(3, 3, true)}, 2, 2, ""},
		// 7 and 2 were committed (last UID 7), 3 is queued: it is saved,
		// and the last UID goes back to 4, before the large message 5.
		{"batch past it", 2, []string{fetchLine(1, 7, false), fetchLine(2, 2, false), fetchLine(3, 3, false), fetchLine(4, 5, true)}, 4, 3, ""},
		// 2 and 7 are queued; 7 comes after the large message 5, so
		// nothing is saved.
		{"queued past it", 10, []string{fetchLine(1, 2, false), fetchLine(2, 7, false), fetchLine(3, 5, true)}, 0, 0,
			"queued messages not saved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var conns atomic.Int32
			s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
				if err := c.Greet(""); err != nil {
					return err
				}
				if conns.Add(1) == 1 {
					if err := sendList(c, "A", "B"); err != nil {
						return err
					}
					if err := expectExamine(c, "A", len(tc.lines), 8); err != nil {
						return err
					}
					if _, err := c.Expect("UID FETCH"); err != nil {
						return err
					}
					if _, err := io.WriteString(c, strings.Join(tc.lines, "")); err != nil {
						return err
					}
					return drainScript(c)
				}
				if err := expectExamine(c, "B", 1, 10); err != nil {
					return err
				}
				// UID 9: a body none of A's messages has.
				if err := serveFetch(c, fetchLine(1, 9, false)); err != nil {
					return err
				}
				return c.Serve()
			})
			f := newFixture(t)
			f.syncer.BatchSize = tc.batch
			acc := f.scriptAccount("mail", s)
			r := f.syncer.SyncAccount(f.ctx, acc)
			var stuck *archive.StuckError
			if !errors.As(r.Err, &stuck) || !strings.Contains(r.Err.Error(), "A: a message (UID ") ||
				!strings.Contains(r.Err.Error(), "is larger than 256 MiB") {
				t.Fatalf("err = %v", r.Err)
			}
			if got := f.lastUID(acc, "A"); got != tc.wantUID {
				t.Errorf("A's last UID %d, want %d", got, tc.wantUID)
			}
			if got := f.lastUID(acc, "B"); got != 9 {
				t.Errorf("B's last UID %d: not synced", got)
			}
			if r.New != tc.wantSaved+1 {
				t.Errorf("%d new messages, want %d", r.New, tc.wantSaved+1)
			}
			runs, _ := f.store.LastRuns(f.ctx)
			if st := runs[acc.ID].Status; st != "partial" {
				t.Errorf("status %s, want partial", st)
			}
			if h := f.health(acc.ID); h.FailureStreak != 1 || h.LastSuccessAt != nil {
				t.Errorf("health %+v, want a failure", h)
			}
			if tc.wantLog != "" && !strings.Contains(f.logs.String(), tc.wantLog) {
				t.Errorf("log: %s", f.logs.String())
			}
		})
	}
}

// silentAfterA is a server with folders A (two messages) and B: it goes
// silent once the client opens B, after A's batch was committed, and
// closes reached then. Later connections find nothing new.
func silentAfterA(t *testing.T, reached chan<- struct{}) *imaptest.Script {
	var conns atomic.Int32
	return imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := sendList(c, "A", "B"); err != nil {
			return err
		}
		if conns.Add(1) > 1 {
			if err := expectExamine(c, "A", 2, 3); err != nil {
				return err
			}
			if err := expectExamine(c, "B", 0, 1); err != nil {
				return err
			}
			return c.Serve()
		}
		if err := expectExamine(c, "A", 2, 3); err != nil {
			return err
		}
		if err := serveFetch(c, fetchLine(1, 1, false), fetchLine(2, 2, false)); err != nil {
			return err
		}
		if _, err := c.Expect("EXAMINE"); err != nil {
			return err
		}
		close(reached)
		return drainScript(c)
	})
}

// waitClosed waits for ch to be closed.
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testwait.Limit(t)):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// At its time limit, a run that committed a batch is partial, counts as
// a success, and asks to be resumed.
func TestSyncTimeLimitWithProgress(t *testing.T) {
	reached := make(chan struct{})
	s := silentAfterA(t, reached)
	f := newFixture(t)
	acc := f.scriptAccount("mail", s)
	var limit runLimit
	archive.SetSyncLimitRun(f.syncer, limit.seam)
	f.syncer.MaxRunTime = time.Hour
	done := make(chan archive.AccountResult, 1)
	go func() { done <- f.syncer.SyncAccount(f.ctx, acc) }()
	waitClosed(t, reached, "the batch of A")
	limit.end()
	var r archive.AccountResult
	select {
	case r = <-done:
	case <-time.After(testwait.Limit(t)):
		t.Fatal("the run did not end at its time limit")
	}
	if !r.Resume || r.New != 2 || !errors.Is(r.Err, archive.ErrRunTimeLimit) || !strings.Contains(r.Err.Error(), "1h0m0s") {
		t.Fatalf("result %+v", r)
	}
	runs, _ := f.store.LastRuns(f.ctx)
	if st := runs[acc.ID].Status; st != "partial" {
		t.Errorf("status %s, want partial", st)
	}
	if h := f.health(acc.ID); h.FailureStreak != 0 || h.LastSuccessAt == nil {
		t.Errorf("health %+v, want a success", h)
	}
}

// silentAt is a server that goes silent once the client opens folder A,
// and closes reached then.
func silentAt(t *testing.T, reached chan<- struct{}) *imaptest.Script {
	return imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := sendList(c, "A"); err != nil {
			return err
		}
		if _, err := c.Expect("EXAMINE"); err != nil {
			return err
		}
		close(reached)
		return drainScript(c)
	})
}

// At its time limit, a run without progress failed and counts as a
// failure; a shutdown at the same point records no health.
func TestSyncTimeLimitWithoutProgress(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprintf("shutdown=%v", shutdown), func(t *testing.T) {
			reached := make(chan struct{})
			s := silentAt(t, reached)
			f := newFixture(t)
			acc := f.scriptAccount("mail", s)
			var limit runLimit
			archive.SetSyncLimitRun(f.syncer, limit.seam)
			parent, stop := context.WithCancel(f.ctx)
			defer stop()
			done := make(chan archive.AccountResult, 1)
			go func() { done <- f.syncer.SyncAccount(parent, acc) }()
			waitClosed(t, reached, "EXAMINE")
			if shutdown {
				stop()
			} else {
				limit.end()
			}
			var r archive.AccountResult
			select {
			case r = <-done:
			case <-time.After(testwait.Limit(t)):
				t.Fatal("the run did not end")
			}
			runs, _ := f.store.LastRuns(f.ctx)
			if r.Resume || runs[acc.ID].Status != "failed" {
				t.Fatalf("result %+v, status %s", r, runs[acc.ID].Status)
			}
			h := f.health(acc.ID)
			switch {
			case shutdown && (h.FailureStreak != 0 || !errors.Is(r.Err, context.Canceled)):
				t.Errorf("a shutdown recorded health %+v (err %v)", h, r.Err)
			case !shutdown && (h.FailureStreak != 1 || !errors.Is(r.Err, archive.ErrRunTimeLimit)):
				t.Errorf("health %+v, err %v; want a failure at the time limit", h, r.Err)
			}
		})
	}
}

// startRunner runs r with the accounts queued. wait waits until want
// syncs have finished (AfterSync), stops r and returns the accounts in
// the order Run took them.
func startRunner(t *testing.T, r *archive.Runner, ids ...int64) (wait func(want int) []int64) {
	t.Helper()
	var (
		mu    sync.Mutex
		order []int64
		after int
		sig   testwait.Signal
	)
	r.BeforeSync = func(_ context.Context, id int64) {
		mu.Lock()
		order = append(order, id)
		mu.Unlock()
	}
	r.AfterSync = func() {
		mu.Lock()
		after++
		mu.Unlock()
		sig.Notify()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	r.Enqueue(ids...)
	return func(want int) []int64 {
		t.Helper()
		testwait.Until(t, &sig, fmt.Sprintf("%d syncs", want), func() bool {
			mu.Lock()
			defer mu.Unlock()
			return after >= want
		})
		cancel()
		<-done
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(order)
	}
}

// A silent server no longer holds the only sync worker: the idle deadline
// (here expired through the conn spy) ends account A's run as failed with
// a failure in its health, naming the setting, and account B syncs.
func TestRunnerSilentServer(t *testing.T) {
	gotList := make(chan struct{})
	silent := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if _, err := c.Expect("LIST"); err != nil {
			return err
		}
		close(gotList)
		return drainScript(c)
	})
	normal := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := sendList(c, "INBOX"); err != nil {
			return err
		}
		if err := expectExamine(c, "INBOX", 1, 2); err != nil {
			return err
		}
		if err := serveFetch(c, fetchLine(1, 1, false)); err != nil {
			return err
		}
		return c.Serve()
	})
	f := newFixture(t)
	a := f.scriptAccount("silent", silent)
	b := f.scriptAccount("normal", normal)
	wrap, spies := imaptest.SpyWrap()
	f.syncer.Dial = func(ctx context.Context, cfg imapsync.Config) (*imapsync.Conn, error) {
		if cfg.Port == silent.Port {
			cfg.WrapConn = wrap
		}
		return imapsync.Dial(ctx, cfg)
	}
	wait := startRunner(t, &archive.Runner{Syncer: f.syncer}, a.ID, b.ID)
	// Expire the idle deadline armed for LIST.
	var spy *imaptest.ConnSpy
	select {
	case spy = <-spies:
	case <-time.After(testwait.Limit(t)):
		t.Fatal("no connection to the silent server")
	}
	waitClosed(t, gotList, "LIST")
	testwait.Until(t, &spy.Changed, "idle deadline armed", func() bool {
		d, _ := spy.Armed()
		return !d.IsZero()
	})
	spy.Expire()
	order := wait(2)
	if len(order) != 2 || order[0] != a.ID || order[1] != b.ID {
		t.Fatalf("order %v", order)
	}
	runs, _ := f.store.LastRuns(f.ctx)
	if runs[a.ID].Status != "failed" || !strings.Contains(runs[a.ID].Error, "during LIST; raise "+config.EnvIMAPIdleTimeout) {
		t.Errorf("silent account: %+v", runs[a.ID])
	}
	if runs[b.ID].Status != "ok" || runs[b.ID].MessagesNew != 1 {
		t.Errorf("normal account: %+v", runs[b.ID])
	}
	if h := f.health(a.ID); h.FailureStreak != 1 {
		t.Errorf("silent account's health %+v", h)
	}
}

// A run that reached its time limit after committing a batch is queued
// again behind the accounts waiting, and the next run finishes it.
func TestRunnerRequeuesAtTimeLimit(t *testing.T) {
	reached := make(chan struct{})
	slow := silentAfterA(t, reached)
	normal := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := sendList(c, "INBOX"); err != nil {
			return err
		}
		if err := expectExamine(c, "INBOX", 0, 1); err != nil {
			return err
		}
		return c.Serve()
	})
	f := newFixture(t)
	a := f.scriptAccount("slow", slow)
	b := f.scriptAccount("normal", normal)
	var (
		limit runLimit
		runs  atomic.Int32
	)
	archive.SetSyncLimitRun(f.syncer, func(ctx context.Context) (context.Context, context.CancelFunc) {
		if runs.Add(1) == 1 { // the slow account's first run
			go func() {
				select {
				case <-reached:
					limit.end()
				case <-time.After(testwait.Limit(t)): // the test fails on its own
				}
			}()
			return limit.seam(ctx)
		}
		return context.WithCancel(ctx)
	})
	order := startRunner(t, &archive.Runner{Syncer: f.syncer}, a.ID, b.ID)(3)
	if len(order) != 3 || order[0] != a.ID || order[1] != b.ID || order[2] != a.ID {
		t.Fatalf("order %v, want [%d %d %d]", order, a.ID, b.ID, a.ID)
	}
	last, _ := f.store.LastRuns(f.ctx)
	if last[a.ID].Status != "ok" || last[b.ID].Status != "ok" {
		t.Errorf("last runs %+v", last)
	}
	if f.lastUID(a, "A") != 2 {
		t.Errorf("A's last UID %d", f.lastUID(a, "A"))
	}
}

// newestScript serves an INBOX of 3 messages with UIDNEXT 10 whose newest
// message has UID newest, as a real server does: a UID FETCH n:* with
// nothing at or after n answers with that newest message, here with a
// 300 KiB body. bodies counts the UID FETCH commands it got.
func newestScript(t *testing.T, newest uint32, bodies *atomic.Int32) *imaptest.Script {
	return imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := sendList(c, "INBOX"); err != nil {
			return err
		}
		if err := expectExamine(c, "INBOX", 3, 10); err != nil {
			return err
		}
		if err := c.Newest(newest); err != nil {
			return err
		}
		for {
			tag, line, err := c.Next()
			if err != nil {
				return nil //nolint:nilerr // the client closed
			}
			switch {
			case strings.HasPrefix(line, "LOGOUT"):
				return c.Send("* BYE", tag+" OK bye")
			case strings.HasPrefix(line, "UID FETCH"):
				bodies.Add(1)
				b := strings.Repeat("x", 300<<10)
				if _, err := fmt.Fprintf(c, "* 3 FETCH (UID %d FLAGS () INTERNALDATE \"05-Mar-2010 10:00:00 +0000\" BODY[] {%d}\r\n%s)\r\n",
					newest, len(b), b); err != nil {
					return nil //nolint:nilerr // the client closed
				}
				if err := c.Send(tag + " OK done"); err != nil {
					return nil //nolint:nilerr // the client closed
				}
			default:
				return fmt.Errorf("unexpected command %q", line)
			}
		}
	})
}

// A folder whose newest message is already archived (later ones were
// deleted on the server, so UIDNEXT is past it) syncs without a body
// fetch: "n:*" would return that message, whose body go-imap does not
// pass to the command and which spent the credit budget, breaking the
// connection on every run (review of #535). With a new message after a
// gap, only that message is fetched.
func TestSyncNothingAfterLastUID(t *testing.T) {
	for _, tc := range []struct {
		name            string
		lastUID, newest uint32
		wantBodies      int32
		wantNew         int
	}{
		{"newest already archived", 7, 7, 0, 0},
		{"new mail after a gap", 5, 9, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bodies atomic.Int32
			s := newestScript(t, tc.newest, &bodies)
			f := newFixture(t)
			acc := f.syncedAccount(s, tc.lastUID)
			r := f.syncer.SyncAccount(f.ctx, acc)
			runs, _ := f.store.LastRuns(f.ctx)
			if r.Err != nil || runs[acc.ID].Status != "ok" || r.New != tc.wantNew {
				t.Fatalf("result %+v, status %s", r, runs[acc.ID].Status)
			}
			if n := bodies.Load(); n != tc.wantBodies {
				t.Errorf("%d body fetches, want %d", n, tc.wantBodies)
			}
			if got := f.lastUID(acc, "INBOX"); got != tc.newest {
				t.Errorf("last UID %d, want %d", got, tc.newest)
			}
		})
	}
}
