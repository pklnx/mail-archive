package store_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/db"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestAttachmentTextCost measures the costs of attachment text per
// location (#539, part 2) that docs/development/architecture.md and the
// One-time steps row in docs/guide/operations.md state. It writes about
// 1 GB, so it runs only with MAIL_ARCHIVE_BENCH=1, on the target (a
// PostgreSQL pinned to 2 cores):
//
//		MAIL_ARCHIVE_BENCH=1 go test -run TestAttachmentTextCost -v ./internal/store/
//
//	  - The insert's group lookups per batch of 100 known messages with
//	    attachments, at 40, 400 and 1,500 folders in the group: part 2's
//	    MessagesWithAttachment and GroupAttachmentTextOK, and #446's
//	    GroupHeldMessages, each the median of 21 runs after 5 warm-up runs
//	    (pgx caches the statements, so they run with the generic plan).
//	  - An import into group 0 with 5,000 folders: SaveBatch of 100 known
//	    messages with attachments, the median of 15 batches.
//	  - A pass of the repair with 10,000 false locations: queueing them, and
//	    again with their jobs queued.
//	  - The migration's Up (its index build) per million locations.
func TestAttachmentTextCost(t *testing.T) {
	if os.Getenv("MAIL_ARCHIVE_BENCH") != "1" {
		t.Skip("set MAIL_ARCHIVE_BENCH=1 to run")
	}
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	const messages = 100000
	exec(`INSERT INTO messages (sha256, size, stored_path, has_attachment)
		SELECT lpad(to_hex(i), 64, '0'), 1000, 'x', true FROM generate_series(1, $1::int) i`, messages)
	exec(`INSERT INTO attachment_index_state (message_sha256, version)
		SELECT lpad(to_hex(i), 64, '0'), $2 FROM generate_series(1, $1::int) i`, messages, store.AttachmentIndexVersion)

	// groupFolders creates users of group g (nil: accounts without an
	// owner) with accounts of perAccount folders, n folders in all, and
	// returns the folder IDs and one account ID.
	groupFolders := func(name string, g *int64, n, perAccount int) ([]int64, int64) {
		t.Helper()
		var ids []int64
		var account int64
		for a := 0; len(ids) < n; a++ {
			acc := &store.Account{Kind: store.KindImport, Name: fmt.Sprintf("%s-%d", name, a)}
			if g != nil {
				u, err := st.CreateUserInGroup(ctx, store.NewUser{Name: fmt.Sprintf("%s-u%d", name, a), PasswordHash: "h", GroupID: *g})
				if err != nil {
					t.Fatal(err)
				}
				acc.OwnerID = &u.ID
			}
			if err := st.CreateAccount(ctx, acc); err != nil {
				t.Fatal(err)
			}
			account = acc.ID
			rows, err := conn.Query(ctx, `INSERT INTO folders (account_id, name) SELECT $1, 'f' || i FROM generate_series(1, $2::int) i RETURNING id`,
				acc.ID, min(perAccount, n-len(ids)))
			if err != nil {
				t.Fatal(err)
			}
			got, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, got...)
		}
		return ids, account
	}
	// spread gives messages [from, to) one location each, round robin over
	// folders, the even ones flagged; each call has a UIDVALIDITY of its
	// own.
	uidValidity := 0
	spread := func(folders []int64, from, to int) {
		t.Helper()
		uidValidity++
		exec(`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid, attachment_text_ok)
			SELECT lpad(to_hex(i), 64, '0'), ($1::bigint[])[1 + i % cardinality($1::bigint[])], $4, i, i % 2 = 0
			FROM generate_series($2::int, $3::int - 1) i`, folders, from, to, uidValidity)
	}
	other, err := st.CreateGroup(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	otherFolders, _ := groupFolders("other", &other.ID, 300, 30)
	spread(otherFolders, 1, messages+1)
	spread(otherFolders[1:], 1, messages+1) // a second copy of each in another folder
	type groupFixture struct {
		folders []int64
		group   int64
		account int64
	}
	sizes := []int{40, 400, 1500}
	groups := map[int]groupFixture{}
	for _, n := range sizes {
		g, err := st.CreateGroup(ctx, fmt.Sprintf("g%d", n))
		if err != nil {
			t.Fatal(err)
		}
		fs, acc := groupFolders(fmt.Sprintf("g%d", n), &g.ID, n, 30)
		spread(fs, 1, messages/2)
		groups[n] = groupFixture{fs, g.ID, acc}
	}
	exec("VACUUM ANALYZE")

	median := func(d []time.Duration) time.Duration {
		slices.Sort(d)
		return d[len(d)/2]
	}
	timed := func(f func() error) time.Duration {
		t.Helper()
		q := make([]time.Duration, 0, 21)
		for i := range 26 {
			start := time.Now()
			if err := f(); err != nil {
				t.Fatal(err)
			}
			if i >= 5 {
				q = append(q, time.Since(start))
			}
		}
		return median(q)
	}
	batch := func(from int) []string {
		out := make([]string, 100)
		for i := range out {
			out[i] = fmt.Sprintf("%064x", from+i*7)
		}
		return out
	}
	q := db.New(conn)
	for _, n := range sizes {
		g := groups[n]
		shas := batch(40000)
		has := timed(func() error { _, err := q.MessagesWithAttachment(ctx, shas); return err })
		ok := timed(func() error {
			_, err := q.GroupAttachmentTextOK(ctx, db.GroupAttachmentTextOKParams{Shas: shas, GroupID: g.group})
			return err
		})
		held := timed(func() error {
			_, err := q.GroupHeldMessages(ctx, db.GroupHeldMessagesParams{Shas: shas, AccountID: g.account, GroupID: g.group})
			return err
		})
		t.Logf("%d folders, batch of 100: MessagesWithAttachment %v, GroupAttachmentTextOK %v, GroupHeldMessages (#446) %v, sum %v",
			n, has, ok, held, has+ok+held)
		if has+ok+held > time.Second {
			t.Errorf("%d folders: the group lookups take %v per batch, above the stop condition of 1 s", n, has+ok+held)
		}
	}

	// An import into group 0 with 5,000 folders.
	g0, _ := groupFolders("unowned", nil, 5000, 100)
	spread(g0, 1, messages/2)
	exec("VACUUM ANALYZE")
	ok0 := timed(func() error {
		_, err := q.GroupAttachmentTextOK(ctx, db.GroupAttachmentTextOKParams{Shas: batch(40000), GroupID: 0})
		return err
	})
	target := g0[0]
	var saves []time.Duration
	uid := uint32(1 << 30)
	for b := range 15 {
		metas := make([]store.MessageMeta, 100)
		locs := make([]store.Location, 100)
		for i := range metas {
			sha := fmt.Sprintf("%064x", 60000+b*100+i)
			metas[i] = store.MessageMeta{SHA256: sha, Size: 1000, StoredPath: "x", Known: true}
			uid++
			locs[i] = store.Location{FolderID: target, UIDValidity: 1, UID: uid}
		}
		start := time.Now()
		if _, err := st.SaveBatch(ctx, target, uid, metas, locs); err != nil {
			t.Fatal(err)
		}
		saves = append(saves, time.Since(start))
	}
	t.Logf("group 0 with 5,000 folders: GroupAttachmentTextOK %v per batch of 100; SaveBatch of 100 known messages with attachments %v (median of 15)",
		ok0, median(saves))

	// The repair with 10,000 false locations of done messages.
	exec(`UPDATE message_locations SET attachment_text_ok = true WHERE NOT attachment_text_ok`)
	exec(`UPDATE message_locations SET attachment_text_ok = false WHERE id IN (SELECT id FROM message_locations ORDER BY id LIMIT 10000)`)
	exec(`DELETE FROM jobs`)
	exec("VACUUM ANALYZE message_locations")
	start := time.Now()
	res, err := st.RepairAttachmentText(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := time.Since(start)
	start = time.Now()
	res2, err := st.RepairAttachmentText(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("repair with 10,000 false locations: %v queuing %d messages; %v when they are queued (%d)", first, res.Queued, time.Since(start), res2.Queued)

	// The migration per million locations: down, then Up of 00044 alone.
	exec(`DELETE FROM jobs`)
	var locations int64
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM message_locations").Scan(&locations); err != nil {
		t.Fatal(err)
	}
	if locations < 1000000 {
		exec(`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid)
			SELECT lpad(to_hex(1 + i % $1::int), 64, '0'), ($2::bigint[])[1 + i % cardinality($2::bigint[])], 1000, i
			FROM generate_series(1, $3::int) i`, messages, g0, 2000000-locations)
	}
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM message_locations").Scan(&locations); err != nil {
		t.Fatal(err)
	}
	downAttachmentTextPerLocationMigration(t, st)
	exec("VACUUM ANALYZE message_locations")
	exec("SELECT count(*) FROM message_locations") // warm the cache, as a running instance has it
	start = time.Now()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Logf("migration 00044 Up over %d locations: %v, %v per million", locations, took, time.Duration(float64(took)*1e6/float64(locations)))
}
