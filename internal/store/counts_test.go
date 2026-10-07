package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// Counts follow the message lists after a UIDVALIDITY change: a message with
// an old and a new location counts once, a message the server deleted before
// the change still counts, and old locations are marked superseded.
func TestCountsAfterUIDValidityChange(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, "bob", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	folder := func(owner *store.User, account string) *store.Folder {
		t.Helper()
		a := &store.Account{Name: account, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &owner.ID}
		if err := st.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
		f, err := st.GetOrCreateFolder(ctx, a.ID, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	sha := func(c byte) string { return strings.Repeat(string(c), 64) }
	save := func(f *store.Folder, uidValidity, uid uint32, hash string) {
		t.Helper()
		meta := store.MessageMeta{SHA256: hash, Size: 1, StoredPath: "x"}
		loc := store.Location{FolderID: f.ID, UIDValidity: uidValidity, UID: uid, InternalDate: time.Now()}
		if _, err := st.SaveBatch(ctx, f.ID, uid, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
			t.Fatal(err)
		}
	}

	inbox := folder(alice, "mail")
	if err := st.ResetFolder(ctx, inbox.ID, 1); err != nil {
		t.Fatal(err)
	}
	save(inbox, 1, 1, sha('a'))
	save(inbox, 1, 2, sha('b'))
	save(inbox, 1, 3, sha('a')) // the same bytes under a second UID
	// The server renumbers the folder; b was deleted before, a is rescanned.
	if err := st.ResetFolder(ctx, inbox.ID, 2); err != nil {
		t.Fatal(err)
	}
	save(inbox, 2, 1, sha('a'))
	// Bob has the same message; it changes nothing for Alice.
	bobInbox := folder(bob, "mail")
	if err := st.ResetFolder(ctx, bobInbox.ID, 7); err != nil {
		t.Fatal(err)
	}
	save(bobInbox, 7, 1, sha('a'))

	folders, err := st.ListAccountFolders(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || len(folders[0].Folders) != 1 || folders[0].Folders[0].Messages != 2 {
		t.Fatalf("folder counts: %+v", folders)
	}
	stats, _, err := st.Stats(ctx, &alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Messages != 2 {
		t.Fatalf("account stats: %+v", stats)
	}

	// b only has its old location: it still counts, marked superseded.
	d, err := st.GetMessageDetail(ctx, alice.ID, sha('b'))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Locations) != 1 || !d.Locations[0].Superseded {
		t.Fatalf("locations of b: %+v", d.Locations)
	}
	// a: the current location first, then the two old ones.
	d, err = st.GetMessageDetail(ctx, alice.ID, sha('a'))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Locations) != 3 || d.Locations[0].Superseded || !d.Locations[1].Superseded || !d.Locations[2].Superseded {
		t.Fatalf("locations of a: %+v", d.Locations)
	}
	// Owner scoping is unchanged.
	d, err = st.GetMessageDetail(ctx, bob.ID, sha('a'))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Locations) != 1 || d.Locations[0].Superseded {
		t.Fatalf("Bob's locations of a: %+v", d.Locations)
	}
	if _, err := st.GetMessageDetail(ctx, bob.ID, sha('b')); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Bob sees b: %v", err)
	}
}
