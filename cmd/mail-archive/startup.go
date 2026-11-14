package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/store"
)

// Exit codes of a start that stops before it serves (#396). They are per
// command: prepare-data --check uses 3 with a meaning of its own.
const (
	// exitMigrationFailed: a migration failed and was rolled back, or
	// failed before with the same file and is not run again at start.
	// Also `migrate` exits with it.
	exitMigrationFailed = 3
	// exitStartRefused: a start-up check refused, such as migrations
	// pending without KEEPIA_ARCHIVE_MIGRATE_ON_START=on, or a database newer
	// than the binary. A command that uses the database refuses with it
	// too.
	exitStartRefused = 4
)

// codedError ends the process with code; main prints the message.
type codedError struct {
	code int
	msg  string
	err  error
}

func (e *codedError) Error() string { return e.msg }

func (e *codedError) Unwrap() error { return e.err }

// Backups at start: a start that migrates logs the newest backup entry of
// the audit log (data.backup), and warns when none is younger than
// backupWarnAge. It looks back backupLookBack at most.
const (
	backupWarnAge  = 26 * time.Hour
	backupLookBack = 30 * 24 * time.Hour
)

// instanceStart is the start-up of serve or worker (#396, start-up
// sequence) up to the point where the process opens its own store. Its
// steps, numbered as in the plan:
//
//   - step 2: open a short-lived store for migrations and take the
//     migration lock, saying so if it has to wait;
//   - step 3: with KEEPIA_ARCHIVE_MIGRATE_ON_START=on, refuse a migration
//     that failed before with the same file (the failure record), else run
//     the sequence of `migrate` (applyMigrations);
//   - step 4: check the schema: refuse when migrations are pending or the
//     database is newer than the binary;
//   - step 3b, after the schema check: encryption at setup, the underLock
//     hook: on a new instance, turn encryption at rest on, or refuse
//     without an archive key (encryptionAtSetup);
//   - step 5, serve only: check the secret key (checkSecretKey);
//   - step 7, serve only: record or revoke the setup token
//     (recordSetupToken);
//   - step 8: release the lock and close the store.
//
// Steps 3 to 7 run under the lock, so serve, worker and `migrate`
// started at the same time apply each migration once, and a process that
// starts while another one migrates waits for it and then finds nothing
// left; encryption at setup and the setup token, too, are done by one
// process. Step 1, listening with the starting handler, is serve's own
// (newServeCmd). Step 6, the start-up checks of encryption at rest, runs
// when the process opens its store (archive.OpenAtRest).
type instanceStart struct {
	cfg  *config.Config
	log  *slog.Logger
	role string // "serve" or "worker", for the log
	// storeOptions are added to the migration store's options; tests
	// inject migration files with them (store.WithMigrationsFS).
	storeOptions []store.Option
	// beforeMigrate is called right before the migrations run; tests
	// count the runs with it.
	beforeMigrate func()
	// underLock is step 3b (#396, part 3: encryption at setup): it runs
	// under the migration lock after the schema check, so on a current
	// schema and before any process of this version starts writing.
	underLock func(ctx context.Context, st *store.Store) error
	// secretKey runs step 5, the check of the secret key (checkSecretKey),
	// after underLock: serve only. The worker seals and opens nothing.
	secretKey bool
	// setupToken runs step 7, the setup token (recordSetupToken), after
	// step 5: serve only.
	setupToken bool
}

// prepareInstance runs the start-up steps of serve and worker before they
// open their store (instanceStart). Both run encryption at setup (step
// 3b); serve also checks the secret key and records the setup token.
func prepareInstance(ctx context.Context, cfg *config.Config, log *slog.Logger, role string) error {
	serve := role == "serve"
	return instanceStart{
		cfg: cfg, log: log, role: role, underLock: encryptionAtSetup(cfg, log),
		secretKey: serve, setupToken: serve,
	}.run(ctx)
}

func (s instanceStart) run(ctx context.Context) error {
	opts := append([]store.Option{store.ForMigrations()}, s.storeOptions...)
	st, err := store.Open(ctx, s.cfg.DatabaseURL, opts...)
	if err != nil {
		return err
	}
	defer st.Close()
	st.SetGooseLockWait(func(h store.GooseLockHolder) {
		s.log.Warn(gooseLockWaitMessage(h))
	})
	unlock, err := st.LockMigrations(ctx, func() {
		s.log.Info("waiting for the migration lock: another start or migrate is running", "role", s.role)
	})
	if err != nil {
		return err
	}
	defer unlock()
	if s.cfg.MigrateOnStart {
		if err := s.migrate(ctx, st); err != nil {
			return err
		}
	}
	state, err := st.SchemaState(ctx)
	if err != nil {
		return err
	}
	if err := schemaError(state, true); err != nil {
		return err
	}
	if s.underLock != nil {
		if err := s.underLock(ctx, st); err != nil {
			return err
		}
	}
	if s.secretKey {
		if err := checkSecretKey(ctx, st, s.cfg, s.log); err != nil {
			return err
		}
	}
	if s.setupToken {
		return recordSetupToken(ctx, st, s.cfg, s.log)
	}
	return nil
}

