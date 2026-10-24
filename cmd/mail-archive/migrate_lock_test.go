package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3/lock"

	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// migrateEnv points the commands at the database url.
func migrateEnv(t *testing.T, url string) {
	t.Helper()
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
}

// connectObserver opens a connection of the test's own on url.
func connectObserver(t *testing.T, ctx context.Context, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// pendingMigrations counts the migrations st has not applied.
func pendingMigrations(t *testing.T, ctx context.Context, st *store.Store) int {
	t.Helper()
	statuses, err := st.MigrationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pending := 0
	for _, s := range statuses {
		if !s.Applied {
			pending++
		}
	}
	return pending
}

// migrateRun is a migrate command running in a goroutine while the test
// holds the migration lock.
type migrateRun struct {
	done      chan error
	collected bool // the test took the result, or gave up on it
	out       bytes.Buffer
}

// startMigrate runs the migrate command with args in a goroutine. When the
// test ends, it cancels the command and calls unlock, which releases the
// lock the test holds, and then waits for the command unless the test took
// its result: a command that never started is never waited for. The wait
// is bounded, so a command that does not end fails the test instead of
// hanging until the go test timeout.
func startMigrate(t *testing.T, ctx context.Context, unlock func(), args ...string) *migrateRun {
	t.Helper()
	cmdCtx, stop := context.WithCancel(ctx)
	r := &migrateRun{done: make(chan error, 1)}
	cmd := newMigrateCmd()
	cmd.SetArgs(args)
	cmd.SetOut(&r.out)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	go func() { r.done <- cmd.ExecuteContext(cmdCtx) }()
	t.Cleanup(func() {
		stop()
		unlock()
		if r.collected {
			return
		}
		wait, giveUp := context.WithTimeout(context.Background(), testwait.Limit(t))
		defer giveUp()
		select {
		case <-r.done:
		case <-wait.Done():
			// Dropping the test database ends the command's connections.
			t.Errorf("the command %v did not end after the test: %v", args, wait.Err())
		}
	})
	return r
}

// waitForMigrateLockWait waits until a session waits for the migration
// lock (its pg_locks row). The command must not end meanwhile.
func (r *migrateRun) waitForMigrateLockWait(t *testing.T, ctx context.Context, observer *pgx.Conn) {
	t.Helper()
	r.waitForLockWait(t, ctx, observer, "the migration lock", 0x6d68, 0, 2)
}

// waitForGooseLockWait waits until a session waits for goose's lock (a
// single bigint key below 2^32). The command must not end meanwhile.
func (r *migrateRun) waitForGooseLockWait(t *testing.T, ctx context.Context, observer *pgx.Conn) {
	t.Helper()
	r.waitForLockWait(t, ctx, observer, "goose's lock", 0, lock.DefaultLockID, 1)
}

// waitForLockWait waits until a session waits for the advisory lock with
// the given keys (its pg_locks row). The command must not end meanwhile.
func (r *migrateRun) waitForLockWait(t *testing.T, ctx context.Context, observer *pgx.Conn, what string, classid, objid int64, objsubid int) {
	t.Helper()
	for {
		var n int
		if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND classid::bigint = $1 AND objid::bigint = $2 AND objsubid = $3 AND NOT granted
			  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, classid, objid, objsubid).Scan(&n); err != nil {
			t.Fatalf("no wait for %s: %v", what, err)
		}
		if n > 0 {
			return
		}
		select {
		case err := <-r.done:
			r.collected = true
			t.Fatalf("ended while %s was held: %v", what, err)
		default:
		}
	}
}

// result returns the command's result. It is bounded by ctx, so a command
// that never ends fails the test with a message instead of hanging until
// the go test timeout.
func (r *migrateRun) result(t *testing.T, ctx context.Context) error {
	t.Helper()
	select {
	case err := <-r.done:
		r.collected = true
		return err
	case <-ctx.Done():
		// Stuck for good (cancelling does not end it): the cleanup must
		// not wait for it either. Dropping the database ends its
		// connections.
		r.collected = true
		t.Fatalf("the command did not end: %v", ctx.Err())
		return nil
	}
}

