package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// Legal holds per customer group (#470, part 4). The group fixture has
// alice (info, private), bob (mine), uwe and root in "Default" (A) and kim
// (kb, message k) in B.

// deleteLocation deletes the locations of sha in folder in a transaction it
// rolls back, and returns the guard trigger's answer.
func (f *groupFixture) deleteLocation(sha string, folder int64) error {
	f.t.Helper()
	tx, err := f.conn.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(f.ctx, "DELETE FROM message_locations WHERE message_sha256 = $1 AND folder_id = $2", sha, folder)
	return err
}

func isGuarded(err error) bool {
	return err != nil && strings.Contains(err.Error(), "still retained or on legal hold")
}

// TestGroupHoldCoversGroup: a hold naming group A keeps the mail of every
// account in A, also of a user who joins A after the hold, from deletion
// runs, the deletion guard and purges; the mail of group B is not held.
func TestGroupHoldCoversGroup(t *testing.T) {
	f := newGroupFixture(t)
	f.rule(f.info, "", 1)
	f.rule(f.kbFold, "", 1)
	hold := f.hold(store.NewLegalHold{GroupID: &f.a.ID})

	// nora joins A after the hold.
	nora, err := f.st.CreateUserInGroup(f.ctx, store.NewUser{Name: "nora", PasswordHash: "h", GroupID: f.a.ID})
	if err != nil {
		t.Fatal(err)
	}
	nf := f.folder(nora, "n", "INBOX")
	f.save(nf, hashOf('n'), "n", old, old)
	f.rule(nf, "", 1)

	run := f.propose()
	got := f.candidates(run.ID)
	if !slices.Contains(got, hashOf('k')) {
		t.Errorf("B's message is not a candidate: %v", got)
	}
	for _, sha := range []string{hashOf('a'), hashOf('n')} {
		if slices.Contains(got, sha) {
			t.Errorf("held message %.1s is a candidate", sha)
		}
	}
	if err := f.st.CancelDeletionRun(f.ctx, run.ID); err != nil {
		t.Fatal(err)
	}

	// The guard trigger.
	if err := f.deleteLocation(hashOf('n'), nf.ID); !isGuarded(err) {
		t.Errorf("deleting nora's held location: %v", err)
	}
	if err := f.deleteLocation(hashOf('a'), f.info.ID); !isGuarded(err) {
		t.Errorf("deleting alice's held location: %v", err)
	}
	if err := f.deleteLocation(hashOf('k'), f.kbFold.ID); err != nil {
		t.Errorf("deleting kim's location in B: %v", err)
	}

	// Purges: nora's removed account is kept by the hold, kim's is not.
	purgeHolds := func(owner *store.User, account string) []int64 {
		t.Helper()
		acc, err := f.st.GetOwnedAccount(f.ctx, owner.ID, account)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.DeleteOrRemoveAccount(f.ctx, acc.Ref()); err != nil {
			t.Fatal(err)
		}
		p, err := f.st.PurgePreview(f.ctx, acc.ID)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, h := range p.Blockers.Holds {
			ids = append(ids, h.ID)
		}
		return ids
	}
	if ids := purgeHolds(nora, "n"); !slices.Equal(ids, []int64{hold}) {
		t.Errorf("purge of nora's account: holds %v, want [%d]", ids, hold)
	}
	if ids := purgeHolds(f.kim, "kb"); len(ids) != 0 {
		t.Errorf("purge of kim's account: holds %v, want none", ids)
	}

	// Released, the hold keeps nothing.
	if err := f.st.ReleaseLegalHold(f.ctx, hold, nil, "done"); err != nil {
		t.Fatal(err)
	}
	if err := f.deleteLocation(hashOf('a'), f.info.ID); err != nil {
		t.Errorf("deleting alice's location after the release: %v", err)
	}
}

