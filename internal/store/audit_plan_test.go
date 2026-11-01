package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store/db"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// auditPlanBase is where the seeded audit log of the plan tests starts: a
// fixed month, so that the partitions holding rows and the cursor positions
// do not move with the calendar (the tests seeded from now() and failed when
// the cursor, now + 1 month, fell on the first minutes of a partition). It
// is the first of a month: a partition holding only a day of rows (72) is
// seq-scanned by cost, which is cheap but fails checkSharedReads.
var auditPlanBase = time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)

// The Log page reads newest first through the primary key (all users) or
// the user index (own entries) of each partition, without sorting, on a
// log of several thousand rows over three months. Other users' reads of an
// owner's shared accounts (ListSharedReadsForOwner, #389) come from the
// partial index audit_log_via_owners_idx of each partition that holds rows
// below the cursor.
func TestAuditListPlans(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	base := auditPlanBase
	// 6,000 rows 20 minutes apart: base to base + 83 days, three partitions.
	seedAuditPlanLog(t, conn, base, 6000, 20*time.Minute)
	since := base.AddDate(0, -1, 0)
	until := base.AddDate(0, 4, 0)
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
	// The cursors lie in the middle of a month, so each partition holds
	// either none or hundreds of rows below them: in the last partition
	// (every partition has rows below it) and in the middle one (the last
	// partition has none).
	late := base.AddDate(0, 2, 14)
	middle := base.AddDate(0, 1, 14)
	for _, cursor := range []*time.Time{nil, &late, &middle} {
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
			checkSharedReads(t, conn, explain(t, &rec), cursor)
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

// seedAuditPlanLog inserts n audit entries step apart from base on, after
// creating the partitions of their months (the migration creates only this
// month's and the next three). 1 % are other users' reads through user 3's
// share.
func seedAuditPlanLog(t *testing.T, conn *pgx.Conn, base time.Time, n int, step time.Duration) {
	t.Helper()
	ctx := context.Background()
	last := base.Add(time.Duration(n) * step)
	for m := time.Date(base.Year(), base.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(last); m = m.AddDate(0, 1, 0) {
		ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS audit_log_%s PARTITION OF audit_log FOR VALUES FROM ('%s') TO ('%s')",
			m.Format("2006_01"), m.Format(time.RFC3339), m.AddDate(0, 1, 0).Format(time.RFC3339))
		if _, err := conn.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO audit_log (at, actor, user_id, user_name, action, details)
SELECT $1::timestamptz + i * $3::interval,
       'session', i % 10 + 1, 'user' || (i % 10 + 1),
       CASE WHEN i % 3 = 0 THEN 'message.search' ELSE 'message.open' END,
       CASE WHEN i % 100 = 0 THEN '{"via_owners":[3]}' ELSE '{}' END::jsonb
FROM generate_series(1, $2::int) i`, base, n, step); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "ANALYZE audit_log"); err != nil {
		t.Fatal(err)
	}
}

// checkSharedReads requires that plan reads each partition of audit_log
// that holds rows below cursor (nil: any rows) through its child of
// audit_log_via_owners_idx, and no partition with rows in full. A partition
// whose rows all lie at or above the cursor may be read through its pkey
// instead: the cursor is its index condition, so that scan ends after one
// descent. Empty partitions are ignored: EnsureAuditPartitions creates them
// ahead, and PostgreSQL 18 seq-scans them. A Sort is allowed.
func checkSharedReads(t *testing.T, conn *pgx.Conn, plan string, cursor *time.Time) {
	t.Helper()
	ctx := context.Background()
	rows, err := conn.Query(ctx, `
SELECT p.relname::text, i.relname::text, l.below
FROM (SELECT tableoid, bool_or($1::timestamptz IS NULL OR at < $1) AS below FROM audit_log GROUP BY 1) l
JOIN pg_class p ON p.oid = l.tableoid
JOIN pg_index x ON x.indrelid = l.tableoid
JOIN pg_class i ON i.oid = x.indexrelid
JOIN pg_inherits ii ON ii.inhrelid = x.indexrelid AND ii.inhparent = 'audit_log_via_owners_idx'::regclass`, cursor)
	if err != nil {
		t.Fatal(err)
	}
	type part struct {
		partition, index string
		below            bool
	}
	parts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (part, error) {
		var p part
		return p, r.Scan(&p.partition, &p.index, &p.below)
	})
	if err != nil {
		t.Fatal(err)
	}
	below := 0
	for _, p := range parts {
		if p.below {
			below++
			if !strings.Contains(plan, "Bitmap Index Scan on "+p.index) {
				t.Errorf("partition %s is not read through %s:\n%s", p.partition, p.index, plan)
			}
		}
		if strings.Contains(plan, "Seq Scan on "+p.partition+" ") {
			t.Errorf("partition %s is read in full:\n%s", p.partition, plan)
		}
	}
	if below == 0 {
		t.Fatal("no partition with rows below the cursor has the via_owners index")
	}
}

// sharedReadsGeneric: ListSharedReadsForOwner keeps the partial index also as a cached generic
// plan, on a log of 100,000 rows (1 minute apart) of which 1 % are shared reads. With the 6,000 rows of
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
	base := auditPlanBase
	// 100,000 rows 1 minute apart: base to base + 70 days, three partitions.
	seedAuditPlanLog(t, conn, base, 100000, time.Minute)
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
	// A generic plan does not know the cursor: every partition with rows
	// keeps the partial index for any of them.
	// EXPLAIN takes no parameters, hence the literals.
	lit := func(t time.Time) string { return "'" + t.Format(time.RFC3339) + "'" }
	for _, cursor := range []string{"NULL", lit(base.AddDate(0, 2, 5))} {
		rows, err := conn.Query(ctx, fmt.Sprintf("EXPLAIN EXECUTE shared_reads(3, %s, %s, ARRAY['%%'], %s, 1, 101)",
			lit(base.AddDate(0, -1, 0)), lit(base.AddDate(0, 4, 0)), cursor))
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
		checkSharedReads(t, conn, plan, nil)
	}
}
