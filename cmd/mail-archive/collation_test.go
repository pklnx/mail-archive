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