// TestGroupHoldMustMatch: a hold whose user or account is in another group
// than the one it names would cover nothing, and is refused.
func TestGroupHoldMustMatch(t *testing.T) {
	f := newGroupFixture(t)
	add := func(h store.NewLegalHold) error {
		h.Name = "x"
		_, err := f.st.AddLegalHold(f.ctx, h)
		return err
	}
	if err := add(store.NewLegalHold{OwnerID: &f.alice.ID, GroupID: &f.b.ID}); !errors.Is(err, store.ErrInvalidHold) {
		t.Errorf("alice with group B: %v, want ErrInvalidHold", err)
	}
	if err := add(store.NewLegalHold{AccountID: &f.kbID, GroupID: &f.a.ID}); !errors.Is(err, store.ErrInvalidHold) {
		t.Errorf("kb with group A: %v, want ErrInvalidHold", err)
	}
	if err := add(store.NewLegalHold{OwnerID: &f.alice.ID, AccountID: &f.kbID, GroupID: &f.a.ID}); !errors.Is(err, store.ErrInvalidHold) {
		t.Errorf("alice and kb with group A: %v, want ErrInvalidHold", err)
	}
	if !errors.Is(store.ErrInvalidHold, store.ErrInvalidRetention) {
		t.Error("ErrInvalidHold is no ErrInvalidRetention: the web would not answer 400")
	}
	unknown := int64(9999)
	if err := add(store.NewLegalHold{GroupID: &unknown}); !errors.Is(err, store.ErrUnknownGroup) {
		t.Errorf("unknown group: %v, want ErrUnknownGroup", err)
	}
	if n := countOf(t, f.conn, "SELECT count(*) FROM legal_holds"); n != 0 {
		t.Fatalf("%d holds after refusals", n)
	}
	for _, h := range []store.NewLegalHold{
		{OwnerID: &f.kim.ID, GroupID: &f.b.ID},
		{AccountID: &f.kbID, GroupID: &f.b.ID},
		{OwnerID: &f.alice.ID, AccountID: &f.infoID, GroupID: &f.a.ID},
	} {
		if err := add(h); err != nil {
			t.Errorf("%+v: %v", h, err)
		}
	}
}

// TestGroupHoldWaitsForGroupChange (#470 part 4 race): a group change of
// the hold's user that is not committed yet holds the user's row FOR
// UPDATE. AddLegalHold waits for it, sees the new group after the commit
// and refuses; without its FOR SHARE lock it would read the old group and
// add a hold that covers nothing.
func TestGroupHoldWaitsForGroupChange(t *testing.T) {
	f := newGroupFixture(t)
	mover, err := f.conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mover.Rollback(f.ctx) }()
	if _, err := mover.Exec(f.ctx, `UPDATE users SET group_id = $1 WHERE id = $2`, f.b.ID, f.uwe.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.st.AddLegalHold(f.ctx, store.NewLegalHold{Name: "x", OwnerID: &f.uwe.ID, GroupID: &f.a.ID})
		done <- err
	}()
	waitForLockWait(t, connect(t, f.url), done)
	if err := mover.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, store.ErrInvalidHold) {
		t.Fatalf("hold during the group change: %v, want ErrInvalidHold", err)
	}
}

