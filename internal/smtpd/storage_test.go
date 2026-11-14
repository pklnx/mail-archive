package smtpd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keepia/archive/internal/auth"
	"github.com/keepia/archive/internal/quota"
	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/store/storetest"
	"github.com/keepia/archive/internal/testwait"
)

// gateDB is the storage gate's database in these tests: the states come
// from figures the test sets (quota 100 per group, warn 80, overflow 110).
type gateDB struct {
	mu   sync.Mutex
	figs store.GateFigures
}

func (d *gateDB) StorageGateFigures(context.Context) (store.GateFigures, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.figs
	f.Groups = append([]store.GateScope(nil), d.figs.Groups...)
	return f, nil
}

func (d *gateDB) TransitionStorageState(_ context.Context, group *int64,
	decide func(string, store.ScopeFigure) (string, error)) (store.StorageTransition, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	sc := &d.figs.Instance
	for i := range d.figs.Groups {
		if group != nil && *d.figs.Groups[i].Group == *group {
			sc = &d.figs.Groups[i]
		}
	}
	f := store.ScopeFigure{Bytes: sc.Bytes, Quota: sc.Quota, Counted: true}
	next, err := decide(sc.State, f)
	if err != nil {
		return store.StorageTransition{}, err
	}
	changed := next != sc.State
	sc.State = next
	return store.StorageTransition{State: next, Changed: changed, Figure: f}, nil
}

// storageFixture is a server with two customer groups: the journal
// account in the owner's group A and "bea" in group B, each with a quota
// of 100 bytes, and a gate the test drives (set, then Refresh).
type storageFixture struct {
	*fixture
	db       *gateDB
	gate     *quota.Gate
	free     int64
	freeMu   sync.Mutex
	groupA   int64
	groupB   int64
	bea      int64
	other    *store.Account
	blobsDir string
	// af is set with AUTH.
	af *authFixture
	// hookMu guards hook, which the server calls at the start of DATA.
	hookMu sync.Mutex
	hook   func()
}

// onData sets what runs at the start of each DATA (nil: nothing).
func (sf *storageFixture) onData(f func()) {
	sf.hookMu.Lock()
	sf.hook = f
	sf.hookMu.Unlock()
}

// bytesFor are figures for each state of a quota of 100.
var bytesFor = map[quota.State]int64{quota.OK: 0, quota.Warn: 85, quota.Full: 100, quota.Overflow: 110}

