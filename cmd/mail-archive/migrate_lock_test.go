package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestMigrateWaitsForMigrateLock (#214): `migrate` and `migrate down`
// started while another migrate holds the migration lock say that they
// wait, wait, and finish once it is free.
func TestMigrateWaitsForMigrateLock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		pending int // migrations pending afterwards
	}{
		{"migrate", nil, 0},
		{"down", []string{"down", "--yes"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, url := storetest.NewWithURL(t)
			t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
			key, _ := crypto.GenerateKey()
			t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			observer, err := pgx.Connect(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = observer.Close(context.Background()) })

			unlock, err := st.LockMigrations(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			cmdCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			collected := false
			// The command ends and the lock is released before the store
			// closes, also when the test fails: closing the pool waits for
			// the held connection.
			t.Cleanup(func() {
				stop()
				unlock()
				if !collected {
					<-done
				}
			})
			var out bytes.Buffer
			cmd := newMigrateCmd()
			cmd.SetArgs(tc.args)
			cmd.SetOut(&out)
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			go func() { done <- cmd.ExecuteContext(cmdCtx) }()

			for {
				var n int
				if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_locks
					WHERE locktype = 'advisory' AND classid = $1 AND objid = 0 AND objsubid = 2 AND NOT granted
					  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, 0x6d68).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					break
				}
				select {
				case err := <-done:
					collected = true
					t.Fatalf("ran while the migration lock was held: %v", err)
				default:
				}
			}
			unlock()
			// Bounded, so a command that never ends fails here with a
			// message instead of hanging until the go test timeout.
			select {
			case err = <-done:
			case <-ctx.Done():
				// Stuck for good (cancelling does not end it): the cleanup
				// must not wait for it either. Dropping the database ends
				// its connections.
				collected = true
				t.Fatalf("the command did not end after the unlock: %v", ctx.Err())
			}
			collected = true
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "another migrate is running; waiting for it") {
				t.Errorf("output %q does not say that it waits", out.String())
			}
			statuses, err := st.MigrationStatus(ctx)
			if err != nil {
				t.Fatal(err)
			}
			pending := 0
			for _, s := range statuses {
				if !s.Applied {
					pending++
				}
			}
			if pending != tc.pending {
				t.Errorf("%d migrations pending afterwards, want %d", pending, tc.pending)
			}
		})
	}
}
