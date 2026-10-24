package store_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3/lock"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// gooseLockRows is the condition on pg_locks (as l) for goose's lock in
// the test's database: a single bigint key below 2^32.
const gooseLockRows = `l.locktype = 'advisory' AND l.classid = 0 AND l.objid::bigint = $1 AND l.objsubid = 1
	AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())`

// countGooseLockWaiters counts the sessions of the test's database that
// wait for goose's lock.
func countGooseLockWaiters(ctx context.Context, conn *pgx.Conn) (int, error) {
	var n int
	err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_locks l WHERE NOT l.granted AND `+gooseLockRows,
		lock.DefaultLockID).Scan(&n)
	return n, err
}

// gooseLockHeld reports whether a session of the test's database holds
// goose's lock.
func gooseLockHeld(t *testing.T, ctx context.Context, conn *pgx.Conn) bool {
	t.Helper()
	var held bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks l WHERE l.granted AND `+gooseLockRows+`)`,
		lock.DefaultLockID).Scan(&held); err != nil {
		t.Fatal(err)
	}
	return held
}

// backendPID returns the backend process ID of conn.
func backendPID(t *testing.T, ctx context.Context, conn *pgx.Conn) int64 {
	t.Helper()
	var pid int64
	if err := conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

// TestForMigrationsSessionSettings (#253): the connections of a store
// opened with ForMigrations check for a vanished client every 10 s during
// a statement and use TCP keepalives; without the option they keep the
// server's defaults. It checks that the settings are applied, not that a
// vanished host is detected (that needs a network fault). On a Unix
// socket PostgreSQL ignores the keepalives, so they are checked over TCP
// only.
func TestForMigrationsSessionSettings(t *testing.T) {
	dbURL := storetest.NewUnmigrated(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	open := func(opts ...store.Option) *store.Store {
		t.Helper()
		st, err := store.Open(ctx, dbURL, opts...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(st.Close)
		return st
	}
	show := func(st *store.Store, name string) string {
		t.Helper()
		var v string
		if err := st.QueryRowForTest(ctx, "SHOW "+name).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	migrating, plain := open(store.ForMigrations()), open()
	var tcp bool
	if err := plain.QueryRowForTest(ctx, "SELECT inet_client_addr() IS NOT NULL").Scan(&tcp); err != nil {
		t.Fatal(err)
	}

	if got := show(migrating, "client_connection_check_interval"); got != "10s" {
		t.Errorf("with ForMigrations: client_connection_check_interval = %q, want 10s", got)
	}
	if got := show(plain, "client_connection_check_interval"); got != "0" {
		t.Errorf("without ForMigrations: client_connection_check_interval = %q, want the default 0", got)
	}
	if !tcp {
		t.Log("not a TCP connection: keepalives not checked")
		return
	}
	for name, want := range map[string]string{
		"tcp_keepalives_idle":     "60",
		"tcp_keepalives_interval": "10",
		"tcp_keepalives_count":    "6",
	} {
		if got := show(migrating, name); got != want {
			t.Errorf("with ForMigrations: %s = %q, want %q", name, got, want)
		}
		if got := show(plain, name); got == want {
			t.Errorf("without ForMigrations: %s = %q, the value of ForMigrations", name, got)
		}
	}
}

// TestForMigrationsRefusedSetting (#253): a SET the server refuses (here
// an invalid check interval, as PostgreSQL before 14 or a pooler would
// refuse one) does not break the connect: the store opens and queries,
// the refused setting keeps its default, and the SETs after it still
// apply. Without the best effort, Open fails.
func TestForMigrationsRefusedSetting(t *testing.T) {
	dbURL := storetest.NewUnmigrated(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	st, err := store.Open(ctx, dbURL, store.ForMigrationsWithCheckInterval("not an interval"))
	if err != nil {
		t.Fatalf("open with a refused SET: %v", err)
	}
	t.Cleanup(st.Close)
	var interval string
	var tcp bool
	if err := st.QueryRowForTest(ctx, "SELECT current_setting('client_connection_check_interval'), inet_client_addr() IS NOT NULL").
		Scan(&interval, &tcp); err != nil {
		t.Fatalf("query after a refused SET: %v", err)
	}
	if interval != "0" {
		t.Errorf("client_connection_check_interval = %q, want the default 0", interval)
	}
	if !tcp {
		t.Log("not a TCP connection: keepalives not checked")
		return
	}
	var idle string
	if err := st.QueryRowForTest(ctx, "SHOW tcp_keepalives_idle").Scan(&idle); err != nil {
		t.Fatal(err)
	}
	if idle != "60" {
		t.Errorf("tcp_keepalives_idle = %q after the refused SET, want 60", idle)
	}
}

// TestSessionSettingBreaksConnection (#253): a connection that breaks
// during the session settings is not handed out: Open fails with the
// server's error. Handing it out would make Open fail later with a bare
// "conn closed" instead.
func TestSessionSettingBreaksConnection(t *testing.T) {
	dbURL := storetest.NewUnmigrated(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	st, err := store.Open(ctx, dbURL, store.WithSessionSettings("SELECT pg_terminate_backend(pg_backend_pid())"))
	if err == nil {
		st.Close()
		t.Fatal("Open succeeded although every connection breaks")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "57P01" {
		t.Fatalf("Open failed with %v, want the server's admin_shutdown (57P01) from the setting", err)
	}
}

// TestMigrateWaitsWhenHolderLookupFails (#253): the lookup of the holder
// only feeds the message. If it fails, Migrate still calls the wait
// function, with an empty holder, waits for goose's lock and migrates once
// it is free.
func TestMigrateWaitsWhenHolderLookupFails(t *testing.T) {
	defer store.SetGooseLockHolderQuery("SELECT $1::bigint / 0, NULL::text, NULL::float8")()
	dbURL := storetest.NewUnmigrated(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	observer := connect(t, dbURL)
	holder := connect(t, dbURL)
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
		t.Fatal(err)
	}
	st := openNamed(t, ctx, dbURL, "migrate")
	waits := make(chan store.GooseLockHolder, 4)
	st.SetGooseLockWait(func(h store.GooseLockHolder) { waits <- h })
	migrateCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	collected := false
	t.Cleanup(func() {
		stop()
		if !collected {
			<-done
		}
	})
	go func() {
		_, err := st.Migrate(migrateCtx)
		done <- err
	}()

	select {
	case h := <-waits:
		if h != (store.GooseLockHolder{}) {
			t.Errorf("holder %+v from a failed lookup, want an empty one", h)
		}
	case err := <-done:
		collected = true
		t.Fatalf("Migrate returned before it waited: %v", err)
	case <-ctx.Done():
		t.Fatalf("no call of the wait function: %v", ctx.Err())
	}
	for {
		n, err := countGooseLockWaiters(ctx, observer)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		select {
		case err := <-done:
			collected = true
			t.Fatalf("Migrate returned while goose's lock was held: %v", err)
		default:
		}
	}
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", lock.DefaultLockID); err != nil {
		t.Fatal(err)
	}
	err := receive(t, ctx, done, "migration result")
	collected = true
	if err != nil {
		t.Fatal(err)
	}
}

// orphanChildEnv makes the test binary act as the child of
// TestMigrationStatementEndsAfterKill. Its value is the URL to connect to.
const orphanChildEnv = "MAIL_ARCHIVE_TEST_ORPHAN_CHILD_URL"

// orphanApp is the application_name of the child's connection.
const orphanApp = "orphan-child"

// orphanChild is the child process of TestMigrationStatementEndsAfterKill:
// on the single connection of its store, which it opens like migrate but
// with a check interval of 1 s, it takes goose's lock and then runs a
// statement that sleeps for an hour, as a long migration statement would.
// The parent kills it meanwhile; it never returns.
func orphanChild(dbURL string) {
	ctx := context.Background()
	st, err := store.Open(ctx, dbURL, store.ForMigrationsWithCheckInterval("1s"))
	if err != nil {
		fmt.Println("unexpected: open the store:", err)
		os.Exit(3)
	}
	var n int
	if err := st.QueryRowForTest(ctx, "SELECT 1 FROM pg_advisory_lock($1)", lock.DefaultLockID).Scan(&n); err != nil {
		fmt.Println("unexpected: take goose's lock:", err)
		os.Exit(3)
	}
	err = st.QueryRowForTest(ctx, "SELECT 1 FROM pg_sleep(3600)").Scan(&n)
	fmt.Println("unexpected: the sleep ended:", err)
	os.Exit(3)
}

// TestMigrationStatementEndsAfterKill (#253): when a migrate is killed
// (SIGKILL) during a long statement on the connection that holds goose's
// lock, the server ends the statement soon after, and with it the backend
// and the lock, instead of running the statement to its end. Without
// ForMigrations, the statement (an hour of pg_sleep) runs on and the test
// fails at its bound.
func TestMigrationStatementEndsAfterKill(t *testing.T) {
	if dbURL := os.Getenv(orphanChildEnv); dbURL != "" {
		orphanChild(dbURL) // before any storetest call: it creates no database
	}

	dbURL := storetest.NewUnmigrated(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	observer := connect(t, dbURL)
	// Registered before the child starts, and run whatever the result: no
	// sleeping backend outlives the test on the shared server.
	t.Cleanup(func() {
		_, _ = observer.Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = $1`, orphanApp)
	})

	childURL := storetest.WithMaxConns(t, storetest.WithApplicationName(t, dbURL, orphanApp), 1)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMigrationStatementEndsAfterKill$") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), orphanChildEnv+"="+childURL)
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

	// The child's statement runs, on the connection that holds goose's lock.
	var pid int64
	for {
		err := observer.QueryRow(ctx, `SELECT a.pid FROM pg_stat_activity a JOIN pg_locks l ON l.pid = a.pid
			WHERE a.datname = current_database() AND a.application_name = $2 AND a.state = 'active'
			  AND a.query LIKE '%pg_sleep%' AND l.granted AND `+gooseLockRows, lock.DefaultLockID, orphanApp).Scan(&pid)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("no sleeping statement of the child: %v", err)
		}
		select {
		case err := <-exited:
			collected = true
			t.Fatalf("the child ended before its statement ran: %v\n%s", err, out.String())
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
		var running bool
		err := observer.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1)`, pid).Scan(&running)
		if ctx.Err() != nil {
			t.Fatalf("the statement of the killed process still runs: %v", ctx.Err())
		}
		if err != nil {
			t.Fatal(err)
		}
		if !running {
			break
		}
	}
	if gooseLockHeld(t, ctx, observer) {
		t.Fatal("goose's lock is held after the backend ended")
	}
}

// TestMigrateSaysWhoHoldsGooseLock (#253): while another session holds
// goose's lock, Migrate calls the function set with SetGooseLockWait once
// with that session, waits without a time limit, and migrates once the
// lock is free; afterwards the lock is free. The holder is idle (a session
// that holds the lock between statements) or active (a statement of a
// killed migrate). Without the custom locker, goose retries silently and
// the function is never called.
func TestMigrateSaysWhoHoldsGooseLock(t *testing.T) {
	for _, active := range []bool{false, true} {
		name := map[bool]string{false: "idle", true: "active"}[active]
		t.Run(name, func(t *testing.T) {
			// Unmigrated: goose takes its lock only when something is
			// pending.
			dbURL := storetest.NewUnmigrated(t)
			ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
			defer cancel()
			observer := connect(t, dbURL)
			holder := connect(t, dbURL)
			holderPID := backendPID(t, ctx, holder)
			if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
				t.Fatal(err)
			}
			// release frees goose's lock: an idle holder lets go of it; an
			// active one is cancelled, which ends its session.
			release := func() {
				if _, err := holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", lock.DefaultLockID); err != nil {
					t.Fatal(err)
				}
			}
			if active {
				sleepCtx, stopSleep := context.WithCancel(ctx)
				slept := make(chan struct{})
				go func() {
					defer close(slept)
					_, _ = holder.Exec(sleepCtx, "SELECT pg_sleep(3600)")
				}()
				release = func() {
					stopSleep()
					<-slept
				}
				t.Cleanup(release)
				for {
					var state string
					if err := observer.QueryRow(ctx, "SELECT coalesce(state, '') FROM pg_stat_activity WHERE pid = $1", holderPID).Scan(&state); err != nil {
						t.Fatal(err)
					}
					if state == "active" {
						break
					}
				}
			}

			st := openNamed(t, ctx, dbURL, "migrate")
			waits := make(chan store.GooseLockHolder, 4)
			st.SetGooseLockWait(func(h store.GooseLockHolder) { waits <- h })
			migrateCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			collected := false
			t.Cleanup(func() {
				stop()
				if !collected {
					<-done
				}
			})
			go func() {
				_, err := st.Migrate(migrateCtx)
				done <- err
			}()

			h := receive(t, ctx, waits, "call of the wait function")
			if h.PID != holderPID {
				t.Errorf("holder pid %d, want %d", h.PID, holderPID)
			}
			if h.State != name {
				t.Errorf("holder state %q, want %q", h.State, name)
			}
			if active && (h.ActiveFor <= 0 || h.ActiveFor > testwait.Limit(t)) {
				t.Errorf("active for %v", h.ActiveFor)
			}
			if !active && h.ActiveFor != 0 {
				t.Errorf("an idle holder is active for %v", h.ActiveFor)
			}
			for {
				n, err := countGooseLockWaiters(ctx, observer)
				if err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					break
				}
				select {
				case err := <-done:
					collected = true
					t.Fatalf("Migrate returned while goose's lock was held: %v", err)
				default:
				}
			}

			release()
			err := receive(t, ctx, done, "migration result")
			collected = true
			if err != nil {
				t.Fatal(err)
			}
			if n := len(waits); n != 0 {
				t.Errorf("the wait function was called %d more times", n)
			}
			if gooseLockHeld(t, ctx, observer) {
				t.Error("goose's lock is held after Migrate")
			}
		})
	}
}

// TestGooseLockHolderOfAnotherRole (#253): when goose's lock is held by a
// session of a role whose activity the migrating role may not see, the
// wait function still gets the holder's pid, with an empty state and no
// duration (pg_stat_activity shows NULL for them). The test needs a role
// that can create roles.
func TestGooseLockHolderOfAnotherRole(t *testing.T) {
	admin, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	// goose takes its lock only when something is pending.
	if _, err := admin.MigrateDown(ctx); err != nil {
		t.Fatal(err)
	}
	holder := connect(t, dbURL)
	var canCreate bool
	if err := holder.QueryRow(ctx, "SELECT rolsuper OR rolcreaterole FROM pg_roles WHERE rolname = current_user").Scan(&canCreate); err != nil {
		t.Fatal(err)
	}
	if !canCreate {
		t.Skip("the test role cannot create roles")
	}
	suffix, password := make([]byte, 6), make([]byte, 16)
	_, _ = rand.Read(suffix)
	_, _ = rand.Read(password)
	role := "mailarchive_test_role_" + hex.EncodeToString(suffix)
	pw := hex.EncodeToString(password) // hex: safe in the literal
	ident := pgx.Identifier{role}.Sanitize()
	if _, err := holder.Exec(ctx, "CREATE ROLE "+ident+" LOGIN PASSWORD '"+pw+"'"); err != nil {
		t.Fatal(err)
	}
	// Registered before the store opens, so it runs after the store
	// closed. DROP OWNED drops the grant below.
	t.Cleanup(func() {
		for _, q := range []string{"DROP OWNED BY " + ident, "DROP ROLE " + ident} {
			if _, err := holder.Exec(context.Background(), q); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
	})
	// goose reads the applied versions before it takes its lock.
	if _, err := holder.Exec(ctx, "GRANT SELECT ON goose_db_version TO "+ident); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, pw)

	holderPID := backendPID(t, ctx, holder)
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = holder.Exec(context.Background(), "SELECT pg_advisory_unlock_all()") })

	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	waits := make(chan store.GooseLockHolder, 1)
	st.SetGooseLockWait(func(h store.GooseLockHolder) { waits <- h })
	migrateCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	collected := false
	t.Cleanup(func() {
		stop()
		if !collected {
			<-done
		}
	})
	go func() {
		_, err := st.Migrate(migrateCtx)
		done <- err
	}()

	h := receive(t, ctx, waits, "call of the wait function")
	if h.PID != holderPID || h.State != "" || h.ActiveFor != 0 {
		t.Errorf("holder %+v, want pid %d without state and duration", h, holderPID)
	}
	stop()
	err = receive(t, ctx, done, "end of the cancelled wait")
	collected = true
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
}

// TestMigrateCancelBeforeQuery (#268): a cancel that lands after pgx
// started one of goose's queries on its *sql.Conn but before pgx sent it
// ends Migrate, MigrateDown and MigrationStatus with an error that wraps
// context.Canceled. pgx's stdlib driver turns such a cancel into a bare
// driver.ErrBadConn; the query-start hook cancels exactly in that window.
// The cases cover each query of goose's lock (try-lock, holder lookup,
// wait: the CI interleaving of TestGooseLockHolderOfAnotherRole) and
// goose's version-table check, which MigrateDown runs after it took the
// lock and Migrate before (in HasPending, without the lock; goose checks
// the table once per provider, so Migrate does not check it again under
// the lock). Afterwards no backend waits for goose's lock, and right after
// the call, with the store still open, only the test's holder holds it
// (#269: before goose had a pool of its own, MigrateDown's cancelled
// version-table check left the lock on an idle connection of the store's
// pool). After Close, too, only the test's holder holds it.
func TestMigrateCancelBeforeQuery(t *testing.T) {
	cases := []struct {
		name string
		// prefix selects the query: the first query of the store that
		// starts with it.
		prefix string
		// holder: another session holds goose's lock during the call.
		holder bool
		// versionTable: the case is goose's version-table check, which
		// runs with or without goose's lock depending on the call.
		versionTable bool
	}{
		{name: "try-lock", prefix: "SELECT pg_try_advisory_lock("},
		{name: "holder-lookup", prefix: "SELECT l.pid, a.state", holder: true},
		{name: "wait", prefix: "SELECT pg_advisory_lock(", holder: true},
		{name: "version-table", prefix: "SELECT EXISTS ( SELECT 1 FROM pg_tables", versionTable: true},
	}
	type call struct {
		name string
		// versionTableLocked: the call checks the version table after it
		// took goose's lock.
		versionTableLocked bool
		run                func(context.Context, *store.Store) error
		db                 func(*testing.T) string
	}
	calls := []call{
		{
			name: "migrate",
			run:  func(ctx context.Context, st *store.Store) error { _, err := st.Migrate(ctx); return err },
			db:   storetest.NewUnmigrated,
		},
		{
			name:               "migrate-down",
			versionTableLocked: true,
			run:                func(ctx context.Context, st *store.Store) error { _, err := st.MigrateDown(ctx); return err },
			db: func(t *testing.T) string {
				_, dbURL := storetest.NewWithURL(t)
				return dbURL
			},
		},
	}
	for _, cl := range calls {
		for _, c := range cases {
			name := cl.name + "/" + c.name
			// locked: goose's lock is held by some session when the
			// cancel lands.
			locked := c.holder
			if c.versionTable {
				locked = cl.versionTableLocked
				if locked {
					name += "-locked"
				} else {
					name += "-unlocked"
				}
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
				defer cancel()
				dbURL := cl.db(t)
				observer := connect(t, dbURL)
				var holderPID int64
				if c.holder {
					holder := connect(t, dbURL)
					holderPID = backendPID(t, ctx, holder)
					if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
						t.Fatal(err)
					}
				}
				migrateCtx, stop := context.WithCancel(ctx)
				defer stop()
				fired := false
				// The hook runs on the goroutine of the call below, which
				// is the test's goroutine.
				st, err := store.Open(ctx, dbURL, store.WithQueryStartHook(func(sql string) {
					if fired || !strings.HasPrefix(sql, c.prefix) {
						return
					}
					fired = true
					if held := gooseLockHeld(t, ctx, observer); held != locked {
						t.Errorf("goose's lock held at the cancel: %v, want %v", held, locked)
					}
					stop()
				}))
				if err != nil {
					t.Fatal(err)
				}
				st.SetGooseLockWait(func(store.GooseLockHolder) {})
				err = cl.run(migrateCtx, st)
				if !fired {
					t.Fatalf("query %q never started; error %v", c.prefix, err)
				}
				if !errors.Is(err, context.Canceled) {
					t.Errorf("cancelled %s: %v, want an error that wraps context.Canceled", cl.name, err)
				}
				if n, err := countGooseLockWaiters(ctx, observer); err != nil || n != 0 {
					t.Errorf("backends waiting for goose's lock: %d (%v), want 0", n, err)
				}
				// #269: with the store still open, no session other than
				// the test's holder holds goose's lock. No wait: the
				// teardown frees it before the call returns.
				var others int
				if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_locks l WHERE l.granted AND l.pid <> $2 AND `+gooseLockRows,
					lock.DefaultLockID, holderPID).Scan(&others); err != nil {
					t.Fatal(err)
				}
				if others != 0 {
					t.Errorf("goose's lock still held by %d session(s) of the open store after the cancelled %s", others, cl.name)
				}
				st.Close()
				// The server releases a closed session's lock
				// asynchronously; wait until only the holder holds it.
				for {
					var others int
					if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_locks l WHERE l.granted AND l.pid <> $2 AND `+gooseLockRows,
						lock.DefaultLockID, holderPID).Scan(&others); err != nil {
						t.Fatalf("goose's lock still held by the store's sessions after Close: %v", err)
					}
					if others == 0 {
						break
					}
				}
			})
		}
	}

	t.Run("migration-status/version-table", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
		defer cancel()
		_, dbURL := storetest.NewWithURL(t)
		statusCtx, stop := context.WithCancel(ctx)
		defer stop()
		fired := false
		st, err := store.Open(ctx, dbURL, store.WithQueryStartHook(func(sql string) {
			if !fired && strings.HasPrefix(sql, "SELECT EXISTS ( SELECT 1 FROM pg_tables") {
				fired = true
				stop()
			}
		}))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		_, err = st.MigrationStatus(statusCtx)
		if !fired {
			t.Fatalf("version-table check never started; error %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled status: %v, want an error that wraps context.Canceled", err)
		}
	})
}

