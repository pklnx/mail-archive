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
// batch of 100 messages (#539; numbers in docs/development/architecture.md,
// "Messages new to the customer group"):
//
//   - part 3: the 100 GroupLookup.Holds calls that sync, fill and SMTP make
//     before the save, one per message, with the group's folders read once
//     per batch here (a folder run reads them once for all its batches);
//   - part 2: MessagesWithAttachment and GroupAttachmentTextOK of the save,
//     for a batch of known messages with attachments (the worst case;
//     messages new to the archive need no probe);
//   - #397/#446: GroupHeldMessages of the save (not for group 0);
//   - part 1: GroupHasLocations of skipDeleted for a batch whose messages
//     all have a deletion mark (the worst case; without marks it does not
//     run).
//
// Hashes the group does not hold: every folder of the group is probed for
// every hash. It asserts nothing about time and runs only with
// MAIL_ARCHIVE_BENCH=1.
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
		{"group 0 the only group, 1500 folders", 0, 0, 10, 1500, true},
		{"group 0, 5000 folders", 2, 2, 10, 5000, true},
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
			// Known to the archive with an attachment, held by nobody: part
			// 2's worst case.
			if _, err := conn.Exec(ctx, `INSERT INTO messages (sha256, size, stored_path, has_attachment)
				SELECT encode(sha256(i::text::bytea), 'hex'), 1000, 'x', true FROM generate_series(1000000, 1000099) i; ANALYZE messages`); err != nil {
				t.Fatal(err)
			}
			q := db.New(conn)
			timed := func(fn func()) time.Duration {
				start := time.Now()
				fn()
				return time.Since(start)
			}
			var part3, part2, usage, marks []time.Duration
			for i := range 25 { // the first 5 warm the cache and the statement cache
				d3 := timed(func() {
					lookup := st.NewGroupLookup(account)
					for _, sha := range batch {
						held, err := lookup.Holds(ctx, sha)
						if err != nil || held {
							t.Fatalf("lookup: %v %v", held, err)
						}
					}
				})
				d2 := timed(func() {
					rows, err := q.MessagesWithAttachment(ctx, batch)
					if err != nil || len(rows) != len(batch) {
						t.Fatalf("messages with attachment: %d %v", len(rows), err)
					}
					if _, err := q.GroupAttachmentTextOK(ctx, db.GroupAttachmentTextOKParams{Shas: batch, GroupID: group}); err != nil {
						t.Fatal(err)
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
				if i >= 5 {
					part3, part2, usage, marks = append(part3, d3), append(part2, d2), append(usage, du), append(marks, dm)
				}
			}
			without, with := make([]time.Duration, len(part3)), make([]time.Duration, len(part3))
			for i := range without {
				without[i] = part3[i] + part2[i] + usage[i]
				with[i] = without[i] + marks[i]
			}
			t.Logf("%s: %d folders; per batch of 100, medians: part 3 Holds ×100 %v; part 2 %v; GroupHeldMessages %v; part 1 (all marked) %v; sum without marks %v (max %v); sum with marks %v (max %v)",
				c.name, folders, medianOf(part3), medianOf(part2), medianOf(usage), medianOf(marks),
				medianOf(without), slices.Max(without), medianOf(with), slices.Max(with))
		})
	}
}
