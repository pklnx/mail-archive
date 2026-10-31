package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// shareFixture: alice owns "info" and "private", bob owns "mine", uwe owns
// nothing and root is an admin without accounts. Nothing is shared yet.
//
//	a: info only          p: private only
//	d: info and private   x: info and bob's mine
//	b: bob's mine only
type shareFixture struct {
	*retentionFixture
	uwe, root            *store.User
	info, private, mine  *store.Folder
	infoSent             *store.Folder
	infoID, mineID, priv int64
}

func newShareFixture(t *testing.T) *shareFixture {
	t.Helper()
	f := &shareFixture{retentionFixture: newRetentionFixture(t)}
	var err error
	if f.uwe, err = f.st.CreateUser(f.ctx, "uwe", "h", false); err != nil {
		t.Fatal(err)
	}
	if f.root, err = f.st.CreateUser(f.ctx, "root", "h", true); err != nil {
		t.Fatal(err)
	}
	f.info = f.folder(f.alice, "info", "INBOX")
	f.infoSent = f.folder(f.alice, "info", "Sent")
	f.private = f.folder(f.alice, "private", "INBOX")
	f.mine = f.folder(f.bob, "mine", "INBOX")
	f.infoID, f.priv, f.mineID = f.info.AccountID, f.private.AccountID, f.mine.AccountID
	f.save(f.info, hashOf('a'), "falcon a", old, old)
	f.save(f.infoSent, hashOf('a'), "falcon a", old, old) // a second location in info
	f.save(f.private, hashOf('p'), "falcon p", old, old)
	f.save(f.info, hashOf('d'), "d", old, old)
	f.save(f.private, hashOf('d'), "d", old, old)
	f.save(f.info, hashOf('x'), "x", old, old)
	f.save(f.mine, hashOf('x'), "x", old, old)
	f.save(f.mine, hashOf('b'), "b", old, old)
	return f
}

