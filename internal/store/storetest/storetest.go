// Package storetest provides a throwaway PostgreSQL database for tests.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
)

// EnvURL names the variable holding a PostgreSQL URL with CREATEDB rights.
const EnvURL = "TEST_DATABASE_URL"

// New creates a fresh, migrated database that is dropped when the test ends.
// The test is skipped if TEST_DATABASE_URL is not set.
func New(t *testing.T) *store.Store {
	t.Helper()
	st, _ := NewWithURL(t)
	return st
}

// NewWithURL is like New and also returns the database URL, for tests that
// need to set up state the store API cannot create.
func NewWithURL(t *testing.T) (*store.Store, string) {
	t.Helper()
	base := os.Getenv(EnvURL)
	if base == "" {
		t.Skip(EnvURL + " not set; skipping database test")
	}
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "mailarchive_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create database: %v", err)
	}

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st, u.String()
}

// SimulateAlpineDatabase makes the test database at dbURL look like one
// created by the postgres:18-alpine image (musl), which records no collation
// version, while the server (glibc) reports one. It needs a superuser and a
// server that reports a collation version for the database; the test is
// skipped otherwise.
func SimulateAlpineDatabase(t *testing.T, dbURL string) {
	t.Helper()
	ctx := context.Background()
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	name := u.Path[1:]
	admin, err := pgx.Connect(ctx, os.Getenv(EnvURL))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()
	var super bool
	if err := admin.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil {
		t.Fatal(err)
	}
	if !super {
		t.Skip("the test role is no superuser: cannot clear datcollversion")
	}
	var provider string
	var actual *string
	if err := admin.QueryRow(ctx, `SELECT datlocprovider::text, pg_database_collation_actual_version(oid) FROM pg_database WHERE datname = $1`, name).
		Scan(&provider, &actual); err != nil {
		t.Fatal(err)
	}
	if provider != "c" || actual == nil {
		t.Skipf("the test database uses locale provider %q and reports no collation version; the test needs a glibc locale such as en_US.UTF-8", provider)
	}
	if _, err := admin.Exec(ctx, `UPDATE pg_database SET datcollversion = NULL WHERE datname = $1`, name); err != nil {
		t.Fatalf("clear datcollversion: %v", err)
	}
}
