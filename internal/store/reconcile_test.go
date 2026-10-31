package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

type reconcileFixture struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	url   string
	alice *store.User
	bob   *store.User
}

func newReconcileFixture(t *testing.T) *reconcileFixture {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, "bob", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	return &reconcileFixture{t: t, ctx: ctx, st: st, url: url, alice: alice, bob: bob}
}

func hashOf(c byte) string { return strings.Repeat(string(c), 64) }

// folder creates an account of owner with one folder at UIDVALIDITY 1.
func (f *reconcileFixture) folder(owner *store.User, account, name string, kind store.AccountKind) *store.Folder {
	f.t.Helper()
	a := &store.Account{Kind: kind, Name: account, OwnerID: &owner.ID}
	if kind == store.KindIMAP {
		a.Host, a.Port, a.TLSMode, a.Username, a.PasswordEnc = "h", 993, store.TLSModeTLS, "u", []byte{1}
	}
	if existing, err := f.st.GetOwnedAccount(f.ctx, owner.ID, account); err == nil {
		a = existing
	} else if err := f.st.CreateAccount(f.ctx, a); err != nil {
		f.t.Fatal(err)
	}
	fo, err := f.st.GetOrCreateFolder(f.ctx, a.ID, name)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.ResetFolder(f.ctx, fo.ID, 1); err != nil {
		f.t.Fatal(err)
	}
	fo.UIDValidity = 1
	return fo
}

