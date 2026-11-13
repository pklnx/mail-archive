package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
)

// Race tests of storage usage (#397, part 1, revisions 4 and 5): each one
// stops a transaction at a lock that the test holds and waits until
// PostgreSQL shows the wait (waitForLockWait), never for time. None may end
// in a deadlock (40P01) or another error.

// begin starts a transaction on a connection of its own.
func (f *usageFixture) begin() pgx.Tx {
	f.t.Helper()
	tx, err := connect(f.t, f.url).Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func (f *usageFixture) txExec(tx pgx.Tx, sql string, args ...any) {
	f.t.Helper()
	if _, err := tx.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

// saveAsync saves metas into fo in the background.
func (f *usageFixture) saveAsync(fo *store.Folder, metas ...store.MessageMeta) <-chan error {
	locs := f.locs(fo, len(metas))
	last := f.uid
	done := make(chan error, 1)
	go func() {
		_, err := f.st.SaveBatch(f.ctx, fo.ID, last, metas, locs)
		done <- err
	}()
	return done
}

func async(fn func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	return done
}

func (f *usageFixture) wait(done <-chan error, what string) {
	f.t.Helper()
	if err := <-done; err != nil {
		f.t.Fatalf("%s: %v", what, err)
	}
}

func (f *usageFixture) watch() *pgx.Conn { return connect(f.t, f.url) }

// TestGroupUsageSameHashHeldElsewhere (revision 4 point 6, first variant):
// a message that only group B holds goes into two accounts of group A at
// the same time. Both saves stop at AdvanceFolder, after their probe, so
// neither sees the other's location: group A counts it twice (exact plus
// one size), and the snapshot makes it exact.
func TestGroupUsageSameHashHeldElsewhere(t *testing.T) {
	f := newUsageFixture(t)
	ga, gb := f.defaultGroup(), f.group("B")
	alice, bob := f.user("alice", ga), f.user("bob", gb)
	a1, a2 := f.folder(&alice.ID, "one"), f.folder(&alice.ID, "two")
	b := f.folder(&bob.ID, "b")
	m := usageMsg(1, 1000)
	f.save(b, m)
	f.refresh()
	// FOR NO KEY UPDATE lets the foreign key check of UpsertLocation pass
	// (KEY SHARE) and stops AdvanceFolder.
	hold := f.begin()
	f.txExec(hold, "SELECT 1 FROM folders WHERE id IN ($1, $2) FOR NO KEY UPDATE", a1.ID, a2.ID)
	d1, d2 := f.saveAsync(a1, m), f.saveAsync(a2, m)
	watch := f.watch()
	waitForLockWaits(t, watch, d1, 2)
	_ = hold.Rollback(f.ctx)
	f.wait(d1, "save 1")
	f.wait(d2, "save 2")
	if got := f.groupUsage(ga); got != (store.Usage{Messages: 2, Bytes: 2000}) {
		t.Errorf("group A before the snapshot: %+v (exact plus one size expected)", got)
	}
	f.refresh()
	if got := f.groupUsage(ga); got != (store.Usage{Messages: 1, Bytes: 1000}) {
		t.Errorf("group A after the snapshot: %+v", got)
	}
}

// TestGroupUsageSameNewHash (revision 4 point 6, second variant): a
// message new to the archive goes into two accounts of one group. The
// second save's insert waits for the first save's row, so its probe runs
// after the first commit and sees that location: exact without a snapshot.
func TestGroupUsageSameNewHash(t *testing.T) {
	f := newUsageFixture(t)
	g := f.defaultGroup()
	alice := f.user("alice", g)
	a1, a2 := f.folder(&alice.ID, "one"), f.folder(&alice.ID, "two")
	f.refresh()
	m := usageMsg(2, 300)
	hold := f.begin()
	f.txExec(hold, "SELECT 1 FROM folders WHERE id = $1 FOR NO KEY UPDATE", a1.ID)
	d1 := f.saveAsync(a1, m)
	watch := f.watch()
	waitForLockWait(t, watch, d1)
	d2 := f.saveAsync(a2, m)
	waitForLockWaits(t, watch, d2, 2)
	_ = hold.Rollback(f.ctx)
	f.wait(d1, "save 1")
	f.wait(d2, "save 2")
	if got := f.groupUsage(g); got != (store.Usage{Messages: 1, Bytes: 300}) {
		t.Errorf("group: %+v", got)
	}
}

// TestGroupUsageSaveDuringRefresh: a save commits while usage_refresh
// holds its snapshot (stopped at its last write by the test's lock on
// storage_snapshot). The snapshot does not see it, its delta survives the
// snapshot, and it counts once.
func TestGroupUsageSaveDuringRefresh(t *testing.T) {
	f := newUsageFixture(t)
	g := f.defaultGroup()
	alice := f.user("alice", g)
	fo := f.folder(&alice.ID, "mail")
	f.save(fo, usageMsg(1, 10))
	f.refresh()
	hold := f.begin()
	f.txExec(hold, "SELECT 1 FROM storage_snapshot FOR UPDATE")
	refresh := async(func() error { _, err := f.st.RefreshUsage(f.ctx); return err })
	watch := f.watch()
	waitForLockWait(t, watch, refresh)
	f.save(fo, usageMsg(2, 5))
	_ = hold.Rollback(f.ctx)
	f.wait(refresh, "refresh")
	if got := f.groupUsage(g); got != (store.Usage{Messages: 2, Bytes: 15}) {
		t.Errorf("group after the snapshot: %+v", got)
	}
	if n := f.int64Of("SELECT count(*) FROM storage_group_deltas"); n != 1 {
		t.Errorf("%d group deltas, want the save's", n)
	}
	f.refresh()
	if got := f.groupUsage(g); got != (store.Usage{Messages: 2, Bytes: 15}) {
		t.Errorf("group after the next snapshot: %+v", got)
	}
}

// TestUsageRefreshWaitsForFold (revision 4 point 4): the refresh takes the
// storage lock before its snapshot. While a fold holds it, the refresh
// waits; the fold commits; the refresh then runs once without 40001, and
// the totals are unchanged.
func TestUsageRefreshWaitsForFold(t *testing.T) {
	f := newUsageFixture(t)
	g := f.defaultGroup()
	alice := f.user("alice", g)
	fo := f.folder(&alice.ID, "mail")
	f.save(fo, usageMsg(1, 10))
	f.refresh()
	f.save(fo, usageMsg(2, 20))
	want := f.totals()
	fold := f.begin()
	f.txExec(fold, "SELECT pg_advisory_xact_lock($1, 0)", store.StorageLockClassForTest)
	refresh := async(func() error { _, err := f.st.RefreshUsage(f.ctx); return err })
	waitForLockWait(t, f.watch(), refresh)
	if err := store.FoldStorageInTxForTest(f.ctx, fold); err != nil {
		t.Fatal(err)
	}
	if err := fold.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.wait(refresh, "refresh")
	if got := f.totals(); got != want {
		t.Errorf("totals %+v, want %+v", got, want)
	}
	if got := f.groupUsage(g); got != (store.Usage{Messages: 2, Bytes: 30}) {
		t.Errorf("group %+v", got)
	}
}

// moveRace is one of the moves that a save races (revision 4 point 2).
type moveRace struct {
	name string
	// setup returns the folder the save writes into, the group whose
	// figure the save must end up in, and the move as a function.
	setup func(f *usageFixture) (fo *store.Folder, to int64, move func() error)
	// rawMove makes the same move in tx, without committing, for "move
	// first".
	rawMove func(f *usageFixture, tx pgx.Tx, fo *store.Folder, to int64)
}

var moveRaces = []moveRace{
	{
		name: "SetUserGroup",
		setup: func(f *usageFixture) (*store.Folder, int64, func() error) {
			g2 := f.group("B")
			alice := f.user("alice", f.defaultGroup())
			fo := f.folder(&alice.ID, "mail")
			return fo, g2, func() error { _, err := f.st.SetUserGroup(f.ctx, alice.ID, g2); return err }
		},
		rawMove: func(f *usageFixture, tx pgx.Tx, fo *store.Folder, to int64) {
			f.txExec(tx, `SELECT 1 FROM users u JOIN accounts a ON a.owner_id = u.id WHERE a.id = $1 FOR UPDATE OF u`, fo.AccountID)
			f.txExec(tx, `UPDATE users SET group_id = $2 FROM accounts a WHERE a.id = $1 AND users.id = a.owner_id`, fo.AccountID, to)
		},
	},
	{
		name: "SetAccountOwner",
		setup: func(f *usageFixture) (*store.Folder, int64, func() error) {
			g2 := f.group("B")
			alice := f.user("alice", f.defaultGroup())
			bob := f.user("bob", g2)
			fo := f.folder(&alice.ID, "mail")
			return fo, g2, func() error {
				acc, err := f.st.GetAccount(f.ctx, fo.AccountID)
				if err != nil {
					return err
				}
				return f.st.SetAccountOwner(f.ctx, acc.Ref(), bob.ID, true)
			}
		},
		rawMove: func(f *usageFixture, tx pgx.Tx, fo *store.Folder, to int64) {
			f.txExec(tx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, fo.AccountID)
			f.txExec(tx, `UPDATE accounts SET owner_id = (SELECT id FROM users WHERE group_id = $2) WHERE id = $1`, fo.AccountID, to)
		},
	},
	{
		name: "adoption",
		setup: func(f *usageFixture) (*store.Folder, int64, func() error) {
			fo := f.folder(nil, "early")
			return fo, f.defaultGroup(), func() error {
				_, err := f.st.CreateUser(f.ctx, "alice", "h", false)
				return err
			}
		},
		rawMove: func(f *usageFixture, tx pgx.Tx, fo *store.Folder, to int64) {
			f.txExec(tx, `INSERT INTO users (name, password_hash, group_id) VALUES ('alice', 'h', $1)`, to)
			f.txExec(tx, `SELECT 1 FROM accounts WHERE owner_id IS NULL ORDER BY id FOR UPDATE`)
			f.txExec(tx, `UPDATE accounts SET owner_id = (SELECT id FROM users WHERE name = 'alice') WHERE owner_id IS NULL`)
		},
	},
}

// TestGroupUsageSaveBeforeMove: the save starts first and holds its
// account and owner (it stops at AdvanceFolder on the test's folder lock);
// the move then waits for it. The save commits for the old group, the
// move commits and requests a snapshot, and after that snapshot every
// figure is exact.
func TestGroupUsageSaveBeforeMove(t *testing.T) {
	for _, r := range moveRaces {
		t.Run(r.name, func(t *testing.T) {
			f := newUsageFixture(t)
			fo, to, move := r.setup(f)
			f.save(fo, usageMsg(1, 100))
			f.refresh()
			hold := f.begin()
			f.txExec(hold, "SELECT 1 FROM folders WHERE id = $1 FOR NO KEY UPDATE", fo.ID)
			save := f.saveAsync(fo, usageMsg(2, 7))
			watch := f.watch()
			waitForLockWait(t, watch, save)
			moved := async(move)
			waitForLockWaits(t, watch, moved, 2)
			_ = hold.Rollback(f.ctx)
			f.wait(save, "save")
			f.wait(moved, "move")
			if n := f.int64Of("SELECT seq - (SELECT covered_seq FROM storage_snapshot) FROM storage_refresh_requests"); n != 1 {
				t.Errorf("the move left %d requests", n)
			}
			f.refresh()
			u := f.usage()
			if got := f.groupUsage(to); got != (store.Usage{Messages: 2, Bytes: 107}) {
				t.Errorf("new group %+v", got)
			}
			for _, g := range u.Groups {
				if g.ID != to && g.Usage != (store.Usage{}) {
					t.Errorf("old group %s keeps %+v", g.Name, g.Usage)
				}
			}
			if u.Ungrouped != (store.Usage{}) {
				t.Errorf("without group %+v", u.Ungrouped)
			}
		})
	}
}

// TestGroupUsageMoveBeforeSave: the move starts first (the test's
// transaction, which holds the row the move locks); the save waits for it
// and then reads the new group, so its delta goes to the new group.
func TestGroupUsageMoveBeforeSave(t *testing.T) {
	for _, r := range moveRaces {
		t.Run(r.name, func(t *testing.T) {
			f := newUsageFixture(t)
			fo, to, _ := r.setup(f)
			f.refresh()
			move := f.begin()
			r.rawMove(f, move, fo, to)
			save := f.saveAsync(fo, usageMsg(2, 7))
			waitForLockWait(t, f.watch(), save)
			if err := move.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			f.wait(save, "save")
			if got := f.groupUsage(to); got != (store.Usage{Messages: 1, Bytes: 7}) {
				t.Errorf("new group before a snapshot: %+v", got)
			}
		})
	}
}

// TestUsageMoveDuringRefresh: SetUserGroup commits while a snapshot runs
// (stopped at its last write). The snapshot then sees a newer request and
// asks to run again (Again); the second run makes the figures exact.
func TestUsageMoveDuringRefresh(t *testing.T) {
	f := newUsageFixture(t)
	g2 := f.group("B")
	alice := f.user("alice", f.defaultGroup())
	fo := f.folder(&alice.ID, "mail")
	f.save(fo, usageMsg(1, 50))
	f.refresh()
	hold := f.begin()
	f.txExec(hold, "SELECT 1 FROM storage_snapshot FOR UPDATE")
	var res store.RefreshResult
	refresh := async(func() (err error) { res, err = f.st.RefreshUsage(f.ctx); return err })
	waitForLockWait(t, f.watch(), refresh)
	if _, err := f.st.SetUserGroup(f.ctx, alice.ID, g2); err != nil {
		t.Fatal(err)
	}
	_ = hold.Rollback(f.ctx)
	f.wait(refresh, "refresh")
	if !res.Again {
		t.Fatalf("the snapshot missed the request: %+v", res)
	}
	if res = f.refresh(); res.Again {
		t.Errorf("second run again: %+v", res)
	}
	if got := f.groupUsage(g2); got != (store.Usage{Messages: 1, Bytes: 50}) {
		t.Errorf("new group %+v", got)
	}
}

// TestUsageOwnerChangeDuringRefresh: SetAccountOwner across groups commits
// while a snapshot runs. The snapshot either fails with 40001 (the job
// runs it again without counting an attempt) or finishes and asks to run
// again; repeated like the job does, the figures end exact.
func TestUsageOwnerChangeDuringRefresh(t *testing.T) {
	f := newUsageFixture(t)
	g2 := f.group("B")
	alice := f.user("alice", f.defaultGroup())
	bob := f.user("bob", g2)
	fo := f.folder(&alice.ID, "mail")
	f.save(fo, usageMsg(1, 50))
	f.refresh()
	hold := f.begin()
	f.txExec(hold, "SELECT 1 FROM storage_snapshot FOR UPDATE")
	var res store.RefreshResult
	refresh := async(func() (err error) { res, err = f.st.RefreshUsage(f.ctx); return err })
	waitForLockWait(t, f.watch(), refresh)
	acc, err := f.st.GetAccount(f.ctx, fo.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	moved := async(func() error { return f.st.SetAccountOwner(f.ctx, acc.Ref(), bob.ID, true) })
	f.wait(moved, "owner change")
	_ = hold.Rollback(f.ctx)
	if err := <-refresh; err != nil && !store.IsSerializationFailure(err) {
		t.Fatalf("refresh: %v", err)
	} else if err == nil && !res.Again {
		t.Fatalf("the snapshot missed the request: %+v", res)
	}
	for range 3 {
		if res = f.refresh(); !res.Again {
			break
		}
	}
	if got := f.groupUsage(g2); got != (store.Usage{Messages: 1, Bytes: 50}) {
		t.Errorf("new group %+v", got)
	}
	if got := f.groupUsage(f.defaultGroup()); got != (store.Usage{}) {
		t.Errorf("old group %+v", got)
	}
}

// TestUsageRequestAfterLastRead (revision 5, test B, and the review's step
// 4): a request whose transaction commits after the snapshot read seq for
// the last time. The requester (the test's transaction) enqueued on the
// running job, which locks its row; the pool's FinishJob waits for that
// lock (the signal), marks the job done after the commit, and the request
// is left uncovered. The next fold queues the snapshot, which covers it.
func TestUsageRequestAfterLastRead(t *testing.T) {
	f := newUsageFixture(t)
	g2 := f.group("B")
	alice := f.user("alice", f.defaultGroup())
	fo := f.folder(&alice.ID, "mail")
	f.save(fo, usageMsg(1, 50))
	f.refresh()
	if err := f.st.RequestUsageRefresh(f.ctx); err != nil {
		t.Fatal(err)
	}
	lease := []byte("lease-of-the-test-pool")
	claimed, err := f.st.ClaimJobs(f.ctx, []string{store.JobUsageRefresh}, lease, time.Minute, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	hold := f.begin()
	f.txExec(hold, "SELECT 1 FROM storage_snapshot FOR UPDATE")
	// The job as the pool runs it: the snapshot, then FinishJob.
	job := async(func() error {
		res, err := f.st.RefreshUsage(f.ctx)
		if err != nil {
			return err
		}
		if res.Again {
			return f.st.ContinueJob(f.ctx, claimed[0].ID, lease, nil)
		}
		return f.st.FinishJob(f.ctx, claimed[0].ID, lease)
	})
	watch := f.watch()
	waitForLockWait(t, watch, job)
	// The move, committed later: the group change itself, then the
	// request in the same transaction.
	requester := f.begin()
	f.txExec(requester, "UPDATE users SET group_id = $2 WHERE id = $1", alice.ID, g2)
	if err := store.RequestUsageRefreshInTxForTest(f.ctx, requester); err != nil {
		t.Fatal(err)
	}
	_ = hold.Rollback(f.ctx)
	waitForRow(t, watch, job, "FinishJob waiting for the requester", `SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%SET state = ''done''%'`)
	if err := requester.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.wait(job, "job")
	var state string
	if err := f.conn.QueryRow(f.ctx, "SELECT state FROM jobs WHERE id = $1", claimed[0].ID).Scan(&state); err != nil || state != store.JobDone {
		t.Fatalf("job %s, %v", state, err)
	}
	if n := f.int64Of("SELECT seq - (SELECT covered_seq FROM storage_snapshot) FROM storage_refresh_requests"); n != 1 {
		t.Fatalf("uncovered requests %d, want 1", n)
	}
	if r, err := f.st.FoldStorage(f.ctx); err != nil || !r.Requested {
		t.Fatalf("fold: %+v %v", r, err)
	}
	if err := f.conn.QueryRow(f.ctx, "SELECT state FROM jobs WHERE id = $1", claimed[0].ID).Scan(&state); err != nil || state != store.JobQueued {
		t.Fatalf("job after the fold %s, %v", state, err)
	}
	f.refresh()
	if got := f.groupUsage(g2); got != (store.Usage{Messages: 1, Bytes: 50}) {
		t.Errorf("new group %+v", got)
	}
}
