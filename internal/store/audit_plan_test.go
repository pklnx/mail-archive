package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store/db"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// The Log page reads newest first through the primary key (all users) or
// the user index (own entries) of each partition, without sorting, on a
// log of several thousand rows over three months. Other users' reads of an
// owner's shared accounts (ListSharedReadsForOwner, #389) come from the
// partial index audit_log_via_owners_idx of each partition that holds rows.
func TestAuditListPlans(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	seed := `
INSERT INTO audit_log (at, actor, user_id, user_name, action, details)
SELECT date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + i * interval '20 minutes',
       'session', i % 10 + 1, 'user' || (i % 10 + 1),
       CASE WHEN i % 3 = 0 THEN 'message.search' ELSE 'message.open' END,
       -- 1 % of other users' reads went through user 3's share.
       CASE WHEN i % 100 = 0 THEN '{"via_owners":[3]}' ELSE '{}' END::jsonb
FROM generate_series(1, 6000) i;
ANALYZE audit_log;`
	if _, err := conn.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	since := time.Now().AddDate(0, -1, 0)
	until := time.Now().AddDate(0, 4, 0)
	explain := func(t *testing.T, rec *explainer) string {
		t.Helper()
		rows, err := conn.Query(ctx, "EXPLAIN "+rec.sql, rec.args...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(lines, "\n")
	}
	check := func(t *testing.T, plan, index string) {
		t.Helper()
		if !strings.Contains(plan, "Index Scan Backward") || !strings.Contains(plan, index) {
			t.Errorf("plan does not read %s backward:\n%s", index, plan)
		}
		if strings.Contains(plan, "Sort  (") || strings.Contains(plan, "Seq Scan on audit_log") {
			t.Errorf("plan sorts or reads every row:\n%s", plan)
		}
	}
	at := time.Now().AddDate(0, 1, 0)
	for _, cursor := range []*time.Time{nil, &at} {
		t.Run("all", func(t *testing.T) {
			var rec explainer
			_, err := db.New(&rec).ListAuditEntries(ctx, db.ListAuditEntriesParams{
				Viewer: 1, Since: since, Until: until, Patterns: []string{"%"}, CursorAt: cursor, CursorID: 1, Lim: 101,
			})
			if !errors.Is(err, errRecorded) {
				t.Fatalf("record: %v", err)
			}
			check(t, explain(t, &rec), "_pkey")
		})
		// Several users and actors with actions keep the backward pkey
		// scan: the union is a filter, not an index condition.
		t.Run("who", func(t *testing.T) {
			var rec explainer
			_, err := db.New(&rec).ListAuditEntries(ctx, db.ListAuditEntriesParams{
				Viewer: 1, Since: since, Until: until, UserIds: []int64{3, 4}, Actors: []string{"cli", "api_key"},
				Patterns: []string{"message.open", "message.search"}, CursorAt: cursor, CursorID: 1, Lim: 101,
			})
			if !errors.Is(err, errRecorded) {
				t.Fatalf("record: %v", err)
			}
			check(t, explain(t, &rec), "_pkey")
		})
		t.Run("shared reads", func(t *testing.T) {
			var rec explainer
			_, err := db.New(&rec).ListSharedReadsForOwner(ctx, db.ListSharedReadsForOwnerParams{
				OwnerID: 3, Since: since, Until: until, Patterns: []string{"%"}, CursorAt: cursor, CursorID: 1, Lim: 101,
			})
			if !errors.Is(err, errRecorded) {
				t.Fatalf("record: %v", err)
			}
			checkSharedReads(t, conn, explain(t, &rec))
		})
		t.Run("own", func(t *testing.T) {
			var rec explainer
			_, err := db.New(&rec).ListAuditEntriesForUser(ctx, db.ListAuditEntriesForUserParams{
				UserID: 3, Since: since, Until: until, Patterns: []string{"%"}, CursorAt: cursor, CursorID: 1, Lim: 101,
			})
			if !errors.Is(err, errRecorded) {
				t.Fatalf("record: %v", err)
			}
			check(t, explain(t, &rec), "user_id_at_id_idx")
		})
	}
	t.Run("shared reads generic", sharedReadsGeneric)
}

// checkSharedReads requires that plan reads each partition of audit_log
// that holds rows through its child of audit_log_via_owners_idx, and none
// of them in full. Empty partitions are ignored: EnsureAuditPartitions
// creates them ahead, and PostgreSQL 18 seq-scans them. A Sort is allowed.
func checkSharedReads(t *testing.T, conn *pgx.Conn, plan string) {
	t.Helper()
	ctx := context.Background()
	rows, err := conn.Query(ctx, `
SELECT p.relname::text, i.relname::text
FROM (SELECT tableoid FROM audit_log GROUP BY 1) l
JOIN pg_class p ON p.oid = l.tableoid
JOIN pg_index x ON x.indrelid = l.tableoid
JOIN pg_class i ON i.oid = x.indexrelid
JOIN pg_inherits ii ON ii.inhrelid = x.indexrelid AND ii.inhparent = 'audit_log_via_owners_idx'::regclass`)
	if err != nil {
		t.Fatal(err)
	}
	type pair struct{ partition, index string }
	pairs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pair, error) {
		var p pair
		return p, r.Scan(&p.partition, &p.index)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) == 0 {
		t.Fatal("no partition with rows has the via_owners index")
	}
	for _, p := range pairs {
		if !strings.Contains(plan, "Bitmap Index Scan on "+p.index) {
			t.Errorf("partition %s is not read through %s:\n%s", p.partition, p.index, plan)
		}
		if strings.Contains(plan, "Seq Scan on "+p.partition+" ") {
			t.Errorf("partition %s is read in full:\n%s", p.partition, plan)
		}
	}
}

