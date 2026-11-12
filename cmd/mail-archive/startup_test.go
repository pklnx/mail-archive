package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// lockedBuffer collects the log of a start that runs in a goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startEnv points the commands at the database url, with
// MAIL_ARCHIVE_MIGRATE_ON_START on or off.
func startEnv(t *testing.T, url string, migrateOnStart bool) {
	t.Helper()
	migrateEnv(t, url)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", t.TempDir())
	t.Setenv(config.EnvMigrateOnStart, map[bool]string{true: "on", false: "off"}[migrateOnStart])
}

// newStart is the start-up of role with the configuration of the
// environment and a log of its own.
func newStart(t *testing.T, role string, opts ...store.Option) (instanceStart, *lockedBuffer) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	return instanceStart{cfg: cfg, log: slog.New(slog.NewTextHandler(logs, nil)), role: role, storeOptions: opts}, logs
}

// exitCode is the exit code and message main gives for err.
func exitCode(err error) (int, string) { return exitStatus(context.Background(), err) }

// startMigrations are the repository's migration files with replace
// (name to content) laid over them.
func startMigrations(t *testing.T, replace map[string]string) fstest.MapFS {
	t.Helper()
	real := os.DirFS("../../internal/store/migrations")
	names, err := fs.Glob(real, "*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("migrations: %v %v", names, err)
	}
	out := fstest.MapFS{}
	for _, name := range names {
		data, err := fs.ReadFile(real, name)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = &fstest.MapFile{Data: data}
	}
	for name, data := range replace {
		out[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return out
}

// openStore opens a store of the test's own on url.
func openStore(t *testing.T, url string, opts ...store.Option) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), url, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

// schemaOf is the schema state of the database at url for the embedded
// migrations.
func schemaOf(t *testing.T, url string) store.SchemaState {
	t.Helper()
	s, err := openStore(t, url).SchemaState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestStartFresh (#396): on an empty database, a start with
// MAIL_ARCHIVE_MIGRATE_ON_START=on applies every migration and creates
// instance_state; it notes no backup, as there is nothing to back up.
func TestStartFresh(t *testing.T) {
	url := storetest.NewUnmigrated(t)
	startEnv(t, url, true)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	s, logs := newStart(t, "serve")
	if err := s.run(ctx); err != nil {
		t.Fatalf("start: %v\n%s", err, logs)
	}
	is, err := openStore(t, url).InstanceStatus(ctx, version)
	if err != nil || len(is.Pending) != 0 || is.SchemaVersion != is.LatestVersion || is.LastMigrationFailure != nil {
		t.Fatalf("after the start: %+v %v", is, err)
	}
	for _, want := range []string{"applying migrations at start", "applied 00001_initial.sql", "applied 00039_instance_state.sql"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs.String(), "backup") {
		t.Errorf("a fresh database got a backup note:\n%s", logs)
	}
}

// TestStartUpgrade (#396): a database one migration behind. Without
// MAIL_ARCHIVE_MIGRATE_ON_START, serve, worker and a command that uses the
// database refuse with exit code 4 and name the pending migration; backup
// and status only warn. With it on, a start applies only that migration,
// after a note on the last backup.
func TestStartUpgrade(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	down, err := st.MigrateDown(ctx)
	if err != nil {
		t.Fatal(err)
	}
	startEnv(t, url, false)

	for _, c := range []struct {
		name    string
		cmd     *cobra.Command
		args    []string
		atStart bool
	}{
		{"serve", newServeCmd(), []string{"--listen", "127.0.0.1:0"}, true},
		{"worker", newWorkerCmd(), nil, true},
		{"user list", newUserCmd(), []string{"list"}, false},
	} {
		code, msg := exitCode(runCmd(t, c.cmd, "", c.args...))
		if code != exitStartRefused || !strings.Contains(msg, "1 migration ("+down+")") || !strings.Contains(msg, "migrate") {
			t.Errorf("%s: exit %d: %s", c.name, code, msg)
		}
		if strings.Contains(msg, config.EnvMigrateOnStart) != c.atStart {
			t.Errorf("%s: the hint on %s: %s", c.name, config.EnvMigrateOnStart, msg)
		}
	}
	if err := runCmd(t, newStatusCmd(), ""); err != nil {
		t.Errorf("status: %v", err)
	}
	if s := schemaOf(t, url); len(s.Pending) != 1 {
		t.Fatalf("pending after the refusals: %+v", s.Pending)
	}

	// On: only the pending one, after the backup note (none yet).
	t.Setenv(config.EnvMigrateOnStart, "on")
	s, logs := newStart(t, "worker")
	if err := s.run(ctx); err != nil {
		t.Fatalf("start: %v\n%s", err, logs)
	}
	if out := logs.String(); !strings.Contains(out, "applied "+down) || strings.Contains(out, "applied 00001_") ||
		!strings.Contains(out, "no backup in the audit log in the last 30 days") {
		t.Errorf("log:\n%s", out)
	}
	if s := schemaOf(t, url); !s.Current() {
		t.Fatalf("after the start: %+v", s)
	}

	// After a backup (which a pending migration does not stop), the note
	// names it instead.
	if _, err := st.MigrateDown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, newBackupCmd(), "", "--note-only", "mailarchive-x.dump"); err != nil {
		t.Fatalf("backup with a pending migration: %v", err)
	}
	s, logs = newStart(t, "serve")
	if err := s.run(ctx); err != nil {
		t.Fatalf("start: %v\n%s", err, logs)
	}
	if out := logs.String(); !strings.Contains(out, `msg="last backup"`) || strings.Contains(out, "no backup") {
		t.Errorf("log after a backup:\n%s", out)
	}
}

// TestStartAhead (#396): a database a newer version migrated is refused
// with exit code 4, with or without start-up migrations, and the message
// names both versions; status only warns.
func TestStartAhead(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	latest := schemaOf(t, url).Latest
	if _, err := connectObserver(t, ctx, url).Exec(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (99999, true)`); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{false, true} {
		startEnv(t, url, on)
		s, logs := newStart(t, "serve")
		code, msg := exitCode(s.run(ctx))
		if code != exitStartRefused || !strings.Contains(msg, "migration 99999") || !strings.Contains(msg, fmt.Sprintf("migrations up to %d", latest)) ||
			!strings.Contains(msg, "Going back after an upgrade") {
			t.Errorf("on=%v: exit %d: %s", on, code, msg)
		}
		if strings.Contains(logs.String(), "applied") {
			t.Errorf("on=%v: the start migrated:\n%s", on, logs)
		}
	}
	if code, msg := exitCode(runCmd(t, newUserCmd(), "", "list")); code != exitStartRefused || !strings.Contains(msg, "99999") {
		t.Errorf("user list: exit %d: %s", code, msg)
	}
	if err := runCmd(t, newStatusCmd(), ""); err != nil {
		t.Errorf("status: %v", err)
	}
	if line, err := schemaStatus(ctx, st); err != nil || !strings.Contains(line, "schema: migration 99999, newer than this binary") {
		t.Errorf("status line %q, %v", line, err)
	}
}

// migrateLockClassForTest is store's migration lock class ("mh").
const migrateLockClassForTest = 0x6d68

// TestStartConcurrent (#396): serve, worker and `migrate` started while
// the migration lock is held all wait for it; once it is free, each
// migration is applied once, by exactly one of them.
func TestStartConcurrent(t *testing.T) {
	url := storetest.NewUnmigrated(t)
	startEnv(t, url, true)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	holder := openStore(t, url)
	unlock, err := holder.LockMigrations(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	type started struct {
		logs *lockedBuffer
		done chan error
	}
	var starts []started
	for _, role := range []string{"serve", "worker"} {
		s, logs := newStart(t, role)
		r := started{logs: logs, done: make(chan error, 1)}
		go func() { r.done <- s.run(ctx) }()
		starts = append(starts, r)
	}
	run := startMigrate(t, ctx, unlock)
	conn := connectObserver(t, ctx, url)
	for {
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND classid::bigint = $1 AND objid = 0 AND objsubid = 2 AND NOT granted
			  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, migrateLockClassForTest).Scan(&n); err != nil {
			t.Fatalf("waiting for three waiters: %v", err)
		}
		if n == 3 {
			break
		}
		for _, s := range starts {
			select {
			case err := <-s.done:
				t.Fatalf("a start ended while the lock was held: %v\n%s", err, s.logs)
			default:
			}
		}
		select {
		case err := <-run.done:
			run.collected = true
			t.Fatalf("migrate ended while the lock was held: %v", err)
		default:
		}
	}
	unlock()

	outputs := []string{}
	for _, s := range starts {
		select {
		case err := <-s.done:
			if err != nil {
				t.Fatalf("start: %v\n%s", err, s.logs)
			}
		case <-ctx.Done():
			t.Fatalf("a start did not end: %v", ctx.Err())
		}
		if !strings.Contains(s.logs.String(), "waiting for the migration lock") {
			t.Errorf("a start did not say that it waits:\n%s", s.logs)
		}
		outputs = append(outputs, s.logs.String())
	}
	if err := run.result(t, ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run.out.String(), waitMessage) {
		t.Errorf("migrate did not say that it waits:\n%s", run.out.String())
	}
	outputs = append(outputs, run.out.String())

	appliers := 0
	for _, out := range outputs {
		if strings.Contains(out, "applied 00001_initial.sql") {
			appliers++
		}
	}
	if appliers != 1 {
		t.Errorf("%d runners applied the migrations, want 1:\n%s", appliers, strings.Join(outputs, "\n----\n"))
	}
	var dup, versions int
	if err := conn.QueryRow(ctx, `SELECT count(*) FILTER (WHERE n > 1), count(*) FROM
		(SELECT version_id, count(*) AS n FROM goose_db_version WHERE version_id > 0 GROUP BY version_id) v`).Scan(&dup, &versions); err != nil {
		t.Fatal(err)
	}
	if s := schemaOf(t, url); dup != 0 || int64(versions) != s.Latest || !s.Current() {
		t.Errorf("%d versions applied more than once, %d versions, state %+v", dup, versions, s)
	}
}

// failingMigrations are the repository's migrations plus a good one and
// one that fails with an undefined table (42P01), until the test creates
// start_fix_me.
const (
	goodMigration = "-- +goose Up\nCREATE TABLE start_good (id int);\n-- +goose Down\nDROP TABLE start_good;\n"
	badMigration  = "-- +goose Up\nCREATE TABLE start_bad (id int);\nINSERT INTO start_fix_me VALUES (1);\n-- +goose Down\nDROP TABLE start_bad;\n"
)

// tableExists reports whether the table name exists in the database at url.
func tableExists(t *testing.T, ctx context.Context, url, name string) bool {
	t.Helper()
	var ok bool
	if err := connectObserver(t, ctx, url).QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// TestStartMigrationFailure (#396): a migration that fails with a
// deterministic error ends the start with exit code 3 and a message that
// names it and the migration this run kept; it is rolled back and
// recorded. The next start with the same file refuses without running the
// migrations; one with a changed file runs it; `migrate` (applyMigrations)
// runs it again and clears the record.
func TestStartMigrationFailure(t *testing.T) {
	for _, way := range []string{"changed file", "migrate"} {
		t.Run(way, func(t *testing.T) {
			_, url := storetest.NewWithURL(t)
			startEnv(t, url, true)
			ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
			defer cancel()
			files := startMigrations(t, map[string]string{"90001_start_good.sql": goodMigration, "90002_start_bad.sql": badMigration})
			var runs atomic.Int32
			start := func(files fs.FS) (instanceStart, *lockedBuffer) {
				s, logs := newStart(t, "serve", store.WithMigrationsFS(files))
				s.beforeMigrate = func() { runs.Add(1) }
				return s, logs
			}

			s, logs := start(files)
			code, msg := exitCode(s.run(ctx))
			for _, want := range []string{"migration 90002_start_bad.sql failed and was rolled back", "SQLSTATE 42P01",
				"applied in this run and kept: 1 migration (90001_start_good.sql)", "starts do not run it again while the file is unchanged",
				"restore the backup from before the upgrade"} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q: %s", want, msg)
				}
			}
			if code != exitMigrationFailed || runs.Load() != 1 {
				t.Fatalf("exit %d after %d runs: %s", code, runs.Load(), msg)
			}
			if !strings.Contains(logs.String(), `level=ERROR msg="migration failed" migration=90002_start_bad.sql`) {
				t.Errorf("no ERROR line:\n%s", logs)
			}
			if tableExists(t, ctx, url, "start_bad") || !tableExists(t, ctx, url, "start_good") {
				t.Fatal("start_bad was not rolled back, or start_good not kept")
			}
			st := openStore(t, url, store.WithMigrationsFS(files))
			rec, err := st.LastMigrationFailure(ctx)
			if err != nil || rec == nil || rec.Version != 90002 {
				t.Fatalf("record: %+v %v", rec, err)
			}

			// The same file again: refused at once, goose does not run.
			s, logs = start(files)
			code, msg = exitCode(s.run(ctx))
			if code != exitMigrationFailed || runs.Load() != 1 || !strings.Contains(msg, "migration 90002_start_bad.sql failed at") ||
				!strings.Contains(msg, "migrate, which runs it again") {
				t.Fatalf("second start: exit %d after %d runs: %s\n%s", code, runs.Load(), msg, logs)
			}

			switch way {
			case "changed file":
				fixed := startMigrations(t, map[string]string{"90001_start_good.sql": goodMigration,
					"90002_start_bad.sql": "-- +goose Up\nCREATE TABLE start_bad (id int);\n-- +goose Down\nDROP TABLE start_bad;\n"})
				s, logs = start(fixed)
				if err := s.run(ctx); err != nil || runs.Load() != 2 {
					t.Fatalf("start with a changed file: %v after %d runs\n%s", err, runs.Load(), logs)
				}
			case "migrate":
				if _, err := connectObserver(t, ctx, url).Exec(ctx, `CREATE TABLE start_fix_me (id int)`); err != nil {
					t.Fatal(err)
				}
				var out strings.Builder
				if err := applyMigrations(ctx, st, s.cfg, func(line string) { out.WriteString(line + "\n") }); err != nil {
					t.Fatalf("migrate: %v", err)
				}
				if !strings.Contains(out.String(), "applied 90002_start_bad.sql") {
					t.Errorf("migrate said:\n%s", out.String())
				}
			}
			if !tableExists(t, ctx, url, "start_bad") {
				t.Error("start_bad missing after the retry")
			}
			if rec, err := st.LastMigrationFailure(ctx); err != nil || rec != nil {
				t.Errorf("record after the retry: %+v %v", rec, err)
			}
		})
	}
}

// TestStartFailureBeforeInstanceState (#396, revision 1, point 6): a
// migration that fails before instance_state exists is only logged; the
// next start runs it again.
func TestStartFailureBeforeInstanceState(t *testing.T) {
	url := storetest.NewUnmigrated(t)
	startEnv(t, url, true)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	files := startMigrations(t, map[string]string{
		"00038_two_factor_challenge_attempts.sql": "-- +goose Up\nSELECT * FROM start_missing;\n-- +goose Down\nSELECT 1;\n",
	})
	var runs atomic.Int32
	for i := 1; i <= 2; i++ {
		s, logs := newStart(t, "serve", store.WithMigrationsFS(files))
		s.beforeMigrate = func() { runs.Add(1) }
		code, msg := exitCode(s.run(ctx))
		if code != exitMigrationFailed || runs.Load() != int32(i) || !strings.Contains(msg, "migration 00038_two_factor_challenge_attempts.sql failed") ||
			!strings.Contains(msg, "no failure record yet: the next start runs the migration again") {
			t.Fatalf("start %d: exit %d after %d runs: %s\n%s", i, code, runs.Load(), msg, logs)
		}
		if i == 1 && !strings.Contains(msg, "applied in this run and kept: 37 migrations (00001_initial.sql to 00037_blob_encryption.sql)") {
			t.Errorf("start 1: %s", msg)
		}
	}
	if tableExists(t, ctx, url, "instance_state") {
		t.Error("instance_state exists")
	}
}

// lockMigration is a migration that alters start_target, which the test
// creates and locks; with timeout set, it ends with a lock timeout.
func lockMigration(timeout string) string {
	set := ""
	if timeout != "" {
		set = "SET LOCAL lock_timeout = '" + timeout + "';\n"
	}
	return "-- +goose Up\n" + set + "ALTER TABLE start_target ADD COLUMN y int;\n-- +goose Down\nALTER TABLE start_target DROP COLUMN y;\n"
}

// holdTargetLock creates start_target and holds a lock on it that the
// lockMigration's ALTER waits for, until release is called.
func holdTargetLock(t *testing.T, ctx context.Context, url string) (release func()) {
	t.Helper()
	conn := connectObserver(t, ctx, url)
	if _, err := conn.Exec(ctx, `CREATE TABLE start_target (id int)`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE start_target IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() { once.Do(func() { _ = tx.Rollback(context.Background()) }) }
	t.Cleanup(release)
	return release
}

// TestStartLockTimeoutNotRecorded (#396, revision 1, point 2): a migration
// that ends with a lock timeout (55P03) fails the start with exit code 3
// but is not recorded; the next start runs it again and succeeds.
func TestStartLockTimeoutNotRecorded(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	startEnv(t, url, true)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	release := holdTargetLock(t, ctx, url)
	files := startMigrations(t, map[string]string{"90001_start_lock.sql": lockMigration("50ms")})

	s, logs := newStart(t, "serve", store.WithMigrationsFS(files))
	err := s.run(ctx)
	code, msg := exitCode(err)
	var pgErr *pgconn.PgError
	if code != exitMigrationFailed || !errors.As(err, &pgErr) || pgErr.Code != "55P03" || !strings.Contains(msg, "the error may pass") {
		t.Fatalf("exit %d: %v: %s\n%s", code, err, msg, logs)
	}
	st := openStore(t, url, store.WithMigrationsFS(files))
	if rec, err := st.LastMigrationFailure(ctx); err != nil || rec != nil {
		t.Fatalf("a lock timeout was recorded: %+v %v", rec, err)
	}

	release()
	s, logs = newStart(t, "serve", store.WithMigrationsFS(files))
	if err := s.run(ctx); err != nil || !strings.Contains(logs.String(), "applied 90001_start_lock.sql") {
		t.Fatalf("the next start: %v\n%s", err, logs)
	}
}

// TestStartCancelNotRecorded (#396): a start cancelled (SIGTERM) while a
// migration waits for a lock ends with the context's error, not exit code
// 3, and records nothing.
func TestStartCancelNotRecorded(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	startEnv(t, url, true)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	holdTargetLock(t, ctx, url)
	files := startMigrations(t, map[string]string{"90001_start_lock.sql": lockMigration("")})

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	s, logs := newStart(t, "serve", store.WithMigrationsFS(files))
	done := make(chan error, 1)
	go func() { done <- s.run(runCtx) }()
	conn := connectObserver(t, ctx, url)
	for {
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_locks l JOIN pg_class c ON c.oid = l.relation
			WHERE c.relname = 'start_target' AND NOT l.granted`).Scan(&n); err != nil {
			t.Fatalf("waiting for the migration's lock wait: %v", err)
		}
		if n > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the start ended before the migration waited: %v\n%s", err, logs)
		default:
		}
	}
	stop()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatalf("the cancelled start did not end: %v", ctx.Err())
	}
	if code, msg := exitCode(err); code != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("exit %d: %s", code, msg)
	}
	if rec, err := openStore(t, url, store.WithMigrationsFS(files)).LastMigrationFailure(ctx); err != nil || rec != nil {
		t.Fatalf("a cancel was recorded: %+v %v", rec, err)
	}
}

// TestExitStatusCoded: main exits with the code of a codedError and prints
// its message, also when it is wrapped.
func TestExitStatusCoded(t *testing.T) {
	err := fmt.Errorf("serve: %w", &codedError{code: exitStartRefused, msg: "refused"})
	if code, msg := exitStatus(context.Background(), err); code != exitStartRefused || msg != "serve: refused" {
		t.Errorf("exit %d, %q", code, msg)
	}
	if code, _ := exitStatus(context.Background(), &codedError{code: exitMigrationFailed, msg: "failed"}); code != exitMigrationFailed {
		t.Errorf("exit %d", code)
	}
}
