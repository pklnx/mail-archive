package archive_test

import (
	"errors"
	"testing"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
)

func TestRenameAccount(t *testing.T) {
	u := imapmemserver.NewUser("u", "pw")
	createMailboxes(t, u, "INBOX")
	appendMsg(t, u, "INBOX", rawMessage("m1", "Hello"))
	f := newFixture(t, u)
	f.addAccount("postmaster@example.com", "u", "pw")
	a, _ := f.store.GetAccountByName(f.ctx, "postmaster@example.com")

	if err := archive.RenameAccount(f.ctx, f.store, f.sealer, a, "bad/name"); err == nil {
		t.Fatal("invalid name accepted")
	}

	// Not while the account is being synced.
	unlock, ok, err := f.store.TryLockSync(f.ctx, a.ID)
	if err != nil || !ok {
		t.Fatal("lock", ok, err)
	}
	if err := archive.RenameAccount(f.ctx, f.store, f.sealer, a, "example"); !errors.Is(err, archive.ErrSyncRunning) {
		t.Fatalf("rename during sync: %v", err)
	}
	unlock()

	if err := archive.RenameAccount(f.ctx, f.store, f.sealer, a, "example"); err != nil {
		t.Fatal(err)
	}
	if a.Name != "example" {
		t.Fatalf("name = %q", a.Name)
	}
	// The password was encrypted for the new name: the account still syncs.
	res := f.sync()
	expectResult(t, res["example"], 1, 1)
}