// TestMigrateSessionSettingsReachGoose (#269): goose runs on a pool of its
// own, made from a copy of the store's pool config. The session settings
// of Open (as ForMigrations sets them) reach goose's session, and that
// session is not a connection of the store's pool. The hook fires when
// goose starts its version-table check, on goose's connection.
func TestMigrateSessionSettingsReachGoose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	dbURL := storetest.NewUnmigrated(t)
	observer := connect(t, dbURL)
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	appName := "goose_269_" + hex.EncodeToString(suffix) // hex: safe in the literal
	fired := false
	var st *store.Store
	// The hook runs on the goroutine of Migrate below, which is the
	// test's goroutine.
	st, err := store.Open(ctx, dbURL,
		store.WithSessionSettings("SET application_name = '"+appName+"'"),
		store.WithQueryStartConnHook(func(pid uint32, sql string) {
			if fired || !strings.HasPrefix(sql, "SELECT EXISTS ( SELECT 1 FROM pg_tables") {
				return
			}
			fired = true
			var got string
			if err := observer.QueryRow(ctx, "SELECT application_name FROM pg_stat_activity WHERE pid = $1", pid).Scan(&got); err != nil {
				t.Errorf("goose's session %d: %v", pid, err)
			} else if got != appName {
				t.Errorf("goose's session: application_name = %q, want %q", got, appName)
			}
			if n := st.AcquiredConnsForTest(); n != 0 {
				t.Errorf("connections of the store's pool in use during goose's query: %d, want 0", n)
			}
		}))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("goose's version-table check never started")
	}
}