// migrate is step 3: the failure record, the backup note, then the
// sequence of `migrate`.
func (s instanceStart) migrate(ctx context.Context, st *store.Store) error {
	state, err := st.SchemaState(ctx)
	if err != nil {
		return err
	}
	if state.Ahead() {
		// A newer version migrated the database: step 4 refuses.
		return nil
	}
	if len(state.Pending) > 0 {
		rec, m, err := st.RecordedFailure(ctx, state)
		if err != nil {
			return err
		}
		if rec != nil {
			s.log.Error("migration failed before; not run again at start", "migration", m.Path,
				"failed_at", rec.At.UTC().Format(time.RFC3339))
			return recordedFailureError(m, rec)
		}
		s.log.Info("applying migrations at start", "role", s.role, "pending", pendingSummary(state.Pending))
		if state.Version > 0 {
			s.backupNote(ctx, st)
		}
	}
	if s.beforeMigrate != nil {
		s.beforeMigrate()
	}
	err = applyMigrations(ctx, st, s.cfg, func(line string) { s.log.Info(line) })
	var failed *store.MigrationFailed
	if errors.As(err, &failed) {
		s.log.Error("migration failed", "migration", failed.Path, "rolled_back", true,
			"applied", summarize(failed.Applied), "err", failed.Err)
	}
	return err
}

// backupNote logs the newest backup entry of the audit log before a start
// migrates an existing database, and warns when there is none younger
// than backupWarnAge. It is a note, not a refusal: the entry is written
// before pg_dump succeeds, and volume snapshots leave none.
func (s instanceStart) backupNote(ctx context.Context, st *store.Store) {
	now := time.Now()
	at, err := st.LastBackupSince(ctx, now.Add(-backupLookBack))
	switch {
	case err != nil:
		s.log.Warn("cannot read the last backup from the audit log", "err", err)
	case at == nil:
		s.log.Warn("no backup in the audit log in the last 30 days; the upgrade needs a backup or snapshot from before it",
			"docs", "docs/guide/operations.md#upgrades")
	case now.Sub(*at) > backupWarnAge:
		s.log.Warn("the last backup in the audit log is older than 26 hours; the upgrade needs a backup or snapshot from before it",
			"last_backup", at.UTC().Format(time.RFC3339), "docs", "docs/guide/operations.md#upgrades")
	default:
		s.log.Info("last backup", "at", at.UTC().Format(time.RFC3339))
	}
}

// applyMigrations is the sequence of `migrate`, which serve and worker
// also run at start with KEEPIA_ARCHIVE_MIGRATE_ON_START=on: the one-time
// index rebuild, the audit log partitions, the migrations, then the
// password upgrade. The caller holds the migration lock. report gets the
// lines to show, among them one per migration applied.
//
// A migration that fails is rolled back (store.Migrate) and ends it with
// a codedError of exitMigrationFailed. A deterministic failure is
// recorded (store.RecordMigrationFailure) so that the next start does not
// run the same migration again; a successful run clears the record.
func applyMigrations(ctx context.Context, st *store.Store, cfg *config.Config, report func(string)) error {
	// The index rebuild first, before anything writes; Migrate checks
	// again and finds nothing left to do.
	if _, err := st.RebuildIndexesIfNeeded(ctx, func(c store.CollationState) {
		report(indexRebuildNotice(c))
	}); err != nil {
		return err
	}
	ensureAuditPartitions(ctx, st)
	applied, err := st.Migrate(ctx)
	if err != nil {
		return migrationError(ctx, st, err)
	}
	if err := st.ClearMigrationFailure(ctx); err != nil {
		return err
	}
	if len(applied) == 0 {
		report("database schema is up to date")
	}
	for _, m := range applied {
		report("applied " + m)
	}
	if pending, err := st.HasUnindexed(ctx); err == nil && pending {
		report("some messages are not fully searchable yet: run `reindex` once")
	}
	if sealer, err := cfg.Sealer(); err == nil {
		n, err := archive.UpgradePasswords(ctx, st, sealer)
		if err != nil {
			return err
		}
		if n > 0 {
			report(fmt.Sprintf("stored passwords of %d account(s) bound to the account ID", n))
		}
	}
	return nil
}

