package store_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestFolderCountCost compares the folder count query of the sidebar with
// the one it replaced, on 500,000 locations of one user in 50 folders. It
// seeds for a few seconds, so it runs only with MAIL_ARCHIVE_BENCH=1:
//
//	MAIL_ARCHIVE_BENCH=1 go test -run TestFolderCountCost -v ./internal/store/
func TestFolderCountCost(t *testing.T) {
	if os.Getenv("MAIL_ARCHIVE_BENCH") != "1" {
		t.Skip("set MAIL_ARCHIVE_BENCH=1 to run")
	}
	_, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// 5 accounts with 10 folders each. The first folder had a UIDVALIDITY
	// change: its locations from the old value stay, and the rescan added
	// new ones for 90% of them. About 10% of all messages also sit in a
	// second folder.
	seed := `
INSERT INTO users (name, password_hash) VALUES ('bench', 'x');
INSERT INTO accounts (name, host, port, tls_mode, username, password_enc, owner_id)
SELECT 'acct' || a, 'h', 993, 'tls', 'u', '\x00', (SELECT id FROM users)
FROM generate_series(1, 5) a;
INSERT INTO folders (account_id, name, uidvalidity)
SELECT a.id, 'folder' || f, 1 FROM accounts a, generate_series(1, 10) f;
CREATE TEMP TABLE fs AS SELECT row_number() OVER (ORDER BY id) - 1 AS n, id FROM folders;
UPDATE folders SET uidvalidity = 2 WHERE id = (SELECT id FROM fs WHERE n = 0);
INSERT INTO messages (sha256, size, stored_path)
SELECT encode(sha256(i::text::bytea), 'hex'), 1000, 'x' FROM generate_series(1, 440000) i;
INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid)
SELECT encode(sha256(i::text::bytea), 'hex'), fs.id, 1, i
FROM generate_series(1, 440000) i JOIN fs ON fs.n = i % 50;
INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid)
SELECT encode(sha256(i::text::bytea), 'hex'), (SELECT id FROM fs WHERE n = 0), 2, i
FROM generate_series(1, 440000) i WHERE i % 50 = 0 AND i % 10 <> 1;
INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid)
SELECT encode(sha256(i::text::bytea), 'hex'), fs.id, 1, 1000000 + i
FROM generate_series(1, 440000) i JOIN fs ON fs.n = (i + 7) % 50 WHERE i % 10 = 3;
ANALYZE;`
	start := time.Now()
	if _, err := conn.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	var locations int64
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM message_locations").Scan(&locations); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %d locations in %s", locations, time.Since(start).Round(time.Millisecond))

	// The old query, the same with count(DISTINCT), and the query in use:
	// count(DISTINCT) per folder in a subquery.
	const grouped = `
SELECT a.name, f.name, %s
FROM accounts a
LEFT JOIN folders f ON f.account_id = a.id
LEFT JOIN message_locations l ON l.folder_id = f.id
WHERE a.owner_id = (SELECT id FROM users)
GROUP BY a.name, a.enabled, a.removed_at, f.name, f.last_synced_at`
	const perFolder = `
SELECT a.name, f.name,
       (SELECT count(*) FROM (SELECT DISTINCT l.message_sha256 FROM message_locations l WHERE l.folder_id = f.id) d)
FROM accounts a
LEFT JOIN folders f ON f.account_id = a.id
WHERE a.owner_id = (SELECT id FROM users)`
	exec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	var version string
	if err := conn.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("PostgreSQL %s", version)

	exec("DROP INDEX message_locations_folder_sha_idx; ANALYZE message_locations")
	old := median(t, conn, fmt.Sprintf(grouped, "count(l.id)"))
	report := func(name string, d time.Duration) {
		t.Logf("%-45s %8s  %.2f× the old query", name, d.Round(time.Millisecond), float64(d)/float64(old))
	}
	report("old: count(l.id)", old)
	report("no index: count(DISTINCT), grouped", median(t, conn, fmt.Sprintf(grouped, "count(DISTINCT l.message_sha256)")))
	report("no index: per-folder subquery", median(t, conn, perFolder))
	exec("CREATE INDEX message_locations_folder_sha_idx ON message_locations (folder_id, message_sha256); ANALYZE message_locations")
	report("index: count(DISTINCT), grouped", median(t, conn, fmt.Sprintf(grouped, "count(DISTINCT l.message_sha256)")))
	report("index: per-folder subquery (in use)", median(t, conn, perFolder))
}

// median runs q once to warm up, then 20 times, and returns the median.
func median(t *testing.T, conn *pgx.Conn, q string) time.Duration {
	t.Helper()
	ctx := context.Background()
	run := func() time.Duration {
		start := time.Now()
		rows, err := conn.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	run()
	var times []time.Duration
	for range 20 {
		times = append(times, run())
	}
	slices.Sort(times)
	return times[len(times)/2]
}
