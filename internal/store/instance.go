package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// MigrationFailed is the error of Migrate when a migration failed (#396).
// goose rolled that migration back; the migrations before it in the same
// run stay applied.
type MigrationFailed struct {
	// Version and Path name the migration that failed, Path as a file
	// name such as 00039_instance_state.sql.
	Version int64
	Path    string
	// SHA256 is the hash of the migration file, the key of the failure
	// record (RecordMigrationFailure). It is nil if the file could not
	// be read again.
	SHA256 []byte
	// Applied are the migrations this run applied before it.
	Applied []string
	// Err is the error the migration failed with.
	Err error
}

func (e *MigrationFailed) Error() string {
	return fmt.Sprintf("migration %s failed: %v", e.Path, e.Err)
}

func (e *MigrationFailed) Unwrap() error { return e.Err }

// deterministicClasses are the SQLSTATE classes of errors that come back
// when the same migration runs again on the same data: data exceptions
// (22), integrity violations (23), syntax errors and access rule
// violations (42), features not supported (0A) and errors a migration
// raises itself (P0). Everything else may pass on its own and is retried:
// a lost connection, a cancel or statement timeout (57), a lock timeout
// (55), a deadlock or serialization failure (40), a full disk or memory
// (53), an internal error.
var deterministicClasses = []string{"22", "23", "42", "0A", "P0"}

// Deterministic reports whether the migration failed with a database error
// that comes back when it runs again unchanged on the same data
// (deterministicClasses).
func (e *MigrationFailed) Deterministic() bool {
	var pgErr *pgconn.PgError
	if !errors.As(e.Err, &pgErr) || len(pgErr.Code) != 5 {
		return false
	}
	return slices.Contains(deterministicClasses, pgErr.Code[:2])
}

// migrationFailed is the MigrationFailed of goose's partial error.
func (s *Store) migrationFailed(partial *goose.PartialError, applied []*goose.MigrationResult) error {
	e := &MigrationFailed{Err: partial.Err}
	if src := partial.Failed.Source; src != nil {
		e.Version, e.Path = src.Version, src.Path
		e.SHA256, _ = s.migrationSHA256(src.Path)
	}
	for _, r := range applied {
		e.Applied = append(e.Applied, r.Source.Path)
	}
	return e
}

// migrationSHA256 hashes the migration file at path.
func (s *Store) migrationSHA256(path string) ([]byte, error) {
	data, err := fs.ReadFile(s.migrations, path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}

// Migration is one migration file this binary has.
type Migration struct {
	Version int64
	Path    string
}

// SchemaState is how the database's schema compares with the migrations
// of this binary.
type SchemaState struct {
	// Version is the highest migration the database has applied, 0 for
	// none.
	Version int64
	// Latest is the highest migration this binary has.
	Latest int64
	// Pending are the migrations of this binary the database has not
	// applied, in order.
	Pending []Migration
}

// Ahead reports whether the database has a migration this binary does not
// know: a newer version migrated it.
func (s SchemaState) Ahead() bool { return s.Version > s.Latest }

// Current reports whether the schema is exactly what this binary needs.
func (s SchemaState) Current() bool { return len(s.Pending) == 0 && !s.Ahead() }

// PendingPaths are the file names of the pending migrations.
func (s SchemaState) PendingPaths() []string {
	out := make([]string, 0, len(s.Pending))
	for _, m := range s.Pending {
		out = append(out, m.Path)
	}
	return out
}

// SchemaState compares the database with the migrations of this binary.
// It writes nothing (a database goose never touched has no version table
// and counts as empty) and does not wait for a running migration: it sees
// the migrations committed so far. A migration counts as applied when
// goose's table has a row for it, as goose itself counts.
func (s *Store) SchemaState(ctx context.Context) (SchemaState, error) {
	var state SchemaState
	var sources []*goose.Source
	// goose is asked only for the files: its pool opens no connection.
	if err := s.withMigrations(ctx, false, func(p *goose.Provider) error {
		sources = p.ListSources()
		return nil
	}); err != nil {
		return state, err
	}
	applied := map[int64]bool{}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&exists); err != nil {
		return state, err
	}
	if exists {
		rows, err := s.pool.Query(ctx, `SELECT DISTINCT version_id FROM goose_db_version WHERE version_id > 0`)
		if err != nil {
			return state, err
		}
		versions, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return state, err
		}
		for _, v := range versions {
			applied[v] = true
			state.Version = max(state.Version, v)
		}
	}
	for _, src := range sources {
		state.Latest = max(state.Latest, src.Version)
		if !applied[src.Version] {
			state.Pending = append(state.Pending, Migration{Version: src.Version, Path: src.Path})
		}
	}
	return state, nil
}

