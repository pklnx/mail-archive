package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// groupFixture is a shareFixture (alice: info, private; bob: mine; uwe;
// root, an admin; all in "Default") with a second group "B" holding kim,
// who owns "kb". Nothing is shared yet.
type groupFixture struct {
	*shareFixture
	conn   *pgx.Conn
	a, b   *store.Group
	kim    *store.User
	kbID   int64
	kbFold *store.Folder
}

func newGroupFixture(t *testing.T) *groupFixture {
	t.Helper()
	f := &groupFixture{shareFixture: newShareFixture(t)}
	f.conn = connect(t, f.url)
	var err error
	if f.a, err = f.st.GetGroupByName(f.ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if f.b, err = f.st.CreateGroup(f.ctx, "B"); err != nil {
		t.Fatal(err)
	}
	if f.kim, err = f.st.CreateUserInGroup(f.ctx, store.NewUser{Name: "kim", PasswordHash: "h", GroupID: f.b.ID}); err != nil {
		t.Fatal(err)
	}
	f.kbFold = f.folder(f.kim, "kb", "INBOX")
	f.kbID = f.kbFold.AccountID
	f.save(f.kbFold, hashOf('k'), "k", old, old)
	return f
}

// crossingShares counts the shares whose reader is not in the account
// owner's group: the invariant every group test keeps at zero.
func crossingShares(t *testing.T, conn *pgx.Conn) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM account_shares s
		JOIN accounts a ON a.id = s.account_id JOIN users o ON o.id = a.owner_id JOIN users r ON r.id = s.user_id
		WHERE o.group_id <> r.group_id`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *groupFixture) checkNoCrossing() {
	f.t.Helper()
	if n := crossingShares(f.t, f.conn); n != 0 {
		f.t.Fatalf("%d shares cross groups", n)
	}
}

func (f *groupFixture) setGroup(u *store.User, g *store.Group) store.GroupChange {
	f.t.Helper()
	c, err := f.st.SetUserGroup(f.ctx, u.ID, g.ID)
	if err != nil {
		f.t.Fatalf("move %s to %s: %v", u.Name, g.Name, err)
	}
	return c
}

// groupedEntry is an audit row with its group.
type groupedEntry struct {
	Action  string
	GroupID *int64
	Details map[string]any
}

// entries returns the audit rows of action, oldest first.
func groupedEntries(t *testing.T, conn *pgx.Conn, action string) []groupedEntry {
	t.Helper()
	rows, err := conn.Query(context.Background(), `SELECT action, group_id, details FROM audit_log WHERE action = $1 ORDER BY at, id`, action)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (groupedEntry, error) {
		var e groupedEntry
		var raw []byte
		err := r.Scan(&e.Action, &e.GroupID, &raw)
		if err == nil {
			err = json.Unmarshal(raw, &e.Details)
		}
		return e, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func groupOf(e groupedEntry) string {
	if e.GroupID == nil {
		return "NULL"
	}
	return fmt.Sprint(*e.GroupID)
}

func scopeIDs(sc store.Scope) []int64 {
	var out []int64
	for _, a := range sc.Accounts {
		out = append(out, a.ID)
	}
	slices.Sort(out)
	return out
}

// TestShareOtherGroupRefused (#470, test 3): the store refuses a share
// with a user of another group, and so does the database for a direct
// insert. Before customer groups, both succeeded.
func TestShareOtherGroupRefused(t *testing.T) {
	f := newGroupFixture(t)
	if _, err := f.st.ShareAccount(f.ctx, f.account(f.infoID).Ref(), f.kim.ID); !errors.Is(err, store.ErrShareOtherGroup) {
		t.Fatalf("share with kim: %v, want ErrShareOtherGroup", err)
	}
	if _, err := f.conn.Exec(f.ctx, `INSERT INTO account_shares (account_id, user_id) VALUES ($1, $2)`, f.infoID, f.kim.ID); !isCheckViolation(err) {
		t.Fatalf("direct insert: %v, want a check violation", err)
	}
	if got := scopeIDs(storetest.Viewer(t, f.st, f.kim.ID)); !slices.Equal(got, []int64{f.kbID}) {
		t.Fatalf("kim reads %v", got)
	}
	// Inside the group sharing works as before.
	f.share(f.infoID, f.uwe)
	if got := scopeIDs(storetest.Viewer(t, f.st, f.uwe.ID)); !slices.Equal(got, []int64{f.infoID}) {
		t.Fatalf("uwe reads %v", got)
	}
	f.checkNoCrossing()
}

func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// TestSetUserGroupEndsCrossingShares (#470, test 5): a group change ends
// exactly the shares that would cross groups, in both directions, records
// them and keeps the others.
func TestSetUserGroupEndsCrossingShares(t *testing.T) {
	f := newGroupFixture(t)
	f.share(f.infoID, f.bob)
	f.share(f.infoID, f.uwe)
	f.share(f.priv, f.uwe)
	f.share(f.mineID, f.alice)
	f.share(f.mineID, f.uwe) // bob and uwe stay together
	move := store.TargetedEntry(store.ActorCLI, "user.group_change")
	move.SetTarget(store.TargetUser, fmt.Sprint(f.alice.ID), "alice")
	c, err := f.st.SetUserGroup(store.WithAudit(f.ctx, move), f.alice.ID, f.b.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.EndedShare{
		{Account: "info", Owner: "alice", User: "bob"},
		{Account: "info", Owner: "alice", User: "uwe"},
		{Account: "private", Owner: "alice", User: "uwe"},
		{Account: "mine", Owner: "bob", User: "alice"},
	}
	if !c.Changed || c.From != "Default" || c.To != "B" || !slices.Equal(c.Ended, want) {
		t.Fatalf("change %+v, want ended %v", c, want)
	}
	f.checkNoCrossing()
	if got := scopeIDs(storetest.Viewer(t, f.st, f.uwe.ID)); !slices.Equal(got, []int64{f.mineID}) {
		t.Fatalf("uwe reads %v, want only bob's mine", got)
	}
	if got := scopeIDs(storetest.Viewer(t, f.st, f.alice.ID)); !slices.Equal(got, []int64{f.infoID, f.priv}) {
		t.Fatalf("alice reads %v, want only her own", got)
	}
	es := groupedEntries(t, f.conn, "user.group_change")
	if len(es) != 1 || groupOf(es[0]) != fmt.Sprint(f.b.ID) {
		t.Fatalf("entries %+v", es)
	}
	d := es[0].Details
	if fmt.Sprint(d["from_group"]) != fmt.Sprintf("map[id:%d name:Default]", f.a.ID) ||
		fmt.Sprint(d["to_group"]) != fmt.Sprintf("map[id:%d name:B]", f.b.ID) ||
		len(d["shares_ended"].([]any)) != 4 {
		t.Fatalf("details %v", d)
	}
	// alice and kim are now in one group: they may share.
	f.share(f.infoID, f.kim)
	// Moving into the group one is in changes nothing and writes nothing.
	again := store.TargetedEntry(store.ActorCLI, "user.group_change")
	if c, err := f.st.SetUserGroup(store.WithAudit(f.ctx, again), f.alice.ID, f.b.ID); err != nil || c.Changed {
		t.Fatalf("same group: %+v, %v", c, err)
	}
	if n := len(groupedEntries(t, f.conn, "user.group_change")); n != 1 {
		t.Fatalf("%d entries after a no-op", n)
	}
	if _, err := f.st.SetUserGroup(f.ctx, f.alice.ID, 999999); !errors.Is(err, store.ErrUnknownGroup) {
		t.Fatalf("unknown group: %v", err)
	}
	f.checkNoCrossing()
}

// TestShareRacesGroupChange (#470, test 6): a share and a group change of
// its reader, in both orders, never leave a crossing share. The barriers
// are row locks held by the test on its own connection; the test waits for
// the blocked statement itself (waitForLockWait).
func TestShareRacesGroupChange(t *testing.T) {
	t.Run("move commits first", func(t *testing.T) {
		f := newGroupFixture(t)
		hold, err := f.conn.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = hold.Rollback(f.ctx) }()
		// The share waits for the account row.
		if _, err := hold.Exec(f.ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, f.infoID); err != nil {
			t.Fatal(err)
		}
		ref := f.account(f.infoID).Ref()
		done := make(chan error, 1)
		go func() { _, err := f.st.ShareAccount(f.ctx, ref, f.uwe.ID); done <- err }()
		waitForLockWait(t, connect(t, f.url), done)
		f.setGroup(f.uwe, f.b)
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, store.ErrShareOtherGroup) {
			t.Fatalf("share after the move: %v, want ErrShareOtherGroup", err)
		}
		f.checkNoCrossing()
	})
	t.Run("share commits first", func(t *testing.T) {
		f := newGroupFixture(t)
		hold, err := f.conn.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = hold.Rollback(f.ctx) }()
		// FOR KEY SHARE blocks the move's FOR UPDATE, not the share's FOR
		// SHARE on the same row.
		if _, err := hold.Exec(f.ctx, `SELECT 1 FROM users WHERE id = $1 FOR KEY SHARE`, f.uwe.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		var c store.GroupChange
		go func() {
			var err error
			c, err = f.st.SetUserGroup(f.ctx, f.uwe.ID, f.b.ID)
			done <- err
		}()
		waitForLockWait(t, connect(t, f.url), done)
		f.share(f.infoID, f.uwe)
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(c.Ended, []store.EndedShare{{Account: "info", Owner: "alice", User: "uwe"}}) {
			t.Fatalf("ended %v", c.Ended)
		}
		f.checkNoCrossing()
	})
}

// TestShareWaitsForGroupChange (#470, test 6, review B of #500): a group
// change of the reader that is not committed yet holds the reader's row
// FOR UPDATE. The share waits for it at ShareAccount's FOR SHARE, sees the
// new group after the commit and refuses; without that lock it would read
// the old group and create a share that crosses groups.
func TestShareWaitsForGroupChange(t *testing.T) {
	f := newGroupFixture(t)
	mover, err := f.conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mover.Rollback(f.ctx) }()
	if _, err := mover.Exec(f.ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, f.uwe.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mover.Exec(f.ctx, `UPDATE users SET group_id = $1 WHERE id = $2`, f.b.ID, f.uwe.ID); err != nil {
		t.Fatal(err)
	}
	ref := f.account(f.infoID).Ref()
	done := make(chan error, 1)
	go func() { _, err := f.st.ShareAccount(f.ctx, ref, f.uwe.ID); done <- err }()
	waitForLockWait(t, connect(t, f.url), done)
	if err := mover.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, store.ErrShareOtherGroup) {
		t.Fatalf("share during the group change: %v, want ErrShareOtherGroup", err)
	}
	f.checkNoCrossing()
}

// TestShareTriggerLocksUsers (#500, coordinator decision): the database
// layer holds on its own against concurrent raw writes. A raw share not
// committed yet holds both users' rows FOR SHARE (the insert trigger), so a
// raw group change of the reader waits for it and is then refused at its
// commit; without the trigger's lock it would not wait and both would
// commit, leaving a share across groups.
func TestShareTriggerLocksUsers(t *testing.T) {
	f := newGroupFixture(t)
	share, err := f.conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = share.Rollback(f.ctx) }()
	if _, err := share.Exec(f.ctx, `INSERT INTO account_shares (account_id, user_id) VALUES ($1, $2)`, f.infoID, f.uwe.ID); err != nil {
		t.Fatal(err)
	}
	other := connect(t, f.url)
	done := make(chan error, 1)
	go func() {
		_, err := other.Exec(context.Background(), `UPDATE users SET group_id = $1 WHERE id = $2`, f.b.ID, f.uwe.ID)
		done <- err
	}()
	waitForLockWait(t, connect(t, f.url), done)
	if err := share.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !isCheckViolation(err) {
		t.Fatalf("group change after the share: %v, want a check violation", err)
	}
	f.checkNoCrossing()
}

// TestGroupTriggersAtCommit (#470, test 7): a raw group change or owner
// change that leaves a crossing share fails at commit.
func TestGroupTriggersAtCommit(t *testing.T) {
	f := newGroupFixture(t)
	f.share(f.infoID, f.uwe)
	if _, err := f.conn.Exec(f.ctx, `UPDATE users SET group_id = $1 WHERE id = $2`, f.b.ID, f.uwe.ID); !isCheckViolation(err) {
		t.Fatalf("move the reader: %v, want a check violation", err)
	}
	if _, err := f.conn.Exec(f.ctx, `UPDATE users SET group_id = $1 WHERE id = $2`, f.b.ID, f.alice.ID); !isCheckViolation(err) {
		t.Fatalf("move the owner: %v, want a check violation", err)
	}
	if _, err := f.conn.Exec(f.ctx, `UPDATE accounts SET owner_id = $1 WHERE id = $2`, f.kim.ID, f.infoID); !isCheckViolation(err) {
		t.Fatalf("hand the account to kim: %v, want a check violation", err)
	}
	// Without a share in the way, both work.
	if _, err := f.conn.Exec(f.ctx, `UPDATE users SET group_id = $1 WHERE id = $2`, f.b.ID, f.root.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(f.ctx, `UPDATE accounts SET owner_id = $1 WHERE id = $2`, f.kim.ID, f.priv); err != nil {
		t.Fatal(err)
	}
	f.checkNoCrossing()
}

// insertCrossingShare writes a share across groups past the triggers, as a
// bug or a manual edit might: the read side must still grant nothing.
func (f *groupFixture) insertCrossingShare(accountID, userID int64) {
	f.t.Helper()
	tx, err := f.conn.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	for _, sql := range []string{
		`ALTER TABLE account_shares DISABLE TRIGGER account_shares_same_group`,
		fmt.Sprintf(`INSERT INTO account_shares (account_id, user_id) VALUES (%d, %d)`, accountID, userID),
		`ALTER TABLE account_shares ENABLE TRIGGER account_shares_same_group`,
	} {
		if _, err := tx.Exec(f.ctx, sql); err != nil {
			f.t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

// TestCrossingShareGrantsNothing (#470, test 8): a crossing row is not in
// the reader's scope and its reader's name is not in the owner's share
// lists.
func TestCrossingShareGrantsNothing(t *testing.T) {
	f := newGroupFixture(t)
	f.share(f.infoID, f.uwe)
	f.insertCrossingShare(f.infoID, f.kim.ID)
	if n := crossingShares(t, f.conn); n != 1 {
		t.Fatalf("%d crossing shares, want the planted one", n)
	}
	if got := scopeIDs(storetest.Viewer(t, f.st, f.kim.ID)); !slices.Equal(got, []int64{f.kbID}) {
		t.Fatalf("kim reads %v, want only kb", got)
	}
	shares, err := f.st.AccountShares(f.ctx, f.infoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 1 || shares[0].User != "uwe" {
		t.Fatalf("info's shares %v", shares)
	}
	owner, err := f.st.OwnerShares(f.ctx, f.alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := owner[f.infoID]; len(got) != 1 || got[0].User != "uwe" {
		t.Fatalf("alice's shares %v", owner)
	}
}

// TestAccountMoveOtherGroup (#470, test 9): an owner change across groups
// needs allowOtherGroup and then ends every share of the account.
func TestAccountMoveOtherGroup(t *testing.T) {
	f := newGroupFixture(t)
	f.share(f.infoID, f.uwe)
	f.share(f.infoID, f.bob)
	if err := f.st.SetAccountOwner(f.ctx, f.account(f.infoID).Ref(), f.kim.ID, false); !errors.Is(err, store.ErrOtherGroup) {
		t.Fatalf("move without allowOtherGroup: %v", err)
	}
	if got := scopeIDs(storetest.Viewer(t, f.st, f.uwe.ID)); !slices.Equal(got, []int64{f.infoID}) {
		t.Fatalf("refused move changed uwe's scope: %v", got)
	}
	e := store.TargetedEntry(store.ActorCLI, "account.move")
	e.SetTarget(store.TargetAccount, fmt.Sprint(f.infoID), "info")
	if err := f.st.SetAccountOwner(store.WithAudit(f.ctx, e), f.account(f.infoID).Ref(), f.kim.ID, true); err != nil {
		t.Fatal(err)
	}
	f.checkNoCrossing()
	if got := scopeIDs(storetest.Viewer(t, f.st, f.uwe.ID)); len(got) != 0 {
		t.Fatalf("uwe still reads %v", got)
	}
	if got := scopeIDs(storetest.Viewer(t, f.st, f.kim.ID)); !slices.Equal(got, []int64{f.infoID, f.kbID}) {
		t.Fatalf("kim reads %v", got)
	}
	es := groupedEntries(t, f.conn, "account.move")
	if len(es) != 1 || groupOf(es[0]) != fmt.Sprint(f.b.ID) ||
		fmt.Sprint(es[0].Details["shares_ended"]) != "[bob uwe]" || es[0].Details["shares_kept"] != 0.0 ||
		fmt.Sprint(es[0].Details["to_group"]) != fmt.Sprintf("map[id:%d name:B]", f.b.ID) {
		t.Fatalf("entries %+v", es)
	}
	// Inside a group a move keeps the shares, as before.
	f.share(f.priv, f.uwe)
	if err := f.st.SetAccountOwner(f.ctx, f.account(f.priv).Ref(), f.bob.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := scopeIDs(storetest.Viewer(t, f.st, f.uwe.ID)); !slices.Equal(got, []int64{f.priv}) {
		t.Fatalf("uwe reads %v after a move inside the group", got)
	}
}

// TestCreateUserGroups (#470, test 10): a new user needs a group while
// several exist; otherwise they join the only one.
func TestCreateUserGroups(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	// The first user adopts the accounts added before any user, and with
	// them into their group.
	early := createAccount(t, st, "early")
	first, err := st.CreateUser(ctx, "first", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := st.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Name != "Default" || groups[0].ID != first.GroupID || groups[0].Users != 1 || groups[0].Accounts != 1 {
		t.Fatalf("groups %+v, first in %d", groups, first.GroupID)
	}
	var g *int64
	if err := connect(t, url).QueryRow(ctx, `SELECT u.group_id FROM accounts a JOIN users u ON u.id = a.owner_id WHERE a.id = $1`, early.ID).Scan(&g); err != nil || g == nil || *g != first.GroupID {
		t.Fatalf("early's group %v, %v", g, err)
	}
	b, err := st.CreateGroup(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "nogroup", "h", false); !errors.Is(err, store.ErrGroupRequired) {
		t.Fatalf("without a group: %v", err)
	}
	if _, err := st.CreateUserWithGeneratedPassword(ctx, "nogroup", "h", false); !errors.Is(err, store.ErrGroupRequired) {
		t.Fatalf("generated, without a group: %v", err)
	}
	u, err := st.CreateUserInGroup(ctx, store.NewUser{Name: "inb", PasswordHash: "h", GroupID: b.ID, Generated: true})
	if err != nil || u.GroupID != b.ID || !u.MustChangePassword {
		t.Fatalf("in B: %+v, %v", u, err)
	}
	if _, err := st.CreateUserInGroup(ctx, store.NewUser{Name: "lost", PasswordHash: "h", GroupID: b.ID + 100}); !errors.Is(err, store.ErrUnknownGroup) {
		t.Fatalf("unknown group: %v", err)
	}
	if _, err := st.GetUserByName(ctx, "lost"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lost was created: %v", err)
	}
	// The name stays unique across groups.
	if _, err := st.CreateUserInGroup(ctx, store.NewUser{Name: "first", PasswordHash: "h", GroupID: b.ID}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("same name in B: %v", err)
	}
	// GetUserByNameInGroup finds a user only in their own group.
	if got, err := st.GetUserByNameInGroup(ctx, "inb", b.ID); err != nil || got.ID != u.ID {
		t.Fatalf("inb in B: %v, %v", got, err)
	}
	if _, err := st.GetUserByNameInGroup(ctx, "inb", first.GroupID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("inb in Default: %v", err)
	}
}

// TestGroupCRUD (#470, test 11).
func TestGroupCRUD(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	cli := func(action string) context.Context {
		return store.WithAudit(ctx, store.TargetedEntry(store.ActorCLI, action))
	}
	def, err := st.GetGroupByName(ctx, " DEFAULT ")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateGroup(cli("group.add"), "Steuerbüro Meier")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateGroup(ctx, "steuerbüro meier"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate in another case: %v", err)
	}
	for _, bad := range []string{"", " x", "x ", strings.Repeat("x", 101), "a\tb"} {
		if _, err := st.CreateGroup(ctx, bad); !errors.Is(err, store.ErrInvalidGroupName) {
			t.Errorf("name %q: %v", bad, err)
		}
	}
	if err := st.RenameGroup(cli("group.rename"), b.ID, "Meier"); err != nil {
		t.Fatal(err)
	}
	if err := st.RenameGroup(ctx, b.ID, "default"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("rename onto Default: %v", err)
	}
	if err := st.RenameGroup(ctx, b.ID+100, "x"); !errors.Is(err, store.ErrUnknownGroup) {
		t.Fatalf("rename unknown: %v", err)
	}
	u, err := st.CreateUserInGroup(ctx, store.NewUser{Name: "m", PasswordHash: "h", GroupID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGroup(ctx, b.ID); !errors.Is(err, store.ErrGroupInUse) {
		t.Fatalf("remove with a user: %v", err)
	}
	if err := st.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGroup(cli("group.remove"), b.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGroup(ctx, def.ID); !errors.Is(err, store.ErrLastGroup) {
		t.Fatalf("remove the last group: %v", err)
	}
	if _, err := st.GetGroup(ctx, b.ID); !errors.Is(err, store.ErrUnknownGroup) {
		t.Fatalf("removed group: %v", err)
	}
	for action, want := range map[string]string{"group.add": "Steuerbüro Meier", "group.rename": "Meier", "group.remove": "Meier"} {
		es := groupedEntries(t, conn, action)
		if len(es) != 1 || groupOf(es[0]) != fmt.Sprint(b.ID) || es[0].Details["group"] != want || es[0].Details["group_id"] != float64(b.ID) {
			t.Errorf("%s: %+v", action, es)
		}
	}
	if es := groupedEntries(t, conn, "group.rename"); len(es) == 1 && es[0].Details["from"] != "Steuerbüro Meier" {
		t.Errorf("rename from: %v", es[0].Details)
	}
}

// TestAuditGroupColumn (#470, test 12): the group each class records.
// root, an admin, sits in group B: instance-level work still has no group,
// and a targeted entry takes its target's group, not the actor's.
func TestAuditGroupColumn(t *testing.T) {
	f := newGroupFixture(t)
	f.setGroup(f.root, f.b)
	a, b := fmt.Sprint(f.a.ID), fmt.Sprint(f.b.ID)
	byRoot := func(e *store.AuditEntry) *store.AuditEntry {
		e.SetUser(f.root.ID, "root")
		return e
	}
	target := func(e *store.AuditEntry, typ string, id any, name string) *store.AuditEntry {
		e.SetTarget(typ, fmt.Sprint(id), name)
		return e
	}
	cases := []struct {
		name  string
		entry *store.AuditEntry
		want  string
	}{
		{"targeted user in A", target(byRoot(store.TargetedEntry(store.ActorSession, "user.change")), store.TargetUser, f.alice.ID, "alice"), a},
		{"targeted user in B", target(byRoot(store.TargetedEntry(store.ActorSession, "user.change")), store.TargetUser, f.kim.ID, "kim"), b},
		{"targeted account in A", target(byRoot(store.TargetedEntry(store.ActorSession, "account.change")), store.TargetAccount, f.infoID, "info"), a},
		{"targeted account in B", target(store.TargetedEntry(store.ActorCLI, "account.change"), store.TargetAccount, f.kbID, "kb"), b},
		{"targeted hold on an account", target(byRoot(store.TargetedEntry(store.ActorSession, "hold.add")), store.TargetAccount, f.infoID, "info"), a},
		{"targeted, unknown user", target(store.TargetedEntry(store.ActorCLI, "user.change"), store.TargetUser, 999999, "x"), "NULL"},
		{"targeted, non-numeric user id", target(store.TargetedEntry(store.ActorCLI, "user.change"), store.TargetUser, "abc", "x"), "NULL"},
		{"targeted, id beyond bigint", target(store.TargetedEntry(store.ActorCLI, "account.change"), store.TargetAccount, "99999999999999999999", "x"), "NULL"},
		{"targeted, a message target", target(store.TargetedEntry(store.ActorCLI, "user.change"), store.TargetMessage, hashOf('a'), ""), "NULL"},
		{"self of root", byRoot(store.SelfEntry(store.ActorSession, "auth.login")), b},
		{"self read of alice", target(func() *store.AuditEntry {
			e := store.SelfEntry(store.ActorSession, "message.open")
			e.SetUser(f.alice.ID, "alice")
			return e
		}(), store.TargetMessage, hashOf('a'), ""), a},
		{"instance by root: hold without owner", byRoot(store.InstanceEntry(store.ActorSession, "hold.add")), "NULL"},
		{"instance by root: deletion run", byRoot(store.InstanceEntry(store.ActorSession, "retention.preview")), "NULL"},
		{"instance by root: failed login", store.InstanceEntry(store.ActorSession, "auth.login_failed"), "NULL"},
		{"instance: backup", store.InstanceEntry(store.ActorCLI, "data.backup"), "NULL"},
	}
	for i, c := range cases {
		c.entry.Detail("case", i)
		if err := f.st.Audit(f.ctx, *c.entry); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	rows, err := f.conn.Query(f.ctx, `SELECT (details->>'case')::int, group_id FROM audit_log WHERE details ? 'case'`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for rows.Next() {
		var i int
		var g *int64
		if err := rows.Scan(&i, &g); err != nil {
			t.Fatal(err)
		}
		got[i] = groupOf(groupedEntry{GroupID: g})
	}
	for i, c := range cases {
		if got[i] != c.want {
			t.Errorf("%s: group %s, want %s", c.name, got[i], c.want)
		}
	}

	// Entries written after their target is gone keep its group.
	gone, err := f.st.CreateUserInGroup(f.ctx, store.NewUser{Name: "gone", PasswordHash: "h", GroupID: f.b.ID})
	if err != nil {
		t.Fatal(err)
	}
	rm := target(store.TargetedEntry(store.ActorCLI, "user.remove"), store.TargetUser, gone.ID, "gone")
	if err := f.st.DeleteUser(store.WithAudit(f.ctx, rm), gone.ID); err != nil {
		t.Fatal(err)
	}
	empty := &store.Account{Name: "empty", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &f.kim.ID}
	if err := f.st.CreateAccount(f.ctx, empty); err != nil {
		t.Fatal(err)
	}
	del := target(store.TargetedEntry(store.ActorCLI, "account.remove"), store.TargetAccount, empty.ID, "empty")
	if res, err := f.st.DeleteOrRemoveAccount(store.WithAudit(f.ctx, del), empty.Ref()); err != nil || res != store.AccountDeleted {
		t.Fatalf("delete empty: %v, %v", res, err)
	}
	// A retention rule is about its account even without a target.
	rule, err := f.st.AddRetentionRule(f.ctx, store.NewRetentionRule{AccountID: f.kbID, Years: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.RemoveRetentionRule(store.WithAudit(f.ctx, store.TargetedEntry(store.ActorCLI, "retention.rule_remove")), rule); err != nil {
		t.Fatal(err)
	}
	for action, want := range map[string]string{"user.remove": b, "account.remove": b, "retention.rule_remove": b} {
		if es := groupedEntries(t, f.conn, action); len(es) != 1 || groupOf(es[0]) != want {
			t.Errorf("%s: %+v, want group %s", action, es, want)
		}
	}
}

// TestPurgeFinishKeepsGroup (#470, test 12, review A of #500): the end of
// an account purge is written after the account row is gone and keeps the
// group the account had, that of its owner (here in group B).
func TestPurgeFinishKeepsGroup(t *testing.T) {
	f := newPurgeFixture(t)
	conn := connect(t, f.url)
	b, err := f.st.CreateGroup(f.ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	// alice's other accounts are not shared, so the move ends nothing.
	if _, err := f.st.SetUserGroup(f.ctx, f.alice.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	p := f.confirm(1)
	if err := f.st.CheckPurgeCount(f.ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	for {
		res, err := f.st.PurgeBatch(f.ctx, p.ID, 500, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if res.Done {
			break
		}
	}
	e := store.TargetedEntry(store.ActorCLI, "account.purge_finish")
	e.SetTarget(store.TargetAccount, fmt.Sprint(f.acc.ID), f.acc.Name)
	if _, err := f.st.FinishPurge(store.WithAudit(f.ctx, e), p.ID); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, conn, `SELECT count(*) FROM accounts WHERE id = $1`, f.acc.ID); n != 0 {
		t.Fatal("the account is still there")
	}
	if es := groupedEntries(t, conn, "account.purge_finish"); len(es) != 1 || groupOf(es[0]) != fmt.Sprint(b.ID) {
		t.Fatalf("purge_finish entries %+v, want group %d", es, b.ID)
	}
}

// TestAuditClassRequired: an entry without a class, of a class its action
// does not allow, of an unknown action or with a group outside the
// targeted class is refused, and the change it describes rolls back.
func TestAuditClassRequired(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	cases := map[string]*store.AuditEntry{
		"no class":          {Actor: store.ActorCLI, Action: "user.add"},
		"class not allowed": store.InstanceEntry(store.ActorCLI, "user.add"),
		"unknown action":    store.TargetedEntry(store.ActorCLI, "user.made_up"),
		"group on self": func() *store.AuditEntry {
			e := store.SelfEntry(store.ActorCLI, "auth.login")
			g := int64(1)
			e.GroupID = &g
			return e
		}(),
	}
	for name, e := range cases {
		if err := st.Audit(ctx, *e); !errors.Is(err, store.ErrAuditUnavailable) || !errors.Is(err, store.ErrAuditClass) {
			t.Errorf("%s, outside a change: %v", name, err)
		}
		if _, err := st.CreateUser(store.WithAudit(ctx, e), "x", "h", false); !errors.Is(err, store.ErrAuditClass) {
			t.Errorf("%s, with a change: %v", name, err)
		}
		if _, err := st.GetUserByName(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s: the user was created: %v", name, err)
		}
	}
}

// actionLiteral finds the actions the code names: quoted "family.verb"
// strings of the audit families.
var actionLiteral = regexp.MustCompile(`"((?:auth|profile|api_key|user|group|account|message|sync|data|audit|jobs|retention|hold|auditor|instance)\.[a-z0-9_]+)"`)

// TestAuditActionsClassified: every action the code names has at least
// one class, without duplicates, and every listed action is still named
// somewhere. Together with the refusal in insertAudit and the tests that
// write each entry, a new action cannot go unclassified.
func TestAuditActionsClassified(t *testing.T) {
	classes := store.AuditActionClasses()
	root := filepath.Join("..", "..")
	named := map[string][]string{}
	for _, dir := range []string{"cmd", "internal", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") ||
				strings.Contains(p, filepath.Join("store", "db")) || strings.HasSuffix(p, filepath.Join("store", "audit.go")) {
				return err
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for _, m := range actionLiteral.FindAllStringSubmatch(string(src), -1) {
				named[m[1]] = append(named[m[1]], p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for a, files := range named {
		if len(classes[a]) == 0 {
			t.Errorf("%s (%v) has no audit class", a, files)
		}
	}
	for a, cs := range classes {
		if len(cs) == 0 || len(slices.Compact(slices.Sorted(slices.Values(cs)))) != len(cs) {
			t.Errorf("%s: classes %v", a, cs)
		}
		if _, ok := named[a]; !ok {
			t.Errorf("%s is listed but no code names it", a)
		}
	}
}

// TestSessionGroup (#470, test 14): a session and an API key login carry
// the user's group.
func TestSessionGroup(t *testing.T) {
	f := newGroupFixture(t)
	hash := auth.HashSessionToken("a session token of kim")
	if err := f.st.CreateSession(f.ctx, hash, f.kim.ID, time.Now().Add(time.Hour), "ua"); err != nil {
		t.Fatal(err)
	}
	sess, err := f.st.GetSession(f.ctx, hash, time.Now().Add(-time.Hour))
	if err != nil || sess.GroupID != f.b.ID {
		t.Fatalf("session %+v, %v", sess, err)
	}
	key, _ := newKey(t, f.st, f.kim.ID, "k", []string{"read"}, nil)
	ks, _, err := f.st.APIKeyLogin(f.ctx, auth.HashAPIKey(key))
	if err != nil || ks.GroupID != f.b.ID {
		t.Fatalf("API key session %+v, %v", ks, err)
	}
}
