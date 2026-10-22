package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestIndexRebuildRefusal(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	t.Setenv("MAIL_ARCHIVE_COMMAND", "./ma")
	ctx := context.Background()

	open := func() error {
		a, err := openApp(ctx)
		if err == nil {
			a.close()
		}
		return err
	}
	run := func(args ...string) error {
		cmd := newMigrateCmd()
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		return cmd.ExecuteContext(ctx)
	}

	if err := open(); err != nil {
		t.Fatalf("a fresh database is refused: %v", err)
	}

	storetest.SimulateAlpineDatabase(t, url)
	err := open()
	if err == nil {
		t.Fatal("an alpine database is not refused")
	}
	for _, want := range []string{"one-time index rebuild", "./ma migrate first", "postgres:18-alpine", "collation version"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	if err := run("down", "--yes"); err == nil || !strings.Contains(err.Error(), "index rebuild") {
		t.Errorf("migrate down: %v", err)
	}
	if err := run("status"); err != nil {
		t.Errorf("migrate status is refused: %v", err)
	}
	if err := open(); err == nil {
		t.Fatal("migrate status rebuilt the indexes")
	}

	if err := run(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := open(); err != nil {
		t.Fatalf("refused after migrate: %v", err)
	}
}

func TestIndexRebuildRefusalAfterCollationChange(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	ctx := context.Background()
	storetest.SimulateAlpineDatabase(t, url)
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := checkIndexRebuild(ctx, st); err != nil {
		t.Fatalf("refused after the rebuild: %v", err)
	}
	c, err := st.Collation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{c.Database}.Sanitize()+" SET mail_archive.collation_reindexed = '0.0'"); err != nil {
		t.Fatal(err)
	}
	err = checkIndexRebuild(ctx, st)
	if err == nil || !strings.Contains(err.Error(), "rebuilt for collation version 0.0") || !strings.Contains(err.Error(), "now uses "+*c.ActualVersion) {
		t.Errorf("refusal after a collation change: %v", err)
	}
}

// migrate rebuilds the indexes before it creates the audit log partitions:
// the indexes of a partition it creates are not rebuilt (their relfilenode
// is still their oid), while those of an older partition are.
func TestMigrateRebuildsBeforeAuditPartitions(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var current, next string
	if err := conn.QueryRow(ctx, `
		SELECT 'audit_log_' || to_char(m, 'YYYY_MM'), 'audit_log_' || to_char(m + interval '1 month', 'YYYY_MM')
		  FROM date_trunc('month', now() AT TIME ZONE 'UTC') AS m`).Scan(&current, &next); err != nil {
		t.Fatal(err)
	}
	// migrate creates this partition again.
	if _, err := conn.Exec(ctx, "DROP TABLE "+pgx.Identifier{next}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	storetest.SimulateAlpineDatabase(t, url)

	cmd := newMigrateCmd()
	cmd.SetArgs(nil)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rebuilt := func(table string) (n, rebuilt int) {
		t.Helper()
		if err := conn.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE c.relfilenode <> c.oid)
			  FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			 WHERE i.indrelid = $1::regclass`, table).Scan(&n, &rebuilt); err != nil {
			t.Fatal(err)
		}
		return n, rebuilt
	}
	if n, r := rebuilt(current); n == 0 || r != n {
		t.Errorf("%s: %d of %d indexes rebuilt", current, r, n)
	}
	if n, r := rebuilt(next); n == 0 || r != 0 {
		t.Errorf("%s, created by migrate: %d of %d indexes rebuilt, so the partition existed before the rebuild", next, r, n)
	}
}
