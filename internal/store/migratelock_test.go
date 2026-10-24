package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3/lock"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// migrateLockClass is store.migrateLockClass.
const migrateLockClass = 0x6d68

// receive returns the next value from ch. It fails the test once ctx
// ends, so a result that never comes fails with what instead of hanging
// until the go test timeout; ctx is the guard, not a delay.
func receive[T any](t *testing.T, ctx context.Context, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatalf("no %s before the deadline: %v", what, ctx.Err())
		var zero T
		return zero
	}
}

// openNamed opens a store on dbURL whose connections carry the
// application_name name. It closes when the test ends.
func openNamed(t *testing.T, ctx context.Context, dbURL, name string) *store.Store {
	t.Helper()
	st, err := store.Open(ctx, storetest.WithApplicationName(t, dbURL, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

// tryingGooseLock counts the given application names of the test's
// database whose last query tries or waits for an advisory lock, as goose's
// lock does. It counts by name, so the query never counts itself, though
// its text contains the pattern.
func tryingGooseLock(t *testing.T, ctx context.Context, conn *pgx.Conn, names ...string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(DISTINCT application_name) FROM pg_stat_activity
		WHERE datname = current_database() AND application_name = ANY($1)
		  AND query LIKE '%advisory_lock(%'`, names).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// waitingForGooseLock counts the given application names of the test's
// database whose backend waits for goose's lock.
func waitingForGooseLock(t *testing.T, ctx context.Context, conn *pgx.Conn, names ...string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(DISTINCT a.application_name)
		FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE a.application_name = ANY($2) AND NOT l.granted AND `+gooseLockRows,
		lock.DefaultLockID, names).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// countMigrateLockWaiters counts the sessions of the test's database that
// wait for the migration command lock.
func countMigrateLockWaiters(ctx context.Context, conn *pgx.Conn) (int, error) {
	var n int
	err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND classid = $1 AND objid = 0 AND objsubid = 2 AND NOT granted
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, migrateLockClass).Scan(&n)
	return n, err
}

// waitingForMigrateLock is countMigrateLockWaiters that fails the test on
// an error.
func waitingForMigrateLock(t *testing.T, ctx context.Context, conn *pgx.Conn) int {
	t.Helper()
	n, err := countMigrateLockWaiters(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// tryMigrateLock reports whether conn can take the migration command lock;
// if it can, it lets go of it again.
func tryMigrateLock(t *testing.T, ctx context.Context, conn *pgx.Conn) bool {
	t.Helper()
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, 0)", migrateLockClass).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	if ok {
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1, 0)", migrateLockClass); err != nil {
			t.Fatal(err)
		}
	}
	return ok
}