func newStorageFixture(t *testing.T, auth bool, configure func(*Server)) *storageFixture {
	t.Helper()
	sf := &storageFixture{db: &gateDB{}, free: 1 << 40}
	sf.gate = quota.New(quota.Config{Persist: true, Thresholds: quota.Thresholds{WarnPercent: 80, OverflowPercent: 10}, MinFreeDisk: 1 << 30,
		Interval: time.Hour}, sf.db, func() (int64, error) {
		sf.freeMu.Lock()
		defer sf.freeMu.Unlock()
		return sf.free, nil
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	setup := func(s *Server) {
		s.Storage = sf.gate
		s.beforeData = func() {
			sf.hookMu.Lock()
			f := sf.hook
			sf.hookMu.Unlock()
			if f != nil {
				f()
			}
		}
		if configure != nil {
			configure(s)
		}
	}
	if auth {
		sf.af = newAuthFixture(t, setup)
		sf.fixture = sf.af.fixture
	} else {
		sf.fixture = newFixture(t, setup)
	}
	ctx := context.Background()
	bea, err := sf.st.CreateUser(ctx, "bea", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	gb, err := sf.st.CreateGroup(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sf.st.SetUserGroup(ctx, bea.ID, gb.ID); err != nil {
		t.Fatal(err)
	}
	owner, err := sf.st.GetUserByID(ctx, sf.owner)
	if err != nil {
		t.Fatal(err)
	}
	sf.groupA, sf.groupB, sf.bea = owner.GroupID, gb.ID, bea.ID
	sf.other = &store.Account{Name: "other", Enabled: true, OwnerID: &sf.bea}
	if err := sf.st.CreateSMTPAccount(ctx, sf.other, []string{"other@archive.test"}); err != nil {
		t.Fatal(err)
	}
	sf.blobsDir = sf.srv.Blobs.Root()
	sf.db.figs = store.GateFigures{Counted: true, Instance: store.GateScope{State: "ok"}, Groups: []store.GateScope{
		{Group: &sf.groupA, Quota: ptr(100), State: "ok"}, {Group: &sf.groupB, Quota: ptr(100), State: "ok"},
	}}
	sf.gate.Refresh(ctx)
	return sf
}

func ptr(v int64) *int64 { return &v }

// set puts group A (and, with b, group B) in a state; quota.DiskLow puts
// the disk below its reserve instead. Only from the test's goroutine.
func (sf *storageFixture) set(a quota.State, b quota.State) {
	sf.t.Helper()
	if err := sf.setState(a, b); err != nil {
		sf.t.Fatal(err)
	}
}

// setState is set for the server's goroutines (the DATA hook): it
// returns what went wrong instead of failing the test there.
func (sf *storageFixture) setState(a quota.State, b quota.State) error {
	sf.freeMu.Lock()
	sf.free = 1 << 40
	if a == quota.DiskLow {
		sf.free = 1 << 20
	}
	sf.freeMu.Unlock()
	sf.db.mu.Lock()
	if a != quota.DiskLow {
		sf.db.figs.Groups[0].Bytes = bytesFor[a]
	}
	sf.db.figs.Groups[1].Bytes = bytesFor[b]
	sf.db.mu.Unlock()
	sf.gate.Refresh(context.Background())
	if a != quota.DiskLow && (sf.gate.State(&sf.groupA) != a || sf.gate.State(&sf.groupB) != b) {
		return fmt.Errorf("gate: A %s, B %s; want %s, %s", sf.gate.State(&sf.groupA), sf.gate.State(&sf.groupB), a, b)
	}
	return nil
}

// hookErrors collects the errors of setState in DATA hooks, for the test
// to check on its own goroutine.
type hookErrors struct {
	mu   sync.Mutex
	errs []error
}

func (h *hookErrors) add(err error) {
	if err != nil {
		h.mu.Lock()
		h.errs = append(h.errs, err)
		h.mu.Unlock()
	}
}

func (h *hookErrors) check(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, err := range h.errs {
		t.Errorf("DATA hook: %v", err)
	}
}

// close ends a client's connection, so that its slot is free again.
func (c *rawClient) close() {
	_, _ = c.cmd("QUIT")
	_ = c.conn.Close()
}

// stored returns the accounts that hold the message with the subject, of
// both owners, with their recipients.
func (sf *storageFixture) stored(subject string) map[string][]string {
	sf.t.Helper()
	ctx := context.Background()
	out := map[string][]string{}
	for _, owner := range []int64{sf.owner, sf.bea} {
		v := storetest.Viewer(sf.t, sf.st, owner)
		res, err := sf.st.SearchMessages(ctx, store.SearchFilter{Scope: v, Query: subject})
		if err != nil {
			sf.t.Fatal(err)
		}
		for _, m := range res {
			d, err := sf.st.GetMessageDetail(ctx, v, m.SHA256)
			if err != nil {
				sf.t.Fatal(err)
			}
			for _, l := range d.Locations {
				if l.Envelope != nil {
					if _, dup := out[l.Account]; dup {
						sf.t.Errorf("account %s has two locations of %q", l.Account, subject)
					}
					out[l.Account] = l.Envelope.RcptTo
				}
			}
		}
	}
	return out
}

// files counts the files in the blob store.
func (sf *storageFixture) files() int {
	sf.t.Helper()
	n := 0
	err := filepath.WalkDir(sf.blobsDir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		sf.t.Fatal(err)
	}
	return n
}

const (
	rcptA = "archive+alice=example.com@archive.test" // the journal account, group A
	rcptB = "other@archive.test"                     // bea's account, group B
)

// With AUTH off, every state of group A: up to full a recipient is
// accepted and stored, from overflow on (and at disk_low) it gets 452
// 4.3.1 at RCPT, never a 5xx, while group B's recipient in the same
// transaction is accepted and stored.
func TestStorageStates(t *testing.T) {
	sf := newStorageFixture(t, false, nil)
	for i, st := range []quota.State{quota.OK, quota.Warn, quota.Full, quota.Overflow, quota.DiskLow} {
		sf.set(st, quota.OK)
		c, _ := sf.raw()
		c.must(250, "EHLO mx.example.com")
		subject := fmt.Sprintf("state%d", i)
		code, msg := c.deliver("sender@example.com", []string{rcptA, rcptB}, message(subject))
		got := sf.stored(subject)
		switch {
		case st == quota.DiskLow:
			// The disk is the instance's: both are deferred.
			if code != 452 || !strings.HasPrefix(msg, "4.3.1") || len(got) != 0 {
				t.Fatalf("%s: %d %s, stored %v", st, code, msg, got)
			}
		case st.Deferred():
			if code != 452 || !strings.HasPrefix(msg, "4.3.1") || len(got) != 1 || got["other"] == nil {
				t.Fatalf("%s: %d %s, stored %v", st, code, msg, got)
			}
		default:
			if code != 250 || len(got) != 2 {
				t.Fatalf("%s: %d %s, stored %v", st, code, msg, got)
			}
		}
		if code >= 500 {
			t.Fatalf("%s: a 5xx for storage", st)
		}
	}
	// One recipient at overflow, both at disk_low (for /api/metrics).
	if n := sf.srv.StorageDeferrals(); n != 3 {
		t.Fatalf("%d recipients counted as deferred, want 3", n)
	}
	sf.logHas(`msg="smtp recipient deferred" code=452`, "reason=storage", "state=overflow", "state=disk_low",
		`msg="smtp recipient accepted above the storage quota"`)
}

// Group A reaches overflow between RCPT and DATA: the copy for group B is
// stored, the answer is 452, and the retry after space was freed stores A
// once and B not twice. When every account is deferred at DATA, no file
// is written.
func TestStorageAtData(t *testing.T) {
	sf := newStorageFixture(t, false, nil)
	var hooks hookErrors
	defer hooks.check(t)
	sf.onData(func() { hooks.add(sf.setState(quota.Overflow, quota.OK)) })
	c, _ := sf.raw()
	c.must(250, "EHLO mx.example.com")
	if code, msg := c.deliver("sender@example.com", []string{rcptA, rcptB}, message("gecko")); code != 452 || !strings.HasPrefix(msg, "4.3.1") {
		t.Fatalf("partial: %d %s", code, msg)
	}
	if got := sf.stored("gecko"); len(got) != 1 || got["other"] == nil {
		t.Fatalf("partial: stored %v", got)
	}
	if n := sf.srv.StorageDeferrals(); n != 1 {
		t.Fatalf("partial: %d recipients counted as deferred, want 1", n)
	}
	// The retry, once space is freed: A is stored, B merges.
	sf.onData(nil)
	sf.set(quota.OK, quota.OK)
	if code, msg := c.deliver("sender@example.com", []string{rcptA, rcptB}, message("gecko")); code != 250 {
		t.Fatalf("retry: %d %s", code, msg)
	}
	if got := sf.stored("gecko"); len(got) != 2 {
		t.Fatalf("retry: stored %v", got)
	}

	// Both groups overflow after RCPT: nothing reaches the disk.
	before := sf.files()
	sf.onData(func() { hooks.add(sf.setState(quota.Overflow, quota.Overflow)) })
	if code, msg := c.deliver("sender@example.com", []string{rcptA, rcptB}, message("skink")); code != 452 || !strings.HasPrefix(msg, "4.3.1") {
		t.Fatalf("all deferred: %d %s", code, msg)
	}
	if n := sf.files(); n != before {
		t.Fatalf("all deferred: %d files, before %d", n, before)
	}
	if got := sf.stored("skink"); len(got) != 0 {
		t.Fatalf("all deferred: stored %v", got)
	}
	if n := sf.srv.StorageDeferrals(); n != 3 {
		t.Fatalf("all deferred: %d recipients counted as deferred, want 3", n)
	}
	sf.logHas(`msg="smtp message deferred" code=452`)
}

// The deferral log: the first deferral of a group is logged in full, the
// next ones only in a summary line per minute; a change of state logs in
// full again.
func TestStorageLogLimit(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	sf := newStorageFixture(t, false, func(s *Server) { s.Now = clock.Now })
	sf.set(quota.Overflow, quota.OK)
	c, _ := sf.raw()
	c.must(250, "EHLO mx.example.com")
	c.must(250, "MAIL FROM:<sender@example.com>")
	for range 5 {
		c.must(452, "RCPT TO:<%s>", rcptA)
	}
	if n := strings.Count(sf.logs.String(), `msg="smtp recipient deferred"`); n != 1 {
		t.Fatalf("%d full lines, want 1:\n%s", n, sf.logs)
	}
	clock.Add(time.Minute)
	c.must(452, "RCPT TO:<%s>", rcptA)
	sf.logHas(`msg="smtp storage deferred since the last line"`, "count=5")
	// Accepted again below full, then overflow: the first deferral is
	// logged in full again.
	sf.set(quota.OK, quota.OK)
	c.must(250, "RCPT TO:<%s>", rcptA)
	sf.set(quota.Overflow, quota.OK)
	c.must(452, "RCPT TO:<%s>", rcptA)
	if n := strings.Count(sf.logs.String(), `msg="smtp recipient deferred"`); n != 2 {
		t.Fatalf("%d full lines, want 2:\n%s", n, sf.logs)
	}
}

// Messages are received while the gate flips between ok and overflow for
// group A (at each DATA, so RCPT and DATA of the concurrent sessions see
// both states): every 250 has the copy of both accounts, every 452 has
// group B's copy and none of A's, and a later retry of each stores A once.
func TestStorageReceiveWhileFlipping(t *testing.T) {
	sf := newStorageFixture(t, false, nil)
	var (
		mu    sync.Mutex
		n     int
		hooks hookErrors
	)
	defer hooks.check(t)
	sf.onData(func() {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n%2 == 0 {
			hooks.add(sf.setState(quota.Overflow, quota.OK))
		} else {
			hooks.add(sf.setState(quota.OK, quota.OK))
		}
	})
	// 10 clients at once, each closed after its message, so that they
	// and the retries below stay within the server's 16 connections.
	const msgs = 10
	codes := make([]int, msgs)
	var wg sync.WaitGroup
	for i := range msgs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := sf.raw()
			defer c.close()
			if code, _ := c.cmd("EHLO mx.example.com"); code != 250 {
				return
			}
			codes[i], _ = c.deliver("sender@example.com", []string{rcptA, rcptB}, message(flipSubject(i)))
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(testwait.Limit(t)):
		t.Fatal("the clients did not finish")
	}
	deferred := 0
	for i, code := range codes {
		got := sf.stored(flipSubject(i))
		switch code {
		case 250:
			if len(got) != 2 {
				t.Errorf("message %d: 250 with %v", i, got)
			}
		case 452:
			deferred++
			if len(got) != 1 || got["other"] == nil {
				t.Errorf("message %d: 452 with %v", i, got)
			}
		default:
			t.Errorf("message %d: %d", i, code)
		}
	}
	mu.Lock()
	flips := n
	mu.Unlock()
	if flips < 2 {
		t.Fatalf("the gate flipped %d times", flips)
	}
	sf.onData(nil)
	sf.set(quota.OK, quota.OK)
	for i, code := range codes {
		if code != 452 {
			continue
		}
		c, _ := sf.raw()
		c.must(250, "EHLO mx.example.com")
		code, msg := c.deliver("sender@example.com", []string{rcptA, rcptB}, message(flipSubject(i)))
		c.close()
		if code != 250 {
			t.Fatalf("retry %d: %d %s", i, code, msg)
		}
		if got := sf.stored(flipSubject(i)); len(got) != 2 {
			t.Fatalf("retry %d: %v", i, got)
		}
	}
}

// flipSubject names message i; no subject is a prefix of another.
func flipSubject(i int) string { return "flip" + string(rune('a'+i)) }

// With AUTH required a transaction holds one account: when the login's
// group is full beyond the overflow, RCPT gets 452 4.3.1 and nothing is
// stored, while a login of another group is still accepted.
func TestStorageWithAuth(t *testing.T) {
	sf := newStorageFixture(t, true, nil)
	secretA, _ := sf.af.secret(sf.journal, 0)
	secretB, _ := sf.af.secret(sf.other, 0)
	sf.set(quota.Overflow, quota.OK)
	before := sf.files()
	ca, _ := sf.af.login(auth.SMTPUsername(sf.journal.ID), secretA)
	ca.must(250, "MAIL FROM:<sender@example.com>")
	if code, msg := ca.cmd("RCPT TO:<%s>", rcptA); code != 452 || !strings.HasPrefix(msg, "4.3.1") {
		t.Fatalf("login of the full group, RCPT: %d %s", code, msg)
	}
	ca.must(250, "RSET")
	if code, msg := ca.deliver("sender@example.com", []string{rcptA}, message("dingo")); code != 452 || !strings.HasPrefix(msg, "4.3.1") {
		t.Fatalf("login of the full group: %d %s", code, msg)
	}
	if got := sf.stored("dingo"); len(got) != 0 || sf.files() != before {
		t.Fatalf("login of the full group: stored %v", got)
	}
	cb, _ := sf.af.login(auth.SMTPUsername(sf.other.ID), secretB)
	if code, msg := cb.deliver("sender@example.com", []string{"other+carol=example.com@archive.test"}, message("dingo")); code != 250 {
		t.Fatalf("login of the other group: %d %s", code, msg)
	}
	if got := sf.stored("dingo"); len(got) != 1 || got["other"] == nil {
		t.Fatalf("login of the other group: stored %v", got)
	}
}
