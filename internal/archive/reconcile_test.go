package archive_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
)

// reconcileFixture has alice's account "mail" with INBOX (m1..m3) and
// Archive (m4), synced once without reconciling, owned by user "alice".
type reconcileFixture struct {
	*fixture
	user  *imapmemserver.User
	owner *store.User
	acc   *store.Account
}

func newReconcileFixture(t *testing.T, excluded ...string) *reconcileFixture {
	u := imapmemserver.NewUser("alice", "pw")
	createMailboxes(t, u, "INBOX", "Archive", "Spam")
	for _, id := range []string{"m1", "m2", "m3"} {
		appendMsg(t, u, "INBOX", rawMessage(id, id))
	}
	appendMsg(t, u, "Archive", rawMessage("m4", "m4"))
	appendMsg(t, u, "Spam", rawMessage("s1", "s1"))
	f := newFixture(t, u)
	f.addAccount("mail", "alice", "pw", excluded...)
	owner, err := f.store.CreateUser(f.ctx, "alice", "h", false) // adopts the account
	if err != nil {
		t.Fatal(err)
	}
	acc, err := f.account("mail")
	if err != nil {
		t.Fatal(err)
	}
	rf := &reconcileFixture{fixture: f, user: u, owner: owner, acc: acc}
	if r := rf.run(archive.SyncOptions{}); r.Err != nil || r.Reconcile.Folders != 0 {
		t.Fatalf("first sync: %+v", r)
	}
	return rf
}

func (f *reconcileFixture) run(opts archive.SyncOptions) archive.AccountResult {
	f.t.Helper()
	acc, err := f.store.GetAccount(f.ctx, f.acc.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.syncer.SyncAccountWith(f.ctx, acc, opts)
}

func (f *reconcileFixture) reconcile() archive.AccountResult {
	f.t.Helper()
	return f.run(archive.SyncOptions{Reconcile: true})
}

// gone returns the subjects of the owner's messages that are only in the
// archive.
func (f *reconcileFixture) gone() []string {
	f.t.Helper()
	rows, err := f.store.SearchMessages(f.ctx, store.SearchFilter{Owner: f.owner.ID, Gone: true, Limit: 50})
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Subject)
	}
	slices.Sort(out)
	return out
}

func (f *reconcileFixture) flags(id string) []string {
	f.t.Helper()
	d, err := f.store.GetMessageDetail(f.ctx, f.owner.ID, hashOf(string(rawMessage(id, id))))
	if err != nil {
		f.t.Fatal(err)
	}
	return d.Locations[0].Flags
}

func (f *reconcileFixture) expunge(mailbox string, uids ...uint32) {
	f.t.Helper()
	f.expungeAs("alice", "pw", mailbox, uids...)
}

func (f *reconcileFixture) expungeAs(user, pw, mailbox string, uids ...uint32) {
	f.t.Helper()
	imaptestExpunge(f.t, f.fixture, user, pw, mailbox, uids...)
}

func expectCounts(t *testing.T, r archive.AccountResult, want store.ReconcileCounts) {
	t.Helper()
	if r.Err != nil {
		t.Fatalf("sync: %v", r.Err)
	}
	if r.Reconcile != want {
		t.Fatalf("reconcile counts = %+v, want %+v", r.Reconcile, want)
	}
}

