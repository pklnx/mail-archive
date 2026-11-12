package store

import (
	"io/fs"
	"regexp"
	"testing"
	"testing/fstest"
)

// noTransaction matches goose's annotation that runs a migration outside a
// transaction.
var noTransaction = regexp.MustCompile(`(?mi)^\s*--\s*\+goose\s+NO\s+TRANSACTION\b`)

// nonTransactional lists the migration files in fsys that goose would run
// outside a transaction.
func nonTransactional(t *testing.T, fsys fs.FS) []string {
	t.Helper()
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no migrations found")
	}
	var out []string
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatal(err)
		}
		if noTransaction.Match(data) {
			out = append(out, name)
		}
	}
	return out
}

// TestMigrationsAreTransactional (#396): every embedded migration runs in
// a transaction of its own. A failed migration must roll back completely:
// serve and worker apply migrations at start, record a failure and refuse
// to run it again, and the operator restores a backup or fixes the cause
// (docs/guide/operations.md, "When a migration fails"). A migration
// outside a transaction could leave half its changes behind.
func TestMigrationsAreTransactional(t *testing.T) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range nonTransactional(t, sub) {
		t.Errorf("migration %s uses -- +goose NO TRANSACTION: the start-up failure handling (#396) needs every "+
			"migration to roll back completely when it fails; split it or make it transactional", name)
	}

	// The check itself finds the annotation, in any case and spacing.
	fake := fstest.MapFS{
		"00001_a.sql": {Data: []byte("-- +goose Up\nCREATE TABLE a (id int);\n")},
		"00002_b.sql": {Data: []byte("-- +goose NO TRANSACTION\n-- +goose Up\nCREATE INDEX CONCURRENTLY b ON a (id);\n")},
		"00003_c.sql": {Data: []byte("--  +goose no transaction\n-- +goose Up\nSELECT 1;\n")},
	}
	if got := nonTransactional(t, fake); len(got) != 2 || got[0] != "00002_b.sql" || got[1] != "00003_c.sql" {
		t.Errorf("found %v, want 00002_b.sql and 00003_c.sql", got)
	}
}
