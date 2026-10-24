package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

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
	for {
		var n int
		if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND classid = $1 AND objid = 0 AND objsubid = 2 AND NOT granted
			  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, 0x6d68).Scan(&n); err != nil {
			t.Fatalf("no wait for the migration lock: %v", err)
		}
		if n > 0 {
			return
		}
		select {
		case err := <-r.done:
			r.collected = true
			t.Fatalf("ended while the migration lock was held: %v", err)
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
