package archive

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// waitingWriters starts blob writers on a small pool (#166): syncs of two
// accounts and one SMTP delivery, which wait for the blob lock. Since #178
// one of them waits in PostgreSQL with a pool connection, the others wait
// for the store's wait slot without one.
type waitingWriters struct {
	t        *testing.T
	small    *store.Store
	observer *pgx.Conn
	syncs    []int64
	smtp     int64
	meta     store.MessageMeta
	results  chan error
	started  int
	seen     int // writers last seen waiting for the blob lock or its slot
}

// newWaitingWriters opens a store with a pool of four connections and
// prepares the writers' accounts and message on s.st.
func newWaitingWriters(t *testing.T, ctx context.Context, s *retentionSetup) *waitingWriters {
	t.Helper()
	small, err := store.Open(ctx, storetest.WithMaxConns(t, s.url, 4))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(small.Close)
	observer, err := pgx.Connect(ctx, s.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close(context.Background()) })
	w := &waitingWriters{t: t, small: small, observer: observer, results: make(chan error, 3)}
	// When the test failed, every writer is still collected before the
	// small store closes (cleanups run last in, first out): pgxpool.Close
	// waits for every connection.
	t.Cleanup(func() {
		for range w.started {
			<-w.results
		}
	})
	for _, name := range []string{"sync1", "sync2"} {
		a := &store.Account{Name: name, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &s.user.ID}
		if err := s.st.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
		w.syncs = append(w.syncs, a.ID)
	}
	smtp := &store.Account{Name: "journal", Enabled: true, OwnerID: &s.user.ID}
	if err := s.st.CreateSMTPAccount(ctx, smtp, []string{"journal@archive.test"}); err != nil {
		t.Fatal(err)
	}
	w.smtp = smtp.ID
	blob, created, err := s.blobs.Put(strings.NewReader("Subject: new\r\nDate: Tue, 3 Mar 2026 10:00:00 +0000\r\n\r\nnew\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if w.meta, err = BuildMeta(ctx, s.st, s.blobs, blob, created); err != nil {
		t.Fatal(err)
	}
	return w
}

// start starts the three writers with ctx (never a session's) and waits
// until all three wait for the blob lock or for its wait slot, or ctx
// ends. Each writer lets go
// of its lock and connection as soon as it has them.
func (w *waitingWriters) start(ctx context.Context) {
	for _, id := range w.syncs {
		w.started++
		go func() {
			release, ok, err := w.small.TryLockSyncForWrite(ctx, id)
			if err == nil && !ok {
				err = errors.New("sync lock busy")
			}
			if ok {
				release()
			}
			w.results <- err
		}()
	}
	w.started++
	go func() {
		env := store.Envelope{MailFrom: "a@example.com", RcptTo: []string{"journal@archive.test"}, ReceivedAt: time.Now(), HELO: "mx"}
		_, err := w.small.ReceiveMessage(ctx, w.smtp, w.meta, env, func() bool { return true })
		w.results <- err
	}()
	for ctx.Err() == nil {
		if w.seen = waitingForBlobs(ctx, w.observer) + w.small.BlobWaiters(); w.seen >= 3 {
			return
		}
	}
}

// collect waits for every writer and fails the test for any error.
func (w *waitingWriters) collect() {
	w.t.Helper()
	for ; w.started > 0; w.started-- {
		if err := <-w.results; err != nil {
			w.t.Errorf("writer: %v", err)
		}
	}
}

// waitingForBlobs counts the sessions of the test's database that wait for
// the blob lock.
func waitingForBlobs(ctx context.Context, observer *pgx.Conn) int {
	var n int
	if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND classid = $1 AND NOT granted
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, 0x6d62).Scan(&n); err != nil {
		return -1
	}
	return n
}

