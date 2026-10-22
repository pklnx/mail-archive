package store_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func strp(s string) *string { return &s }

func TestNeedsIndexRebuild(t *testing.T) {
	v := strp("2.41")
	cases := []struct {
		name string
		c    store.CollationState
		want bool
	}{
		{"alpine volume on glibc, no marker", store.CollationState{Provider: "c", ActualVersion: v}, true},
		{"alpine volume on glibc, marker equal", store.CollationState{Provider: "c", ActualVersion: v, Marker: strp("2.41")}, false},
		{"alpine volume on glibc, marker differs", store.CollationState{Provider: "c", ActualVersion: v, Marker: strp("2.36")}, true},
		{"debian volume", store.CollationState{Provider: "c", StoredVersion: strp("2.41"), ActualVersion: v}, false},
		{"debian volume, older glibc version stored", store.CollationState{Provider: "c", StoredVersion: strp("2.36"), ActualVersion: v}, false},
		{"debian volume with marker", store.CollationState{Provider: "c", StoredVersion: strp("2.41"), ActualVersion: v, Marker: strp("2.36")}, false},
		{"still on the alpine image", store.CollationState{Provider: "c"}, false},
		{"still on the alpine image, marker", store.CollationState{Provider: "c", Marker: strp("2.41")}, false},
		{"C locale", store.CollationState{Provider: "c", StoredVersion: nil, ActualVersion: nil}, false},
		{"icu", store.CollationState{Provider: "i", ActualVersion: v}, false},
		{"builtin", store.CollationState{Provider: "b", ActualVersion: v}, false},
	}
	for _, tc := range cases {
		if got := tc.c.NeedsIndexRebuild(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCollationReason(t *testing.T) {
	c := store.CollationState{Provider: "c", ActualVersion: strp("2.41")}
	if r := c.Reason(); !strings.Contains(r, "postgres:18-alpine") || !strings.Contains(r, "2.41") {
		t.Errorf("no marker: %q", r)
	}
	c.Marker = strp("2.36")
	if r := c.Reason(); !strings.Contains(r, "2.36") || !strings.Contains(r, "2.41") {
		t.Errorf("marker differs: %q", r)
	}
}

func TestRebuildError(t *testing.T) {
	c := store.CollationState{Database: "mailarchive", User: "mailarchive", ActualVersion: strp("2.41")}
	dup := &pgconn.PgError{Code: "23505", Message: `could not create unique index "accounts_name_key"`, Detail: "Key (name)=(a) is duplicated."}
	msg := store.RebuildError(c, dup).Error()
	for _, want := range []string{"nothing was migrated", "accounts_name_key", "Key (name)=(a) is duplicated.", "Restore"} {
		if !strings.Contains(msg, want) {
			t.Errorf("duplicate key: %q lacks %q", msg, want)
		}
	}
	msg = store.RebuildError(c, &pgconn.PgError{Code: "42501", Message: "must be owner of database mailarchive"}).Error()
	for _, want := range []string{"nothing was migrated", "must be owner", "as a superuser",
		`docker compose exec -T postgres psql -U mailarchive -d mailarchive -c "REINDEX DATABASE mailarchive"`,
		`docker compose exec -T postgres psql -U mailarchive -d mailarchive -c "ALTER DATABASE mailarchive SET mail_archive.collation_reindexed = '2.41'"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("rights: %q lacks %q", msg, want)
		}
	}
	if i, j := strings.Index(msg, "REINDEX DATABASE"), strings.Index(msg, "ALTER DATABASE"); i < 0 || j < i {
		t.Errorf("the marker command does not follow the REINDEX: %q", msg)
	}
	if msg := store.RebuildError(c, errors.New("boom")).Error(); !strings.Contains(msg, "REINDEX DATABASE") {
		t.Errorf("other: %q", msg)
	}
}

func TestMarkerError(t *testing.T) {
	c := store.CollationState{Database: "mailarchive", User: "mailarchive", ActualVersion: strp("2.41")}
	msg := store.MarkerError(c, &pgconn.PgError{Code: "42501", Message: `permission denied to set parameter "mail_archive.collation_reindexed"`}).Error()
	for _, want := range []string{"index rebuild is done", "recording it failed", "nothing was migrated", "permission denied", "as a superuser",
		`docker compose exec -T postgres psql -U mailarchive -d mailarchive -c "ALTER DATABASE mailarchive SET mail_archive.collation_reindexed = '2.41'"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "REINDEX") || strings.Contains(msg, "rebuild failed") {
		t.Errorf("the rebuild is done, but the message asks for it again: %q", msg)
	}
}

func TestManualCommandQuoting(t *testing.T) {
	c := store.CollationState{Database: `Mail"$x`, User: "ma", ActualVersion: strp("2'41")}
	got := c.MarkerCommand()
	want := `docker compose exec -T postgres psql -U ma -d Mail\"\$x -c "ALTER DATABASE \"Mail\"\"\$x\" SET mail_archive.collation_reindexed = '2''41'"`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// indexFiles returns the relfilenode of every index in the public schema.
func indexFiles(t *testing.T, conn *pgx.Conn) map[string]uint32 {
	t.Helper()
	rows, err := conn.Query(context.Background(), `
		SELECT c.relname, c.relfilenode::bigint FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind = 'i' AND c.relfilenode <> 0`)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]uint32{}
	for rows.Next() {
		var name string
		var node int64
		if err := rows.Scan(&name, &node); err != nil {
			t.Fatal(err)
		}
		out[name] = uint32(node)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// textIndex is the unique index on user names, a text index whose order
// differs between musl and glibc.
func textIndex(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var name string
	err := conn.QueryRow(context.Background(), `
		SELECT i.indexrelid::regclass::text FROM pg_index i
		  JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[0]
		 WHERE i.indrelid = 'users'::regclass AND i.indisunique AND i.indnatts = 1
		   AND a.attname = 'name' AND a.atttypid = 'text'::regtype
		 LIMIT 1`).Scan(&name)
	if err != nil {
		t.Fatalf("find the unique index on users.name: %v", err)
	}
	return name
}

func TestMigrateRebuildsIndexesOfAlpineDatabase(t *testing.T) {
	st, url := storetest.NewWithURL(t) // migrated: the schema is up to date
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	idx := textIndex(t, conn)

	c, err := st.Collation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.NeedsIndexRebuild() {
		t.Fatalf("a fresh database needs a rebuild: %+v", c)
	}

	storetest.SimulateAlpineDatabase(t, url)
	c, err = st.Collation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !c.NeedsIndexRebuild() || c.Marker != nil {
		t.Fatalf("an alpine database needs no rebuild: %+v", c)
	}

	// The schema is up to date, as on an installation that already ran
	// ./ma migrate without the rebuild: Migrate rebuilds all the same.
	before := indexFiles(t, conn)
	applied, err := st.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Errorf("applied %v", applied)
	}
	after := indexFiles(t, conn)
	if before[idx] == after[idx] {
		t.Fatalf("Migrate did not rebuild %s", idx)
	}
	for name, node := range before {
		if after[name] == node {
			t.Errorf("Migrate did not rebuild %s", name)
		}
	}
	c, err = st.Collation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Marker == nil || *c.Marker != *c.ActualVersion || c.NeedsIndexRebuild() {
		t.Fatalf("no marker after the rebuild: %+v", c)
	}

	// The second run does not rebuild again.
	before = after
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if after := indexFiles(t, conn); after[idx] != before[idx] {
		t.Errorf("the second Migrate rebuilt %s again", idx)
	}

	// A marker for another collation version (a later glibc change)
	// triggers it again.
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{c.Database}.Sanitize()+" SET mail_archive.collation_reindexed = '0.0'"); err != nil {
		t.Fatal(err)
	}
	c, err = st.Collation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Marker == nil || *c.Marker != "0.0" || !c.NeedsIndexRebuild() {
		t.Fatalf("a changed marker needs no rebuild: %+v", c)
	}
	notified := false
	rebuilt, err := st.RebuildIndexesIfNeeded(ctx, func(store.CollationState) { notified = true })
	if err != nil {
		t.Fatal(err)
	}
	if !rebuilt || !notified {
		t.Errorf("rebuilt %v, notified %v", rebuilt, notified)
	}
	if after := indexFiles(t, conn); after[idx] == before[idx] {
		t.Errorf("a changed marker did not rebuild %s", idx)
	}
	if c, err = st.Collation(ctx); err != nil || c.NeedsIndexRebuild() {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestMigrateRebuildsBeforePendingMigrations(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	// The latest migration is pending, as on an upgrade.
	if _, err := st.MigrateDown(ctx); err != nil {
		t.Fatal(err)
	}
	storetest.SimulateAlpineDatabase(t, url)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	idx := textIndex(t, conn)
	before := indexFiles(t, conn)
	applied, err := st.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Errorf("applied %v", applied)
	}
	if after := indexFiles(t, conn); after[idx] == before[idx] {
		t.Errorf("Migrate did not rebuild %s", idx)
	}
	if c, err := st.Collation(ctx); err != nil || c.NeedsIndexRebuild() {
		t.Fatalf("%+v %v", c, err)
	}
}

// A database whose owner is no superuser: the REINDEX works, setting the
// marker does not, and the error says how to record it.
func TestRebuildMarkerNeedsSuperuser(t *testing.T) {
	base := os.Getenv(storetest.EnvURL)
	if base == "" {
		t.Skip(storetest.EnvURL + " not set; skipping database test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	var super bool
	if err := admin.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil {
		t.Fatal(err)
	}
	if !super {
		t.Skip("the test role is no superuser: cannot create a role and clear datcollversion")
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "mailarchive_owner_" + hex.EncodeToString(suffix)
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE ROLE "+ident+" LOGIN NOSUPERUSER PASSWORD 'owner'"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+ident) })
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident+" OWNER "+ident); err != nil {
		t.Fatalf("create database: %v", err)
	}
	// Registered after the role, so it runs before the role is dropped.
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)") })

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	u.User = url.UserPassword(name, "owner")
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate as owner: %v", err)
	}
	storetest.SimulateAlpineDatabase(t, u.String())

	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	idx := textIndex(t, conn)
	before := indexFiles(t, conn)
	rebuilt, err := st.RebuildIndexesIfNeeded(ctx, nil)
	if err == nil {
		t.Fatal("a database owner without superuser set the marker")
	}
	if !rebuilt {
		t.Fatalf("the REINDEX failed: %v", err)
	}
	if after := indexFiles(t, conn); after[idx] == before[idx] {
		t.Errorf("%s was not rebuilt", idx)
	}
	for _, want := range []string{"index rebuild is done", "recording it failed", "as a superuser", "ALTER DATABASE " + name + " SET mail_archive.collation_reindexed = '"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q lacks %q", err, want)
		}
	}
	if _, err := st.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "recording it failed") {
		t.Errorf("Migrate: %v", err)
	}

	// The command from the message, run as a superuser, unlocks it.
	c, err := st.Collation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "ALTER DATABASE "+ident+" SET mail_archive.collation_reindexed = '"+*c.ActualVersion+"'"); err != nil {
		t.Fatal(err)
	}
	if c, err := st.Collation(ctx); err != nil || c.NeedsIndexRebuild() {
		t.Fatalf("still needs a rebuild after the manual marker: %+v %v", c, err)
	}
}