func TestReconcileMarksDeletedAndChangedMessages(t *testing.T) {
	f := newReconcileFixture(t, "Spam")
	f.expunge("INBOX", 2)
	imaptestSetFlags(t, f.fixture, "INBOX", 3, imap.FlagFlagged)
	f.srv.ResetCommands()

	expectCounts(t, f.reconcile(), store.ReconcileCounts{Folders: 2, Gone: 1, FlagsChanged: 1})
	if got := f.gone(); !slices.Equal(got, []string{"m2"}) {
		t.Fatalf("gone = %v", got)
	}
	if got := f.flags("m3"); !slices.Equal(got, []string{`\Flagged`}) {
		t.Errorf("m3 flags = %v", got)
	}
	// Read-only: EXAMINE and FETCH without bodies or with BODY.PEEK only.
	for _, c := range f.srv.Commands() {
		if !strings.HasPrefix(c, "EXAMINE ") && c != "UID FETCH FLAGS" && c != "UID FETCH BODY.PEEK" {
			t.Errorf("archive sent %q", c)
		}
	}
	if !slices.Contains(f.srv.Commands(), "UID FETCH FLAGS") {
		t.Errorf("no flag listing in %v", f.srv.Commands())
	}

	stats, _, err := f.store.Stats(f.ctx, &f.owner.ID)
	if err != nil || len(stats) != 1 || stats[0].Gone != 1 || stats[0].LastReconciledAt == nil {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	runs, err := f.store.LastRuns(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := runs[f.acc.ID]; r.Status != "ok" || r.Reconcile != (store.ReconcileCounts{Folders: 2, Gone: 1, FlagsChanged: 1}) {
		t.Errorf("last run = %+v", r)
	}
	// Nothing new: nothing changes.
	expectCounts(t, f.reconcile(), store.ReconcileCounts{Folders: 2})
}

func TestReconcileHiddenMessageComesBack(t *testing.T) {
	f := newReconcileFixture(t)
	f.srv.Hide("INBOX", 2)
	expectCounts(t, f.reconcile(), store.ReconcileCounts{Folders: 3, Gone: 1})
	if got := f.gone(); !slices.Equal(got, []string{"m2"}) {
		t.Fatalf("gone = %v", got)
	}
	f.srv.Unhide("INBOX", 2)
	expectCounts(t, f.reconcile(), store.ReconcileCounts{Folders: 3, Back: 1})
	if got := f.gone(); len(got) != 0 {
		t.Fatalf("gone after unhide = %v", got)
	}
}

// A renumbered folder is rescanned; the messages still there are not gone.
func TestReconcileAfterUIDValidityChange(t *testing.T) {
	f := newReconcileFixture(t)
	if err := f.user.Delete("Archive"); err != nil {
		t.Fatal(err)
	}
	createMailboxes(t, f.user, "Archive")
	appendMsg(t, f.user, "Archive", rawMessage("m4", "m4"))
	r := f.reconcile()
	expectCounts(t, r, store.ReconcileCounts{Folders: 3, Gone: 1}) // the superseded location
	if got := f.gone(); len(got) != 0 {
		t.Fatalf("gone after renumbering = %v", got)
	}
	stats, _, err := f.store.Stats(f.ctx, &f.owner.ID)
	if err != nil || stats[0].Gone != 0 {
		t.Fatalf("goneMessages = %+v, %v", stats, err)
	}
}

func TestReconcileVanishedAndExcludedFolders(t *testing.T) {
	f := newReconcileFixture(t)
	// Exclude Spam after it was archived: its state is unknown, not gone.
	if err := f.store.UpdateAccount(f.ctx, f.acc.Ref(), store.AccountChange{Folders: &store.FolderFilters{Excluded: []string{"Spam"}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.user.Delete("Spam"); err != nil {
		t.Fatal(err)
	}
	if err := f.user.Delete("Archive"); err != nil {
		t.Fatal(err)
	}
	expectCounts(t, f.reconcile(), store.ReconcileCounts{Folders: 2, Gone: 1})
	if got := f.gone(); !slices.Equal(got, []string{"m4"}) {
		t.Fatalf("gone = %v", got)
	}
	if !strings.Contains(f.logs.String(), `msg="folder no longer on the server" account=mail folder=Archive gone=1`) {
		t.Errorf("no warning for the vanished folder:\n%s", f.logs.String())
	}
}

func TestReconcileFailureWritesNothing(t *testing.T) {
	f := newReconcileFixture(t)
	f.expunge("INBOX", 1)
	f.srv.FailFlagFetch("INBOX", true)
	r := f.reconcile()
	if r.Err == nil || r.Reconcile.Folders != 2 {
		t.Fatalf("result = %+v", r)
	}
	if got := f.gone(); len(got) != 0 {
		t.Fatalf("gone after a failed listing = %v", got)
	}
	runs, _ := f.store.LastRuns(f.ctx)
	if s := runs[f.acc.ID].Status; s != "partial" {
		t.Errorf("status = %s, want partial", s)
	}
	h, err := f.store.SyncHealth(f.ctx, nil)
	if err != nil || h[f.acc.ID].FailureStreak != 0 {
		t.Errorf("a reconcile error counted as a failed sync: %+v %v", h[f.acc.ID], err)
	}
	// INBOX was not reconciled, so it is due on the next plain sync.
	f.srv.FailFlagFetch("INBOX", false)
	f.syncer.ReconcileInterval = 24 * time.Hour
	expectCounts(t, f.run(archive.SyncOptions{}), store.ReconcileCounts{Folders: 1, Gone: 1})
}

func TestReconcileFolderLimit(t *testing.T) {
	f := newReconcileFixture(t)
	f.syncer.MaxReconcileMessages = 2
	r := f.reconcile()
	if r.Err == nil || !strings.Contains(r.Err.Error(), "INBOX: reconcile: 3 messages, more than the limit of 2") {
		t.Fatalf("err = %v", r.Err)
	}
	if r.Reconcile.Folders != 2 {
		t.Errorf("other folders not reconciled: %+v", r.Reconcile)
	}
}

func TestReconcileInterval(t *testing.T) {
	f := newReconcileFixture(t)
	// Off: a plain sync reconciles nothing.
	expectCounts(t, f.run(archive.SyncOptions{}), store.ReconcileCounts{})
	// Daily: every folder is due once.
	f.syncer.ReconcileInterval = 24 * time.Hour
	expectCounts(t, f.run(archive.SyncOptions{}), store.ReconcileCounts{Folders: 3})
	expectCounts(t, f.run(archive.SyncOptions{}), store.ReconcileCounts{})
	// The web button skips folders reconciled in the last minutes.
	expectCounts(t, f.run(archive.SyncOptions{Reconcile: true, ReconcileMinAge: 5 * time.Minute}), store.ReconcileCounts{})
	// The command line forces all.
	expectCounts(t, f.reconcile(), store.ReconcileCounts{Folders: 3})
}

func TestReconcileLogsLargeLosses(t *testing.T) {
	f := newReconcileFixture(t)
	f.expunge("INBOX", 1)
	f.reconcile()
	if !strings.Contains(f.logs.String(), `msg="many messages no longer on the server" account=mail folder=INBOX gone=1 before=3`) {
		t.Errorf("no warning:\n%s", f.logs.String())
	}
	if !strings.Contains(f.logs.String(), `msg="account reconciled" account=mail folders=3 gone=1 back=0 flags=0`) {
		t.Errorf("no summary:\n%s", f.logs.String())
	}
	if strings.Contains(f.logs.String(), "m1") {
		t.Errorf("log names a message:\n%s", f.logs.String())
	}
}

// Two reconciles at once: one runs, the other is skipped.
func TestReconcileTakesTheSyncLock(t *testing.T) {
	f := newReconcileFixture(t)
	var wg sync.WaitGroup
	results := make([]archive.AccountResult, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acc, _ := f.store.GetAccount(f.ctx, f.acc.ID)
			results[i] = f.syncer.SyncAccountWith(f.ctx, acc, archive.SyncOptions{Reconcile: true})
		}()
	}
	wg.Wait()
	ran, skipped := 0, 0
	for _, r := range results {
		switch {
		case r.Err == nil && r.Reconcile.Folders == 3:
			ran++
		case errors.Is(r.Err, archive.ErrSyncRunning):
			skipped++
		}
	}
	// Both may run one after the other; never both at once.
	if ran+skipped != 2 || ran == 0 {
		t.Fatalf("results = %+v", results)
	}
}

func imaptestExpunge(t *testing.T, f *fixture, user, pw, mailbox string, uids ...uint32) {
	t.Helper()
	imaptest.Expunge(t, f.host, f.port, user, pw, mailbox, uids...)
}

func imaptestSetFlags(t *testing.T, f *fixture, mailbox string, uid uint32, flags ...imap.Flag) {
	t.Helper()
	imaptest.SetFlags(t, f.host, f.port, "alice", "pw", mailbox, uid, flags...)
}