// TestDeletionRunWithWaitingWriters (#166): with a pool of four
// connections, three writers waiting for the blob lock while a batch
// holds it must not keep the batch from its connection. Before the run
// held its locks on one connection, only two writers got a connection
// (the run lock and the blob lock held the other two), and the batch then
// waited for a fifth. The deadline only guards against the hang.
func TestDeletionRunWithWaitingWriters(t *testing.T) {
	s := newRetentionSetup(t, 2)
	id := s.confirmedRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w := newWaitingWriters(t, ctx, s)
	started := false
	run, err := ExecuteDeletionRun(ctx, w.small, id, DeletionOptions{
		BatchSize: 1,
		// Called with the blob lock held, before the batch.
		Audit: func(action string) *store.AuditEntry {
			if action == "retention.batch" && !started {
				started = true
				w.start(ctx)
			}
			return &store.AuditEntry{Actor: store.ActorCLI, Action: action}
		},
	})
	if err != nil || run.State != store.RunDone || run.Deleted != 2 {
		t.Fatalf("run with waiting writers: %+v %v (%d writers seen waiting)", run, err, w.seen)
	}
	if w.started != 3 {
		t.Fatalf("%d writers started", w.started)
	}
	w.collect()
}

// TestSweepWithWaitingWriters (#166): the sweep writes its audit entry
// under the blob lock; with three writers waiting for it on a pool of
// four, the entry must not need another connection.
func TestSweepWithWaitingWriters(t *testing.T) {
	s := newRetentionSetup(t, 1)
	id := s.confirmedRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := ExecuteDeletionRun(ctx, s.st, id, DeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	w := newWaitingWriters(t, ctx, s)
	n, err := SweepBlobs(ctx, w.small, s.blobs, SweepOptions{
		// Called with the blob lock held, after the removals.
		Audit: func() *store.AuditEntry {
			w.start(ctx)
			return &store.AuditEntry{Actor: store.ActorCLI, Action: "retention.sweep"}
		},
	})
	if err != nil || n != 1 {
		t.Fatalf("sweep with waiting writers: %d %v (%d writers seen waiting)", n, err, w.seen)
	}
	w.collect()
	var entries int
	if err := w.observer.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action = 'retention.sweep'").Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("%d sweep entries %v", entries, err)
	}
}

// TestSweepWaitHoldsNoConnection (#178): while the sweep retries the blob
// lock, it keeps no connection between its tries, so on a pool of one a
// read still gets the connection. Before, the sweep kept its session's
// connection idle for the whole lock timeout.
func TestSweepWaitHoldsNoConnection(t *testing.T) {
	s := newRetentionSetup(t, 1)
	id := s.confirmedRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := ExecuteDeletionRun(ctx, s.st, id, DeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(storetest.WithMaxConns(t, s.url, 1))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("application_name", "sweep-test")
	u.RawQuery = q.Encode()
	small, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(small.Close)
	observer, err := pgx.Connect(ctx, s.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close(context.Background()) })
	if _, err := observer.Exec(ctx, "SELECT pg_advisory_lock($1, 0)", 0x6d62); err != nil {
		t.Fatal(err)
	}

	sweepCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	collected := false
	// The sweep ends before the small store closes, also when the test
	// fails: pgxpool.Close waits for every connection.
	t.Cleanup(func() {
		stop()
		if !collected {
			<-done
		}
	})
	go func() {
		_, err := SweepBlobs(sweepCtx, small, s.blobs, SweepOptions{LockTimeout: time.Hour})
		done <- err
	}()
	// Wait until the sweep's connection has run its first try.
	for {
		var n int
		if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE application_name = 'sweep-test' AND datname = current_database()
			  AND (query LIKE '%pg_try_advisory_lock%' OR query LIKE '%pg_advisory_unlock_all%')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
	}
	if _, err := small.ListAccounts(ctx); err != nil {
		t.Fatalf("read while the sweep waits for the blob lock: %v", err)
	}
	stop()
	collected = true
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped sweep: %v", err)
	}
}
