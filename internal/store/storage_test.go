package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/db"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// usageFixture is a database with customer groups, users and accounts for
// the storage usage tests (#397, part 1).
type usageFixture struct {
	t    *testing.T
	ctx  context.Context
	st   *store.Store
	url  string
	conn *pgx.Conn
	uid  uint32
}

func newUsageFixture(t *testing.T) *usageFixture {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	return &usageFixture{t: t, ctx: context.Background(), st: st, url: url, conn: connect(t, url)}
}

// usageMsg is message i of size bytes.
func usageMsg(i int, size int64) store.MessageMeta {
	sha := fmt.Sprintf("%064x", 0x5000+i)
	return store.MessageMeta{SHA256: sha, Size: size, StoredPath: sha[:2] + "/" + sha}
}

func (f *usageFixture) group(name string) int64 {
	f.t.Helper()
	g, err := f.st.CreateGroup(f.ctx, name)
	if err != nil {
		f.t.Fatal(err)
	}
	return g.ID
}

func (f *usageFixture) defaultGroup() int64 {
	f.t.Helper()
	g, err := f.st.GetGroupByName(f.ctx, "Default")
	if err != nil {
		f.t.Fatal(err)
	}
	return g.ID
}

func (f *usageFixture) user(name string, group int64) *store.User {
	f.t.Helper()
	u, err := f.st.CreateUserInGroup(f.ctx, store.NewUser{Name: name, PasswordHash: "h", GroupID: group})
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

// folder returns folder INBOX of owner's import account (nil owner: an
// account without an owner).
func (f *usageFixture) folder(owner *int64, account string) *store.Folder {
	f.t.Helper()
	a := &store.Account{Kind: store.KindImport, Name: account, OwnerID: owner}
	if err := f.st.CreateAccount(f.ctx, a); err != nil {
		f.t.Fatal(err)
	}
	fo, err := f.st.GetOrCreateFolder(f.ctx, a.ID, "INBOX")
	if err != nil {
		f.t.Fatal(err)
	}
	return fo
}

// save stores metas into fo with SaveBatch.
func (f *usageFixture) save(fo *store.Folder, metas ...store.MessageMeta) store.SaveResult {
	f.t.Helper()
	locs := f.locs(fo, len(metas))
	res, err := f.st.SaveBatch(f.ctx, fo.ID, f.uid, metas, locs)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *usageFixture) locs(fo *store.Folder, n int) []store.Location {
	locs := make([]store.Location, n)
	for i := range locs {
		f.uid++
		locs[i] = store.Location{FolderID: fo.ID, UIDValidity: fo.UIDValidity, UID: f.uid}
	}
	return locs
}

func (f *usageFixture) refresh() store.RefreshResult {
	f.t.Helper()
	res, err := f.st.RefreshUsage(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *usageFixture) usage() *store.StorageUsage {
	f.t.Helper()
	u, err := f.st.StorageUsage(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *usageFixture) groupUsage(id int64) store.Usage {
	f.t.Helper()
	for _, g := range f.usage().Groups {
		if g.ID == id {
			return g.Usage
		}
	}
	f.t.Fatalf("no group %d", id)
	return store.Usage{}
}

func (f *usageFixture) accountUsage(id int64) store.AccountUsage {
	f.t.Helper()
	for _, a := range f.usage().Accounts {
		if a.ID == id {
			return a
		}
	}
	f.t.Fatalf("no account %d", id)
	return store.AccountUsage{}
}

func (f *usageFixture) totals() store.Usage {
	f.t.Helper()
	m, b, err := f.st.StorageTotals(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return store.Usage{Messages: m, Bytes: b}
}

// exact counts the messages table.
func (f *usageFixture) exact() store.Usage {
	f.t.Helper()
	var u store.Usage
	if err := f.conn.QueryRow(f.ctx, "SELECT count(*), coalesce(sum(size), 0) FROM messages").Scan(&u.Messages, &u.Bytes); err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *usageFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.conn.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f *usageFixture) int64Of(sql string, args ...any) int64 {
	f.t.Helper()
	var v int64
	if err := f.conn.QueryRow(f.ctx, sql, args...).Scan(&v); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// TestStorageCounters (revision 2, point 4): inserts and deletes never
// update the base row; base plus deltas equal an exact count after
// inserts, a duplicate, a rollback and deletes; a fold keeps the total and
// empties the deltas; a reader during a fold gets the same total before,
// during and after it; a change of size or hash is refused, other updates
// of messages pass.
func TestStorageCounters(t *testing.T) {
	f := newUsageFixture(t)
	baseVersion := func() string {
		t.Helper()
		var v string
		if err := f.conn.QueryRow(f.ctx, "SELECT xmin::text FROM storage_counter_base").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	check := func(when string) {
		t.Helper()
		if got, want := f.totals(), f.exact(); got != want {
			t.Errorf("%s: counters %+v, exact %+v", when, got, want)
		}
	}
	before := baseVersion()
	u := f.user("alice", f.defaultGroup())
	fo := f.folder(&u.ID, "mail")
	f.save(fo, usageMsg(1, 100), usageMsg(2, 20), usageMsg(2, 20))
	f.save(fo, usageMsg(1, 100)) // a duplicate: ON CONFLICT DO NOTHING
	check("after inserts")
	if got := f.totals(); got != (store.Usage{Messages: 2, Bytes: 120}) {
		t.Errorf("totals %+v", got)
	}
	// A rolled back insert takes its delta along.
	tx, err := f.conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, "INSERT INTO messages (sha256, size, stored_path) VALUES ($1, 5000, 'x')", usageMsg(3, 0).SHA256); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(f.ctx)
	check("after a rollback")
	// A multi-row delete (as retention does) and a multi-row insert.
	f.exec("INSERT INTO messages (sha256, size, stored_path) SELECT lpad(to_hex(i), 64, 'e'), i, 'x' FROM generate_series(1, 50) i")
	f.exec(`WITH r AS (INSERT INTO deletion_runs (state, confirmed_at, finished_at) VALUES ('done', now(), now()) RETURNING id)
		INSERT INTO deleted_messages (sha256, size, message_seq, archived_at, retain_until, locations, run_id)
		SELECT m.sha256, m.size, 1, m.created_at, now(), 1, r.id FROM messages m, r WHERE m.sha256 LIKE 'eee%' AND m.size <= 20`)
	f.exec("DELETE FROM messages WHERE sha256 LIKE 'eee%' AND size <= 20")
	check("after a bulk insert and delete")
	if baseVersion() != before {
		t.Error("inserts or deletes updated the base row")
	}
	if n := f.int64Of("SELECT count(*) FROM storage_counter_deltas"); n == 0 {
		t.Error("no delta rows")
	}

	// A fold keeps the total and empties the deltas. A reader while the
	// fold waits before its commit (the test holds a row the fold updates
	// last) sees the same total.
	want := f.totals()
	g := f.defaultGroup()
	f.refresh() // writes the group's usage row
	f.save(fo, usageMsg(4, 7))
	want = store.Usage{Messages: want.Messages + 1, Bytes: want.Bytes + 7}
	hold, err := f.conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hold.Exec(f.ctx, "SELECT 1 FROM storage_usage_groups WHERE group_id = $1 FOR UPDATE", g); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.st.FoldStorage(f.ctx)
		done <- err
	}()
	watch := connect(t, f.url)
	waitForLockWait(t, watch, done)
	if got := f.totals(); got != want {
		t.Errorf("during the fold: %+v, want %+v", got, want)
	}
	_ = hold.Rollback(f.ctx)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := f.totals(); got != want {
		t.Errorf("after the fold: %+v, want %+v", got, want)
	}
	if n := f.int64Of("SELECT count(*) FROM storage_counter_deltas"); n != 0 {
		t.Errorf("%d deltas left after the fold", n)
	}
	check("after the fold")

	// size and sha256 never change; the search index columns do.
	sha := usageMsg(1, 0).SHA256
	for _, sql := range []string{
		"UPDATE messages SET size = size + 1 WHERE sha256 = $1",
		"UPDATE messages SET sha256 = lpad('f', 64, 'f') WHERE sha256 = $1",
	} {
		_, err := f.conn.Exec(f.ctx, sql, sha)
		if state, _ := store.SQLState(err); state != "23514" {
			t.Errorf("%s: %v", sql, err)
		}
	}
	f.exec("UPDATE messages SET size = size, body_text = 'indexed', index_version = index_version WHERE sha256 = $1", sha)
	check("at the end")
}

// TestStorageCountersConcurrent: two open transactions insert overlapping
// sets of messages; neither waits for the counters (append-only rows), and
// the totals are exact after both commit.
func TestStorageCountersConcurrent(t *testing.T) {
	f := newUsageFixture(t)
	a, err := f.conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	other := connect(t, f.url)
	b, err := other.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(f.ctx, "INSERT INTO messages (sha256, size, stored_path) SELECT lpad(to_hex(i), 64, 'a'), 10, 'x' FROM generate_series(1, 20) i"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exec(f.ctx, "INSERT INTO messages (sha256, size, stored_path) SELECT lpad(to_hex(i), 64, 'b'), 3, 'x' FROM generate_series(1, 30) i"); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.totals(); got != (store.Usage{Messages: 50, Bytes: 290}) {
		t.Errorf("totals %+v", got)
	}
}

// TestUsageRefreshFigures: a message in accounts A and B (one group)
// counts in both accounts' bytes, in neither's exclusive bytes, once for
// the group and the instance; gone locations count; a message in two
// groups counts fully in each; an account without an owner counts
// "Without group"; group deltas between snapshots; the counter check.
func TestUsageRefreshFigures(t *testing.T) {
	f := newUsageFixture(t)
	// Before the first user: an ownerless account.
	nobody := f.folder(nil, "early")
	f.save(nobody, usageMsg(9, 9))
	res := f.refresh()
	if u := f.usage(); u.Ungrouped != (store.Usage{Messages: 1, Bytes: 9}) || u.SnapshotAt == nil || res.Drift() != 0 {
		t.Fatalf("without group: %+v, drift %d", u.Ungrouped, res.Drift())
	}
	g1 := f.defaultGroup()
	alice := f.user("alice", g1) // adopts "early"
	g2 := f.group("Other")
	bob := f.user("bob", g1)
	carol := f.user("carol", g2)
	a := f.folder(&alice.ID, "a")
	b := f.folder(&bob.ID, "b")
	c := f.folder(&carol.ID, "c")
	f.save(a, usageMsg(1, 100), usageMsg(2, 10))
	f.save(b, usageMsg(1, 100), usageMsg(3, 1))
	f.save(c, usageMsg(1, 100))
	// A gone location still counts.
	f.exec("UPDATE message_locations SET gone_at = now() WHERE message_sha256 = $1", usageMsg(3, 0).SHA256)
	// Between snapshots: deltas for what is new to each group. The
	// adopted account's bytes stay "Without group" until the snapshot the
	// adoption requested. A fold keeps the deltas of a group that has no
	// usage row yet ("Other", created after the last snapshot), and moves
	// those of a group with one.
	if _, err := f.st.FoldStorage(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.int64Of("SELECT count(*) FROM storage_usage_groups WHERE group_id = $1", g2); n != 0 {
		t.Fatalf("group 2 has a usage row before its first snapshot")
	}
	if got := f.groupUsage(g1); got != (store.Usage{Messages: 3, Bytes: 111}) {
		t.Errorf("group 1 before the snapshot: %+v", got)
	}
	if got := f.groupUsage(g2); got != (store.Usage{Messages: 1, Bytes: 100}) {
		t.Errorf("group 2 (no usage row) after a fold, before the snapshot: %+v", got)
	}
	f.refresh()
	u := f.usage()
	if u.Messages != 4 || u.Bytes != 120 || u.Ungrouped != (store.Usage{}) {
		t.Errorf("instance %+v", u)
	}
	for id, want := range map[int64]store.Usage{g1: {Messages: 4, Bytes: 120}, g2: {Messages: 1, Bytes: 100}} {
		if got := f.groupUsage(id); got != want {
			t.Errorf("group %d: %+v, want %+v", id, got, want)
		}
	}
	for fo, want := range map[*store.Folder][3]int64{a: {2, 110, 10}, b: {2, 101, 1}, c: {1, 100, 0}} {
		got := f.accountUsage(fo.AccountID)
		if got.Messages != want[0] || got.Bytes != want[1] || got.ExclusiveBytes != want[2] || got.SnapshotAt == nil {
			t.Errorf("account %d: %+v, want %v", fo.AccountID, got, want)
		}
	}
	if got := f.accountUsage(nobody.AccountID); got.GroupID == nil || *got.GroupID != g1 || got.Bytes != 9 {
		t.Errorf("adopted account %+v", got)
	}
	if n := f.int64Of("SELECT count(*) FROM storage_group_deltas"); n != 0 {
		t.Errorf("%d group deltas left after the snapshot", n)
	}
	// A fold moves the deltas into the group rows; the figure stays.
	f.save(a, usageMsg(5, 5))
	if _, err := f.st.FoldStorage(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.groupUsage(g1); got != (store.Usage{Messages: 5, Bytes: 125}) {
		t.Errorf("group 1 after the fold: %+v", got)
	}
	// A difference between the counters and the messages is found.
	f.exec("INSERT INTO storage_counter_deltas (messages, bytes) VALUES (1, 1000)")
	res = f.refresh()
	if res.Drift() != 1000 || res.CountedMessages != res.ExactMessages+1 {
		t.Errorf("counter check %+v", res)
	}
	if got := f.usage().CounterDrift; got != 1000 {
		t.Errorf("stored drift %d", got)
	}
	if got := f.usage().RelationBytes; got["messages"] <= 0 || got["audit_log"] < 0 || f.usage().DatabaseBytes <= 0 {
		t.Errorf("database sizes %v", got)
	}
}

// TestUsageRequests: every request source bumps the request seq and queues
// usage_refresh, a failed one too; a snapshot records the seq it covered;
// the fold queues a snapshot while a request is not covered, and re-arms a
// failed one only after an hour.
func TestUsageRequests(t *testing.T) {
	f := newUsageFixture(t)
	seq := func() int64 { return f.int64Of("SELECT seq FROM storage_refresh_requests") }
	covered := func() int64 { return f.int64Of("SELECT covered_seq FROM storage_snapshot") }
	jobState := func() string {
		t.Helper()
		var s string
		if err := f.conn.QueryRow(f.ctx, "SELECT state FROM jobs WHERE kind = 'usage_refresh' AND key = 'singleton'").Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	setJob := func(state, finished string) {
		t.Helper()
		f.exec("UPDATE jobs SET state = $1, finished_at = now() - $2::interval WHERE kind = 'usage_refresh'", state, finished)
	}
	if err := f.st.RequestUsageRefresh(f.ctx); err != nil || jobState() != store.JobQueued {
		t.Fatalf("request: %v, job %s", err, jobState())
	}
	f.refresh()
	setJob(store.JobFailed, "0")

	// The sources: adoption, a group move, an owner change from no owner
	// and across groups, an import's end (RequestUsageRefresh).
	nobody := f.folder(nil, "early")
	n := seq()
	alice := f.user("alice", f.defaultGroup())
	if seq() != n+1 || jobState() != store.JobQueued {
		t.Errorf("adoption: seq %d, job %s", seq(), jobState())
	}
	g2 := f.group("B")
	bob := f.user("bob", g2) // no adoption: seq stays
	if seq() != n+1 {
		t.Errorf("second user bumped seq to %d", seq())
	}
	setJob(store.JobFailed, "0")
	if _, err := f.st.SetUserGroup(f.ctx, alice.ID, g2); err != nil {
		t.Fatal(err)
	}
	if seq() != n+2 || jobState() != store.JobQueued {
		t.Errorf("group move: seq %d, job %s", seq(), jobState())
	}
	if _, err := f.st.SetUserGroup(f.ctx, alice.ID, g2); err != nil { // no change
		t.Fatal(err)
	}
	ownerless := f.folder(nil, "late")
	acc, err := f.st.GetAccount(f.ctx, ownerless.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetAccountOwner(f.ctx, acc.Ref(), bob.ID, false); err != nil {
		t.Fatal(err)
	}
	if seq() != n+3 {
		t.Errorf("owner from none: seq %d", seq())
	}
	// Same group: no request.
	acc, _ = f.st.GetAccount(f.ctx, nobody.AccountID)
	if err := f.st.SetAccountOwner(f.ctx, acc.Ref(), bob.ID, false); err != nil {
		t.Fatal(err)
	}
	if seq() != n+3 {
		t.Errorf("owner in the same group: seq %d", seq())
	}
	g3 := f.group("C")
	carol := f.user("carol", g3)
	acc, _ = f.st.GetAccount(f.ctx, nobody.AccountID)
	if err := f.st.SetAccountOwner(f.ctx, acc.Ref(), carol.ID, true); err != nil {
		t.Fatal(err)
	}
	if seq() != n+4 {
		t.Errorf("owner across groups: seq %d", seq())
	}
	if err := f.st.RequestUsageRefresh(f.ctx); err != nil {
		t.Fatal(err)
	}
	if seq() != n+5 {
		t.Errorf("request: seq %d", seq())
	}

	// A snapshot covers the seq it read; the fold then queues nothing.
	f.refresh()
	if covered() != seq() {
		t.Errorf("covered %d, seq %d", covered(), seq())
	}
	setJob(store.JobDone, "0")
	if r, err := f.st.FoldStorage(f.ctx); err != nil || r.Requested || jobState() != store.JobDone {
		t.Errorf("fold with everything covered: %+v %v %s", r, err, jobState())
	}
	// The state a lost request leaves (revision 5, test A): covered 5,
	// seq 6, the job done. The fold queues the snapshot, which then covers
	// 6.
	f.exec("UPDATE storage_snapshot SET covered_seq = 5")
	f.exec("UPDATE storage_refresh_requests SET seq = 6")
	if r, err := f.st.FoldStorage(f.ctx); err != nil || !r.Requested || jobState() != store.JobQueued {
		t.Errorf("fold with a lost request: %+v %v %s", r, err, jobState())
	}
	f.refresh()
	if covered() != 6 {
		t.Errorf("covered %d after the queued snapshot", covered())
	}
	// A failed snapshot is re-armed by the fold only once it failed more
	// than an hour ago.
	f.exec("UPDATE storage_refresh_requests SET seq = 7")
	setJob(store.JobFailed, "10 minutes")
	if r, err := f.st.FoldStorage(f.ctx); err != nil || !r.Requested || jobState() != store.JobFailed {
		t.Errorf("fold with a fresh failure: %+v %v %s", r, err, jobState())
	}
	setJob(store.JobFailed, "61 minutes")
	if _, err := f.st.FoldStorage(f.ctx); err != nil || jobState() != store.JobQueued {
		t.Errorf("fold with an old failure: %v %s", err, jobState())
	}
}

// TestUsageRequestsAtRunEnds: the end of a deletion run, the end of a
// purge and the cancel of a purge each bump the request seq and queue
// usage_refresh, a failed one too: deletions lower a group's figure only
// at a snapshot.
func TestUsageRequestsAtRunEnds(t *testing.T) {
	f := newUsageFixture(t)
	seq := func() int64 { return f.int64Of("SELECT seq FROM storage_refresh_requests") }
	jobState := func() string {
		t.Helper()
		var s string
		if err := f.conn.QueryRow(f.ctx, "SELECT state FROM jobs WHERE kind = 'usage_refresh' AND key = 'singleton'").Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// arm marks the snapshot job failed and returns the seq: a request
	// must queue it again.
	arm := func() int64 {
		t.Helper()
		f.exec("UPDATE jobs SET state = 'failed', finished_at = now() WHERE kind = 'usage_refresh'")
		return seq()
	}
	requested := func(what string, before int64) {
		t.Helper()
		if got := seq(); got != before+1 || jobState() != store.JobQueued {
			t.Errorf("%s: seq %d (was %d), job %s", what, got, before, jobState())
		}
	}
	if err := f.st.RequestUsageRefresh(f.ctx); err != nil {
		t.Fatal(err)
	}
	alice := f.user("alice", f.defaultGroup())
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	saveOld := func(fo *store.Folder, metas ...store.MessageMeta) {
		t.Helper()
		locs := f.locs(fo, len(metas))
		for i := range metas {
			metas[i].SentAt = &old
			locs[i].InternalDate = old
		}
		if _, err := f.st.SaveBatch(f.ctx, fo.ID, f.uid, metas, locs); err != nil {
			t.Fatal(err)
		}
	}

	// A deletion run.
	a := f.folder(&alice.ID, "a")
	if _, err := f.st.AddRetentionRule(f.ctx, store.NewRetentionRule{AccountID: a.AccountID, Years: 1}); err != nil {
		t.Fatal(err)
	}
	saveOld(a, usageMsg(1, 10), usageMsg(2, 20))
	run, err := f.st.ProposeDeletion(f.ctx, nil)
	if err != nil || run.Candidates != 2 {
		t.Fatalf("propose: %+v %v", run, err)
	}
	if err := f.st.ConfirmDeletionRun(f.ctx, run.ID, nil); err != nil {
		t.Fatal(err)
	}
	shas, err := f.st.PendingCandidates(f.ctx, run.ID, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.DeleteBatch(f.ctx, run.ID, shas, 0); err != nil {
		t.Fatal(err)
	}
	n := arm()
	if err := f.st.FinishDeletionRun(f.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	requested("end of a deletion run", n)

	// A purge to its end, and one cancelled.
	confirmPurge := func(name string, msg int) *store.AccountPurge {
		t.Helper()
		fo := f.folder(&alice.ID, name)
		saveOld(fo, usageMsg(msg, 30))
		acc, err := f.st.GetAccount(f.ctx, fo.AccountID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.DeleteOrRemoveAccount(f.ctx, acc.Ref()); err != nil {
			t.Fatal(err)
		}
		p, err := f.st.PurgePreview(f.ctx, fo.AccountID)
		if err != nil {
			t.Fatal(err)
		}
		if acc, err = f.st.GetAccount(f.ctx, fo.AccountID); err != nil {
			t.Fatal(err)
		}
		purge, err := f.st.ConfirmPurge(f.ctx, acc.Ref(), p.Deleted, nil)
		if err != nil {
			t.Fatal(err)
		}
		return purge
	}
	purge := confirmPurge("purged", 3)
	if err := f.st.CheckPurgeCount(f.ctx, purge.ID); err != nil {
		t.Fatal(err)
	}
	for {
		res, err := f.st.PurgeBatch(f.ctx, purge.ID, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		if res.Done {
			break
		}
	}
	n = arm()
	if _, err := f.st.FinishPurge(f.ctx, purge.ID); err != nil {
		t.Fatal(err)
	}
	requested("end of a purge", n)

	purge = confirmPurge("cancelled", 4)
	n = arm()
	if err := f.st.CancelPurge(f.ctx, purge.ID, "test"); err != nil {
		t.Fatal(err)
	}
	requested("cancel of a purge", n)
}

// TestUsageRefreshPlan: the snapshot aggregates with hashes and no
// per-row subplan, and reads the sizes with an index-only scan of
// messages_sha_size_idx, not the message heap; GroupHeldMessages probes
// message_locations_folder_sha_idx from the group's folders, never
// message_locations_sha_idx (by hash, over every group: #539), in the
// custom and in the generic plan.
func TestUsageRefreshPlan(t *testing.T) {
	f := newUsageFixture(t)
	u := f.user("alice", f.defaultGroup())
	fo := f.folder(&u.ID, "mail")
	// Wide rows, as real messages with their search text, so that the
	// heap is far larger than the index.
	f.exec(`INSERT INTO messages (sha256, size, stored_path, body_text)
		SELECT lpad(to_hex(i), 64, '0'), i, 'x', repeat('lorem ipsum dolor ', 60) FROM generate_series(1, 20000) i`)
	f.exec(`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid)
		SELECT lpad(to_hex(i), 64, '0'), $1, 1, i FROM generate_series(1, 20000) i`, fo.ID)
	f.exec("VACUUM ANALYZE messages")
	f.exec("VACUUM ANALYZE message_locations")
	explain := func(sql string, args ...any) string {
		t.Helper()
		rows, err := f.conn.Query(f.ctx, "EXPLAIN "+sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(lines, "\n")
	}
	var rec explainer
	if _, err := db.New(&rec).RefreshUsage(f.ctx); !errors.Is(err, errRecorded) {
		t.Fatalf("record: %v", err)
	}
	plan := explain(rec.sql)
	if !strings.Contains(plan, "HashAggregate") || strings.Contains(plan, "SubPlan") || strings.Contains(plan, "GroupAggregate") {
		t.Errorf("snapshot plan without hash aggregates, or with a per-row subplan:\n%s", plan)
	}
	if !strings.Contains(plan, "Index Only Scan using messages_sha_size_idx") || strings.Contains(plan, "Seq Scan on messages") {
		t.Errorf("snapshot plan reads the message heap:\n%s", plan)
	}
	if _, err := db.New(&rec).StorageCounterCheck(f.ctx); !errors.Is(err, errRecorded) {
		t.Fatalf("record: %v", err)
	}
	if plan := explain(rec.sql); !strings.Contains(plan, "Index Only Scan using messages_sha_size_idx") {
		t.Errorf("counter check reads the message heap:\n%s", plan)
	}
	// Account 0: alice's folder is one of the group's other folders.
	shas := []string{lpadHex(1), lpadHex(2)}
	if _, err := db.New(&rec).GroupHeldMessages(f.ctx, db.GroupHeldMessagesParams{Shas: shas, AccountID: 0, GroupID: f.defaultGroup()}); !errors.Is(err, errRecorded) {
		t.Fatalf("record: %v", err)
	}
	probeOK := func(plan string) bool {
		return strings.Contains(plan, "message_locations_folder_sha_idx") &&
			!strings.Contains(plan, "message_locations_sha_idx") && !strings.Contains(plan, "Seq Scan on message_locations")
	}
	if plan := explain(rec.sql, rec.args...); !probeOK(plan) {
		t.Errorf("GroupHeldMessages (custom plan) does not probe message_locations_folder_sha_idx only:\n%s", plan)
	}
	// The generic plan, as a prepared statement gets it after five runs
	// (pgx caches statements): an unnamed statement is always planned
	// with its values, so this uses PREPARE.
	f.exec("PREPARE group_held(bpchar[], bigint, bigint) AS " + rec.sql)
	f.exec("SET plan_cache_mode = force_generic_plan")
	defer f.exec("RESET plan_cache_mode")
	// EXPLAIN EXECUTE takes no parameters: the values as literals (hex
	// hashes and numbers).
	generic := fmt.Sprintf("EXECUTE group_held(ARRAY['%s']::bpchar[], %d, 0)", strings.Join(shas, "','"), f.defaultGroup())
	if plan := explain(generic); !probeOK(plan) {
		t.Errorf("GroupHeldMessages (generic plan) does not probe message_locations_folder_sha_idx only:\n%s", plan)
	}
}

// lpadHex is the hash TestUsageRefreshPlan stores for message i.
func lpadHex(i int) string { return fmt.Sprintf("%064x", i) }

// TestStorageMigration: 00041 fills the base from the messages there; Down
// drops everything; Up again works.
func TestStorageMigration(t *testing.T) {
	f := newUsageFixture(t)
	u := f.user("alice", f.defaultGroup())
	fo := f.folder(&u.ID, "mail")
	f.save(fo, usageMsg(1, 11), usageMsg(2, 22))
	downStorageUsageMigration(t, f.st)
	for _, table := range []string{"storage_counter_base", "storage_states", "storage_refresh_requests"} {
		if n := f.int64Of("SELECT count(*) FROM pg_tables WHERE tablename = $1", table); n != 0 {
			t.Errorf("%s survived the Down", table)
		}
	}
	if n := f.int64Of("SELECT count(*) FROM pg_indexes WHERE indexname = 'messages_sha_size_idx'"); n != 0 {
		t.Error("the covering index survived the Down")
	}
	f.exec("INSERT INTO messages (sha256, size, stored_path) SELECT lpad(to_hex(i), 64, 'c'), i, 'x' FROM generate_series(1, 100) i")
	if _, err := f.st.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := f.totals(), f.exact(); got != want || want.Messages != 102 {
		t.Errorf("after Up: counters %+v, exact %+v", got, want)
	}
	if n := f.int64Of("SELECT count(*) FROM storage_states WHERE group_id IS NULL AND state = 'ok'"); n != 1 {
		t.Errorf("%d instance states", n)
	}
	f.refresh()
	if got := f.groupUsage(f.defaultGroup()); got != (store.Usage{Messages: 2, Bytes: 33}) {
		t.Errorf("group after Up and a snapshot: %+v", got)
	}
}

// downStorageUsageMigration rolls back 00042_attachment_text_per_location
// and 00041_storage_usage.
func downStorageUsageMigration(t *testing.T, st *store.Store) {
	t.Helper()
	downAttachmentTextPerLocationMigration(t, st)
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00041_storage_usage") {
		t.Fatalf("down: %q %v", m, err)
	}
}
