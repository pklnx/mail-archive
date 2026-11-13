package store_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store/db"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestGroupLookupCost measures the group lookups of the save path per
// batch of 100 messages (#539, part 3; numbers in
// docs/development/architecture.md): the 100 GroupLookup.Holds calls that
// sync, fill and SMTP make before the save, one per message, with the
// group's folders read once per batch here (a folder run reads them once
// for all its batches) (part 3), next
// to GroupHeldMessages of the save (storage usage, #397) and
// GroupHasLocations of skipDeleted for a batch whose messages all have a
// deletion mark (part 1's worst case; without marks it does not run).
// Hashes the group does not hold: every folder of the group is probed for
// every hash. It measures and asserts nothing about time; it runs only
// with MAIL_ARCHIVE_BENCH=1.
func TestGroupLookupCost(t *testing.T) {
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
		{"group 0 only, 5000 folders (no lookup)", 0, 0, 10, 5000, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, url := storetest.NewWithURL(t)
			ctx := context.Background()
			conn := connect(t, url)
			group := seedGroupFolders(t, conn, c.users, c.accounts, c.folders, c.unowned, 60000)
			owner := "a.owner_id IN (SELECT id FROM users WHERE group_id = $1)"
			if c.group0 {
				group = 0
				owner = "a.owner_id IS NULL AND $1::bigint = 0"
			}
			var account int64
			if err := conn.QueryRow(ctx, `SELECT min(a.id) FROM accounts a WHERE `+owner, group).Scan(&account); err != nil {
				t.Fatal(err)
			}
			var folders int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM folders f JOIN accounts a ON a.id = f.account_id LEFT JOIN users u ON u.id = a.owner_id
				WHERE coalesce(u.group_id, 0) = $1`, group).Scan(&folders); err != nil {
				t.Fatal(err)
			}
			batch := make([]string, 100)
			for i := range batch {
				batch[i] = seedHash(1000000 + i)
			}
			q := db.New(conn)
			timed := func(fn func()) time.Duration {
				start := time.Now()
				fn()
				return time.Since(start)
			}
			var part3, usage, marks, state []time.Duration
			for i := range 25 { // the first 5 warm the cache and the statement cache
				d3 := timed(func() {
					lookup := st.NewGroupLookup(account) // as a folder run: folders read once
					for _, sha := range batch {
						held, err := lookup.Holds(ctx, sha)
						// With group 0 alone there is no lookup: held.
						if err != nil || held != (c.users == 0) {
							t.Fatalf("lookup: %v %v", held, err)
						}
					}
				})
				du := timed(func() {
					if !c.group0 { // GroupHeldMessages runs only for an account with a group
						if _, err := q.GroupHeldMessages(ctx, db.GroupHeldMessagesParams{Shas: batch, AccountID: account, GroupID: group}); err != nil {
							t.Fatal(err)
						}
					}
				})
				dm := timed(func() {
					if _, err := q.GroupHasLocations(ctx, db.GroupHasLocationsParams{Shas: batch, GroupID: group}); err != nil {
						t.Fatal(err)
					}
				})
				ds := timed(func() {
					for range batch {
						if _, err := q.AccountGroupState(ctx, account); err != nil {
							t.Fatal(err)
						}
					}
				})
				if i >= 5 {
					part3, usage, marks, state = append(part3, d3), append(usage, du), append(marks, dm), append(state, ds)
				}
			}
			sum := make([]time.Duration, len(part3))
			for i := range sum {
				sum[i] = part3[i] + usage[i] + marks[i]
			}
			t.Logf("%s: %d folders; per batch of 100: GroupLookup.Holds ×100 median %v (min %v, max %v), of which AccountGroupState ×100 median %v; GroupHeldMessages median %v; GroupHasLocations (all marked) median %v; sum median %v (max %v)",
				c.name, folders, medianOf(part3), slices.Min(part3), slices.Max(part3), medianOf(state), medianOf(usage), medianOf(marks), medianOf(sum), slices.Max(sum))
		})
	}
}