// TestMigrateWaitsForLock (#214): two migrations of one database at the
// same time wait for goose's lock; afterwards one applied everything and
// the other nothing. Without the lock, a Migrate returns while the test
// holds it, which fails the test.
func TestMigrateWaitsForLock(t *testing.T) {
	dbURL := storetest.NewUnmigrated(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	holder := connect(t, dbURL)
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
		t.Fatal(err)
	}
	stores := map[string]*store.Store{
		"migrate-a": openNamed(t, ctx, dbURL, "migrate-a"),
		"migrate-b": openNamed(t, ctx, dbURL, "migrate-b"),
	}

	type result struct {
		name    string
		applied []string
		err     error
	}
	migrateCtx, stop := context.WithCancel(ctx)
	results := make(chan result, len(stores))
	pending := len(stores)
	// The migrations end before the stores close, also when the test
	// fails: pgxpool.Close waits for every connection, and a migration
	// would otherwise wait for the lock without a time limit.
	t.Cleanup(func() {
		stop()
		_, _ = holder.Exec(context.Background(), "SELECT pg_advisory_unlock_all()")
		for ; pending > 0; pending-- {
			<-results
		}
	})
	for name, st := range stores {
		go func() {
			applied, err := st.Migrate(migrateCtx)
			results <- result{name, applied, err}
		}()
	}

	// Wait until both wait for goose's lock; neither may finish meanwhile.
	for waitingForGooseLock(t, ctx, holder, "migrate-a", "migrate-b") < 2 {
		select {
		case r := <-results:
			pending--
			t.Fatalf("%s migrated while the lock was held: %d applied, %v", r.name, len(r.applied), r.err)
		default:
		}
	}
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", lock.DefaultLockID); err != nil {
		t.Fatal(err)
	}

	applied := map[string]int{}
	for pending > 0 {
		r := receive(t, ctx, results, "migration result")
		// Counted before a failure, so the cleanup waits only for the
		// results still to come.
		pending--
		if r.err != nil {
			t.Fatalf("%s: %v", r.name, r.err)
		}
		applied[r.name] = len(r.applied)
	}
	statusA, err := stores["migrate-a"].MigrationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	statusB, err := stores["migrate-b"].MigrationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statusA) == 0 || !slices.EqualFunc(statusA, statusB, func(a, b store.MigrationStatus) bool {
		return a.Path == b.Path && a.Applied == b.Applied
	}) {
		t.Fatalf("status differs:\n%v\n%v", statusA, statusB)
	}
	for _, s := range statusA {
		if !s.Applied {
			t.Errorf("%s is not applied", s.Path)
		}
	}
	all := len(statusA)
	if a, b := applied["migrate-a"], applied["migrate-b"]; min(a, b) != 0 || max(a, b) != all {
		t.Errorf("applied %d and %d migrations, want %d and 0", a, b, all)
	}
	var dup int
	if err := holder.QueryRow(ctx, "SELECT count(*) - count(DISTINCT version_id) FROM goose_db_version").Scan(&dup); err != nil || dup != 0 {
		t.Errorf("%d duplicate versions in goose_db_version %v", dup, err)
	}
}

// TestMigrationStatusIgnoresLock (#214): reading the status does not wait
// for goose's lock, so `migrate status` and `backup` do not wait behind a
// running migration.
func TestMigrationStatusIgnoresLock(t *testing.T) {
	_, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	holder := connect(t, dbURL)
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
		t.Fatal(err)
	}
	st := openNamed(t, ctx, dbURL, "status")

	type result struct {
		statuses []store.MigrationStatus
		err      error
	}
	statusCtx, stop := context.WithCancel(ctx)
	done := make(chan result, 1)
	collected := false
	t.Cleanup(func() {
		stop()
		if !collected {
			<-done
		}
	})
	go func() {
		statuses, err := st.MigrationStatus(statusCtx)
		done <- result{statuses, err}
	}()
	var r result
	for {
		select {
		case r = <-done:
			collected = true
		default:
			if tryingGooseLock(t, ctx, holder, "status") > 0 {
				t.Fatal("MigrationStatus waits for the migration lock")
			}
			continue
		}
		break
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.statuses) == 0 {
		t.Fatal("no migrations")
	}
	for _, s := range r.statuses {
		if !s.Applied {
			t.Errorf("%s is not applied", s.Path)
		}
	}
}