// MigrationFailure is the record of a migration that failed at start with
// a deterministic error (MigrationFailed.Deterministic).
type MigrationFailure struct {
	Version int64
	// SHA256 is the hash of the migration file that failed.
	SHA256 []byte
	At     time.Time
}

// instanceStateExists reports whether migration 00039 created
// instance_state. Before it, nothing is read from it or written to it.
func (s *Store) instanceStateExists(ctx context.Context) (bool, error) {
	return s.q.InstanceStateExists(ctx)
}

// LastMigrationFailure returns the failure record, nil when there is none
// or instance_state does not exist yet.
func (s *Store) LastMigrationFailure(ctx context.Context) (*MigrationFailure, error) {
	if ok, err := s.instanceStateExists(ctx); err != nil || !ok {
		return nil, err
	}
	row, err := s.q.GetMigrationFailure(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.MigrationFailedVersion == nil || row.MigrationFailedAt == nil {
		return nil, nil
	}
	return &MigrationFailure{Version: *row.MigrationFailedVersion, SHA256: row.MigrationFailedSha256, At: *row.MigrationFailedAt}, nil
}

// RecordMigrationFailure records that the migration of f failed with a
// deterministic error. It runs in a transaction of its own, after goose
// rolled the migration back. recorded is false when instance_state does
// not exist yet, or f has no hash: then nothing is written, and the next
// start runs the migration again.
func (s *Store) RecordMigrationFailure(ctx context.Context, f *MigrationFailed) (recorded bool, err error) {
	if len(f.SHA256) != sha256.Size || f.Version <= 0 {
		return false, nil
	}
	if ok, err := s.instanceStateExists(ctx); err != nil || !ok {
		return false, err
	}
	n, err := s.q.SetMigrationFailure(ctx, db.SetMigrationFailureParams{Version: f.Version, Sha256: f.SHA256})
	return n > 0, err
}

// ClearMigrationFailure removes the failure record, if there is one.
func (s *Store) ClearMigrationFailure(ctx context.Context) error {
	if ok, err := s.instanceStateExists(ctx); err != nil || !ok {
		return err
	}
	_, err := s.q.ClearMigrationFailure(ctx)
	return err
}

// RecordedFailure returns the failure record if it is for a migration of
// state that is still pending and whose file is unchanged: that migration
// would fail again in the same way. It returns nil otherwise.
func (s *Store) RecordedFailure(ctx context.Context, state SchemaState) (*MigrationFailure, *Migration, error) {
	rec, err := s.LastMigrationFailure(ctx)
	if err != nil || rec == nil {
		return nil, nil, err
	}
	for _, m := range state.Pending {
		if m.Version != rec.Version {
			continue
		}
		sum, err := s.migrationSHA256(m.Path)
		if err != nil {
			return nil, nil, err
		}
		if string(sum) != string(rec.SHA256) {
			return nil, nil, nil
		}
		return rec, &m, nil
	}
	return nil, nil, nil
}

// LastBackupSince returns the time of the newest backup in the audit log
// (data.backup) not older than since, nil when there is none or the
// database has no audit log yet. It reads only the entries since then.
func (s *Store) LastBackupSince(ctx context.Context, since time.Time) (*time.Time, error) {
	at, err := s.q.LastBackupSince(ctx, since)
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case errors.As(err, &pgErr) && pgErr.Code == "42P01": // undefined_table: before the migrations
		return nil, nil
	case err != nil:
		return nil, err
	}
	return &at, nil
}

// InstanceStatus is the state of the instance for operators (#396, #397):
// numbers, enums and times only, never an error text.
type InstanceStatus struct {
	// BinaryVersion is the version of the running binary.
	BinaryVersion string
	// SchemaVersion is the highest migration the database has applied,
	// LatestVersion the highest this binary has.
	SchemaVersion int64
	LatestVersion int64
	// Pending are the file names of the migrations not applied yet.
	Pending []string
	// LastMigrationFailure is the failure record, nil if there is none.
	LastMigrationFailure *MigrationFailure
}

// InstanceStatus returns the instance's state for the binary of version
// binaryVersion.
func (s *Store) InstanceStatus(ctx context.Context, binaryVersion string) (InstanceStatus, error) {
	st := InstanceStatus{BinaryVersion: binaryVersion}
	schema, err := s.SchemaState(ctx)
	if err != nil {
		return st, err
	}
	st.SchemaVersion, st.LatestVersion, st.Pending = schema.Version, schema.Latest, schema.PendingPaths()
	if st.LastMigrationFailure, err = s.LastMigrationFailure(ctx); err != nil {
		return st, err
	}
	return st, nil
}
