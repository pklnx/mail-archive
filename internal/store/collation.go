package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// collationMarker is the per-database setting that records the collation
// version the indexes were last rebuilt for. It is set with ALTER DATABASE
// and read from pg_db_role_setting, so it needs no schema change.
const collationMarker = "mail_archive.collation_reindexed"

// CollationState describes the current database's collation and whether
// its indexes were rebuilt for it.
//
// A database created by the postgres:18-alpine image (musl) records no
// collation version, because musl reports none. Served by the Debian-based
// image (glibc), it reports one, and glibc sorts text differently from musl:
// indexes on text columns built by the old image are in the wrong order
// until they are rebuilt, and PostgreSQL does not warn about it.
type CollationState struct {
	Database string
	User     string
	// Provider is datlocprovider: "c" (libc), "i" (ICU) or "b" (builtin).
	Provider string
	// StoredVersion is datcollversion, nil when the database recorded none.
	StoredVersion *string
	// ActualVersion is the collation version the server reports now, nil
	// when it reports none (musl, or the C and POSIX locales).
	ActualVersion *string
	// Marker is the collation version the indexes were last rebuilt for by
	// RebuildIndexesIfNeeded, nil when they never were.
	Marker *string
}

// NeedsIndexRebuild reports whether the database was created without a
// collation version (musl) but is now served with one (glibc), and its
// indexes were not yet rebuilt for that version.
func (c CollationState) NeedsIndexRebuild() bool {
	if c.Provider != "c" || c.StoredVersion != nil || c.ActualVersion == nil {
		return false
	}
	return c.Marker == nil || *c.Marker != *c.ActualVersion
}

// Reason explains why the indexes need a rebuild, for messages.
func (c CollationState) Reason() string {
	actual := ""
	if c.ActualVersion != nil {
		actual = *c.ActualVersion
	}
	if c.Marker == nil {
		return fmt.Sprintf("the database was created by the postgres:18-alpine image, which sorts text differently, and its indexes were never rebuilt for collation version %s", actual)
	}
	return fmt.Sprintf("the indexes were rebuilt for collation version %s, but the database server now uses %s", *c.Marker, actual)
}

// ManualCommand is how to rebuild the indexes and record it by hand with
// Docker Compose: the REINDEX, then the marker. Setting the marker needs a
// superuser (the bundle's database user is one).
func (c CollationState) ManualCommand() string {
	return "as a superuser, run both:\n  " + c.psql("REINDEX DATABASE "+sqlIdent(c.Database)) +
		"\n  " + c.MarkerCommand()
}

// MarkerCommand is how to record by hand, as a superuser, that the indexes
// were rebuilt for the actual collation version.
func (c CollationState) MarkerCommand() string {
	actual := ""
	if c.ActualVersion != nil {
		actual = *c.ActualVersion
	}
	return c.psql(fmt.Sprintf("ALTER DATABASE %s SET %s = '%s'",
		sqlIdent(c.Database), collationMarker, strings.ReplaceAll(actual, "'", "''")))
}

func (c CollationState) psql(sql string) string {
	return fmt.Sprintf(`docker compose exec -T postgres psql -U %s -d %s -c "%s"`,
		shellDQ(c.User), shellDQ(c.Database), shellDQ(sql))
}

var plainIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// sqlIdent quotes a name for SQL only when it needs it.
func sqlIdent(name string) string {
	if plainIdent.MatchString(name) {
		return name
	}
	return pgx.Identifier{name}.Sanitize()
}

// shellDQ escapes s for use inside double quotes in a POSIX shell.
func shellDQ(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(s)
}

// Collation reads the current database's collation state.
func (s *Store) Collation(ctx context.Context) (CollationState, error) {
	var c CollationState
	err := s.pool.QueryRow(ctx, `
		SELECT d.datname, current_user, d.datlocprovider::text, d.datcollversion,
		       pg_database_collation_actual_version(d.oid),
		       (SELECT substr(cfg, length($1) + 2)
		          FROM pg_db_role_setting r, unnest(r.setconfig) AS cfg
		         WHERE r.setdatabase = d.oid AND r.setrole = 0
		           AND starts_with(cfg, $1 || '=')
		         LIMIT 1)
		  FROM pg_database d
		 WHERE d.datname = current_database()`, collationMarker).
		Scan(&c.Database, &c.User, &c.Provider, &c.StoredVersion, &c.ActualVersion, &c.Marker)
	if err != nil {
		return c, fmt.Errorf("read the database collation: %w", err)
	}
	return c, nil
}

// RebuildIndexesIfNeeded runs REINDEX DATABASE when NeedsIndexRebuild holds
// and then records the collation version in the marker. notify, if not nil,
// is called before the rebuild starts. It reports whether it rebuilt.
func (s *Store) RebuildIndexesIfNeeded(ctx context.Context, notify func(CollationState)) (bool, error) {
	c, err := s.Collation(ctx)
	if err != nil {
		return false, err
	}
	if !c.NeedsIndexRebuild() {
		return false, nil
	}
	if notify != nil {
		notify(c)
	}
	// A single statement without arguments runs outside a transaction
	// block, which REINDEX DATABASE requires.
	if _, err := s.pool.Exec(ctx, "REINDEX DATABASE "+pgx.Identifier{c.Database}.Sanitize()); err != nil {
		return false, rebuildError(c, err)
	}
	// format() quotes the name and the version on the server.
	var stmt string
	if err := s.pool.QueryRow(ctx, `SELECT format('ALTER DATABASE %I SET `+collationMarker+` = %L', current_database(), $1::text)`,
		*c.ActualVersion).Scan(&stmt); err != nil {
		return true, markerError(c, err)
	}
	if _, err := s.pool.Exec(ctx, stmt); err != nil {
		return true, markerError(c, err)
	}
	return true, nil
}

// markerError is the error when the indexes were rebuilt but the marker
// could not be set, typically because the database user is no superuser.
// Until it is set, the other commands keep refusing.
func markerError(c CollationState, err error) error {
	return fmt.Errorf("the index rebuild is done, but recording it failed, so nothing was migrated: %w.\n"+
		"Until it is recorded, the other commands refuse to run. Record it as a superuser, then run migrate again:\n  %s",
		err, c.MarkerCommand())
}

func rebuildError(c CollationState, err error) error {
	var b strings.Builder
	b.WriteString("index rebuild failed, nothing was migrated: ")
	b.WriteString(err.Error())
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		if pgErr.Detail != "" {
			fmt.Fprintf(&b, " (%s)", pgErr.Detail)
		}
		b.WriteString(".\nTwo rows collide under the new sort order: something wrote to the database before the rebuild. " +
			"Restore the backup from before the upgrade, see \"Restore\" in docs/guide/operations.md")
	} else {
		fmt.Fprintf(&b, ".\nTo rebuild the indexes and record it by hand, %s", c.ManualCommand())
	}
	return errors.New(b.String())
}