// TestLockMigrations (#214): while one store holds the migration command
// lock, a second LockMigrations says that it waits and waits until the
// lock is free; then it holds the lock until unlock.
func TestLockMigrations(t *testing.T) {
	a, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	b, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	observer := connect(t, dbURL)

	unlockA, err := a.LockMigrations(ctx, func() { t.Error("waited for a free lock") })
	if err != nil {
		t.Fatal(err)
	}
	if tryMigrateLock(t, ctx, observer) {
		t.Fatal("the lock is not held")
	}

	type result struct {
		unlock func()
		err    error
	}
	lockCtx, stop := context.WithCancel(ctx)
	waited := make(chan struct{})
	done := make(chan result, 1)
	collected := false
	// B ends, and both locks are released, also when the test fails, so
	// that no wait for the lock outlives the test.
	t.Cleanup(func() {
		stop()
		unlockA()
		if !collected {
			if r := <-done; r.unlock != nil {
				r.unlock()
			}
		}
	})
	go func() {
		unlock, err := b.LockMigrations(lockCtx, func() { close(waited) })
		done <- result{unlock, err}
	}()

	for waitingForMigrateLock(t, ctx, observer) < 1 {
		select {
		case r := <-done:
			collected = true
			if r.unlock != nil {
				r.unlock()
			}
			t.Fatalf("got the lock while another store held it: %v", r.err)
		default:
		}
	}
	select {
	case <-waited:
	default:
		t.Fatal("onWait was not called before the wait")
	}

	unlockA()
	r := receive(t, ctx, done, "lock after the unlock")
	collected = true
	if r.err != nil {
		t.Fatal(r.err)
	}
	if tryMigrateLock(t, ctx, observer) {
		t.Fatal("the lock is not held after the wait")
	}
	r.unlock()
	r.unlock() // a second call does nothing
	if !tryMigrateLock(t, ctx, observer) {
		t.Fatal("the lock is held after unlock")
	}
}

// TestLockMigrationsCancel (#214): cancelling the wait (Ctrl-C) returns an
// error and leaves neither the lock nor a waiting session behind.
func TestLockMigrationsCancel(t *testing.T) {
	a, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	b, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	observer := connect(t, dbURL)

	unlockA, err := a.LockMigrations(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlockA)

	lockCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	collected := false
	t.Cleanup(func() {
		stop()
		if !collected {
			<-done
		}
	})
	go func() {
		unlock, err := b.LockMigrations(lockCtx, nil)
		if unlock != nil {
			unlock()
		}
		done <- err
	}()
	for waitingForMigrateLock(t, ctx, observer) < 1 {
		select {
		case err := <-done:
			collected = true
			t.Fatalf("got the lock while another store held it: %v", err)
		default:
		}
	}
	stop()
	err = receive(t, ctx, done, "end of the cancelled wait")
	collected = true
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
	// The cancelled backend leaves the queue; until then it would get the
	// lock when A lets go.
	for waitingForMigrateLock(t, ctx, observer) > 0 {
	}
	unlockA()
	if !tryMigrateLock(t, ctx, observer) {
		t.Fatal("the lock is held after the cancelled wait and unlock")
	}
}

// TestLockMigrationsOutsidePool (#228): the migration lock is held on a
// connection of its own, outside the pool. With a pool of one connection,
// a store query still runs while the lock is held, and closing the store
// neither waits for the lock's connection nor releases the lock.
func TestLockMigrationsOutsidePool(t *testing.T) {
	_, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	st, err := store.Open(ctx, storetest.WithMaxConns(t, dbURL, 1))
	if err != nil {
		t.Fatal(err)
	}
	observer := connect(t, dbURL)

	unlock, err := st.LockMigrations(ctx, func() { t.Error("waited for a free lock") })
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	closing := false
	// unlock first, also when the test fails: a lock on a pool connection
	// would keep the store from closing.
	t.Cleanup(func() {
		unlock()
		if !closing {
			st.Close()
			return
		}
		// Bounded, so a Close that hangs for another reason fails here
		// instead of at the go test timeout.
		wait, stop := context.WithTimeout(context.Background(), testwait.Limit(t))
		defer stop()
		select {
		case <-closed:
		case <-wait.Done():
			t.Errorf("the store did not close after the unlock: %v", wait.Err())
		}
	})

	// The pool's only connection is free for the store's queries.
	if _, err := st.MigrationStatus(ctx); err != nil {
		t.Fatalf("a store query while the lock is held: %v", err)
	}
	// Closing the store does not wait for the lock: the defer order of
	// unlock and close in the commands does not matter.
	closing = true
	go func() {
		st.Close()
		close(closed)
	}()
	receive(t, ctx, closed, "closed store")
	if tryMigrateLock(t, ctx, observer) {
		t.Fatal("closing the store released the lock")
	}
	unlock()
	if !tryMigrateLock(t, ctx, observer) {
		t.Fatal("the lock is held after unlock")
	}
}
