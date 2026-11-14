package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/keepia/archive/internal/store/db"
	"github.com/keepia/archive/internal/store/storetest"
)

// seedGroupFolders seeds groups for the group lookup tests in SQL: the
// default group with users × accounts × folders folders, a second group
// "other" with the same shape, and unowned accounts without an owner
// (group 0) with one folder each. Every message has one location in a
// folder of each of the three groups, so a hash has few copies: the case
// in which a lookup by hash is cheapest for the planner. It returns the
// default group's id.
func seedGroupFolders(t *testing.T, conn *pgx.Conn, users, accounts, folders, unowned, messages int) int64 {
	t.Helper()
	ctx := context.Background()
	seed := fmt.Sprintf(`
INSERT INTO customer_groups (name) VALUES ('other');
INSERT INTO users (name, password_hash, group_id)
SELECT 'g' || g.id || '-u' || u, 'x', g.id FROM customer_groups g, generate_series(1, %[1]d) u;
INSERT INTO accounts (name, host, port, tls_mode, username, password_enc, owner_id)
SELECT 'acct' || a, 'h', 993, 'tls', 'u', '\x00', u.id FROM users u, generate_series(1, %[2]d) a;
INSERT INTO accounts (name, host, port, tls_mode, username, password_enc, owner_id)
SELECT 'unowned' || a, 'h', 993, 'tls', 'u', '\x00', NULL FROM generate_series(1, %[4]d) a;
INSERT INTO folders (account_id, name, uidvalidity)
SELECT a.id, 'folder' || f, 1 FROM accounts a, generate_series(1, %[3]d) f WHERE a.owner_id IS NOT NULL;
INSERT INTO folders (account_id, name, uidvalidity)
SELECT a.id, 'INBOX', 1 FROM accounts a WHERE a.owner_id IS NULL;
CREATE TEMP TABLE seed_gf AS
SELECT row_number() OVER (PARTITION BY coalesce(u.group_id, 0) ORDER BY f.id) - 1 AS n, coalesce(u.group_id, 0) AS group_id, f.id
FROM folders f JOIN accounts a ON a.id = f.account_id LEFT JOIN users u ON u.id = a.owner_id;
INSERT INTO messages (sha256, size, stored_path)
SELECT encode(sha256(i::text::bytea), 'hex'), 1000, 'x' FROM generate_series(1, %[5]d) i;
INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid)
SELECT encode(sha256(i::text::bytea), 'hex'), gf.id, 1, i
FROM generate_series(1, %[5]d) i
CROSS JOIN (SELECT group_id, count(*) AS c FROM seed_gf GROUP BY group_id) gc
JOIN seed_gf gf ON gf.group_id = gc.group_id AND gf.n = i %% gc.c;
ANALYZE;`, users, accounts, folders, unowned, messages)
	if _, err := conn.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	var group int64
	if err := conn.QueryRow(ctx, `SELECT min(id) FROM customer_groups`).Scan(&group); err != nil {
		t.Fatal(err)
	}
	return group
}

// TestGroupHasLocationsPlan: the group lookup of skipDeleted probes
// message_locations_folder_sha_idx from the group's own folders and never
// looks a hash up in message_locations_sha_idx, which would make its cost
// depend on the copies other groups hold (#539). Both the custom plan and
// the generic plan that pgx's statement cache gets after five runs, on 500
// folders per group, for a group and for group 0.
func TestGroupHasLocationsPlan(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	group := seedGroupFolders(t, conn, 5, 10, 10, 500, 20000)
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM folders f JOIN accounts a ON a.id = f.account_id JOIN users u ON u.id = a.owner_id
		WHERE u.group_id = $1`, group).Scan(&n); err != nil || n < 400 {
		t.Fatalf("folders of the group: %d %v", n, err)
	}
	shas := make([]string, 100)
	for i := range shas {
		shas[i] = seedHash(i*7 + 1)
	}
	explain := func(t *testing.T, sql string, args ...any) string {
		t.Helper()
		rows, err := conn.Query(ctx, "EXPLAIN "+sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(lines, "\n")
	}
	probeOK := func(plan string) bool {
		return strings.Contains(plan, "message_locations_folder_sha_idx") &&
			!strings.Contains(plan, "message_locations_sha_idx") && !strings.Contains(plan, "Seq Scan on message_locations")
	}
	var rec explainer
	if _, err := db.New(&rec).GroupHasLocations(ctx, db.GroupHasLocationsParams{Shas: shas, GroupID: group}); !errors.Is(err, errRecorded) {
		t.Fatalf("record: %v", err)
	}
	if os.Getenv("MAIL_ARCHIVE_PLAN_DEBUG") != "" {
		t.Logf("query:\n%s", rec.sql)
	}
	// The generic plan, as a cached statement gets it: an unnamed
	// statement is always planned with its values, so this uses PREPARE.
	if _, err := conn.Exec(ctx, "PREPARE group_has(bpchar[], bigint) AS "+rec.sql); err != nil {
		t.Fatal(err)
	}
	for _, g := range []struct {
		name string
		id   int64
	}{{"group", group}, {"group 0", 0}} {
		t.Run(g.name, func(t *testing.T) {
			if plan := explain(t, rec.sql, shas, g.id); !probeOK(plan) {
				t.Errorf("custom plan does not probe message_locations_folder_sha_idx only:\n%s", plan)
			}
			if _, err := conn.Exec(ctx, "SET plan_cache_mode = force_generic_plan"); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = conn.Exec(ctx, "RESET plan_cache_mode") }()
			// EXPLAIN EXECUTE takes no parameters: the values as literals.
			plan := explain(t, fmt.Sprintf("EXECUTE group_has(ARRAY['%s']::bpchar[], %d)", strings.Join(shas, "','"), g.id))
			if !strings.Contains(plan, "$1") && !strings.Contains(plan, "$2") {
				t.Errorf("not a generic plan:\n%s", plan)
			}
			if !probeOK(plan) {
				t.Errorf("generic plan does not probe message_locations_folder_sha_idx only:\n%s", plan)
			}
		})
	}
}

// seedHash is the hash seedGroupFolders stores for message i.
func seedHash(i int) string {
	h := sha256.Sum256([]byte(strconv.Itoa(i)))
	return hex.EncodeToString(h[:])
}
