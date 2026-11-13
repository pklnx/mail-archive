package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// migrationsWith returns the repository's migration files plus extra
// (name to content), for a store opened with store.WithMigrationsFS.
func migrationsWith(t *testing.T, extra map[string]string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	real := os.DirFS("migrations")
	names, err := fs.Glob(real, "*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("migrations: %v %v", names, err)
	}
	for _, name := range names {
		data, err := fs.ReadFile(real, name)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = &fstest.MapFile{Data: data}
	}
	for name, data := range extra {
		out[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return out
}

// latestMigration is the number of the newest migration file.
func latestMigration(t *testing.T) int64 {
	t.Helper()
	names, err := fs.Glob(os.DirFS("migrations"), "*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("migrations: %v %v", names, err)
	}
	var v int64
	if _, err := fmt.Sscanf(names[len(names)-1], "%05d_", &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func pgErr(code string) error { return &pgconn.PgError{Code: code, Message: "x"} }

// TestMigrationFailedDeterministic (#396, revision 1, point 2): only errors
// that come back on the same SQL and data count as deterministic; timeouts,
// lock and serialization failures, a full disk, a shutdown or a lost
// connection may pass and are retried.
func TestMigrationFailedDeterministic(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{pgErr("42P01"), true}, // undefined_table
		{pgErr("42601"), true}, // syntax_error
		{pgErr("23505"), true}, // unique_violation
		{pgErr("22012"), true}, // division_by_zero
		{pgErr("0A000"), true}, // feature_not_supported
		{pgErr("P0001"), true}, // raise_exception
		{fmt.Errorf("wrapped: %w", pgErr("23503")), true},
		{pgErr("55P03"), false}, // lock_not_available
		{pgErr("57014"), false}, // query_canceled (statement_timeout)
		{pgErr("40P01"), false}, // deadlock_detected
		{pgErr("40001"), false}, // serialization_failure
		{pgErr("53100"), false}, // disk_full
		{pgErr("53200"), false}, // out_of_memory
		{pgErr("57P01"), false}, // admin_shutdown
		{pgErr("08006"), false}, // connection_failure
		{pgErr("XX000"), false}, // internal_error
		{errors.New("unexpected EOF"), false},
		{context.Canceled, false},
	} {
		f := &store.MigrationFailed{Err: c.err}
		if got := f.Deterministic(); got != c.want {
			t.Errorf("%v: deterministic %v, want %v", c.err, got, c.want)
		}
	}
}

// TestSchemaState (#396, step 4): pending migrations and a database ahead
// of the binary, without writing anything.
func TestSchemaState(t *testing.T) {
	ctx := context.Background()
	latest := latestMigration(t)
	url := storetest.NewUnmigrated(t)
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	conn := connect(t, url)

	// A database goose never touched: everything pending, no table made.
	s, err := st.SchemaState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 0 || s.Latest != latest || int64(len(s.Pending)) != latest || s.Current() || s.Ahead() {
		t.Fatalf("fresh: %+v", s)
	}
	if s.Pending[0].Path != "00001_initial.sql" || s.Pending[0].Version != 1 {
		t.Errorf("first pending: %+v", s.Pending[0])
	}
	var goose bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&goose); err != nil || goose {
		t.Fatalf("SchemaState created goose's table: %v %v", goose, err)
	}

	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if s, err = st.SchemaState(ctx); err != nil || !s.Current() || s.Version != latest {
		t.Fatalf("migrated: %+v %v", s, err)
	}

	// One step back: only the newest is pending.
	down, err := st.MigrateDown(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s, err = st.SchemaState(ctx); err != nil || len(s.Pending) != 1 || s.Pending[0].Path != down || s.Version != latest-1 || s.Ahead() {
		t.Fatalf("after down %s: %+v %v", down, s, err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// A newer binary migrated the database.
	if _, err := conn.Exec(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (99999, true)`); err != nil {
		t.Fatal(err)
	}
	if s, err = st.SchemaState(ctx); err != nil || !s.Ahead() || s.Current() || s.Version != 99999 || len(s.Pending) != 0 {
		t.Fatalf("ahead: %+v %v", s, err)
	}
	is, err := st.InstanceStatus(ctx, "1.6.0")
	if err != nil || is.BinaryVersion != "1.6.0" || is.SchemaVersion != 99999 || is.LatestVersion != latest || len(is.Pending) != 0 || is.LastMigrationFailure != nil {
		t.Fatalf("instance status: %+v %v", is, err)
	}
}

// TestMigrationFailure (#396): a failed migration is rolled back, the
// migrations before it stay and are named; the failure record is keyed on
// the version and the SHA-256 of the file, and a changed file no longer
// matches it.
func TestMigrationFailure(t *testing.T) {
	ctx := context.Background()
	_, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	const bad = "-- +goose Up\nCREATE TABLE fail_bad (id int);\nINSERT INTO fail_missing VALUES (1);\n-- +goose Down\nDROP TABLE fail_bad;\n"
	files := migrationsWith(t, map[string]string{
		"90001_fail_good.sql": "-- +goose Up\nCREATE TABLE fail_good (id int);\n-- +goose Down\nDROP TABLE fail_good;\n",
		"90002_fail_bad.sql":  bad,
	})
	st, err := store.Open(ctx, url, store.WithMigrationsFS(files))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	applied, err := st.Migrate(ctx)
	var failed *store.MigrationFailed
	if !errors.As(err, &failed) {
		t.Fatalf("Migrate: %v", err)
	}
	sum := sha256.Sum256([]byte(bad))
	if failed.Version != 90002 || failed.Path != "90002_fail_bad.sql" || string(failed.SHA256) != string(sum[:]) ||
		len(failed.Applied) != 1 || failed.Applied[0] != "90001_fail_good.sql" || !failed.Deterministic() {
		t.Fatalf("failure: %+v", failed)
	}
	if len(applied) != 1 || applied[0] != "90001_fail_good.sql" {
		t.Errorf("applied %v", applied)
	}
	if !strings.Contains(err.Error(), "90002_fail_bad.sql") || !strings.Contains(err.Error(), "42P01") {
		t.Errorf("error %q", err)
	}
	var tables int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE tablename IN ('fail_good', 'fail_bad')`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("tables after the failure: %d %v", tables, err)
	}
	state, err := st.SchemaState(ctx)
	if err != nil || len(state.Pending) != 1 || state.Pending[0].Version != 90002 {
		t.Fatalf("state: %+v %v", state, err)
	}

	if rec, _, err := st.RecordedFailure(ctx, state); err != nil || rec != nil {
		t.Fatalf("a record before one was written: %+v %v", rec, err)
	}
	before := time.Now()
	if ok, err := st.RecordMigrationFailure(ctx, failed); err != nil || !ok {
		t.Fatalf("record: %v %v", ok, err)
	}
	rec, m, err := st.RecordedFailure(ctx, state)
	if err != nil || rec == nil || m == nil || rec.Version != 90002 || string(rec.SHA256) != string(sum[:]) || m.Path != "90002_fail_bad.sql" ||
		rec.At.Before(before.Add(-time.Minute)) {
		t.Fatalf("recorded: %+v %+v %v", rec, m, err)
	}
	is, err := st.InstanceStatus(ctx, "dev")
	if err != nil || is.LastMigrationFailure == nil || is.LastMigrationFailure.Version != 90002 || len(is.Pending) != 1 {
		t.Fatalf("instance status: %+v %v", is, err)
	}

	// The same version with another file does not match the record.
	changed, err := store.Open(ctx, url, store.WithMigrationsFS(migrationsWith(t, map[string]string{
		"90001_fail_good.sql": "-- +goose Up\nCREATE TABLE fail_good (id int);\n-- +goose Down\nDROP TABLE fail_good;\n",
		"90002_fail_bad.sql":  "-- +goose Up\nCREATE TABLE fail_bad (id int);\n-- +goose Down\nDROP TABLE fail_bad;\n",
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer changed.Close()
	if rec, _, err := changed.RecordedFailure(ctx, state); err != nil || rec != nil {
		t.Fatalf("a changed file matched the record: %+v %v", rec, err)
	}

	if err := st.ClearMigrationFailure(ctx); err != nil {
		t.Fatal(err)
	}
	if rec, err := st.LastMigrationFailure(ctx); err != nil || rec != nil {
		t.Fatalf("after clear: %+v %v", rec, err)
	}
}

// TestMigrationFailureBeforeInstanceState (#396, revision 1, point 6):
// before migration 00039 created instance_state, nothing is read or
// written.
func TestMigrationFailureBeforeInstanceState(t *testing.T) {
	ctx := context.Background()
	url := storetest.NewUnmigrated(t)
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f := &store.MigrationFailed{Version: 5, Path: "00005_x.sql", SHA256: make([]byte, sha256.Size), Err: pgErr("42P01")}
	if ok, err := st.RecordMigrationFailure(ctx, f); err != nil || ok {
		t.Fatalf("record without the table: %v %v", ok, err)
	}
	if rec, err := st.LastMigrationFailure(ctx); err != nil || rec != nil {
		t.Fatalf("read without the table: %+v %v", rec, err)
	}
	if err := st.ClearMigrationFailure(ctx); err != nil {
		t.Fatal(err)
	}
	if at, err := st.LastBackupSince(ctx, time.Now().Add(-time.Hour)); err != nil || at != nil {
		t.Fatalf("last backup without the audit log: %v %v", at, err)
	}
	is, err := st.InstanceStatus(ctx, "dev")
	if err != nil || is.SchemaVersion != 0 || int64(len(is.Pending)) != latestMigration(t) || is.LastMigrationFailure != nil {
		t.Fatalf("instance status: %+v %v", is, err)
	}
}

// downInstanceStateMigration rolls back 00042_metrics_key_scope, 00041_storage_usage, 00040_smtp_credentials and
// 00039_instance_state.
func downInstanceStateMigration(t *testing.T, st *store.Store) {
	t.Helper()
	downSMTPCredentialsMigration(t, st)
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00039_instance_state") {
		t.Fatalf("down: %q %v", m, err)
	}
}

// TestInstanceStateMigration: 00039 marks an instance with users as set up
// and a new one as not; Down removes the table.
func TestInstanceStateMigration(t *testing.T) {
	ctx := context.Background()
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	setUp := func() bool {
		t.Helper()
		var done bool
		if err := conn.QueryRow(ctx, `SELECT setup_completed_at IS NOT NULL FROM instance_state`).Scan(&done); err != nil {
			t.Fatal(err)
		}
		return done
	}
	if setUp() {
		t.Error("a new instance counts as set up")
	}
	downInstanceStateMigration(t, st)
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('instance_state') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("instance_state after Down: %v %v", exists, err)
	}
	if _, err := st.CreateUser(ctx, "alice", "h", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !setUp() {
		t.Error("an instance with users does not count as set up")
	}
	var rows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM instance_state`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("%d rows, %v", rows, err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO instance_state (id) VALUES (false)`); err == nil {
		t.Error("a second row was accepted")
	}
}

// TestLastBackupSince: the newest data.backup entry not older than since.
// The times come from the database's clock, 2 seconds apart.
func TestLastBackupSince(t *testing.T) {
	ctx := context.Background()
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	var now time.Time
	if err := conn.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	if at, err := st.LastBackupSince(ctx, now.Add(-time.Hour)); err != nil || at != nil {
		t.Fatalf("no backup: %v %v", at, err)
	}
	backup := now.Add(-2 * time.Second)
	if _, err := conn.Exec(ctx, `INSERT INTO audit_log (actor, action, at) VALUES ('cli', 'data.backup', $1),
		('cli', 'data.backup', $1::timestamptz - interval '1 second'), ('cli', 'data.export', $2)`, backup, now); err != nil {
		t.Fatal(err)
	}
	if at, err := st.LastBackupSince(ctx, now.Add(-time.Second)); err != nil || at != nil {
		t.Fatalf("a backup older than since: %v %v", at, err)
	}
	at, err := st.LastBackupSince(ctx, now.Add(-4*time.Second))
	if err != nil || at == nil || !at.Equal(backup) {
		t.Fatalf("the newest backup: %v, want %v (%v)", at, backup, err)
	}
}
