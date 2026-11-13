package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
)

// Deletion marks per account and customer group (#539, part 1). Alice and
// bob are in group A (the default group), carol in B, dave in C; frank
// starts in A and moves to D, where gina is.
type marksFixture struct {
	*retentionFixture
	conn                     *pgx.Conn
	groupB, groupC, groupD   int64
	carol, dave, frank, gina *store.User
}

func newMarksFixture(t *testing.T) *marksFixture {
	t.Helper()
	f := &marksFixture{retentionFixture: newRetentionFixture(t)}
	f.conn = connect(t, f.url)
	frank, err := f.st.CreateUser(f.ctx, "frank", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	f.frank = frank
	user := func(name string, group int64) *store.User {
		u, err := f.st.CreateUserInGroup(f.ctx, store.NewUser{Name: name, PasswordHash: "h", GroupID: group})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	group := func(name string) int64 {
		g, err := f.st.CreateGroup(f.ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return g.ID
	}
	f.groupB, f.groupC, f.groupD = group("B"), group("C"), group("D")
	f.carol, f.dave, f.gina = user("carol", f.groupB), user("dave", f.groupC), user("gina", f.groupD)
	return f
}

// expired returns folder INBOX of owner's account with a 1-year rule, so
// that a message from 2010 is already expired there.
func (f *marksFixture) expired(owner *store.User, account string) *store.Folder {
	f.t.Helper()
	fo := f.folder(owner, account, "INBOX")
	if n := countOf(f.t, f.conn, `SELECT count(*) FROM retention_rules WHERE account_id = $1`, fo.AccountID); n == 0 {
		f.rule(fo, "", 1)
	}
	return fo
}

// try saves message hash from 2010 into fo and returns what was stored.
func (f *marksFixture) try(fo *store.Folder, hash string) store.SaveResult {
	f.t.Helper()
	f.uid++
	res, err := f.st.SaveBatch(f.ctx, fo.ID, f.uid, []store.MessageMeta{{SHA256: hash, Size: 1, StoredPath: "x", SentAt: old}},
		[]store.Location{{FolderID: fo.ID, UID: f.uid, InternalDate: *old}})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// deleteExpired runs retention over everything expired. The file grace is
// long, so a removal that skipDeleted schedules shows in blob_removals
// only after the run's own are cleared (clearRemovals).
func (f *marksFixture) deleteExpired() int64 {
	f.t.Helper()
	run := f.propose()
	f.confirm(run.ID)
	if _, _, err := f.st.DeleteBatch(f.ctx, run.ID, f.candidates(run.ID), 0); err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.FinishDeletionRun(f.ctx, run.ID); err != nil {
		f.t.Fatal(err)
	}
	return run.ID
}

// clearRemovals empties blob_removals, as the sweep does once it removed
// the files.
func (f *marksFixture) clearRemovals() {
	f.t.Helper()
	if _, err := f.conn.Exec(f.ctx, `DELETE FROM blob_removals`); err != nil {
		f.t.Fatal(err)
	}
}

func (f *marksFixture) removalScheduled(hash string) bool {
	f.t.Helper()
	return countOf(f.t, f.conn, `SELECT count(*) FROM blob_removals WHERE sha256 = $1`, hash) > 0
}

func (f *marksFixture) locations(fo *store.Folder, hash string) int {
	f.t.Helper()
	return countOf(f.t, f.conn, `SELECT count(*) FROM message_locations WHERE folder_id = $1 AND message_sha256 = $2`, fo.ID, hash)
}

func skipped(res store.SaveResult) bool { return res.Skipped == 1 && res.Added == 0 }
func stored(res store.SaveResult) bool  { return res.Skipped == 0 && res.Added == 1 }

// A retention run deletes messages from group A. Whether they are archived
// again in a folder where they would already be expired depends only on
// that folder's account and group, never on what other groups hold or
// held. Each case fails on main as noted.
func TestDeletionMarksRetention(t *testing.T) {
	f := newMarksFixture(t)
	m1, m2, m3, m4 := hashOf('1'), hashOf('2'), hashOf('3'), hashOf('4')
	work := f.expired(f.alice, "work")
	f.save(work, m1, "copied to B later", old, old)
	f.save(work, m2, "only in A", old, old)
	// m3 was in A and in B: the mark names both.
	f.save(work, m3, "in A and B", old, old)
	f.save(f.expired(f.carol, "old"), m3, "in A and B", old, old)
	// m4 was in frank's account, then frank moves to D.
	mover := f.expired(f.frank, "mover")
	f.save(mover, m4, "frank's", old, old)
	f.deleteExpired()
	f.clearRemovals()
	if _, err := f.st.SetUserGroup(f.ctx, f.frank.ID, f.groupD); err != nil {
		t.Fatal(err)
	}

	// B archives m1 again, where it is kept.
	carolKeep := f.folder(f.carol, "keep", "INBOX")
	if res := f.try(carolKeep, m1); !stored(res) {
		t.Fatalf("B archives m1 again: %+v", res)
	}

	second := f.expired(f.alice, "second")
	// The core case: A's other account skips m1 although B holds it now
	// (main stores it: the row exists); the file stays, it is B's.
	if res := f.try(second, m1); !skipped(res) || f.locations(second, m1) != 0 || f.removalScheduled(m1) {
		t.Errorf("A, m1 held by B: %+v, %d locations, removal %v", res, f.locations(second, m1), f.removalScheduled(m1))
	}
	if f.locations(carolKeep, m1) != 1 {
		t.Error("B's copy of m1 changed")
	}
	// Without another copy: skipped, its file is removed.
	if res := f.try(second, m2); !skipped(res) || !f.removalScheduled(m2) {
		t.Errorf("A, m2: %+v, removal %v", res, f.removalScheduled(m2))
	}
	f.clearRemovals()
	// Group C never held them: both stored and new (main skips m2).
	daveOld := f.expired(f.dave, "old")
	for _, m := range []string{m1, m2} {
		if res := f.try(daveOld, m); !stored(res) {
			t.Errorf("C, %s: %+v", m[:1], res)
		}
	}
	// B held m3 too: skipped in B's expired account while B holds no copy;
	// stored there once B archived it again in a kept folder.
	carolSecond := f.expired(f.carol, "second")
	if res := f.try(carolSecond, m3); !skipped(res) {
		t.Errorf("B, m3 without a copy: %+v", res)
	}
	if res := f.try(carolKeep, m3); !stored(res) {
		t.Errorf("B archives m3 again: %+v", res)
	}
	if res := f.try(carolSecond, m3); !stored(res) {
		t.Errorf("B, m3 with B's copy: %+v", res)
	}
	// Frank's account moved to D with frank: still skipped through the
	// account; gina, in D, never held m4: stored (main skips it).
	if res := f.try(mover, m4); !skipped(res) {
		t.Errorf("moved account, m4: %+v", res)
	}
	if res := f.try(f.expired(f.gina, "mail"), m4); !stored(res) {
		t.Errorf("D, m4: %+v", res)
	}
	// Where the period has not ended, A stores them again, with and without
	// another group's copy.
	free := f.folder(f.alice, "free", "INBOX")
	for _, m := range []string{m1, m2} {
		if res := f.try(free, m); !stored(res) {
			t.Errorf("A unruled, %s: %+v", m[:1], res)
		}
	}
	// Then A holds them, so A's expired account stores them too.
	if res := f.try(second, m1); res.Skipped != 0 || f.locations(second, m1) != 1 {
		t.Errorf("A, m1 once A holds it: %+v", res)
	}

	// SMTP: m5, deleted from A and archived again by B, is skipped (250) in
	// a journal of A with an ended rule (main stores it: the row exists),
	// and stored in one of C.
	journal := createSMTPAccount(t, f.st, &f.bob.ID, "journal", "archive@example.com")
	f.rule(&store.Folder{AccountID: journal.ID}, "", 1)
	env := envelope("a@example.com")
	env.ReceivedAt = *old
	m5 := hashOf('5')
	f.save(work, m5, "smtp", old, old)
	f.deleteExpired()
	f.save(carolKeep, m5, "smtp", old, old)
	r, err := f.st.ReceiveMessage(f.ctx, journal.ID, store.MessageMeta{SHA256: m5, Size: 1, StoredPath: "x", SentAt: old}, env, present)
	if err != nil || !r.Skipped || r.New {
		t.Errorf("smtp into A, m5 held by B: %+v %v", r, err)
	}
	daveJournal := createSMTPAccount(t, f.st, &f.dave.ID, "journal", "dave@example.com")
	f.rule(&store.Folder{AccountID: daveJournal.ID}, "", 1)
	r, err = f.st.ReceiveMessage(f.ctx, daveJournal.ID, store.MessageMeta{SHA256: m5, Size: 1, StoredPath: "x", SentAt: old}, env, present)
	if err != nil || r.Skipped || !r.New {
		t.Errorf("smtp into C, m5: %+v %v", r, err)
	}

	// The marks name the accounts and groups that held each message.
	var accounts, groups string
	if err := f.conn.QueryRow(f.ctx, `SELECT account_ids::text, group_ids::text FROM deletion_marks WHERE sha256 = $1`, m3).
		Scan(&accounts, &groups); err != nil {
		t.Fatal(err)
	}
	var groupA int64
	if err := f.conn.QueryRow(f.ctx, `SELECT group_id FROM users WHERE id = $1`, f.alice.ID).Scan(&groupA); err != nil {
		t.Fatal(err)
	}
	if want := "{" + itoa64(groupA) + "," + itoa64(f.groupB) + "}"; groups != want || strings.Count(accounts, ",") != 1 {
		t.Errorf("m3's mark: accounts %s, groups %s, want groups %s", accounts, groups, want)
	}
}

// A purge marks every purged message with the account and its group,
// also one that stays in other accounts and so gets no tombstone.
func TestDeletionMarksPurge(t *testing.T) {
	f := &purgeFixture{retentionFixture: newRetentionFixture(t)}
	m := &marksFixture{retentionFixture: f.retentionFixture, conn: connect(t, f.url)}
	g, err := f.st.CreateGroup(f.ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	carol, err := f.st.CreateUserInGroup(f.ctx, store.NewUser{Name: "carol", PasswordHash: "h", GroupID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.st.CreateGroup(f.ctx, "C")
	if err != nil {
		t.Fatal(err)
	}
	dave, err := f.st.CreateUserInGroup(f.ctx, store.NewUser{Name: "dave", PasswordHash: "h", GroupID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	p1, p2 := hashOf('1'), hashOf('2')
	f.inbox = f.folder(f.alice, "old", "INBOX")
	f.save(f.inbox, p1, "also in B", old, old)
	f.save(f.inbox, p2, "only here", old, old)
	f.save(f.folder(carol, "mail", "INBOX"), p1, "also in B", old, old)
	f.acc = f.account(f.alice, "old")
	if _, err := f.st.DeleteOrRemoveAccount(f.ctx, f.acc.Ref()); err != nil {
		t.Fatal(err)
	}
	f.acc = f.account(f.alice, "old")
	p := f.confirm(1)
	if _, err := f.run(p.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
	var tombstones int
	if err := m.conn.QueryRow(f.ctx, `SELECT count(tombstone_id) FROM deletion_marks WHERE purge_id = $1`, p.ID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, m.conn, `SELECT count(*) FROM deletion_marks WHERE purge_id = $1`, p.ID); n != 2 || tombstones != 1 {
		t.Fatalf("marks %d, with a tombstone %d", n, tombstones)
	}
	m.clearRemovals()

	// A's other account: both skipped. p1, which B holds, was stored on
	// main (the purge left no tombstone); its file stays.
	second := m.expired(f.alice, "second")
	if res := m.try(second, p1); !skipped(res) || m.removalScheduled(p1) {
		t.Errorf("A, p1 held by B: %+v, removal %v", res, m.removalScheduled(p1))
	}
	if res := m.try(second, p2); !skipped(res) || !m.removalScheduled(p2) {
		t.Errorf("A, p2: %+v, removal %v", res, m.removalScheduled(p2))
	}
	// Group C: both stored (main skips p2).
	daveOld := m.expired(dave, "old")
	for _, h := range []string{p1, p2} {
		if res := m.try(daveOld, h); !stored(res) {
			t.Errorf("C, %s: %+v", h[:1], res)
		}
	}
}

// A tombstone without a mark, as written before 1.6.0 or by a process of
// an older version after the upgrade, counts in every group that holds no
// copy, also while another group holds the message.
func TestDeletionMarksLegacyTombstone(t *testing.T) {
	f := newMarksFixture(t)
	work := f.expired(f.alice, "work")
	f.save(work, hashOf('0'), "deleted with a mark", old, old)
	runID := f.deleteExpired()
	legacy := hashOf('9')
	carolKeep := f.folder(f.carol, "keep", "INBOX")
	f.save(carolKeep, legacy, "kept by B", old, old)
	if _, err := f.conn.Exec(f.ctx, `INSERT INTO deleted_messages (sha256, size, message_seq, archived_at, retain_until, locations, run_id)
		SELECT sha256, size, seq, created_at, now(), 1, $2 FROM messages WHERE sha256 = $1`, legacy, runID); err != nil {
		t.Fatal(err)
	}
	for _, fo := range []*store.Folder{f.expired(f.alice, "second"), f.expired(f.dave, "old"), f.expired(f.gina, "mail")} {
		if res := f.try(fo, legacy); !skipped(res) {
			t.Errorf("folder %d: %+v", fo.ID, res)
		}
	}
	// B holds a copy: stored.
	if res := f.try(f.expired(f.carol, "second"), legacy); !stored(res) {
		t.Errorf("B: %+v", res)
	}
}

// 00041: the marks refuse UPDATE, DELETE and TRUNCATE; a second mark of a
// tombstone is refused; Down drops the table, Up again works.
func TestDeletionMarksMigration(t *testing.T) {
	f := newMarksFixture(t)
	work := f.expired(f.alice, "work")
	f.save(work, hashOf('1'), "a", old, old)
	f.deleteExpired()
	for _, sql := range []string{
		`UPDATE deletion_marks SET group_ids = '{99}'`,
		`DELETE FROM deletion_marks`,
		`TRUNCATE deletion_marks`,
		`INSERT INTO deletion_marks (sha256, archived_at, account_ids, group_ids, run_id, tombstone_id)
		 SELECT sha256, archived_at, account_ids, group_ids, run_id, tombstone_id FROM deletion_marks`,
	} {
		if _, err := f.conn.Exec(f.ctx, sql); err == nil {
			t.Errorf("%s: no error", sql)
		}
	}
	if n := countOf(t, f.conn, `SELECT count(*) FROM deletion_marks`); n != 1 {
		t.Fatalf("marks %d", n)
	}
	downDeletionMarksMigration(t, f.st)
	if n := countOf(t, f.conn, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'deletion_marks'`); n != 0 {
		t.Fatalf("table after down: %d", n)
	}
	if _, err := f.st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// After Up again the tombstone has no mark: it counts for every group.
	if res := f.try(f.expired(f.dave, "old"), hashOf('1')); !skipped(res) {
		t.Errorf("C after down and up: %+v", res)
	}
}

// downDeletionMarksMigration rolls back 00041_deletion_marks, the latest
// migration.
func downDeletionMarksMigration(t *testing.T, st *store.Store) {
	t.Helper()
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00041_deletion_marks") {
		t.Fatalf("down: %q %v", m, err)
	}
}

// A save reads its account's group under lockAccountGroup: while a move of
// the owner to another group is in flight (SetUserGroup locks the user FOR
// UPDATE), the save waits, and then decides with the new group. Here gina's
// group D deleted m; frank moves from A to D; his save into an expired
// folder is skipped. Without the lock it would store m with group A.
func TestDeletionMarksSaveWaitsForMove(t *testing.T) {
	f := newMarksFixture(t)
	m := hashOf('7')
	f.save(f.expired(f.gina, "mail"), m, "deleted in D", old, old)
	f.deleteExpired()
	frankOld := f.expired(f.frank, "old")

	move, err := f.conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = move.Rollback(f.ctx) }()
	if _, err := move.Exec(f.ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, f.frank.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := move.Exec(f.ctx, `UPDATE users SET group_id = $2 WHERE id = $1`, f.frank.ID, f.groupD); err != nil {
		t.Fatal(err)
	}
	type result struct {
		res store.SaveResult
		err error
	}
	results := make(chan result, 1)
	done := make(chan error, 1)
	go func() {
		res, err := f.st.SaveBatch(f.ctx, frankOld.ID, 1, []store.MessageMeta{{SHA256: m, Size: 1, StoredPath: "x", SentAt: old}},
			[]store.Location{{FolderID: frankOld.ID, UID: 1, InternalDate: *old}})
		results <- result{res, err}
		done <- err
	}()
	waitForLockWait(t, connect(t, f.url), done)
	if err := move.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	r := <-results
	if r.err != nil || !skipped(r.res) {
		t.Errorf("save after the move: %+v %v", r.res, r.err)
	}
}