func (f *reconcileFixture) save(fo *store.Folder, uid uint32, hash string, flags ...string) {
	f.t.Helper()
	meta := store.MessageMeta{SHA256: hash, Size: 1, StoredPath: "x"}
	loc := store.Location{FolderID: fo.ID, UIDValidity: fo.UIDValidity, UID: uid, Flags: flags, InternalDate: time.Now()}
	if _, err := f.st.SaveBatch(f.ctx, fo.ID, uid, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *reconcileFixture) reconcile(fo *store.Folder, listed map[uint32]string) store.FolderReconcile {
	f.t.Helper()
	var uids []int64
	var flags []string
	for uid, fl := range listed {
		uids = append(uids, int64(uid))
		flags = append(flags, fl)
	}
	r, err := f.st.ReconcileFolder(f.ctx, fo.ID, fo.UIDValidity, uids, flags)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

// locations returns "folder/uid:flags[:gone]" of the owner's locations of a message.
func (f *reconcileFixture) locations(owner *store.User, hash string) []string {
	f.t.Helper()
	d, err := f.st.GetMessageDetail(f.ctx, storetest.Viewer(f.t, f.st, owner.ID), hash)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, l := range d.Locations {
		s := l.Folder + "/" + itoa(l.UID) + ":" + strings.Join(l.Flags, ",")
		if l.GoneAt != nil {
			s += ":gone"
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

func (f *reconcileFixture) gone(owner *store.User, account string) []string {
	f.t.Helper()
	sc := storetest.Viewer(f.t, f.st, owner.ID)
	if account != "" {
		sc = sc.OwnAccount(account)
	}
	rows, err := f.st.SearchMessages(f.ctx, store.SearchFilter{Scope: sc, Gone: true, Limit: 50})
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.SHA256[:1])
	}
	slices.Sort(out)
	return out
}

func TestReconcileFolder(t *testing.T) {
	f := newReconcileFixture(t)
	inbox := f.folder(f.alice, "mail", "INBOX", store.KindIMAP)
	other := f.folder(f.alice, "mail", "Archive", store.KindIMAP)
	f.save(inbox, 1, hashOf('a'), `\Seen`)
	f.save(inbox, 2, hashOf('b'), `\Seen`, `\Recent`)
	f.save(inbox, 3, hashOf('c'))
	f.save(other, 1, hashOf('d'))

	// b keeps its flags (\Recent does not count), c gets \Flagged, a is
	// gone. UID 4 arrived after the sync and has no location yet.
	r := f.reconcile(inbox, map[uint32]string{2: `\Seen`, 3: `\Flagged`, 4: ""})
	if want := (store.FolderReconcile{PresentBefore: 3, Gone: 1, FlagsChanged: 1}); r != want {
		t.Fatalf("first reconcile = %+v, want %+v", r, want)
	}
	if got := f.locations(f.alice, hashOf('a')); !slices.Equal(got, []string{`INBOX/1:\Seen:gone`}) {
		t.Errorf("a: %v", got)
	}
	if got := f.locations(f.alice, hashOf('c')); !slices.Equal(got, []string{`INBOX/3:\Flagged`}) {
		t.Errorf("c: %v", got)
	}
	if got := f.locations(f.alice, hashOf('d')); !slices.Equal(got, []string{`Archive/1:`}) {
		t.Errorf("another folder changed: %v", got)
	}
	if got := f.gone(f.alice, ""); !slices.Equal(got, []string{"a"}) {
		t.Errorf("gone = %v", got)
	}

	// Nothing changed: nothing is written.
	if r := f.reconcile(inbox, map[uint32]string{2: `\Seen`, 3: `\Flagged`}); r != (store.FolderReconcile{PresentBefore: 2}) {
		t.Errorf("unchanged reconcile = %+v", r)
	}
	// a is listed again under its UID: it is back, with the listed flags.
	r = f.reconcile(inbox, map[uint32]string{1: `\Answered`, 2: `\Seen`, 3: `\Flagged`})
	if r != (store.FolderReconcile{PresentBefore: 2, Back: 1}) {
		t.Errorf("reappear = %+v", r)
	}
	if got := f.locations(f.alice, hashOf('a')); !slices.Equal(got, []string{`INBOX/1:\Answered`}) {
		t.Errorf("a after reappearing: %v", got)
	}
	if got := f.gone(f.alice, ""); len(got) != 0 {
		t.Errorf("gone after reappearing = %v", got)
	}
}

// Locations above the folder's last UID are not judged: the sync has not
// stored them yet. Locations of an older UIDVALIDITY are gone.
func TestReconcileLastUIDAndSuperseded(t *testing.T) {
	f := newReconcileFixture(t)
	inbox := f.folder(f.alice, "mail", "INBOX", store.KindIMAP)
	f.save(inbox, 1, hashOf('a'))
	f.save(inbox, 2, hashOf('b'))
	if err := f.st.ResetFolder(f.ctx, inbox.ID, 2); err != nil {
		t.Fatal(err)
	}
	inbox.UIDValidity = 2
	f.save(inbox, 1, hashOf('a')) // still on the server, renumbered
	r := f.reconcile(inbox, map[uint32]string{1: ""})
	if r != (store.FolderReconcile{PresentBefore: 1, Superseded: 2}) {
		t.Fatalf("reconcile = %+v", r)
	}
	// b was deleted before the change: only in the archive. a is not.
	if got := f.gone(f.alice, ""); !slices.Equal(got, []string{"b"}) {
		t.Errorf("gone = %v", got)
	}
	d, err := f.st.GetMessageDetail(f.ctx, storetest.Viewer(t, f.st, f.alice.ID), hashOf('a'))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Locations) != 2 || d.Locations[0].Superseded || d.Locations[0].GoneAt != nil ||
		!d.Locations[1].Superseded || d.Locations[1].GoneAt == nil {
		t.Errorf("locations of a: %+v", d.Locations)
	}

	// The UIDVALIDITY moved on meanwhile: nothing is written.
	if err := f.st.ResetFolder(f.ctx, inbox.ID, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ReconcileFolder(f.ctx, inbox.ID, 2, nil, nil); !errors.Is(err, store.ErrFolderChanged) {
		t.Fatalf("err = %v, want ErrFolderChanged", err)
	}
	if got := f.gone(f.alice, ""); !slices.Equal(got, []string{"b"}) {
		t.Errorf("gone after refused reconcile = %v", got)
	}

	// UIDs above last_uid are left alone even if the server does not list them.
	f2 := f.folder(f.alice, "mail", "Sent", store.KindIMAP)
	f.save(f2, 5, hashOf('c'))
	if _, err := rawConn(t, f.url).Exec(f.ctx, "UPDATE folders SET last_uid = 4 WHERE id = $1", f2.ID); err != nil {
		t.Fatal(err)
	}
	if r := f.reconcile(f2, nil); r != (store.FolderReconcile{}) {
		t.Errorf("above last_uid: %+v", r)
	}
}

// A failure in the statement writes nothing, also not the reconcile time.
func TestReconcileRollsBack(t *testing.T) {
	f := newReconcileFixture(t)
	inbox := f.folder(f.alice, "mail", "INBOX", store.KindIMAP)
	f.save(inbox, 1, hashOf('a'))
	f.save(inbox, 2, hashOf('b'), `\Seen`)
	conn := rawConn(t, f.url)
	for _, q := range []string{
		`CREATE FUNCTION fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$`,
		`CREATE TRIGGER fail BEFORE UPDATE ON folders FOR EACH ROW WHEN (NEW.last_reconciled_at IS NOT NULL) EXECUTE FUNCTION fail()`,
	} {
		if _, err := conn.Exec(f.ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.ReconcileFolder(f.ctx, inbox.ID, 1, []int64{2}, []string{""}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if got := f.locations(f.alice, hashOf('b')); !slices.Equal(got, []string{`INBOX/2:\Seen`}) {
		t.Errorf("b changed: %v", got)
	}
	if got := f.gone(f.alice, ""); len(got) != 0 {
		t.Errorf("gone = %v", got)
	}
}

// Storing a location again (a UIDVALIDITY that returns) clears gone_at.
func TestUpsertLocationClearsGone(t *testing.T) {
	f := newReconcileFixture(t)
	inbox := f.folder(f.alice, "mail", "INBOX", store.KindIMAP)
	f.save(inbox, 1, hashOf('a'))
	f.reconcile(inbox, nil)
	if got := f.gone(f.alice, ""); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("gone = %v", got)
	}
	f.save(inbox, 1, hashOf('a'))
	if got := f.gone(f.alice, ""); len(got) != 0 {
		t.Errorf("gone after storing again = %v", got)
	}
}

// Gone is per user: Alice's gone copy says nothing to Bob, and a message
// that is also in an import account or in another folder is not gone.
func TestGoneIsPerUser(t *testing.T) {
	f := newReconcileFixture(t)
	aliceInbox := f.folder(f.alice, "mail", "INBOX", store.KindIMAP)
	bobInbox := f.folder(f.bob, "mail", "INBOX", store.KindIMAP)
	imported := f.folder(f.alice, "old", "Archive", store.KindImport)
	f.save(aliceInbox, 1, hashOf('a'))
	f.save(aliceInbox, 2, hashOf('b'))
	f.save(aliceInbox, 3, hashOf('c'))
	f.save(bobInbox, 1, hashOf('a'))
	f.save(imported, 1, hashOf('b'))
	f.save(imported, 2, hashOf('i'))
	moved := f.folder(f.alice, "mail", "Moved", store.KindIMAP)
	f.save(moved, 1, hashOf('c'))
	f.reconcile(aliceInbox, nil)

	if got := f.gone(f.alice, ""); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("alice gone = %v (b is gone from the server, i was never on it, c moved)", got)
	}
	if got := f.gone(f.alice, "mail"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("alice gone in mail = %v", got)
	}
	if got := f.gone(f.alice, "old"); len(got) != 0 {
		t.Errorf("alice gone in old = %v", got)
	}
	if got := f.gone(f.bob, ""); len(got) != 0 {
		t.Errorf("bob gone = %v", got)
	}
	d, err := f.st.GetMessageDetail(f.ctx, storetest.Viewer(t, f.st, f.bob.ID), hashOf('a'))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Locations) != 1 || d.Locations[0].GoneAt != nil {
		t.Errorf("bob's locations: %+v", d.Locations)
	}
	stats, _, err := f.st.Stats(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	gone := map[string]int64{}
	for _, s := range stats {
		gone[ownerKey(s)] = s.Gone
	}
	// Per account, c's other folder keeps it present.
	if gone["alice/mail"] != 2 || gone["bob/mail"] != 0 || gone["alice/old"] != 0 {
		t.Errorf("gone per account = %v", gone)
	}
}

func ownerKey(s store.AccountStats) string {
	owner := "?"
	if s.OwnerID != nil {
		owner = map[int64]string{1: "alice", 2: "bob"}[*s.OwnerID]
	}
	return owner + "/" + s.Account
}

func TestMarkFolderVanished(t *testing.T) {
	f := newReconcileFixture(t)
	inbox := f.folder(f.alice, "mail", "Old", store.KindIMAP)
	f.save(inbox, 1, hashOf('a'))
	f.save(inbox, 2, hashOf('b'))
	n, err := f.st.MarkFolderVanished(f.ctx, inbox.ID)
	if err != nil || n != 2 {
		t.Fatalf("vanished = %d, %v", n, err)
	}
	if n, err := f.st.MarkFolderVanished(f.ctx, inbox.ID); err != nil || n != 0 {
		t.Fatalf("again = %d, %v", n, err)
	}
	states, err := f.st.ListFolderStates(f.ctx, inbox.AccountID)
	if err != nil || len(states) != 1 || states[0].LastReconciledAt == nil {
		t.Fatalf("states = %+v, %v", states, err)
	}
}

// The per-row gone flag of a listing is exactly membership in the gone=1
// listing with the same account (#310): over messages that are all gone,
// gone in one folder and present in another, gone in one account and
// present in another, only in an import account, and also in a removed
// account, in every listing and in the thread listing.
func TestGoneFlagMatchesFilter(t *testing.T) {
	f := newReconcileFixture(t)
	inbox := f.folder(f.alice, "a", "INBOX", store.KindIMAP)
	other := f.folder(f.alice, "a", "Other", store.KindIMAP)
	bInbox := f.folder(f.alice, "b", "INBOX", store.KindIMAP)
	imported := f.folder(f.alice, "imp", "Archive", store.KindImport)
	old := f.folder(f.alice, "old", "INBOX", store.KindIMAP)
	bob := f.folder(f.bob, "a", "INBOX", store.KindIMAP)
	f.save(inbox, 1, hashOf('g')) // all gone
	f.save(inbox, 2, hashOf('f')) // gone here, present in a/Other
	f.save(other, 1, hashOf('f'))
	f.save(inbox, 3, hashOf('x')) // gone in a, present in b
	f.save(bInbox, 1, hashOf('x'))
	f.save(inbox, 4, hashOf('r')) // gone in a, also in a removed account
	f.save(old, 1, hashOf('r'))
	f.save(inbox, 5, hashOf('p')) // present
	f.save(imported, 1, hashOf('i'))
	f.save(bob, 1, hashOf('g')) // Bob's copies say nothing to Alice:
	f.save(bob, 2, hashOf('i')) // gone for Bob, only imported for Alice
	f.save(bob, 3, hashOf('p')) // gone for Bob, present for Alice
	f.reconcile(inbox, map[uint32]string{5: ""})
	f.reconcile(bob, map[uint32]string{1: ""})
	acc, err := f.st.GetOwnedAccount(f.ctx, f.alice.ID, "old")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := f.st.DeleteOrRemoveAccount(f.ctx, acc.Ref()); err != nil || res != store.AccountRemoved {
		t.Fatalf("remove: %v, %v", res, err)
	}

	type listing struct{ account, folder string }
	listings := []listing{{"", ""}, {"a", ""}, {"a", "INBOX"}, {"a", "Other"}, {"b", ""}, {"imp", ""}, {"old", ""}}
	flagged := map[listing][]string{}
	for _, threads := range []bool{false, true} {
		for _, l := range listings {
			sc := storetest.Viewer(t, f.st, f.alice.ID)
			if l.account != "" {
				sc = sc.OwnAccount(l.account)
			}
			filter := store.SearchFilter{Scope: sc, Folder: l.folder, Limit: 50}
			gone, err := f.st.SearchMessages(f.ctx, store.SearchFilter{Scope: sc, Gone: true, Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			inGone := map[string]bool{}
			for _, r := range gone {
				inGone[r.SHA256] = true
			}
			search := f.st.SearchMessages
			if threads {
				search = f.st.SearchThreads
			}
			rows, err := search(f.ctx, filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == 0 {
				t.Fatalf("%+v (threads %v): empty listing", l, threads)
			}
			var got []string
			for _, r := range rows {
				if r.Gone != inGone[r.SHA256] {
					t.Errorf("%+v (threads %v): %s gone = %v, in the gone=1 listing = %v", l, threads, r.SHA256[:1], r.Gone, inGone[r.SHA256])
				}
				if r.Gone {
					got = append(got, r.SHA256[:1])
				}
			}
			slices.Sort(got)
			if !threads {
				flagged[l] = got
			} else if !slices.Equal(got, flagged[l]) {
				t.Errorf("%+v: threads flag %v, messages %v", l, got, flagged[l])
			}
		}
	}
	// The fixture covers both values and the account scope.
	want := map[listing][]string{
		{"", ""}: {"g"}, {"a", ""}: {"g", "r", "x"}, {"a", "INBOX"}: {"g", "r", "x"}, {"a", "Other"}: nil,
		{"b", ""}: nil, {"imp", ""}: nil, {"old", ""}: nil,
	}
	for l, w := range want {
		if !slices.Equal(flagged[l], w) {
			t.Errorf("%+v: flagged %v, want %v", l, flagged[l], w)
		}
	}
}
