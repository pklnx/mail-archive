package store_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// old is a date whose 1-year period has long ended.
var old = ts("2010-03-05T10:00:00Z")

// expiredFixture has alice's account "work" with a 1-year rule and
// messages a and b from 2010 in INBOX.
func expiredFixture(t *testing.T) (*retentionFixture, *store.Folder) {
	t.Helper()
	f := newRetentionFixture(t)
	inbox := f.folder(f.alice, "work", "INBOX")
	f.rule(inbox, "", 1)
	f.save(inbox, hashOf('a'), "a", old, old)
	f.save(inbox, hashOf('b'), "b", old, old)
	return f, inbox
}

func (f *retentionFixture) propose() *store.DeletionRun {
	f.t.Helper()
	run, err := f.st.ProposeDeletion(f.ctx, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return run
}

func (f *retentionFixture) candidates(runID int64) []string {
	f.t.Helper()
	shas, err := f.st.PendingCandidates(f.ctx, runID, "", 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return shas
}

func (f *retentionFixture) confirm(runID int64) {
	f.t.Helper()
	if err := f.st.ConfirmDeletionRun(f.ctx, runID, nil); err != nil {
		f.t.Fatal(err)
	}
}

func TestDeletionGuards(t *testing.T) {
	f := newRetentionFixture(t)
	inbox := f.folder(f.alice, "work", "INBOX")
	f.save(inbox, hashOf('a'), "a", old, old)
	c := connect(t, f.url)
	exec := func(sql string, args ...any) error {
		_, err := c.Exec(f.ctx, sql, args...)
		return err
	}
	// No rule: the location is kept for ever.
	if err := exec("DELETE FROM message_locations WHERE message_sha256 = $1", hashOf('a')); err == nil ||
		!strings.Contains(err.Error(), "still retained or on legal hold") {
		t.Errorf("unruled location: %v", err)
	}
	// An ended rule, but a hold.
	f.rule(inbox, "", 1)
	hold := f.hold(store.NewLegalHold{OwnerID: &f.alice.ID})
	if err := exec("DELETE FROM message_locations WHERE message_sha256 = $1", hashOf('a')); err == nil {
		t.Error("held location deleted")
	}
	_ = f.st.ReleaseLegalHold(f.ctx, hold, nil, "done")
	// No tombstone: the message row stays, also after its locations went.
	if err := exec("DELETE FROM message_locations WHERE message_sha256 = $1", hashOf('a')); err != nil {
		t.Fatalf("expired location: %v", err)
	}
	if err := exec("DELETE FROM messages WHERE sha256 = $1", hashOf('a')); err == nil ||
		!strings.Contains(err.Error(), "has no deletion record") {
		t.Errorf("message without tombstone: %v", err)
	}
}

func TestDeletionRunLifecycle(t *testing.T) {
	f, _ := expiredFixture(t)
	// The same message a in bob's account without a rule: never a
	// candidate (bool_and over an unruled location, #55).
	other := f.folder(f.bob, "private", "INBOX")
	f.save(other, hashOf('a'), "a", old, old)

	run := f.propose()
	if run.State != store.RunProposed || run.Candidates != 1 || run.Bytes != 1 {
		t.Fatalf("run %+v", run)
	}
	if got := f.candidates(run.ID); !slices.Equal(got, []string{hashOf('b')}) {
		t.Fatalf("candidates %v", got)
	}
	groups, err := f.st.DeletionRunSummary(f.ctx, run.ID)
	if err != nil || len(groups) != 1 || groups[0].Owner != "alice" || groups[0].Account != "work" || groups[0].Year != 2011 || groups[0].Messages != 1 {
		t.Fatalf("summary %+v %v", groups, err)
	}
	details, err := f.st.DeletionCandidates(f.ctx, run.ID)
	if err != nil || len(details) != 1 || details[0].Subject != "b" || !slices.Equal(details[0].Places, []string{"alice/work/INBOX"}) {
		t.Fatalf("details %+v %v", details, err)
	}

	if _, err := f.st.ProposeDeletion(f.ctx, nil); !errors.Is(err, store.ErrRunOpen) {
		t.Errorf("second run: %v", err)
	}
	if err := f.st.CancelDeletionRun(f.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ConfirmDeletionRun(f.ctx, run.ID, nil); !errors.Is(err, store.ErrRunState) {
		t.Errorf("confirm a cancelled run: %v", err)
	}
	if err := f.st.ConfirmDeletionRun(f.ctx, 999, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("confirm an unknown run: %v", err)
	}

	run = f.propose()
	if _, _, err := f.st.DeleteBatch(f.ctx, run.ID, f.candidates(run.ID), 0); !errors.Is(err, store.ErrRunState) {
		t.Errorf("batch of a proposed run: %v", err)
	}
	f.confirm(run.ID)
	deleted, kept, err := f.st.DeleteBatch(f.ctx, run.ID, f.candidates(run.ID), time.Hour)
	if err != nil || deleted != 1 || kept != 0 {
		t.Fatalf("batch: %d %d %v", deleted, kept, err)
	}
	if err := f.st.FinishDeletionRun(f.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	done, err := f.st.DeletionRun(f.ctx, run.ID)
	if err != nil || done.State != store.RunDone || done.Deleted != 1 || done.FinishedAt == nil {
		t.Fatalf("done %+v %v", done, err)
	}
	if _, err := f.st.GetMessageDetail(f.ctx, storetest.Viewer(t, f.st, f.alice.ID), hashOf('b')); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("b still there: %v", err)
	}
	// The tombstone and the attestation's deletion record match; the file
	// waits for the grace period.
	c := connect(t, f.url)
	var tomb, del string
	if err := c.QueryRow(f.ctx, "SELECT sha256 FROM deleted_messages").Scan(&tomb); err != nil {
		t.Fatal(err)
	}
	if err := c.QueryRow(f.ctx, "SELECT sha256 FROM message_deletions WHERE reason = 'retention'").Scan(&del); err != nil {
		t.Fatal(err)
	}
	if tomb != hashOf('b') || del != hashOf('b') {
		t.Errorf("tombstone %s, deletion %s", tomb, del)
	}
	if due, err := f.st.BlobRemovalDue(f.ctx); err != nil || due {
		t.Errorf("due within the grace: %v %v", due, err)
	}
	if _, err := c.Exec(f.ctx, "UPDATE deleted_messages SET size = 2"); err == nil {
		t.Error("tombstone changed")
	}
	if counts, err := f.st.DeletionCounts(f.ctx); err != nil || counts.PendingFiles != 1 || counts.OpenRun != 0 {
		t.Errorf("counts %+v %v", counts, err)
	}
}

// The run's year is the period's last year also for a rule zone west of
// UTC: a New York period ends at 1 January 2012 05:00 UTC, its last year is
// 2011 (#406).
func TestDeletionRunSummaryRuleZoneWest(t *testing.T) {
	f := newRetentionFixture(t)
	inbox := f.folder(f.alice, "work", "INBOX")
	if _, err := f.st.AddRetentionRule(f.ctx, store.NewRetentionRule{AccountID: inbox.AccountID, Years: 1, TimeZone: "America/New_York"}); err != nil {
		t.Fatal(err)
	}
	f.save(inbox, hashOf('a'), "a", old, old)
	run := f.propose()
	groups, err := f.st.DeletionRunSummary(f.ctx, run.ID)
	if err != nil || len(groups) != 1 || groups[0].Year != 2011 || groups[0].Messages != 1 {
		t.Fatalf("summary %+v %v", groups, err)
	}
}

// Between the preview and the deletion, a hold added or a new location
// keeps the candidate.
func TestDeletionRechecks(t *testing.T) {
	f := newRetentionFixture(t)
	inbox := f.folder(f.alice, "work", "INBOX")
	f.rule(inbox, "", 1)
	f.save(inbox, hashOf('a'), "a", old, old)
	b, c := ts("2011-02-01T10:00:00Z"), ts("2011-06-01T10:00:00Z")
	f.save(inbox, hashOf('b'), "b", b, b)
	f.save(inbox, hashOf('c'), "c", c, c)
	run := f.propose()
	if run.Candidates != 3 {
		t.Fatalf("candidates %d", run.Candidates)
	}
	f.confirm(run.ID)

	// a: a hold on its period; b: a new location in an unruled account.
	f.hold(store.NewLegalHold{PeriodStart: ts("2010-01-01T00:00:00Z"), PeriodEnd: ts("2011-01-01T00:00:00Z")})
	priv := f.folder(f.bob, "private", "INBOX")
	f.save(priv, hashOf('b'), "b", b, b)
	deleted, kept, err := f.st.DeleteBatch(f.ctx, run.ID, f.candidates(run.ID), 0)
	if err != nil || deleted != 1 || kept != 2 {
		t.Fatalf("batch: %d %d %v", deleted, kept, err)
	}
	for h, want := range map[string]bool{hashOf('a'): false, hashOf('b'): false, hashOf('c'): true} {
		_, err := f.st.GetMessageDetail(f.ctx, storetest.Viewer(t, f.st, f.alice.ID), h)
		if gone := errors.Is(err, store.ErrNotFound); gone != want {
			t.Errorf("%c gone %v, want %v", h[0], gone, want)
		}
	}
	if n, _ := f.st.PendingCandidates(f.ctx, run.ID, "", 10); len(n) != 0 {
		t.Errorf("pending after the batch: %v", n)
	}
}

func TestDeletionKeepsWhenRuleRemoved(t *testing.T) {
	f, _ := expiredFixture(t)
	run := f.propose()
	f.confirm(run.ID)
	rules, err := f.st.RetentionRules(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.RemoveRetentionRule(f.ctx, rules[0].ID); err != nil {
		t.Fatal(err)
	}
	deleted, kept, err := f.st.DeleteBatch(f.ctx, run.ID, f.candidates(run.ID), 0)
	if err != nil || deleted != 0 || kept != 2 {
		t.Fatalf("batch after the rule went: %d %d %v", deleted, kept, err)
	}
}

// A deleted message comes back where it would already be expired: it is
// skipped and its file scheduled for removal; in an unruled folder it is
// archived again.
func TestRearchiveAfterDeletion(t *testing.T) {
	f, inbox := expiredFixture(t)
	run := f.propose()
	f.confirm(run.ID)
	// No grace, so the removal is due at once. (A skipped message keeps
	// the later of the two due times.)
	if _, _, err := f.st.DeleteBatch(f.ctx, run.ID, f.candidates(run.ID), 0); err != nil {
		t.Fatal(err)
	}

	f.uid++
	res, err := f.st.SaveBatch(f.ctx, inbox.ID, f.uid, []store.MessageMeta{{SHA256: hashOf('a'), Size: 1, StoredPath: "x", SentAt: old}},
		[]store.Location{{FolderID: inbox.ID, UID: f.uid, InternalDate: *old}})
	if err != nil || res.Skipped != 1 || res.New != 0 {
		t.Fatalf("expired folder: %+v %v", res, err)
	}
	if due, err := f.st.BlobRemovalDue(f.ctx); err != nil || !due {
		t.Errorf("skipped file not due: %v %v", due, err)
	}

	other := f.folder(f.bob, "private", "INBOX")
	f.uid++
	res, err = f.st.SaveBatch(f.ctx, other.ID, f.uid, []store.MessageMeta{{SHA256: hashOf('a'), Size: 1, StoredPath: "x", SentAt: old}},
		[]store.Location{{FolderID: other.ID, UID: f.uid, InternalDate: *old}})
	if err != nil || res.Skipped != 0 || res.New != 1 {
		t.Fatalf("unruled folder: %+v %v", res, err)
	}
	rows, err := f.st.DueBlobRemovals(f.ctx, time.Time{}, "", 10)
	archived := map[string]bool{}
	for _, r := range rows {
		archived[r.SHA256] = r.Archived
	}
	if err != nil || len(rows) != 2 || !archived[hashOf('a')] || archived[hashOf('b')] {
		t.Fatalf("due removals %+v %v", rows, err)
	}

	// Over SMTP into an account with an ended rule: skipped, 250.
	journal := createSMTPAccount(t, f.st, &f.alice.ID, "journal", "archive@example.com")
	f.rule(&store.Folder{AccountID: journal.ID}, "", 1)
	meta := store.MessageMeta{SHA256: hashOf('b'), Size: 1, StoredPath: "x", SentAt: old}
	env := envelope("a@example.com")
	env.ReceivedAt = *old
	r, err := f.st.ReceiveMessage(f.ctx, journal.ID, meta, env, present)
	if err != nil || !r.Skipped || r.New {
		t.Fatalf("smtp: %+v %v", r, err)
	}
}