// waitMessage is what a migrate command says while it waits for the lock.
const waitMessage = "another migrate is running; waiting for it"

// TestMigrateWaitsForMigrateLock (#214): `migrate` and `migrate down`
// started while another migrate holds the migration lock say that they
// wait, wait, and finish once it is free.
func TestMigrateWaitsForMigrateLock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		pending int // migrations pending afterwards
	}{
		{"migrate", nil, 0},
		{"down", []string{"down", "--yes"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, url := storetest.NewWithURL(t)
			migrateEnv(t, url)
			ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
			defer cancel()
			observer := connectObserver(t, ctx, url)

			unlock, err := st.LockMigrations(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			run := startMigrate(t, ctx, unlock, tc.args...)
			run.waitForMigrateLockWait(t, ctx, observer)
			unlock()
			if err := run.result(t, ctx); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(run.out.String(), waitMessage) {
				t.Errorf("output %q does not say that it waits", run.out.String())
			}
			if n := pendingMigrations(t, ctx, st); n != tc.pending {
				t.Errorf("%d migrations pending afterwards, want %d", n, tc.pending)
			}
		})
	}
}

// TestMigrateOnePoolConnection (#228): with a pool of one connection
// (pool_max_conns=1), `migrate` and `migrate down` finish: the migration
// lock does not hold the pool's only connection.
func TestMigrateOnePoolConnection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		pending int // migrations pending afterwards
	}{
		{"migrate", nil, 0},
		{"down", []string{"down", "--yes"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var url string
			if tc.args == nil {
				url = storetest.NewUnmigrated(t)
			} else {
				_, url = storetest.NewWithURL(t)
			}
			migrateEnv(t, storetest.WithMaxConns(t, url, 1))
			ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
			defer cancel()

			cmd := newMigrateCmd()
			cmd.SetArgs(tc.args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			// Without the fix it waits for a pool connection until ctx ends.
			if err := cmd.ExecuteContext(ctx); err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if n := pendingMigrations(t, ctx, st); n != tc.pending {
				t.Errorf("%d migrations pending afterwards, want %d", n, tc.pending)
			}
		})
	}
}

