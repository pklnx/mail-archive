package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// Tests of the bounded blob lock waits (#178). An observer connection
// outside the small pool stands in for a CLI verify or deletion in another
// process: it holds the blob lock exclusively. Every wait is for a signal
// (pg_locks, the store's count of parked writers); the deadline only
// guards against a hang.

const (
	blobClass = 0x6d62
	syncClass = 0x6d61
)

type blobWaitFixture struct {
	t        *testing.T
	ctx      context.Context
	small    *store.Store
	observer *pgx.Conn
	smtp     *store.Account
	imap     *store.Account

	wg      sync.WaitGroup
	mu      sync.Mutex
	unlocks []func()
}

// syncResult is what TryLockSyncForWrite returned; unlock is safe to call
// more than once.
type syncResult struct {
	unlock func()
	ok     bool
	err    error
}

// newBlobWaitFixture opens a store with a pool of n connections. Every
// writer it starts ends, and every sync lock it took is released, before
// that store closes, also when the test fails: pgxpool.Close waits for
// every connection.
func newBlobWaitFixture(t *testing.T, n int) *blobWaitFixture {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	observer := connect(t, url)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	small, err := store.Open(ctx, storetest.WithMaxConns(t, url, n))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(small.Close)
	f := &blobWaitFixture{t: t, ctx: ctx, small: small, observer: observer}
	t.Cleanup(func() {
		cancel()
		f.wg.Wait()
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, u := range f.unlocks {
			u()
		}
	})
	u, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	f.smtp = createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")
	f.imap = &store.Account{Name: "work", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &u.ID}
	if err := st.CreateAccount(ctx, f.imap); err != nil {
		t.Fatal(err)
	}
	return f
}

// lockBlobs takes the blob lock exclusively on the observer.
func (f *blobWaitFixture) lockBlobs() {
	f.t.Helper()
	if _, err := f.observer.Exec(f.ctx, "SELECT pg_advisory_lock($1, 0)", blobClass); err != nil {
		f.t.Fatal(err)
	}
}

func (f *blobWaitFixture) unlockBlobs() {
	f.t.Helper()
	var ok bool
	if err := f.observer.QueryRow(f.ctx, "SELECT pg_advisory_unlock($1, 0)", blobClass).Scan(&ok); err != nil || !ok {
		f.t.Fatalf("unlock the blob lock: %v %v", ok, err)
	}
}

// tryBlobs reports whether the observer can take the blob lock
// exclusively; if it can, it lets go of it again.
func (f *blobWaitFixture) tryBlobs() bool {
	f.t.Helper()
	var ok bool
	if err := f.observer.QueryRow(f.ctx, "SELECT pg_try_advisory_lock($1, 0)", blobClass).Scan(&ok); err != nil {
		f.t.Fatal(err)
	}
	if ok {
		f.unlockBlobs()
	}
	return ok
}

// waitingInPostgres counts the sessions of the test's database that wait
// for the blob lock.
func (f *blobWaitFixture) waitingInPostgres() int {
	f.t.Helper()
	var n int
	if err := f.observer.QueryRow(f.ctx, `SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND classid = $1 AND objid = 0 AND NOT granted
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, blobClass).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// heldSyncLocks counts the granted sync locks of an account in the test's
// database.
func (f *blobWaitFixture) heldSyncLocks(accountID int64) int {
	f.t.Helper()
	var n int
	if err := f.observer.QueryRow(f.ctx, `SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND classid = $1 AND objid = $2 AND objsubid = 2 AND granted
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, syncClass, accountID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// until polls cond until it holds, or fails the test at the deadline.
func (f *blobWaitFixture) until(what string, cond func() bool) {
	f.t.Helper()
	for !cond() {
		if f.ctx.Err() != nil {
			f.t.Fatalf("%s: %v", what, f.ctx.Err())
		}
	}
}

// deliver starts a delivery of the message with the given hash byte.
func (f *blobWaitFixture) deliver(ctx context.Context, sha byte) <-chan error {
	return f.deliverWith(ctx, sha, present)
}

func (f *blobWaitFixture) deliverWith(ctx context.Context, sha byte, fileExists func() bool) <-chan error {
	done := make(chan error, 1)
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		res, err := f.small.ReceiveMessage(ctx, f.smtp.ID, receivedMeta(sha), envelope("archive@example.com"), fileExists)
		if err == nil && !res.NewMessage {
			err = errors.New("the message was not new")
		}
		done <- err
	}()
	return done
}

// lockSync starts TryLockSyncForWrite for the IMAP account.
func (f *blobWaitFixture) lockSync(ctx context.Context) <-chan syncResult {
	done := make(chan syncResult, 1)
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		unlock, ok, err := f.small.TryLockSyncForWrite(ctx, f.imap.ID)
		res := syncResult{ok: ok, err: err}
		if ok {
			res.unlock = sync.OnceFunc(unlock)
			f.mu.Lock()
			f.unlocks = append(f.unlocks, res.unlock)
			f.mu.Unlock()
		}
		done <- res
	}()
	return done
}