// account returns the current state of an account by ID.
func (f *shareFixture) account(id int64) *store.Account {
	f.t.Helper()
	a, err := f.st.GetAccount(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

func (f *shareFixture) share(accountID int64, u *store.User) {
	f.t.Helper()
	if _, err := f.st.ShareAccount(f.ctx, f.account(accountID).Ref(), u.ID); err != nil {
		f.t.Fatalf("share %d with %s: %v", accountID, u.Name, err)
	}
}

func (f *shareFixture) unshare(accountID int64, u *store.User) {
	f.t.Helper()
	if err := f.st.UnshareAccount(f.ctx, f.account(accountID).Ref(), u.ID); err != nil {
		f.t.Fatalf("unshare %d from %s: %v", accountID, u.Name, err)
	}
}

// listed returns the first letter of each message sc lists, plain and
// grouped by thread (which must agree), sorted.
func (f *shareFixture) listed(sc store.Scope) []string {
	f.t.Helper()
	filter := store.SearchFilter{Scope: sc, Limit: 50}
	plain, err := f.st.SearchMessages(f.ctx, filter)
	if err != nil {
		f.t.Fatal(err)
	}
	grouped, err := f.st.SearchThreads(f.ctx, filter)
	if err != nil {
		f.t.Fatal(err)
	}
	letters := func(rows []store.MessageSummary) []string {
		out := []string{}
		for _, r := range rows {
			out = append(out, r.SHA256[:1])
		}
		slices.Sort(out)
		return out
	}
	if p, g := letters(plain), letters(grouped); !slices.Equal(p, g) {
		f.t.Fatalf("plain %v, grouped %v", p, g)
	}
	return letters(plain)
}

// places returns the locations a user sees of a message, as
// "owner/account/folder", or nil when the message is not found.
func (f *shareFixture) places(u *store.User, hash string) []string {
	f.t.Helper()
	sc := storetest.Viewer(f.t, f.st, u.ID)
	d, err := f.st.GetMessageDetail(f.ctx, sc, hash)
	if errors.Is(err, store.ErrNotFound) {
		if _, err := f.st.GetConversation(f.ctx, sc, hash); !errors.Is(err, store.ErrNotFound) {
			f.t.Fatalf("%s: no detail of %s, but its conversation: %v", u.Name, hash[:1], err)
		}
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.st.GetConversation(f.ctx, sc, hash); err != nil {
		f.t.Fatalf("%s: detail of %s, but no conversation: %v", u.Name, hash[:1], err)
	}
	var out []string
	for _, l := range d.Locations {
		out = append(out, l.Owner+"/"+l.Account+"/"+l.Folder)
	}
	slices.Sort(out)
	return out
}

// accounts returns the accounts ListAccountFolders gives a user, as
// "owner/account:messages".
func (f *shareFixture) accounts(u *store.User) []string {
	f.t.Helper()
	list, err := f.st.ListAccountFolders(f.ctx, storetest.Viewer(f.t, f.st, u.ID))
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, a := range list {
		var n int64
		for _, fo := range a.Folders {
			n += fo.Messages
		}
		out = append(out, a.Owner+"/"+a.Name+":"+string(rune('0'+n)))
	}
	return out
}

// Security property 2 with shares: a shared user reads exactly the
// locations of the shared account (and their own); everyone else gets
// nothing, also an admin; the access ends with the unshare.
func TestShareReadScope(t *testing.T) {
	f := newShareFixture(t)
	a, d, p, x := hashOf('a'), hashOf('d'), hashOf('p'), hashOf('x')
	check := func(stage string, u *store.User, wantList []string, wantPlaces map[string][]string, wantAccounts []string) {
		t.Helper()
		if got := f.listed(storetest.Viewer(t, f.st, u.ID)); !slices.Equal(got, wantList) {
			t.Errorf("%s: %s lists %v, want %v", stage, u.Name, got, wantList)
		}
		for hash, want := range wantPlaces {
			if got := f.places(u, hash); !slices.Equal(got, want) {
				t.Errorf("%s: %s sees %s in %v, want %v", stage, u.Name, hash[:1], got, want)
			}
		}
		if got := f.accounts(u); !slices.Equal(got, wantAccounts) {
			t.Errorf("%s: %s has accounts %v, want %v", stage, u.Name, got, wantAccounts)
		}
	}
	nobody := map[string][]string{a: nil, d: nil, p: nil, x: nil}
	before := func(stage string) {
		t.Helper()
		check(stage, f.bob, []string{"b", "x"}, map[string][]string{a: nil, d: nil, p: nil, x: {"bob/mine/INBOX"}}, []string{"bob/mine:2"})
		check(stage, f.uwe, []string{}, nobody, nil)
		check(stage, f.root, []string{}, nobody, nil)
	}
	before("before sharing")

	f.share(f.infoID, f.bob)
	shared := func(stage string) {
		t.Helper()
		check(stage, f.bob, []string{"a", "b", "d", "x"}, map[string][]string{
			a: {"alice/info/INBOX", "alice/info/Sent"},
			// Only the shared account's location, not private's.
			d: {"alice/info/INBOX"},
			p: nil,
			x: {"alice/info/INBOX", "bob/mine/INBOX"},
		}, []string{"alice/info:4", "bob/mine:2"})
		// Nothing changes for the owner and for everyone else.
		check(stage, f.alice, []string{"a", "d", "p", "x"}, map[string][]string{
			a: {"alice/info/INBOX", "alice/info/Sent"}, d: {"alice/info/INBOX", "alice/private/INBOX"},
			x: {"alice/info/INBOX"},
		}, []string{"alice/info:4", "alice/private:2"})
		check(stage, f.uwe, []string{}, nobody, nil)
		check(stage, f.root, []string{}, nobody, nil)
	}
	shared("shared")
	d0, err := f.st.GetMessageDetail(f.ctx, storetest.Viewer(t, f.st, f.bob.ID), a)
	if err != nil || d0.Locations[0].AccountID != f.infoID || d0.Locations[0].OwnerID != f.alice.ID {
		t.Fatalf("bob's location of a: %+v %v", d0, err)
	}

	// The next read after the unshare sees nothing of info.
	f.unshare(f.infoID, f.bob)
	before("unshared")
	f.share(f.infoID, f.bob)
	shared("shared again")
}

// A share does not reach what stays owner-only: OwnerScope, account
// management by name, and the account's folder list of the owner.
func TestShareScopes(t *testing.T) {
	f := newShareFixture(t)
	f.share(f.infoID, f.bob)
	viewer, err := f.st.ViewerScope(f.ctx, f.bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range viewer.Accounts {
		got = append(got, a.Owner+"/"+a.Name+":"+map[bool]string{true: "own", false: "shared"}[a.Own])
	}
	if !slices.Equal(got, []string{"bob/mine:own", "alice/info:shared"}) || !viewer.HasShared() {
		t.Fatalf("viewer scope %v", got)
	}
	owner, err := f.st.OwnerScope(f.ctx, f.bob.ID)
	if err != nil || len(owner.Accounts) != 1 || owner.Accounts[0].ID != f.mineID || owner.HasShared() {
		t.Fatalf("owner scope %+v %v", owner, err)
	}
	// account= names only own accounts.
	if sc := viewer.OwnAccount("info"); len(sc.Accounts) != 0 {
		t.Errorf("own account info: %+v", sc)
	}
	if got := f.listed(viewer.Only(f.infoID)); !slices.Equal(got, []string{"a", "d", "x"}) {
		t.Errorf("only info: %v", got)
	}
	if got := f.listed(viewer.Only(f.priv)); len(got) != 0 {
		t.Errorf("only alice's private, not shared: %v", got)
	}
	if _, err := f.st.GetOwnedAccount(f.ctx, f.bob.ID, "info"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("bob owns info: %v", err)
	}

	// Name collision: bob gets an info of his own. account=info is his, the
	// folder counts stay apart.
	bobInfo := f.folder(f.bob, "info", "INBOX")
	f.save(bobInfo, hashOf('b'), "b", old, old)
	viewer = storetest.Viewer(t, f.st, f.bob.ID)
	if sc := viewer.OwnAccount("info"); len(sc.Accounts) != 1 || sc.Accounts[0].ID != bobInfo.AccountID {
		t.Fatalf("own info: %+v", sc)
	}
	if got := f.listed(viewer.OwnAccount("info")); !slices.Equal(got, []string{"b"}) {
		t.Errorf("account=info lists %v", got)
	}
	if got := f.accounts(f.bob); !slices.Equal(got, []string{"alice/info:4", "bob/info:1", "bob/mine:2"}) {
		t.Errorf("accounts %v", got)
	}
}

// Empty and nil scopes match nothing: no read treats "no accounts" as all.
func TestEmptyScopeMatchesNothing(t *testing.T) {
	f := newShareFixture(t)
	f.share(f.infoID, f.bob)
	for name, sc := range map[string]store.Scope{
		"zero":         {},
		"nil accounts": {Viewer: f.alice.ID},
		"empty":        {Viewer: f.alice.ID, Accounts: []store.ScopeAccount{}},
		"no accounts":  storetest.Viewer(t, f.st, f.uwe.ID),
		"unknown name": storetest.Viewer(t, f.st, f.alice.ID).OwnAccount("nope"),
		"not in scope": storetest.Viewer(t, f.st, f.alice.ID).Only(f.mineID),
	} {
		t.Run(name, func(t *testing.T) {
			if got := f.listed(sc); len(got) != 0 {
				t.Errorf("lists %v", got)
			}
			for _, gone := range []bool{true, false} {
				if rows, err := f.st.SearchMessages(f.ctx, store.SearchFilter{Scope: sc, Gone: gone, To: "e", Limit: 50}); err != nil || len(rows) != 0 {
					t.Errorf("gone=%v: %v %v", gone, rows, err)
				}
			}
			if _, err := f.st.GetMessageDetail(f.ctx, sc, hashOf('a')); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("detail: %v", err)
			}
			if _, err := f.st.GetConversation(f.ctx, sc, hashOf('a')); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("conversation: %v", err)
			}
			if list, err := f.st.ListAccountFolders(f.ctx, sc); err != nil || len(list) != 0 {
				t.Errorf("folders: %+v %v", list, err)
			}
			if rows, err := f.st.ListShareOnlyLocations(f.ctx, sc, sc, []string{hashOf('a')}); err != nil || len(rows) != 0 {
				t.Errorf("share-only: %+v %v", rows, err)
			}
		})
	}
}

// ShareAccount and UnshareAccount follow the account mutation rules.
func TestShareAccountRules(t *testing.T) {
	f := newShareFixture(t)
	info := f.account(f.infoID)
	if created, err := f.st.ShareAccount(f.ctx, info.Ref(), f.bob.ID); err != nil || !created {
		t.Fatalf("share: %v %v", created, err)
	}
	if created, err := f.st.ShareAccount(f.ctx, info.Ref(), f.bob.ID); err != nil || created {
		t.Fatalf("share again: %v %v", created, err)
	}
	f.share(f.infoID, f.uwe)
	shares, err := f.st.AccountShares(f.ctx, f.infoID)
	if err != nil || len(shares) != 2 || shares[0].User != "bob" || shares[0].UserID != f.bob.ID || shares[1].User != "uwe" || shares[0].Since.IsZero() {
		t.Fatalf("shares %+v %v", shares, err)
	}

	stale := info.Ref()
	if err := f.st.UpdateAccount(f.ctx, info.Ref(), store.AccountChange{Name: ptr("info2")}); err != nil {
		t.Fatal(err)
	}
	foreign := f.account(f.infoID).Ref()
	foreign.OwnerID = &f.bob.ID
	j := createSMTPAccount(t, f.st, &f.alice.ID, "journal", "journal@example.com")
	for name, c := range map[string]struct {
		ref  store.AccountRef
		user int64
		want error
	}{
		"stale":        {stale, f.root.ID, store.ErrStale},
		"foreign":      {foreign, f.root.ID, store.ErrNotFound},
		"unknown":      {store.AccountRef{ID: 999999, OwnerID: &f.alice.ID}, f.root.ID, store.ErrNotFound},
		"self":         {f.account(f.infoID).Ref(), f.alice.ID, store.ErrShareSelf},
		"smtp":         {j.Ref(), f.bob.ID, store.ErrShareSMTP},
		"unknown user": {f.account(f.infoID).Ref(), 999999, store.ErrNotFound},
	} {
		if _, err := f.st.ShareAccount(f.ctx, c.ref, c.user); !errors.Is(err, c.want) {
			t.Errorf("share %s: %v, want %v", name, err, c.want)
		}
		if name != "self" && name != "smtp" && name != "unknown user" {
			if err := f.st.UnshareAccount(f.ctx, c.ref, f.bob.ID); !errors.Is(err, c.want) {
				t.Errorf("unshare %s: %v, want %v", name, err, c.want)
			}
		}
	}
	if err := f.st.UnshareAccount(f.ctx, f.account(f.infoID).Ref(), f.root.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unshare without a share: %v", err)
	}

	// A removed account keeps its shares; no new share, but unshare works.
	if res, err := f.st.DeleteOrRemoveAccount(f.ctx, f.account(f.infoID).Ref()); err != nil || res != store.AccountRemoved {
		t.Fatalf("remove: %v %v", res, err)
	}
	if got := f.places(f.bob, hashOf('a')); len(got) != 2 {
		t.Errorf("bob after the removal: %v", got)
	}
	if _, err := f.st.ShareAccount(f.ctx, f.account(f.infoID).Ref(), f.root.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("share a removed account: %v", err)
	}
	f.unshare(f.infoID, f.uwe)
	if shares, _ := f.st.AccountShares(f.ctx, f.infoID); len(shares) != 1 || shares[0].User != "bob" {
		t.Errorf("shares after unshare %+v", shares)
	}
}

func ptr[T any](v T) *T { return &v }

// Moving an account drops the new owner's share and keeps the others; the
// old owner keeps no access. Removing a user ends their shares, and a new
// user of the same name gets nothing; deleting an account ends its shares.
func TestShareLifecycle(t *testing.T) {
	f := newShareFixture(t)
	f.share(f.infoID, f.bob)
	f.share(f.infoID, f.uwe)
	if err := f.st.SetAccountOwner(f.ctx, f.account(f.infoID).Ref(), f.bob.ID); err != nil {
		t.Fatal(err)
	}
	shares, err := f.st.AccountShares(f.ctx, f.infoID)
	if err != nil || len(shares) != 1 || shares[0].User != "uwe" {
		t.Fatalf("shares after the move %+v %v", shares, err)
	}
	if got := f.places(f.alice, hashOf('a')); got != nil {
		t.Errorf("alice after the move: %v", got)
	}
	if got := f.places(f.uwe, hashOf('a')); !slices.Equal(got, []string{"bob/info/INBOX", "bob/info/Sent"}) {
		t.Errorf("uwe after the move: %v", got)
	}

	if err := f.st.DeleteUser(f.ctx, f.uwe.ID); err != nil {
		t.Fatal(err)
	}
	if shares, _ := f.st.AccountShares(f.ctx, f.infoID); len(shares) != 0 {
		t.Errorf("shares after removing uwe %+v", shares)
	}
	again, err := f.st.CreateUser(f.ctx, "uwe", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.places(again, hashOf('a')); got != nil {
		t.Errorf("the new uwe: %v", got)
	}

	// An account without mail is deleted, and its shares with it.
	empty := f.folder(f.alice, "empty", "INBOX")
	f.share(empty.AccountID, f.bob)
	if res, err := f.st.DeleteOrRemoveAccount(f.ctx, f.account(empty.AccountID).Ref()); err != nil || res != store.AccountDeleted {
		t.Fatalf("delete: %v %v", res, err)
	}
	if shares, err := f.st.AccountShares(f.ctx, empty.AccountID); err != nil || len(shares) != 0 {
		t.Errorf("shares of a deleted account %+v %v", shares, err)
	}
}

// Envelopes (BCC recipients) come only from the viewer's own smtp
// accounts, even with a share row the store would refuse.
func TestShareKeepsEnvelopes(t *testing.T) {
	f := newShareFixture(t)
	j := createSMTPAccount(t, f.st, &f.alice.ID, "journal", "journal@example.com")
	meta := receivedMeta(7)
	if _, err := f.st.ReceiveMessage(f.ctx, j.ID, meta, envelope("hidden-bcc@example.com"), present); err != nil {
		t.Fatal(err)
	}
	if _, err := connect(t, f.url).Exec(f.ctx, `INSERT INTO account_shares (account_id, user_id) VALUES ($1, $2)`, j.ID, f.bob.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		u    *store.User
		want int
	}{{f.alice, 1}, {f.bob, 0}} {
		sc := storetest.Viewer(t, f.st, c.u.ID)
		for _, few := range []bool{true, false} {
			restore := func() {}
			if !few {
				restore = store.SetMaxEnvelopeMatches(0)
			}
			rows, err := f.st.SearchMessages(f.ctx, store.SearchFilter{Scope: sc, To: "hidden-bcc", Limit: 50})
			threads, terr := f.st.SearchThreads(f.ctx, store.SearchFilter{Scope: sc, To: "hidden-bcc", Limit: 50})
			restore()
			if err != nil || terr != nil || len(rows) != c.want || len(threads) != c.want {
				t.Errorf("%s few=%v: to:hidden-bcc gives %d rows, %d threads (%v %v), want %d", c.u.Name, few, len(rows), len(threads), err, terr, c.want)
			}
		}
		d, err := f.st.GetMessageDetail(f.ctx, sc, meta.SHA256)
		if err != nil || len(d.Locations) != 1 {
			t.Fatalf("%s: %+v %v", c.u.Name, d, err)
		}
		if l := d.Locations[0]; (l.Envelope != nil) != (c.want == 1) || l.Filled {
			t.Errorf("%s: envelope %+v, filled %v", c.u.Name, l.Envelope, l.Filled)
		}
	}
}

// The gone flag and filter work on the scope: a location gone from the
// shared account's server counts for the shared user, unless the message is
// still on the server in one of the user's own IMAP accounts.
func TestShareGoneFlag(t *testing.T) {
	f := newShareFixture(t)
	f.share(f.infoID, f.bob)
	g := hashOf('g')
	f.save(f.info, g, "g", old, old)
	if _, err := connect(t, f.url).Exec(f.ctx, `UPDATE message_locations SET gone_at = now() WHERE message_sha256 = $1`, g); err != nil {
		t.Fatal(err)
	}
	gone := func(stage string, want bool) {
		t.Helper()
		sc := storetest.Viewer(t, f.st, f.bob.ID)
		for _, threads := range []bool{false, true} {
			search := f.st.SearchMessages
			if threads {
				search = f.st.SearchThreads
			}
			rows, err := search(f.ctx, store.SearchFilter{Scope: sc, Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			flagged := false
			for _, r := range rows {
				if r.SHA256 == g {
					flagged = r.Gone
				}
			}
			listed, err := search(f.ctx, store.SearchFilter{Scope: sc, Gone: true, Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			inGone := len(listed) == 1 && listed[0].SHA256 == g
			if flagged != want || inGone != want || (!want && len(listed) != 0) {
				t.Errorf("%s threads=%v: flag %v, gone listing %v, want %v", stage, threads, flagged, inGone, want)
			}
		}
	}
	gone("only in the shared account", true)
	f.save(f.mine, g, "g", old, old)
	gone("also in bob's own account", false)
}

// ListShareOnlyLocations names, once per message and account, the shared
// accounts through which a message is seen, for messages in none of the
// viewer's own accounts.
func TestListShareOnlyLocations(t *testing.T) {
	f := newShareFixture(t)
	f.share(f.infoID, f.bob)
	other := f.folder(f.uwe, "sales", "INBOX")
	f.save(other, hashOf('a'), "falcon a", old, old)
	f.save(other, hashOf('u'), "u", old, old)
	f.share(other.AccountID, f.bob)
	full := storetest.Viewer(t, f.st, f.bob.ID)
	all := []string{hashOf('a'), hashOf('b'), hashOf('d'), hashOf('p'), hashOf('u'), hashOf('x')}
	got := func(sc store.Scope) []string {
		t.Helper()
		rows, err := f.st.ListShareOnlyLocations(f.ctx, sc, full, all)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			owner := map[int64]string{f.alice.ID: "alice", f.uwe.ID: "uwe"}[r.OwnerID]
			acc := map[int64]string{f.infoID: "info", other.AccountID: "sales"}[r.AccountID]
			out = append(out, r.SHA256[:1]+":"+owner+"/"+acc)
		}
		return out
	}
	// a has two locations in info and one in sales; x is also bob's own; p
	// is not shared; b is only bob's own.
	if g := got(full); !slices.Equal(g, []string{"a:alice/info", "a:uwe/sales", "d:alice/info", "u:uwe/sales"}) {
		t.Errorf("full scope: %v", g)
	}
	if g := got(full.Only(f.infoID)); !slices.Equal(g, []string{"a:alice/info", "d:alice/info"}) {
		t.Errorf("narrowed to info: %v", g)
	}
	if g := got(full.Only(f.mineID)); len(g) != 0 {
		t.Errorf("narrowed to bob's own: %v", g)
	}
}

// Security property 5: holds stay on the owner. A hold on a shared user,
// with or without a filter, covers nothing of the account shared with them,
// and does not keep its mail from a deletion run; a hold on the owner does.
func TestShareHolds(t *testing.T) {
	f := newShareFixture(t)
	f.rule(f.info, "", 1)
	f.share(f.infoID, f.bob)
	a := hashOf('a')
	held := func(u *store.User) bool {
		t.Helper()
		d := f.detail(u, a)
		for _, l := range d.Locations {
			if l.OnHold {
				return true
			}
		}
		return false
	}
	candidate := func() bool {
		t.Helper()
		run := f.propose()
		defer func() {
			if err := f.st.CancelDeletionRun(f.ctx, run.ID); err != nil {
				t.Fatal(err)
			}
		}()
		return slices.Contains(f.candidates(run.ID), a)
	}
	if !candidate() {
		t.Fatal("a is no candidate without a hold")
	}

	// A hold on bob without a filter.
	h := f.hold(store.NewLegalHold{OwnerID: &f.bob.ID})
	if held(f.alice) || held(f.bob) || !candidate() {
		t.Errorf("a hold on bob covers alice's shared info")
	}
	_ = f.st.ReleaseLegalHold(f.ctx, h, nil, "done")

	// A filter hold on bob that matches a message only in info adds
	// nothing; the same hold on alice adds it.
	h = f.hold(store.NewLegalHold{OwnerID: &f.bob.ID, Filter: &store.HoldFilter{Query: "falcon"}})
	if n, err := f.st.MatchLegalHolds(f.ctx); err != nil || n != 0 {
		t.Fatalf("matching bob's hold: %d %v", n, err)
	}
	if held(f.alice) || held(f.bob) || !candidate() {
		t.Errorf("a filter hold on bob covers alice's shared info")
	}
	_ = f.st.ReleaseLegalHold(f.ctx, h, nil, "done")

	h = f.hold(store.NewLegalHold{OwnerID: &f.alice.ID, Filter: &store.HoldFilter{Query: "falcon"}})
	if n, err := f.st.MatchLegalHolds(f.ctx); err != nil || n != 2 { // a and p
		t.Fatalf("matching alice's hold: %d %v", n, err)
	}
	if !held(f.alice) || candidate() {
		t.Errorf("alice's hold does not cover a")
	}
	_ = f.st.ReleaseLegalHold(f.ctx, h, nil, "done")

	// An account hold on info, matched as its owner.
	h = f.hold(store.NewLegalHold{AccountID: &f.infoID, Filter: &store.HoldFilter{Query: "falcon"}})
	if n, err := f.st.MatchLegalHolds(f.ctx); err != nil || n != 1 {
		t.Fatalf("matching the account hold: %d %v", n, err)
	}
	_ = f.st.ReleaseLegalHold(f.ctx, h, nil, "done")
}

// The partial index for the owner's Log exists on audit log partitions
// created after the migration, too.
func TestAuditViaOwnersIndexOnNewPartitions(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	c := connect(t, url)
	count := func() (partitions, indexed int) {
		t.Helper()
		err := c.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM pg_inherits ii
    JOIN pg_index x ON x.indexrelid = ii.inhrelid
    WHERE ii.inhparent = 'audit_log_via_owners_idx'::regclass AND x.indrelid = p.inhrelid))
FROM pg_inherits p WHERE p.inhparent = 'audit_log'::regclass`).Scan(&partitions, &indexed)
		if err != nil {
			t.Fatal(err)
		}
		return partitions, indexed
	}
	before, _ := count()
	if n, err := st.EnsureAuditPartitions(ctx, 30); err != nil || n == 0 {
		t.Fatalf("new partitions: %d %v", n, err)
	}
	partitions, indexed := count()
	if partitions <= before || indexed != partitions {
		t.Fatalf("%d partitions (%d before), %d with the index", partitions, before, indexed)
	}
	var predicate string
	if err := c.QueryRow(ctx, `SELECT pg_get_expr(indpred, indrelid) FROM pg_index WHERE indexrelid = 'audit_log_via_owners_idx'::regclass`).Scan(&predicate); err != nil ||
		!strings.Contains(predicate, "via_owners") {
		t.Fatalf("predicate %q %v", predicate, err)
	}
}
