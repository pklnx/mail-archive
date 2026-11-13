package store_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/db"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestDeletionMarkLookupCost measures the group lookup of skipDeleted
// (GroupHasLocations, the only group lookup part 1 of #539 adds to the
// save path) for a batch of 100 hashes, at 40, 400 and 1,500 folders of a
// customer group and at 1,500 and 5,000 folders of group 0 (an import into
// unowned accounts), and a whole SaveBatch of 100 messages that all have a
// deletion mark against the same batch without marks. Every message has a
// location in each group, 60,000 locations per group. It seeds for a
// while, so it runs only with MAIL_ARCHIVE_BENCH=1. On a larger machine,
// start PostgreSQL and the test under taskset -c 0,1, as on the 2-core
// target:
//
//	MAIL_ARCHIVE_BENCH=1 go test -run TestDeletionMarkLookupCost -v ./internal/store/
func TestDeletionMarkLookupCost(t *testing.T) {
	if os.Getenv("MAIL_ARCHIVE_BENCH") != "1" {
		t.Skip("set MAIL_ARCHIVE_BENCH=1 to run")
	}
	for _, c := range []struct {
		name                     string
		users, accounts, folders int
		unowned                  int
		group0                   bool
	}{
		{"group, 40 folders", 2, 2, 10, 40, false},
		{"group, 400 folders", 4, 10, 10, 40, false},
		{"group, 1500 folders", 10, 15, 10, 40, false},
		{"group 0, 1500 folders", 2, 2, 10, 1500, true},
		{"group 0, 5000 folders", 2, 2, 10, 5000, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, url := storetest.NewWithURL(t)
			ctx := context.Background()
			conn := connect(t, url)
			group := seedGroupFolders(t, conn, c.users, c.accounts, c.folders, c.unowned, 60000)
			if c.group0 {
				group = 0
			}
			// A batch the group does not hold (the worst case: every
			// folder is probed for every hash), with a tombstone each.
			batch := make([]string, 100)
			for i := range batch {
				batch[i] = seedHash(1000000 + i)
			}
			var folder int64
			owner := "a.owner_id IN (SELECT id FROM users WHERE group_id = $1)"
			if c.group0 {
				owner = "a.owner_id IS NULL AND $1::bigint = 0"
			}
			if err := conn.QueryRow(ctx, `SELECT min(f.id) FROM folders f JOIN accounts a ON a.id = f.account_id WHERE `+owner, group).Scan(&folder); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(ctx, `INSERT INTO retention_rules (account_id, years) SELECT account_id, 1 FROM folders WHERE id = $1`, folder); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(ctx, `
INSERT INTO deletion_runs (state, confirmed_at, finished_at) VALUES ('done', now(), now());
INSERT INTO deleted_messages (sha256, size, message_seq, archived_at, retain_until, locations, run_id)
SELECT encode(sha256(i::text::bytea), 'hex'), 1000, i, '2010-03-05', '2011-12-31', 1, (SELECT max(id) FROM deletion_runs)
FROM generate_series(1000000, 1000099) i;
ANALYZE;`); err != nil {
				t.Fatal(err)
			}
			q := db.New(conn)
			var lookups []time.Duration
			for range 40 {
				start := time.Now()
				held, err := q.GroupHasLocations(ctx, db.GroupHasLocationsParams{Shas: batch, GroupID: group})
				if err != nil || len(held) != 0 {
					t.Fatalf("lookup: %v %v", held, err)
				}
				lookups = append(lookups, time.Since(start))
			}
			var probes int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM folders f JOIN accounts a ON a.id = f.account_id LEFT JOIN users u ON u.id = a.owner_id
				WHERE coalesce(u.group_id, 0) = $1`, group).Scan(&probes); err != nil {
				t.Fatal(err)
			}
			save := func(shas []string, uid uint32) time.Duration {
				metas := make([]store.MessageMeta, len(shas))
				locs := make([]store.Location, len(shas))
				for i, sha := range shas {
					metas[i] = store.MessageMeta{SHA256: sha, Size: 1000, StoredPath: "x", SentAt: old}
					locs[i] = store.Location{FolderID: folder, UID: uid + uint32(i), InternalDate: *old} //nolint:gosec // small
				}
				start := time.Now()
				if _, err := st.SaveBatch(ctx, folder, uid+uint32(len(shas)), metas, locs); err != nil { //nolint:gosec // small
					t.Fatal(err)
				}
				return time.Since(start)
			}
			var marked, unmarked []time.Duration
			for i := range 15 {
				marked = append(marked, save(batch, 1))
				fresh := make([]string, 100)
				for j := range fresh {
					fresh[j] = seedHash(2000000 + i*100 + j)
				}
				unmarked = append(unmarked, save(fresh, uint32(1000+i*100))) //nolint:gosec // small
			}
			t.Logf("%s: %d folders, %d probes per batch; GroupHasLocations median %v (min %v, max %v); SaveBatch of 100 marked (all skipped) median %v, of 100 unmarked (stored) median %v",
				c.name, probes, probes*len(batch), medianOf(lookups), slices.Min(lookups), slices.Max(lookups), medianOf(marked), medianOf(unmarked))
		})
	}
}

func medianOf(d []time.Duration) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[len(s)/2]
}