// TestGroupHoldRacesAccountMove (#503 review B): a hold naming an account
// and its group, and a move of that account to another group, in both
// orders. Never a hold whose account check passed on the account's old
// owner. The barriers are row locks the test holds on its own connection;
// it waits for the blocked statement itself (waitForLockWait).
func TestGroupHoldRacesAccountMove(t *testing.T) {
	t.Run("move commits first", func(t *testing.T) {
		// The move is not committed yet and holds the account row. The hold
		// waits at LockAccountOwnerShare (FOR SHARE), sees kim, of group B,
		// after the commit and refuses; without that lock it would read
		// alice, of group A, and add a hold on an account of B.
		f := newGroupFixture(t)
		mover, err := f.conn.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = mover.Rollback(f.ctx) }()
		if _, err := mover.Exec(f.ctx, `UPDATE accounts SET owner_id = $1 WHERE id = $2`, f.kim.ID, f.infoID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := f.st.AddLegalHold(f.ctx, store.NewLegalHold{Name: "x", AccountID: &f.infoID, GroupID: &f.a.ID})
			done <- err
		}()
		waitForLockWait(t, connect(t, f.url), done)
		if err := mover.Commit(f.ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, store.ErrInvalidHold) {
			t.Fatalf("hold during the move: %v, want ErrInvalidHold", err)
		}
		if n := countOf(t, f.conn, "SELECT count(*) FROM legal_holds"); n != 0 {
			t.Fatalf("%d holds after the refused one", n)
		}
	})
	t.Run("hold commits first", func(t *testing.T) {
		// A hold that is not committed yet holds the account row FOR SHARE,
		// as AddLegalHold does. The move waits for it at its FOR UPDATE,
		// then sees the hold and refuses.
		f := newGroupFixture(t)
		holder, err := f.conn.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback(f.ctx) }()
		var hold int64
		if _, err := holder.Exec(f.ctx, `SELECT owner_id FROM accounts WHERE id = $1 FOR SHARE`, f.infoID); err != nil {
			t.Fatal(err)
		}
		if err := holder.QueryRow(f.ctx, `INSERT INTO legal_holds (name, account_id, group_id) VALUES ('x', $1, $2) RETURNING id`,
			f.infoID, f.a.ID).Scan(&hold); err != nil {
			t.Fatal(err)
		}
		ref := f.account(f.infoID).Ref()
		done := make(chan error, 1)
		go func() { done <- f.st.SetAccountOwner(f.ctx, ref, f.kim.ID, true) }()
		waitForLockWait(t, connect(t, f.url), done)
		if err := holder.Commit(f.ctx); err != nil {
			t.Fatal(err)
		}
		err = <-done
		checkHoldError(t, err, fmt.Sprintf("named by a legal hold: the account's owner or their group is on legal hold #%d", hold), hold)
		if a := f.account(f.infoID); *a.OwnerID != f.alice.ID {
			t.Fatal("info moved")
		}
	})
}

// TestGroupHoldGuardsMoves: a user or an account cannot leave a group
// whose hold covers their mail; inside the group, and without mail, they
// can.
func TestGroupHoldGuardsMoves(t *testing.T) {
	f := newGroupFixture(t)
	hold := f.hold(store.NewLegalHold{GroupID: &f.a.ID})
	text := fmt.Sprintf("named by a legal hold: the user's accounts are held through their group by legal hold #%d", hold)

	_, err := f.st.SetUserGroup(f.ctx, f.alice.ID, f.b.ID)
	checkHoldError(t, err, text, hold)
	if u, _ := f.st.GetUserByID(f.ctx, f.alice.ID); u.GroupID != f.a.ID {
		t.Fatal("alice moved")
	}
	// uwe owns no account: the hold covers nothing of his.
	f.setGroup(f.uwe, f.b)
	// kim joins A: the hold now covers kb as well, so kim cannot leave A
	// again while it is active.
	f.setGroup(f.kim, f.a)
	_, err = f.st.SetUserGroup(f.ctx, f.kim.ID, f.b.ID)
	checkHoldError(t, err, text, hold)
	if err := f.st.ReleaseLegalHold(f.ctx, hold, nil, "done"); err != nil {
		t.Fatal(err)
	}
	f.setGroup(f.kim, f.b)

	// The account move across groups.
	hold = f.hold(store.NewLegalHold{GroupID: &f.a.ID})
	err = f.st.SetAccountOwner(f.ctx, f.account(f.infoID).Ref(), f.kim.ID, true)
	checkHoldError(t, err, fmt.Sprintf("named by a legal hold: the account's owner or their group is on legal hold #%d", hold), hold)
	if a := f.account(f.infoID); *a.OwnerID != f.alice.ID {
		t.Fatal("info moved")
	}
	// Inside A the hold keeps covering it.
	if err := f.st.SetAccountOwner(f.ctx, f.account(f.infoID).Ref(), f.bob.ID, false); err != nil {
		t.Fatalf("move inside the group: %v", err)
	}
	// A hold on a user and a group: the move to another user of the group
	// releases it, as a hold on the user alone does.
	owned := f.hold(store.NewLegalHold{OwnerID: &f.bob.ID, GroupID: &f.a.ID})
	err = f.st.SetAccountOwner(f.ctx, f.account(f.infoID).Ref(), f.alice.ID, false)
	checkHoldError(t, err, fmt.Sprintf("named by a legal hold: the account's owner or their group is on legal hold #%d", owned), owned)
}

