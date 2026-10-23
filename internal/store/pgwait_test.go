package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/testwait"
)

// waitForRow returns once query, run on conn, counts at least one row: a
// state that PostgreSQL shows but does not signal, such as a statement
// that waits for a lock. It queries again right away, without a pause,
// until the test's wait limit (testwait.Limit). It fails if done delivers
// first: what the test waits for ended without reaching that state.
func waitForRow(t *testing.T, conn *pgx.Conn, done <-chan error, what, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	for {
		var n int
		if err := conn.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if n > 0 {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("finished before %s: %v", what, err)
		default:
		}
	}
}

// waitForLockWait returns once a statement of the test's database (each
// test has its own) waits for a lock.
func waitForLockWait(t *testing.T, conn *pgx.Conn, done <-chan error) {
	t.Helper()
	waitForRow(t, conn, done, "a statement waiting for a lock", `SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND wait_event_type = 'Lock'`)
}
