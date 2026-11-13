package archive_test

import (
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
)

// Deletion marks per customer group (#539, part 1) through a sync from the
// test IMAP server and a fill. Retention deletes "target" and "second"
// from alice's account (group A); carol (group B) then archives target
// again. A sync and a fill into accounts of group A with an ended rule
// skip both; into accounts of group C (dave) they store both. On main the
// sync and the fill of group A store target (B's row exists), and those of
// group C skip second.
func TestDeletionMarksSyncAndFill(t *testing.T) {
	at := time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC)
	alice := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, alice, "INBOX")
	imaptest.AppendAt(t, alice, "INBOX", fillMessage("target"), at)
	imaptest.AppendAt(t, alice, "INBOX", fillMessage("second"), at)
	other := imapmemserver.NewUser("other@example.com", "secret")
	imaptest.CreateMailboxes(t, other, "INBOX")
	imaptest.AppendAt(t, other, "INBOX", fillMessage("target"), at)
	f := newFixture(t, alice, other)

	userA, err := f.store.CreateUser(f.ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	inGroup := func(name string) *store.User {
		g, err := f.store.CreateGroup(f.ctx, "group-"+name)
		if err != nil {
			t.Fatal(err)
		}
		u, err := f.store.CreateUserInGroup(f.ctx, store.NewUser{Name: name, PasswordHash: "h", GroupID: g.ID})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	carol, dave := inGroup("carol"), inGroup("dave")
	rule := func(a *store.Account) {
		if _, err := f.store.AddRetentionRule(f.ctx, store.NewRetentionRule{AccountID: a.ID, Years: 1}); err != nil {
			t.Fatal(err)
		}
	}
	imapAccount := func(name, user string, owner *store.User, ruled bool) *store.Account {
		a := &store.Account{Name: name, Host: f.host, Port: f.port, TLSMode: store.TLSModeNone, Username: user, Enabled: true, OwnerID: &owner.ID}
		seal := func(id int64) ([]byte, error) { return archive.SealPassword(f.sealer, id, "secret") }
		if err := f.store.CreateAccountSealed(f.ctx, a, seal); err != nil {
			t.Fatal(err)
		}
		if ruled {
			rule(a)
		}
		return a
	}
	sync := func(a *store.Account) archive.AccountResult {
		t.Helper()
		r := f.syncer.SyncAccount(f.ctx, a)
		if r.Err != nil {
			t.Fatalf("sync %s: %v", a.Name, r.Err)
		}
		return r
	}

	if r := sync(imapAccount("work", "alice@example.com", userA, true)); r.New != 2 {
		t.Fatalf("first sync %+v", r)
	}
	run, err := f.store.ProposeDeletion(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ConfirmDeletionRun(f.ctx, run.ID, nil); err != nil {
		t.Fatal(err)
	}
	if done, err := archive.ExecuteDeletionRun(f.ctx, f.store, run.ID, archive.DeletionOptions{}); err != nil || done.Deleted != 2 {
		t.Fatalf("run %+v %v", done, err)
	}
	if r := sync(imapAccount("kept", "other@example.com", carol, false)); r.New != 1 {
		t.Fatalf("B archives target again: %+v", r)
	}

	if r := sync(imapAccount("again", "alice@example.com", userA, true)); r.Skipped != 2 || r.New != 0 {
		t.Errorf("sync into A: %+v", r)
	}
	journalA := &store.Account{Name: "journal", Enabled: true, OwnerID: &userA.ID}
	if err := f.store.CreateSMTPAccount(f.ctx, journalA, []string{"archive@archive.local"}); err != nil {
		t.Fatal(err)
	}
	rule(journalA)
	got, err := f.filler().Run(f.ctx, f.createFill(journalA, "secret").ID)
	if err != nil {
		t.Fatal(err)
	}
	if c := folderOf(got, "INBOX").Counts; c.Deleted != 2 || c.Imported != 0 {
		t.Errorf("fill into A: %+v", c)
	}

	if r := sync(imapAccount("dave", "alice@example.com", dave, true)); r.Skipped != 0 || r.New != 2 {
		t.Errorf("sync into C: %+v", r)
	}
	journalC := &store.Account{Name: "journal", Enabled: true, OwnerID: &dave.ID}
	if err := f.store.CreateSMTPAccount(f.ctx, journalC, []string{"dave@archive.local"}); err != nil {
		t.Fatal(err)
	}
	rule(journalC)
	got, err = f.filler().Run(f.ctx, f.createFill(journalC, "secret").ID)
	if err != nil {
		t.Fatal(err)
	}
	if c := folderOf(got, "INBOX").Counts; c.Deleted != 0 || c.Imported != 2 {
		t.Errorf("fill into C: %+v", c)
	}
}
