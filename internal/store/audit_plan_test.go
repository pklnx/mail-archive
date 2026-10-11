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
// log of several thousand rows over three months.
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
       CASE WHEN i % 3 = 0 THEN 'message.search' ELSE 'message.open' END, '{}'
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
}