// TestDeleteGroupNamedByHold (#470 plan review, finding 7): a group that a
// hold names, also a released one, stays; DeleteGroup maps the foreign key
// violation to ErrGroupHeld, which is also ErrGroupInUse.
func TestDeleteGroupNamedByHold(t *testing.T) {
	f := newGroupFixture(t)
	c, err := f.st.CreateGroup(f.ctx, "C")
	if err != nil {
		t.Fatal(err)
	}
	hold := f.hold(store.NewLegalHold{GroupID: &c.ID})
	if err := f.st.ReleaseLegalHold(f.ctx, hold, nil, "done"); err != nil {
		t.Fatal(err)
	}
	err = f.st.DeleteGroup(f.ctx, c.ID)
	if !errors.Is(err, store.ErrGroupHeld) || !errors.Is(err, store.ErrGroupInUse) {
		t.Fatalf("delete a group a released hold names: %v, want ErrGroupHeld and ErrGroupInUse", err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Fatalf("the raw database error came through: %v", err)
	}
	if _, err := f.st.GetGroup(f.ctx, c.ID); err != nil {
		t.Fatalf("the group is gone: %v", err)
	}
	// An empty group without holds still goes.
	d, err := f.st.CreateGroup(f.ctx, "D")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteGroup(f.ctx, d.ID); err != nil {
		t.Fatal(err)
	}
}

// TestGroupHoldMatching: a filter hold on a group matches as the group's
// users only.
func TestGroupHoldMatching(t *testing.T) {
	f := newGroupFixture(t)
	// "falcon" is in alice's a and p (A); kim's k (B) has none.
	inB := f.hold(store.NewLegalHold{Name: "b", GroupID: &f.b.ID, Filter: &store.HoldFilter{Query: "falcon"}})
	inA := f.hold(store.NewLegalHold{Name: "a", GroupID: &f.a.ID, Filter: &store.HoldFilter{Query: "falcon"}})
	if _, err := f.st.MatchLegalHolds(f.ctx); err != nil {
		t.Fatal(err)
	}
	holds, err := f.st.LegalHolds(f.ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	matched := map[int64]int64{}
	for _, h := range holds {
		matched[h.ID] = h.Matched
	}
	if matched[inB] != 0 || matched[inA] != 2 {
		t.Fatalf("matched B %d (want 0), A %d (want 2)", matched[inB], matched[inA])
	}
}

// TestGroupHoldAudit: an entry about a hold that names only a group is
// recorded under that group, for hold.add and hold.release, with the
// group in its details.
func TestGroupHoldAudit(t *testing.T) {
	f := newGroupFixture(t)
	ctx := store.WithAudit(f.ctx, store.TargetedEntry(store.ActorCLI, "hold.add"))
	id, err := f.st.AddLegalHold(ctx, store.NewLegalHold{Name: "x", GroupID: &f.b.ID})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithAudit(f.ctx, store.TargetedEntry(store.ActorCLI, "hold.release"))
	if err := f.st.ReleaseLegalHold(ctx, id, nil, "done"); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"hold.add", "hold.release"} {
		es := groupedEntries(t, f.conn, action)
		if len(es) != 1 || groupOf(es[0]) != fmt.Sprint(f.b.ID) || es[0].Details["group"] != "B" {
			t.Errorf("%s: %+v, want group %d with its name", action, es, f.b.ID)
		}
	}
	scope, err := f.st.LegalHoldScope(f.ctx, id)
	if err != nil || scope.Instance() || scope.GroupID == nil || *scope.GroupID != f.b.ID {
		t.Errorf("scope %+v, %v", scope, err)
	}
}

// TestRetentionListsGroups: rules, accounts, holds and the review of a
// deletion run carry the customer group.
func TestRetentionListsGroups(t *testing.T) {
	f := newGroupFixture(t)
	f.rule(f.kbFold, "", 1)
	// Holds that do not cover k (sent at old, the period ends before).
	f.hold(store.NewLegalHold{Name: "kim", OwnerID: &f.kim.ID, PeriodEnd: old})
	f.hold(store.NewLegalHold{Name: "group", GroupID: &f.a.ID})
	f.hold(store.NewLegalHold{Name: "all", PeriodEnd: old})

	rules, err := f.st.RetentionRules(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].GroupID == nil || *rules[0].GroupID != f.b.ID || rules[0].Group != "B" {
		t.Errorf("rules %+v", rules)
	}
	accounts, err := f.st.RetentionAccounts(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accounts {
		want := "Default"
		if a.ID == f.kbID {
			want = "B"
		}
		if a.Group != want || a.GroupID == nil {
			t.Errorf("account %s in %q, want %q", a.Name, a.Group, want)
		}
	}
	holds, err := f.st.LegalHolds(f.ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, h := range holds {
		got[h.Name] = fmt.Sprintf("%s/%s", h.Group, h.CoveredGroup)
	}
	if want := map[string]string{"kim": "/B", "group": "Default/Default", "all": "/"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("holds %v, want %v", got, want)
	}

	run := f.propose()
	sum, err := f.st.DeletionRunSummary(f.ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum) != 1 || sum[0].CustomerGroup != "B" || sum[0].GroupID == nil || *sum[0].GroupID != f.b.ID {
		t.Errorf("summary %+v", sum)
	}
}

// TestGroupHoldsMigration: 00035 Down refuses while a hold names a group,
// and otherwise drops the column; the holds of owners keep working with
// the 00030 body of location_holds; Up adds the column again.
func TestGroupHoldsMigration(t *testing.T) {
	t.Run("refused with a group hold", func(t *testing.T) {
		f := newGroupFixture(t)
		f.hold(store.NewLegalHold{GroupID: &f.b.ID})
		downBlobEncryptionMigration(t, f.st)
		downAuditorMigration(t, f.st)
		if _, err := f.st.MigrateDown(f.ctx); err == nil || !strings.Contains(err.Error(), "legal holds name customer groups") {
			t.Fatalf("down with a group hold: %v", err)
		}
		if n := countOf(t, f.conn, "SELECT count(*) FROM legal_holds WHERE group_id IS NOT NULL"); n != 1 {
			t.Fatalf("%d group holds after the refused Down", n)
		}
	})
	t.Run("down and up", func(t *testing.T) {
		st, url := storetest.NewWithURL(t)
		ctx := context.Background()
		conn := connect(t, url)
		f := &groupFixture{shareFixture: &shareFixture{retentionFixture: &retentionFixture{t: t, ctx: ctx, st: st, url: url}}, conn: conn}
		alice, err := st.CreateUser(ctx, "alice", "h", false)
		if err != nil {
			t.Fatal(err)
		}
		inbox := f.folder(alice, "work", "INBOX")
		f.save(inbox, hashOf('a'), "a", old, old)
		f.rule(inbox, "", 1)
		f.hold(store.NewLegalHold{OwnerID: &alice.ID})
		downGroupHoldsMigration(t, st)
		if n := countOf(t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'legal_holds' AND column_name = 'group_id'`); n != 0 {
			t.Fatal("legal_holds.group_id survived the Down")
		}
		if err := f.deleteLocation(hashOf('a'), inbox.ID); !isGuarded(err) {
			t.Fatalf("the owner's hold after the Down: %v", err)
		}
		if _, err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if n := countOf(t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'legal_holds' AND column_name = 'group_id'`); n != 1 {
			t.Fatal("legal_holds.group_id missing after the Up")
		}
		if err := f.deleteLocation(hashOf('a'), inbox.ID); !isGuarded(err) {
			t.Fatalf("the owner's hold after the Up: %v", err)
		}
	})
}