func (f *blobWaitFixture) result(ch <-chan error) error {
	f.t.Helper()
	select {
	case err := <-ch:
		return err
	case <-f.ctx.Done():
		f.t.Fatal("writer still running at the deadline")
		return nil
	}
}

func (f *blobWaitFixture) syncResult(ch <-chan syncResult) syncResult {
	f.t.Helper()
	select {
	case r := <-ch:
		return r
	case <-f.ctx.Done():
		f.t.Fatal("sync lock still waiting at the deadline")
		return syncResult{}
	}
}

// TestReadsWorkWhileWritersWait (#176): while another process holds the
// blob lock exclusively, six deliveries and a sync wait for it on a pool of
// four, and reads still get a connection. Only one writer waits in
// PostgreSQL; the others wait without a connection. Before, every writer
// waited in PostgreSQL with its connection: four of them took the whole
// pool, and the reads waited until the lock was released.
func TestReadsWorkWhileWritersWait(t *testing.T) {
	f := newBlobWaitFixture(t, 4)
	f.lockBlobs()
	var deliveries []<-chan error
	for i := range 6 {
		deliveries = append(deliveries, f.deliver(f.ctx, byte(i+1)))
	}
	syncDone := f.lockSync(f.ctx)
	f.until("seven writers waiting", func() bool {
		inPG := f.waitingInPostgres()
		if inPG > 1 {
			t.Fatalf("%d writers wait for the blob lock in PostgreSQL", inPG)
		}
		return inPG+f.small.BlobWaiters() == 7
	})

	var wg sync.WaitGroup
	errs := make([]error, 3)
	wg.Add(3)
	go func() { defer wg.Done(); errs[0] = f.small.TouchSession(f.ctx, []byte("no such session")) }()
	go func() { defer wg.Done(); _, errs[1] = f.small.ListAccounts(f.ctx) }()
	go func() { defer wg.Done(); _, errs[2] = f.small.CountUsers(f.ctx) }()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("read %d while the writers wait: %v", i, err)
		}
	}
	if f.waitingInPostgres() != 1 {
		t.Fatal("the lock was released during the reads")
	}

	f.unlockBlobs()
	for i, d := range deliveries {
		if err := f.result(d); err != nil {
			t.Errorf("delivery %d: %v", i, err)
		}
	}
	r := f.syncResult(syncDone)
	if r.err != nil || !r.ok {
		t.Fatalf("sync lock: %v %v", r.ok, r.err)
	}
	if f.tryBlobs() {
		t.Error("the sync holds no shared blob lock")
	}
	r.unlock()
	var n int
	if err := f.observer.QueryRow(f.ctx, "SELECT count(*) FROM messages").Scan(&n); err != nil || n != 6 {
		t.Errorf("%d messages stored: %v", n, err)
	}
	if !f.tryBlobs() {
		t.Error("a writer kept the blob lock")
	}
}

// TestBlobWaitCancelled: a cancelled writer, whether it waits for the slot
// or holds it, leaks neither the slot nor a connection: the next writer
// waits in PostgreSQL and succeeds once the lock is released.
func TestBlobWaitCancelled(t *testing.T) {
	for _, holder := range []string{"delivery", "sync"} {
		t.Run(holder+" holds the slot", func(t *testing.T) {
			f := newBlobWaitFixture(t, 4)
			f.lockBlobs()
			deliver := func(ctx context.Context) <-chan error { return f.deliver(ctx, 1) }
			lockSync := func(ctx context.Context) <-chan error {
				ch := make(chan error, 1)
				r := f.lockSync(ctx)
				go func() {
					res := <-r
					if res.ok {
						res.unlock()
						res.err = errors.New("the sync lock was granted")
					}
					ch <- res.err
				}()
				return ch
			}
			first, second := deliver, lockSync
			if holder == "sync" {
				first, second = lockSync, deliver
			}
			ctxA, cancelA := context.WithCancel(f.ctx)
			defer cancelA()
			a := first(ctxA)
			f.until("A waits in PostgreSQL", func() bool { return f.waitingInPostgres() == 1 })
			ctxB, cancelB := context.WithCancel(f.ctx)
			defer cancelB()
			b := second(ctxB)
			f.until("B waits for the slot", func() bool { return f.small.BlobWaiters() == 1 })

			cancelB()
			if err := f.result(b); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled B: %v", err)
			}
			if n := f.small.BlobWaiters(); n != 0 {
				t.Fatalf("%d writers still wait for the slot", n)
			}
			cancelA()
			if err := f.result(a); err == nil {
				t.Fatal("cancelled A succeeded")
			}
			f.until("A's wait ends in PostgreSQL", func() bool { return f.waitingInPostgres() == 0 })

			c := f.deliver(f.ctx, 2)
			f.until("C waits in PostgreSQL", func() bool { return f.waitingInPostgres() == 1 })
			f.unlockBlobs()
			if err := f.result(c); err != nil {
				t.Fatalf("C: %v", err)
			}
			if n := f.small.AcquiredConnsForTest(); n != 0 {
				t.Errorf("%d connections still in use", n)
			}
		})
	}
}