// sharedReadsGeneric: ListSharedReadsForOwner keeps the partial index also as a cached generic
// plan, on a log of 100,000 rows (1 minute apart, so each falls into an
// existing partition) of which 1 % are shared reads. With the 6,000 rows of
// TestAuditListPlans the generic plan reads the pkey instead, which is
// still cheap at that size. Reference times on PostgreSQL 18.6 (plan review
// of #389, not asserted): 100,000 rows, custom 1.8 ms, generic 3.9 ms
// (BitmapAnd of the pkey and the partial index); 1,000,000 rows, 19 ms and
// 57 ms.
func sharedReadsGeneric(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 100,000 audit rows")
	}
	_, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	seed := `
INSERT INTO audit_log (at, actor, user_id, user_name, action, details)
SELECT date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + i * interval '1 minute',
       'session', i % 10 + 1, 'user' || (i % 10 + 1),
       CASE WHEN i % 3 = 0 THEN 'message.search' ELSE 'message.open' END,
       CASE WHEN i % 100 = 0 THEN '{"via_owners":[3]}' ELSE '{}' END::jsonb
FROM generate_series(1, 100000) i;
ANALYZE audit_log;`
	if _, err := conn.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	var rec explainer
	_, err = db.New(&rec).ListSharedReadsForOwner(ctx, db.ListSharedReadsForOwnerParams{})
	if !errors.Is(err, errRecorded) {
		t.Fatalf("record: %v", err)
	}
	if _, err := conn.Exec(ctx, "SET plan_cache_mode = force_generic_plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "PREPARE shared_reads AS "+rec.sql); err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []string{"NULL", "now() + interval '1 month'"} {
		rows, err := conn.Query(ctx, `EXPLAIN EXECUTE shared_reads(3, now() - interval '1 month', now() + interval '4 months', ARRAY['%'], `+cursor+`, 1, 101)`)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		if !strings.Contains(plan, "$1") && !strings.Contains(plan, "$2") {
			t.Errorf("not a generic plan:\n%s", plan)
		}
		checkSharedReads(t, conn, plan)
	}
}