// partitionAttached reports whether the table part is a partition of
// audit_log.
func partitionAttached(t *testing.T, ctx context.Context, conn *pgx.Conn, part string) bool {
	t.Helper()
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_inherits
		WHERE inhparent = 'audit_log'::regclass AND inhrelid = to_regclass($1))`, part).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// TestMigrateDownLocksFirst (#228): `migrate down` takes the migration
// lock before it touches the database: it creates no audit log partition
// while it waits, and once it has the lock it creates the missing one and
// rolls back. It does not depend on the database's locale.
func TestMigrateDownLocksFirst(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	migrateEnv(t, url)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	observer := connectObserver(t, ctx, url)

	// Next month's partition: the commands create this month's and the
	// next one, so `down` creates it again also when the test runs across
	// the turn of a month (UTC).
	var part string
	if err := observer.QueryRow(ctx, `SELECT 'audit_log_' || to_char(date_trunc('month', now() AT TIME ZONE 'UTC') + interval '1 month', 'YYYY_MM')`).Scan(&part); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"ALTER TABLE audit_log DETACH PARTITION " + pgx.Identifier{part}.Sanitize(),
		"ALTER TABLE " + pgx.Identifier{part}.Sanitize() + " RENAME TO " + pgx.Identifier{"detached_" + part}.Sanitize(),
	} {
		if _, err := observer.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	unlock, err := st.LockMigrations(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := startMigrate(t, ctx, unlock, "down", "--yes")
	run.waitForMigrateLockWait(t, ctx, observer)
	if partitionAttached(t, ctx, observer, part) {
		t.Fatalf("down created the audit log partition %s before it took the lock", part)
	}
	unlock()
	if err := run.result(t, ctx); err != nil {
		t.Fatal(err)
	}
	if !partitionAttached(t, ctx, observer, part) {
		t.Errorf("down did not create the audit log partition %s", part)
	}
	if n := pendingMigrations(t, ctx, st); n != 1 {
		t.Errorf("%d migrations pending afterwards, want 1", n)
	}
}

// TestMigrateDownWaitsForIndexRebuild (#228): `migrate down` started while
// a first `migrate` rebuilds the indexes of a database created by the
// postgres:18-alpine image waits for it instead of refusing, and rolls
// back once the rebuild is done. The test holds the lock in place of that
// migrate. It needs a glibc locale (see storetest.SimulateAlpineDatabase).
func TestMigrateDownWaitsForIndexRebuild(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	migrateEnv(t, url)
	storetest.SimulateAlpineDatabase(t, url)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	observer := connectObserver(t, ctx, url)

	unlock, err := st.LockMigrations(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := startMigrate(t, ctx, unlock, "down", "--yes")
	run.waitForMigrateLockWait(t, ctx, observer)
	if rebuilt, err := st.RebuildIndexesIfNeeded(ctx, nil); err != nil || !rebuilt {
		t.Fatalf("rebuild in place of the first migrate: %v, %v", rebuilt, err)
	}
	unlock()
	if err := run.result(t, ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run.out.String(), waitMessage) {
		t.Errorf("output %q does not say that it waits", run.out.String())
	}
	if n := pendingMigrations(t, ctx, st); n != 1 {
		t.Errorf("%d migrations pending afterwards, want 1", n)
	}
}

// TestMigrateSaysItWaitsForGooseLock (#253): when another session holds
// goose's lock, as a statement of a killed migrate does, `migrate` and
// `migrate down` get the command lock, say who holds goose's lock, wait,
// and finish once it is free. Without the message they wait silently.
func TestMigrateSaysItWaitsForGooseLock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		pending int // migrations pending afterwards
	}{
		{"migrate", nil, 0},
		{"down", []string{"down", "--yes"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// goose takes its lock only when something is to do: for
			// migrate, an unmigrated database.
			var url string
			if tc.args == nil {
				url = storetest.NewUnmigrated(t)
			} else {
				_, url = storetest.NewWithURL(t)
			}
			migrateEnv(t, url)
			ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
			defer cancel()
			observer := connectObserver(t, ctx, url)
			holder := connectObserver(t, ctx, url)
			var pid int64
			if err := holder.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
				t.Fatal(err)
			}
			release := func() { _, _ = holder.Exec(context.Background(), "SELECT pg_advisory_unlock_all()") }

			run := startMigrate(t, ctx, release, tc.args...)
			run.waitForGooseLockWait(t, ctx, observer)
			release()
			if err := run.result(t, ctx); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("waiting for goose's migration lock, held by database backend %d (idle): ", pid)
			if out := run.out.String(); !strings.Contains(out, want) || strings.Contains(out, waitMessage) {
				t.Errorf("output %q does not say that it waits for goose's lock (%q), or says that it waits for the command lock", out, want)
			}
			st, err := store.Open(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if n := pendingMigrations(t, ctx, st); n != tc.pending {
				t.Errorf("%d migrations pending afterwards, want %d", n, tc.pending)
			}
		})
	}
}

// TestGooseLockWaitMessage (#253): the message names the holder's backend
// and how to stop it, says how long its statement has run only when it is
// active, and leaves out what this role may not see. Only a holder that
// may run a statement is said to end shortly; an idle one is not.
func TestGooseLockWaitMessage(t *testing.T) {
	const advice = ": usually a statement of an earlier migrate that was stopped, which ends shortly; " +
		"if no migrate is running anywhere, it can be stopped with SELECT pg_cancel_backend(12345), " +
		"or with SELECT pg_terminate_backend(12345) if it is idle"
	const idleAdvice = ": a session holds it and releases it when it ends or unlocks; " +
		"if no migrate is running anywhere, it can be stopped with SELECT pg_terminate_backend(12345)"
	for _, tc := range []struct {
		holder store.GooseLockHolder
		want   string
	}{
		{store.GooseLockHolder{PID: 12345, State: "active", ActiveFor: 3*time.Minute + 12400*time.Millisecond},
			"waiting for goose's migration lock, held by database backend 12345 (active for 3m12s)" + advice},
		{store.GooseLockHolder{PID: 12345, State: "idle"},
			"waiting for goose's migration lock, held by database backend 12345 (idle)" + idleAdvice},
		{store.GooseLockHolder{PID: 12345, State: "idle in transaction"},
			"waiting for goose's migration lock, held by database backend 12345 (idle in transaction)" + idleAdvice},
		// A session of another role: no state, no duration.
		{store.GooseLockHolder{PID: 12345},
			"waiting for goose's migration lock, held by database backend 12345" + advice},
		// Released before it was looked up.
		{store.GooseLockHolder{}, "waiting for goose's migration lock ..."},
	} {
		if got := gooseLockWaitMessage(tc.holder); got != tc.want {
			t.Errorf("%+v:\ngot  %q\nwant %q", tc.holder, got, tc.want)
		}
	}
}

// migrateChildEnv makes the test binary run `migrate` as the child of
// TestMigrateConnectionsNoticeKilledClient.
const migrateChildEnv = "MAIL_ARCHIVE_TEST_MIGRATE_CHILD"

// migrateChildApp is the application_name of the child's connections.
const migrateChildApp = "migrate-child"

// TestMigrateConnectionsNoticeKilledClient (#253): the connections of
// `migrate` check for a vanished client during a statement
// (store.ForMigrations): when the command is killed (SIGKILL) while it
// waits for goose's lock, its backend ends about 10 s later instead of
// staying queued for the lock. Without the option, the backend, which does
// not notice the closed socket while it waits, stays and the test fails at
// its bound.
func TestMigrateConnectionsNoticeKilledClient(t *testing.T) {
	if os.Getenv(migrateChildEnv) != "" {
		cmd := newMigrateCmd()
		cmd.SetArgs(nil)
		err := cmd.ExecuteContext(context.Background())
		fmt.Println("unexpected: migrate returned:", err)
		os.Exit(3)
	}

	// Unmigrated: goose takes its lock only when something is pending.
	url := storetest.NewUnmigrated(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	observer := connectObserver(t, ctx, url)
	holder := connectObserver(t, ctx, url)
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
		t.Fatal(err)
	}
	// Registered before the child starts, and run whatever the result: no
	// backend of the child outlives the test on the shared server.
	t.Cleanup(func() {
		_, _ = observer.Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = $1`, migrateChildApp)
	})

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMigrateConnectionsNoticeKilledClient$") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), migrateChildEnv+"=1",
		"MAIL_ARCHIVE_DATABASE_URL="+storetest.WithApplicationName(t, url, migrateChildApp),
		"MAIL_ARCHIVE_SECRET_KEY="+key)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	collected := false
	t.Cleanup(func() {
		cancel() // kills the child if it still runs
		if !collected {
			<-exited
		}
	})

	// The child's backend waits for goose's lock.
	var pid int64
	for {
		err := observer.QueryRow(ctx, `SELECT l.pid FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE a.application_name = $2 AND l.locktype = 'advisory' AND NOT l.granted
			  AND l.classid = 0 AND l.objid::bigint = $1 AND l.objsubid = 1
			  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())`,
			lock.DefaultLockID, migrateChildApp).Scan(&pid)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("no wait of the child for goose's lock: %v", err)
		}
		select {
		case err := <-exited:
			collected = true
			t.Fatalf("the child ended before it waited for goose's lock: %v\n%s", err, out.String())
		default:
		}
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
		collected = true
	case <-ctx.Done():
		t.Fatalf("the child did not end: %v", ctx.Err())
	}

	for {
		var waiting bool
		err := observer.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1)`, pid).Scan(&waiting)
		if ctx.Err() != nil {
			t.Fatalf("the backend of the killed migrate still waits for goose's lock: %v", ctx.Err())
		}
		if err != nil {
			t.Fatal(err)
		}
		if !waiting {
			break
		}
	}
}