// TestBlobTryPathSkipsSlot: without an exclusive holder, writers take the
// lock at the first try and never wait for the slot.
func TestBlobTryPathSkipsSlot(t *testing.T) {
	f := newBlobWaitFixture(t, 4)
	release, err := f.small.HoldBlobWaitSlotForTest(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := f.result(f.deliver(f.ctx, 1)); err != nil {
		t.Fatalf("delivery: %v", err)
	}
	r := f.syncResult(f.lockSync(f.ctx))
	if r.err != nil || !r.ok {
		t.Fatalf("sync lock: %v %v", r.ok, r.err)
	}
	r.unlock()
}

// TestBlobTryPathHoldsLock (plan review of #176): a writer that took the
// blob lock at the first try holds it until it ends, so verify never
// moves a file whose row is not committed yet.
func TestBlobTryPathHoldsLock(t *testing.T) {
	t.Run("delivery", func(t *testing.T) {
		f := newBlobWaitFixture(t, 4)
		checked := make(chan bool, 1)
		// The callback runs in the delivery's transaction, after the lock.
		fileExists := func() bool {
			var ok bool
			if err := f.observer.QueryRow(f.ctx, "SELECT pg_try_advisory_lock($1, 0)", blobClass).Scan(&ok); err != nil {
				t.Error(err)
			}
			if ok {
				_, _ = f.observer.Exec(f.ctx, "SELECT pg_advisory_unlock($1, 0)", blobClass)
			}
			checked <- ok
			return true
		}
		if err := f.result(f.deliverWith(f.ctx, 1, fileExists)); err != nil {
			t.Fatalf("delivery: %v", err)
		}
		if <-checked {
			t.Error("verify got the blob lock while the delivery was in its transaction")
		}
		if !f.tryBlobs() {
			t.Error("the delivery kept the blob lock after its commit")
		}
	})
	t.Run("sync", func(t *testing.T) {
		f := newBlobWaitFixture(t, 4)
		r := f.syncResult(f.lockSync(f.ctx))
		if r.err != nil || !r.ok {
			t.Fatalf("sync lock: %v %v", r.ok, r.err)
		}
		if f.tryBlobs() {
			t.Error("verify got the blob lock while the sync held its lock")
		}
		r.unlock()
		if !f.tryBlobs() {
			t.Error("the sync kept the blob lock after unlock")
		}
	})
}

// TestParkedSyncReleasesAccountLock: a sync that waits for the blob lock
// without a connection holds no account lock either. If another session
// takes the account's lock meanwhile, the sync gets ok=false once the blob
// lock is free, and gives its connection back.
func TestParkedSyncReleasesAccountLock(t *testing.T) {
	f := newBlobWaitFixture(t, 4)
	f.lockBlobs()
	d := f.deliver(f.ctx, 1)
	f.until("the delivery waits in PostgreSQL", func() bool { return f.waitingInPostgres() == 1 })
	s := f.lockSync(f.ctx)
	f.until("the sync waits for the slot", func() bool { return f.small.BlobWaiters() == 1 })
	if n := f.heldSyncLocks(f.imap.ID); n != 0 {
		t.Fatalf("the parked sync holds %d account locks", n)
	}
	var ok bool
	if err := f.observer.QueryRow(f.ctx, "SELECT pg_try_advisory_lock($1, $2)", syncClass, f.imap.ID).Scan(&ok); err != nil || !ok {
		t.Fatalf("account lock: %v %v", ok, err)
	}
	f.unlockBlobs()
	if err := f.result(d); err != nil {
		t.Fatalf("delivery: %v", err)
	}
	r := f.syncResult(s)
	if r.err != nil || r.ok {
		t.Fatalf("sync after the account was locked elsewhere: %v %v", r.ok, r.err)
	}
	if n := f.small.AcquiredConnsForTest(); n != 0 {
		t.Errorf("%d connections still in use", n)
	}
	if !f.tryBlobs() {
		t.Error("the sync kept the blob lock")
	}
}