// migrationError turns the error of a failed migration into a codedError
// and records a deterministic failure. Any other error, and a failure
// after ctx ended (an interrupted start), is returned as it is and not
// recorded: the next start or migrate runs it again.
func migrationError(ctx context.Context, st *store.Store, err error) error {
	var failed *store.MigrationFailed
	if !errors.As(err, &failed) || ctx.Err() != nil {
		return err
	}
	recorded, recErr := false, error(nil)
	if failed.Deterministic() {
		recorded, recErr = st.RecordMigrationFailure(ctx, failed)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "migration %s failed and was rolled back: %v", failed.Path, failed.Err)
	if len(failed.Applied) > 0 {
		fmt.Fprintf(&b, "\napplied in this run and kept: %s", summarize(failed.Applied))
	} else {
		b.WriteString("\nno migration of this run was applied")
	}
	switch {
	case recErr != nil:
		fmt.Fprintf(&b, "\nthe failure could not be recorded (%v): the next start runs the migration again", recErr)
	case recorded:
		fmt.Fprintf(&b, "\nstarts do not run it again while the file is unchanged; %s migrate does", commandName())
	case failed.Deterministic():
		b.WriteString("\nthe database has no failure record yet: the next start runs the migration again")
	default:
		b.WriteString("\nthe error may pass (a timeout, a lock, the connection): the next start runs the migration again")
	}
	b.WriteString("\n" + restoreOrFix)
	return &codedError{code: exitMigrationFailed, msg: b.String(), err: err}
}

// restoreOrFix ends the message of a failed migration.
const restoreOrFix = `restore the backup from before the upgrade, or fix the cause and start again (docs/guide/operations.md, "When a migration fails")`

// recordedFailureError is the refusal of a start whose next migration
// failed before with the same file.
func recordedFailureError(m *store.Migration, rec *store.MigrationFailure) error {
	return &codedError{code: exitMigrationFailed, msg: fmt.Sprintf(
		"migration %s failed at %s and was rolled back; starts do not run it again while the file is unchanged\n"+
			"fix the cause and run %s migrate, which runs it again, or restore the backup from before the upgrade "+
			`(docs/guide/operations.md, "When a migration fails")`,
		m.Path, rec.At.UTC().Format(time.DateTime+" MST"), commandName())}
}

// schemaError is the refusal for a schema that does not match this
// binary, nil when it does. atStart adds the start-up setting to the hint.
func schemaError(state store.SchemaState, atStart bool) error {
	switch {
	case state.Ahead():
		return &codedError{code: exitStartRefused, msg: fmt.Sprintf(
			"the database schema is at migration %d, newer than this binary (version %s, migrations up to %d): "+
				"run the version that migrated it, or restore the backup from before the upgrade "+
				`(docs/guide/operations.md, "Going back after an upgrade")`,
			state.Version, version, state.Latest)}
	case len(state.Pending) > 0:
		hint := fmt.Sprintf("run %s migrate", commandName())
		if atStart {
			hint += fmt.Sprintf(", or set %s=on", config.EnvMigrateOnStart)
		}
		return &codedError{code: exitStartRefused, msg: fmt.Sprintf(
			"the database schema is not up to date: %s pending; %s", pendingSummary(state.Pending), hint)}
	}
	return nil
}

// pendingSummary names the pending migrations (summarize).
func pendingSummary(pending []store.Migration) string {
	paths := make([]string, 0, len(pending))
	for _, m := range pending {
		paths = append(paths, m.Path)
	}
	return summarize(paths)
}

// summarize counts migration files and names them, only the first and the
// last of a longer list: "3 migrations (00001_a.sql to 00003_c.sql)".
func summarize(paths []string) string {
	switch len(paths) {
	case 0:
		return "no migration"
	case 1:
		return "1 migration (" + paths[0] + ")"
	case 2:
		return "2 migrations (" + paths[0] + ", " + paths[1] + ")"
	}
	return fmt.Sprintf("%d migrations (%s to %s)", len(paths), paths[0], paths[len(paths)-1])
}

// schemaStatus is the schema line of `status`.
func schemaStatus(ctx context.Context, st *store.Store) (string, error) {
	is, err := st.InstanceStatus(ctx, version)
	if err != nil {
		return "", err
	}
	var line string
	switch {
	case is.SchemaVersion > is.LatestVersion:
		line = fmt.Sprintf("schema: migration %d, newer than this binary (migrations up to %d)", is.SchemaVersion, is.LatestVersion)
	case len(is.Pending) > 0:
		line = fmt.Sprintf("schema: migration %d, %d pending: run %s migrate", is.SchemaVersion, len(is.Pending), commandName())
	default:
		line = fmt.Sprintf("schema: migration %d, up to date", is.SchemaVersion)
	}
	if f := is.LastMigrationFailure; f != nil {
		line += fmt.Sprintf("\nschema: migration %d failed at %s; starts do not run it again while its file is unchanged",
			f.Version, f.At.Local().Format(time.DateTime))
	}
	return line, nil
}
