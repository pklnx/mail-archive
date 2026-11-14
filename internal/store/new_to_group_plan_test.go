package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/keepia/archive/internal/store/db"
	"github.com/keepia/archive/internal/store/storetest"
)

// TestFoldersHoldMessagesPlan: the per-message lookup before a save
// (GroupLookup, #539 part 3) probes message_locations_folder_sha_idx for
// each folder of the group and never looks the hash up in
// message_locations_sha_idx, which would make its cost depend on the
// copies other groups hold. Both the custom plan and the generic plan that
// pgx's statement cache gets after five runs, on 500 folders per group,
// for a group and for group 0, with one hash (as GroupLookup asks) and a
// batch. Without the LATERAL's LIMIT 1 the planner flattens the probes.
func TestFoldersHoldMessagesPlan(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	group := seedGroupFolders(t, conn, 5, 10, 10, 500, 20000)
	shas := make([]string, 100)
	for i := range shas {
		shas[i] = seedHash(i*7 + 1)
	}
	folderIDs := func(t *testing.T, id int64) []int64 {
		t.Helper()
		ids, err := db.New(conn).GroupFolderIDs(ctx, id)
		if err != nil || len(ids) < 400 {
			t.Fatalf("folders of group %d: %d %v", id, len(ids), err)
		}
		return ids
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
	if _, err := db.New(&rec).FoldersHoldMessages(ctx, db.FoldersHoldMessagesParams{Shas: shas, FolderIds: []int64{1}, GroupID: group}); !errors.Is(err, errRecorded) {
		t.Fatalf("record: %v", err)
	}
	if os.Getenv("MAIL_ARCHIVE_PLAN_DEBUG") != "" {
		t.Logf("query:\n%s", rec.sql)
	}
	if _, err := conn.Exec(ctx, "PREPARE folders_hold(bpchar[], bigint[], bigint) AS "+rec.sql); err != nil {
		t.Fatal(err)
	}
	ints := func(ids []int64) string {
		s := make([]string, len(ids))
		for i, id := range ids {
			s[i] = fmt.Sprint(id)
		}
		return strings.Join(s, ",")
	}
	for _, b := range []struct {
		name string
		shas []string
	}{{"one hash", shas[:1]}, {"batch", shas}} {
		for _, g := range []struct {
			name string
			id   int64
		}{{"group", group}, {"group 0", 0}} {
			t.Run(b.name+"/"+g.name, func(t *testing.T) {
				ids := folderIDs(t, g.id)
				if plan := explain(t, rec.sql, b.shas, ids, g.id); !probeOK(plan) {
					t.Errorf("custom plan does not probe message_locations_folder_sha_idx only:\n%s", plan)
				}
				if _, err := conn.Exec(ctx, "SET plan_cache_mode = force_generic_plan"); err != nil {
					t.Fatal(err)
				}
				defer func() { _, _ = conn.Exec(ctx, "RESET plan_cache_mode") }()
				// EXPLAIN EXECUTE takes no parameters: the values as literals.
				plan := explain(t, fmt.Sprintf("EXECUTE folders_hold(ARRAY['%s']::bpchar[], ARRAY[%s]::bigint[], %d)",
					strings.Join(b.shas, "','"), ints(ids), g.id))
				if !strings.Contains(plan, "$1") && !strings.Contains(plan, "$2") && !strings.Contains(plan, "$3") {
					t.Errorf("not a generic plan:\n%s", plan)
				}
				if !probeOK(plan) {
					t.Errorf("generic plan does not probe message_locations_folder_sha_idx only:\n%s", plan)
				}
			})
		}
	}
}
