package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/store/storetest"
)

// Auditors and customer groups (#393, revision 2): the group tests of the
// plan for the store, numbered as there. In the plan's names pat is alice,
// jo is bob (a member of group a who owns mine), kim is kim in group b.

// downAuditorMigration rolls back 00036_auditor_access.
func downAuditorMigration(t *testing.T, st *store.Store) {
	t.Helper()
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00036_auditor_access") {
		t.Fatalf("down: %q %v", m, err)
	}
}

// waitForLockWaits returns once n statements of the test's database wait
// for a lock.
func waitForLockWaits(t *testing.T, conn *pgx.Conn, done <-chan error, n int) {
	t.Helper()
	waitForRow(t, conn, done, fmt.Sprintf("%d statements waiting for a lock", n), `SELECT (count(*) >= $1)::int FROM pg_stat_activity
		WHERE datname = current_database() AND wait_event_type = 'Lock'`, n)
}

// insertRaw runs statements in one transaction with the given triggers
// off, as a bug or a manual edit might write past them.
func (f *auditorFixture) insertRaw(triggers map[string]string, stmts ...string) {
	f.t.Helper()
	tx, err := f.conn.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	var all []string
	for table, trigger := range triggers {
		all = append(all, fmt.Sprintf(`ALTER TABLE %s DISABLE TRIGGER %s`, table, trigger))
	}
	all = append(all, stmts...)
	for table, trigger := range triggers {
		all = append(all, fmt.Sprintf(`ALTER TABLE %s ENABLE TRIGGER %s`, table, trigger))
	}
	for _, sql := range all {
		if _, err := tx.Exec(f.ctx, sql); err != nil {
			f.t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

// TestGetAuditorByNameInGroup (group test 1, store part): the lookup finds
// only an auditor of the group.
func TestGetAuditorByNameInGroup(t *testing.T) {
	f := newAuditorFixture(t)
	for _, name := range []string{"audb", "bob", "kim", "root", "nosuch"} {
		if _, err := f.st.GetAuditorByNameInGroup(f.ctx, name, f.a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s in a: %v, want ErrNotFound", name, err)
		}
	}
	if u, err := f.st.GetAuditorByNameInGroup(f.ctx, "auda", f.a.ID); err != nil || u.ID != f.auda.ID {
		t.Fatalf("auda: %+v, %v", u, err)
	}
}

// TestGrantStoreGroups (group test 2): the store refuses a grant across
// groups or to a non-auditor with a typed error, also for an extension of
// a grant that crosses groups past the triggers.
func TestGrantStoreGroups(t *testing.T) {
	f := newAuditorFixture(t)
	if _, err := f.st.CreateGrant(f.ctx, f.grantDefaults(store.NewGrant{AuditorID: f.audb.ID})); !errors.Is(err, store.ErrGrantOtherGroup) {
		t.Errorf("alice to audb: %v, want ErrGrantOtherGroup", err)
	}
	if _, err := f.st.CreateGrant(f.ctx, f.grantDefaults(store.NewGrant{AuditorID: f.bob.ID})); !errors.Is(err, store.ErrGrantNotAuditor) {
		t.Errorf("alice to bob: %v, want ErrGrantNotAuditor", err)
	}
	if n := f.count(`SELECT count(*) FROM auditor_grants`); n != 0 {
		t.Fatalf("%d grants", n)
	}
	f.insertRaw(map[string]string{"auditor_grants": "auditor_grants_same_group"},
		fmt.Sprintf(`INSERT INTO auditor_grants (id, auditor_id, owner_id, label, ends_at) VALUES (900, %d, %d, 'x', now() + interval '1 day')`, f.audb.ID, f.alice.ID))
	if err := f.st.ExtendGrant(f.ctx, 900, f.alice.ID, time.Now().AddDate(0, 0, 10)); !errors.Is(err, store.ErrGrantOtherGroup) {
		t.Errorf("extend the crossing grant: %v, want ErrGrantOtherGroup", err)
	}
}

// TestGrantTriggers (group test 3): the database refuses what the store
// refuses, whatever writes it.
func TestGrantTriggers(t *testing.T) {
	f := newAuditorFixture(t)
	journal := &store.Account{Name: "journal", Kind: store.KindSMTP, OwnerID: &f.alice.ID}
	if err := f.st.CreateAccount(f.ctx, journal); err != nil {
		t.Fatal(err)
	}
	g := f.grant(store.NewGrant{})
	revoked := f.grant(store.NewGrant{})
	if err := f.st.RevokeGrant(f.ctx, revoked, nil, nil); err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{
		"grant across groups": fmt.Sprintf(`INSERT INTO auditor_grants (auditor_id, owner_id, label, ends_at) VALUES (%d, %d, 'x', now() + interval '1 day')`, f.audb.ID, f.alice.ID),
		"member as auditor":   fmt.Sprintf(`INSERT INTO auditor_grants (auditor_id, owner_id, label, ends_at) VALUES (%d, %d, 'x', now() + interval '1 day')`, f.bob.ID, f.alice.ID),
		"auditor as owner":    fmt.Sprintf(`INSERT INTO auditor_grants (auditor_id, owner_id, label, ends_at) VALUES (%d, %d, 'x', now() + interval '1 day')`, f.auda.ID, f.aud2.ID),
		"bob's account":       fmt.Sprintf(`INSERT INTO auditor_grant_accounts (grant_id, account_id, all_folders) VALUES (%d, %d, true)`, g, f.mineID),
		"kim's account":       fmt.Sprintf(`INSERT INTO auditor_grant_accounts (grant_id, account_id, all_folders) VALUES (%d, %d, true)`, g, f.kbID),
		"smtp account":        fmt.Sprintf(`INSERT INTO auditor_grant_accounts (grant_id, account_id, all_folders) VALUES (%d, %d, true)`, g, journal.ID),
		"move the auditor":    fmt.Sprintf(`UPDATE users SET group_id = %d WHERE id = %d`, f.b.ID, f.auda.ID),
		"move the owner":      fmt.Sprintf(`UPDATE users SET group_id = %d WHERE id = %d`, f.b.ID, f.alice.ID),
		"move the account":    fmt.Sprintf(`UPDATE accounts SET owner_id = %d WHERE id = %d`, f.bob.ID, f.a1),
		"change the role":     fmt.Sprintf(`UPDATE users SET is_auditor = false WHERE id = %d`, f.auda.ID),
		"clear revoked_at":    fmt.Sprintf(`UPDATE auditor_grants SET revoked_at = NULL, revoke_reason = NULL WHERE id = %d`, revoked),
		"extend a revoked":    fmt.Sprintf(`UPDATE auditor_grants SET ends_at = ends_at + interval '1 day' WHERE id = %d`, revoked),
		"change the scope":    fmt.Sprintf(`UPDATE auditor_grants SET period_from = now() WHERE id = %d`, g),
		"change the auditor":  fmt.Sprintf(`UPDATE auditor_grants SET auditor_id = %d WHERE id = %d`, f.aud2.ID, g),
	} {
		if _, err := f.conn.Exec(f.ctx, sql); !isCheckViolation(err) {
			t.Errorf("%s: %v, want a check violation", name, err)
		}
	}
	// What stays allowed: extending and revoking an unrevoked grant, a
	// move of a user without grants, an account move of an ungranted one.
	for _, sql := range []string{
		fmt.Sprintf(`UPDATE auditor_grants SET ends_at = ends_at + interval '1 day' WHERE id = %d`, g),
		fmt.Sprintf(`UPDATE users SET group_id = %d WHERE id = %d`, f.b.ID, f.uwe.ID),
		fmt.Sprintf(`UPDATE accounts SET owner_id = %d WHERE id = %d`, f.bob.ID, f.a2),
		fmt.Sprintf(`UPDATE auditor_grants SET revoked_at = now(), revoke_reason = 'operator' WHERE id = %d`, g),
	} {
		if _, err := f.conn.Exec(f.ctx, sql); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
	f.checkNoCrossingGrants()
}

// TestCrossingGrantGrantsNothing (group test 4): rows written past the
// triggers grant nothing on the read side: a grant across groups, and
// accounts of another owner or group in an active grant.
func TestCrossingGrantGrantsNothing(t *testing.T) {
	f := newAuditorFixture(t)
	f.insertRaw(map[string]string{"auditor_grants": "auditor_grants_same_group"},
		fmt.Sprintf(`INSERT INTO auditor_grants (id, auditor_id, owner_id, label, ends_at) VALUES (900, %d, %d, 'crossing', now() + interval '1 day')`, f.audb.ID, f.alice.ID),
		fmt.Sprintf(`INSERT INTO auditor_grant_accounts (grant_id, account_id, all_folders) VALUES (900, %d, true)`, f.a1))
	if n := f.count(`SELECT count(*) FROM active_auditor_grants WHERE id = 900`); n != 0 {
		t.Fatal("the crossing grant is active")
	}
	if sc := f.scope(f.audb, 900); len(sc.Accounts) != 0 {
		t.Errorf("audb reads %v", scopeIDs(sc))
	}
	sess := f.session(f.audb, 0)
	if _, err := f.conn.Exec(f.ctx, `UPDATE sessions SET grant_id = 900 WHERE id = $1`, sess); err != nil {
		t.Fatal(err)
	}
	if f.sessionOK(sess) {
		t.Error("audb's session on the crossing grant is accepted")
	}
	if list, err := f.st.AuditorGrants(f.ctx, f.audb.ID); err != nil || len(list) != 0 {
		t.Errorf("audb's grants %+v, %v", list, err)
	}
	list, err := f.st.OwnerGrants(f.ctx, f.alice.ID)
	if err != nil || len(list) != 1 || list[0].AuditorID != nil || list[0].Auditor != "" || list[0].Active {
		t.Fatalf("alice's grants %+v, %v", list, err)
	}

	// Stray accounts in an active grant: kim's kb (group b) and bob's mine
	// (group a, another owner).
	g := f.grant(store.NewGrant{})
	f.insertRaw(map[string]string{"auditor_grant_accounts": "auditor_grant_accounts_owner"},
		fmt.Sprintf(`INSERT INTO auditor_grant_accounts (grant_id, account_id, all_folders) VALUES (%d, %d, true), (%d, %d, true)`, g, f.kbID, g, f.mineID))
	if n := f.count(`SELECT count(*) FROM auditor_grant_accounts WHERE grant_id = $1`, g); n != 3 {
		t.Fatalf("%d rows, want the planted ones", n)
	}
	if got := f.count(`SELECT count(*) FROM active_auditor_grant_accounts WHERE grant_id = $1`, g); got != 1 {
		t.Errorf("the active view has %d accounts, want a1 only", got)
	}
	sc := f.scope(f.auda, g)
	if got := scopeIDs(sc); !slices.Equal(got, []int64{f.a1}) {
		t.Errorf("auda reads %v, want only a1", got)
	}
	if got := f.listed(sc); slices.ContainsFunc(got, func(s string) bool { return s == "k" || s == "b" || s == "x" }) {
		t.Errorf("auda lists %v", got)
	}
	if got := f.folders(sc); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "kb") || strings.HasPrefix(s, "mine") }) {
		t.Errorf("auda's folders %v", got)
	}
	names := func(list []store.AuditorGrant) []string {
		var out []string
		for _, x := range list {
			if x.ID == g {
				for _, a := range x.Accounts {
					out = append(out, a.Name)
				}
			}
		}
		return out
	}
	mine, err := f.st.AuditorGrants(f.ctx, f.auda.ID)
	if err != nil || !slices.Equal(names(mine), []string{"a1"}) {
		t.Errorf("auda's grant lists %v, %v", names(mine), err)
	}
	owner, err := f.st.OwnerGrants(f.ctx, f.alice.ID)
	if err != nil || !slices.Equal(names(owner), []string{"a1"}) {
		t.Errorf("alice's grant lists %v, %v", names(owner), err)
	}
}

// TestGrantRaces (group test 5): a grant change and a group move or user
// removal, in both orders, never cross groups and never deadlock. The
// barriers are locks the test holds on its own connection; it waits for
// the blocked statements themselves.
func TestGrantRaces(t *testing.T) {
	begin := func(f *auditorFixture, sql string, args ...any) pgx.Tx {
		t.Helper()
		tx, err := f.conn.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		if _, err := tx.Exec(f.ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	moveAuda := func(f *auditorFixture, c *store.GroupChange) <-chan error {
		done := make(chan error, 1)
		go func() {
			var err error
			*c, err = f.st.SetUserGroup(f.ctx, f.auda.ID, f.b.ID)
			done <- err
		}()
		return done
	}
	extend := func(f *auditorFixture, g int64, to time.Time) <-chan error {
		done := make(chan error, 1)
		go func() { done <- f.st.ExtendGrant(f.ctx, g, f.alice.ID, to) }()
		return done
	}
	noDeadlock := func(t *testing.T, err error) {
		t.Helper()
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
			t.Fatalf("deadlock: %v", err)
		}
	}
	t.Run("create, move commits first", func(t *testing.T) {
		f := newAuditorFixture(t)
		hold := begin(f, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, f.a1)
		done := make(chan error, 1)
		go func() { _, err := f.st.CreateGrant(f.ctx, f.grantDefaults(store.NewGrant{})); done <- err }()
		waitForLockWait(t, connect(t, f.url), done)
		f.setGroup(f.auda, f.b)
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, store.ErrGrantOtherGroup) {
			t.Fatalf("grant after the move: %v, want ErrGrantOtherGroup", err)
		}
		f.checkNoCrossingGrants()
	})
	t.Run("create, grant commits first", func(t *testing.T) {
		f := newAuditorFixture(t)
		hold := begin(f, `SELECT 1 FROM users WHERE id = $1 FOR KEY SHARE`, f.auda.ID)
		var c store.GroupChange
		done := moveAuda(f, &c)
		waitForLockWait(t, connect(t, f.url), done)
		g := f.grant(store.NewGrant{})
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if len(c.GrantsRevoked) != 1 || c.GrantsRevoked[0].ID != g {
			t.Fatalf("revoked %+v", c.GrantsRevoked)
		}
		f.checkNoCrossingGrants()
	})
	t.Run("extend, extension commits first", func(t *testing.T) {
		f := newAuditorFixture(t)
		g := f.grant(store.NewGrant{})
		hold := begin(f, `SELECT 1 FROM users WHERE id = $1 FOR KEY SHARE`, f.auda.ID)
		var c store.GroupChange
		done := moveAuda(f, &c)
		waitForLockWait(t, connect(t, f.url), done)
		to := time.Now().AddDate(0, 0, 100).Truncate(time.Microsecond)
		if err := f.st.ExtendGrant(f.ctx, g, f.alice.ID, to); err != nil {
			t.Fatal(err)
		}
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if _, ends := f.grantWindow(g); !ends.Equal(to) {
			t.Errorf("ends %v, want the extension %v", ends, to)
		}
		if r, _ := f.grantState(g); r != store.RevokeGroupChange {
			t.Errorf("reason %q after the move", r)
		}
		f.checkNoCrossingGrants()
	})
	// The order that deadlocked in revision 2: the move holds auda's row
	// and waits (here for the target group's row, which its foreign key
	// check locks) while the extension starts. The extension must wait
	// for auda's row before it locks the grant, which the move revokes
	// next.
	t.Run("extend, move commits first", func(t *testing.T) {
		f := newAuditorFixture(t)
		g := f.grant(store.NewGrant{})
		hold := begin(f, `SELECT 1 FROM customer_groups WHERE id = $1 FOR UPDATE`, f.b.ID)
		var c store.GroupChange
		moved := moveAuda(f, &c)
		watch := connect(t, f.url)
		waitForLockWait(t, watch, moved)
		extended := extend(f, g, time.Now().AddDate(0, 0, 100))
		waitForLockWaits(t, watch, extended, 2)
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		errMove, errExtend := <-moved, <-extended
		noDeadlock(t, errMove)
		noDeadlock(t, errExtend)
		if errMove != nil {
			t.Fatalf("move: %v", errMove)
		}
		if !errors.Is(errExtend, store.ErrGrantRevoked) {
			t.Fatalf("extension after the move: %v, want ErrGrantRevoked", errExtend)
		}
		f.checkNoCrossingGrants()
	})
	// #509 review A1: the owner revokes their own grant while an admin
	// moves them. The foreign key of revoked_by locks the owner's row; the
	// revocation must take it before the grant row, which the move revokes
	// next while it holds the owner.
	t.Run("revoke, move commits first", func(t *testing.T) {
		f := newAuditorFixture(t)
		g := f.grant(store.NewGrant{})
		hold := begin(f, `SELECT 1 FROM customer_groups WHERE id = $1 FOR UPDATE`, f.b.ID)
		moved := make(chan error, 1)
		go func() { _, err := f.st.SetUserGroup(f.ctx, f.alice.ID, f.b.ID); moved <- err }()
		watch := connect(t, f.url)
		waitForLockWait(t, watch, moved)
		revoked := make(chan error, 1)
		go func() { revoked <- f.st.RevokeGrant(f.ctx, g, &f.alice.ID, &f.alice.ID) }()
		waitForLockWaits(t, watch, revoked, 2)
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		errMove, errRevoke := <-moved, <-revoked
		noDeadlock(t, errMove)
		noDeadlock(t, errRevoke)
		if errMove != nil {
			t.Fatalf("move: %v", errMove)
		}
		if !errors.Is(errRevoke, store.ErrGrantRevoked) {
			t.Fatalf("revocation after the move: %v, want ErrGrantRevoked", errRevoke)
		}
		if r, _ := f.grantState(g); r != store.RevokeGroupChange {
			t.Errorf("reason %q", r)
		}
		f.checkNoCrossingGrants()
	})
	t.Run("revoke, revocation commits first", func(t *testing.T) {
		f := newAuditorFixture(t)
		g := f.grant(store.NewGrant{})
		hold := begin(f, `SELECT 1 FROM users WHERE id = $1 FOR KEY SHARE`, f.alice.ID)
		moved := make(chan error, 1)
		var c store.GroupChange
		go func() {
			var err error
			c, err = f.st.SetUserGroup(f.ctx, f.alice.ID, f.b.ID)
			moved <- err
		}()
		waitForLockWait(t, connect(t, f.url), moved)
		if err := f.st.RevokeGrant(f.ctx, g, &f.alice.ID, &f.alice.ID); err != nil {
			t.Fatal(err)
		}
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-moved; err != nil {
			t.Fatal(err)
		}
		if len(c.GrantsRevoked) != 0 {
			t.Errorf("the move revoked %+v again", c.GrantsRevoked)
		}
		if r, _ := f.grantState(g); r != store.RevokeOwner {
			t.Errorf("reason %q", r)
		}
		f.checkNoCrossingGrants()
	})
	// The acting admin is recorded in revoked_by: the move locks the
	// admin's row before the grant rows, so a concurrent removal that
	// locks every admin first (keepAnAdmin) and then the auditor waits
	// instead of closing a cycle.
	t.Run("move by an admin, removal locking the admins", func(t *testing.T) {
		f := newAuditorFixture(t)
		f.grant(store.NewGrant{})
		hold := begin(f, `SELECT 1 FROM customer_groups WHERE id = $1 FOR UPDATE`, f.b.ID)
		e := store.TargetedEntry(store.ActorSession, "user.group_change")
		e.SetUser(f.root.ID, "root")
		moved := make(chan error, 1)
		go func() { _, err := f.st.SetUserGroup(store.WithAudit(f.ctx, e), f.auda.ID, f.b.ID); moved <- err }()
		watch := connect(t, f.url)
		waitForLockWait(t, watch, moved)
		removed := make(chan error, 1)
		go func() { removed <- f.st.DeleteUser(f.ctx, f.auda.ID) }()
		waitForLockWaits(t, watch, removed, 2)
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		errMove, errRemove := <-moved, <-removed
		noDeadlock(t, errMove)
		noDeadlock(t, errRemove)
		if errMove != nil || errRemove != nil {
			t.Fatalf("move: %v, removal: %v", errMove, errRemove)
		}
	})
	t.Run("extend, extension before the removal", func(t *testing.T) {
		f := newAuditorFixture(t)
		g := f.grant(store.NewGrant{})
		hold := begin(f, `SELECT 1 FROM users WHERE id = $1 FOR KEY SHARE`, f.auda.ID)
		done := make(chan error, 1)
		go func() { done <- f.st.DeleteUser(f.ctx, f.auda.ID) }()
		waitForLockWait(t, connect(t, f.url), done)
		to := time.Now().AddDate(0, 0, 100).Truncate(time.Microsecond)
		if err := f.st.ExtendGrant(f.ctx, g, f.alice.ID, to); err != nil {
			t.Fatal(err)
		}
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if r, a := f.grantState(g); r != store.RevokeUserRemoved || a != nil {
			t.Errorf("after the removal: %q, auditor %v", r, a)
		}
		if _, ends := f.grantWindow(g); !ends.Equal(to) {
			t.Errorf("ends %v, want the extension %v", ends, to)
		}
	})
	t.Run("extend, removal first", func(t *testing.T) {
		f := newAuditorFixture(t)
		g := f.grant(store.NewGrant{})
		// The removal holds auda's row and waits to revoke the grant.
		hold := begin(f, `SELECT 1 FROM auditor_grants WHERE id = $1 FOR SHARE`, g)
		removed := make(chan error, 1)
		go func() { removed <- f.st.DeleteUser(f.ctx, f.auda.ID) }()
		watch := connect(t, f.url)
		waitForLockWait(t, watch, removed)
		extended := extend(f, g, time.Now().AddDate(0, 0, 100))
		waitForLockWaits(t, watch, extended, 2)
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		errRemove, errExtend := <-removed, <-extended
		noDeadlock(t, errRemove)
		noDeadlock(t, errExtend)
		if errRemove != nil {
			t.Fatalf("removal: %v", errRemove)
		}
		if !errors.Is(errExtend, store.ErrGrantRevoked) {
			t.Fatalf("extension after the removal: %v, want ErrGrantRevoked", errExtend)
		}
	})
}

// TestSetUserGroupRevokesGrants (group test 6): a group move revokes every
// unrevoked grant of the moved auditor or owner that would cross groups
// (active, future and expired ones), keeps revoked ones as they are, ends
// the sessions and records each revocation in the old group. Moving back
// restores nothing.
func TestSetUserGroupRevokesGrants(t *testing.T) {
	f := newAuditorFixture(t)
	active := f.grant(store.NewGrant{Label: "active"})
	future := f.grant(store.NewGrant{Label: "future", StartsAt: ptr(time.Now().Add(24 * time.Hour)), EndsAt: time.Now().Add(48 * time.Hour)})
	expired := f.grant(store.NewGrant{Label: "expired"})
	storetest.ShiftGrant(t, f.url, expired, -60*24*time.Hour)
	revoked := f.grant(store.NewGrant{Label: "revoked"})
	if err := f.st.RevokeGrant(f.ctx, revoked, &f.alice.ID, &f.alice.ID); err != nil {
		t.Fatal(err)
	}
	bound, unbound := f.session(f.auda, active), f.session(f.auda, 0)
	other := f.grant(store.NewGrant{AuditorID: f.aud2.ID, Label: "aud2"})

	e := store.TargetedEntry(store.ActorSession, "user.group_change")
	e.SetUser(f.root.ID, "root")
	e.SetTarget(store.TargetUser, fmt.Sprint(f.auda.ID), "auda")
	c, err := f.st.SetUserGroup(store.WithAudit(f.ctx, e), f.auda.ID, f.b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, r := range c.GrantsRevoked {
		ids = append(ids, r.ID)
		if r.Owner != "alice" || r.Auditor != "auda" {
			t.Errorf("revoked %+v", r)
		}
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []int64{active, future, expired}) {
		t.Fatalf("revoked %v, want %v", ids, []int64{active, future, expired})
	}
	for _, g := range []int64{active, future, expired} {
		if r, _ := f.grantState(g); r != store.RevokeGroupChange {
			t.Errorf("grant %d: %q", g, r)
		}
	}
	if r, _ := f.grantState(revoked); r != store.RevokeOwner {
		t.Errorf("the revoked grant now says %q", r)
	}
	if r, _ := f.grantState(other); r != "" {
		t.Errorf("aud2's grant: %q", r)
	}
	if f.sessionOK(bound) || f.sessionOK(unbound) || f.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, f.auda.ID) != 0 {
		t.Error("auda's sessions survived the move")
	}
	if n := f.count(`SELECT count(*) FROM auditor_grants WHERE revoked_by = $1`, f.root.ID); n != 3 {
		t.Errorf("%d grants revoked by root", n)
	}
	moves := groupedEntries(t, f.conn, "user.group_change")
	if len(moves) != 1 || groupOf(moves[0]) != fmt.Sprint(f.b.ID) || len(moves[0].Details["grants_revoked"].([]any)) != 3 {
		t.Fatalf("user.group_change %+v", moves)
	}
	revokes := groupedEntries(t, f.conn, "auditor.revoke")
	n := 0
	for _, r := range revokes {
		if r.Details["reason"] != store.RevokeGroupChange {
			continue
		}
		n++
		if groupOf(r) != fmt.Sprint(f.a.ID) || r.Details[store.DetailViaOwners] == nil || r.Details[store.DetailAuditorGrant] == nil {
			t.Errorf("auditor.revoke %+v, want group a with the grant and its owner", r)
		}
	}
	if n != 3 {
		t.Errorf("%d auditor.revoke entries of the move", n)
	}
	// Back to a: nothing comes back.
	f.setGroup(f.auda, f.a)
	if sc := f.scope(f.auda, active); len(sc.Accounts) != 0 {
		t.Error("the grant came back with the auditor")
	}
	if err := f.st.ExtendGrant(f.ctx, expired, f.alice.ID, time.Now().AddDate(0, 0, 10)); !errors.Is(err, store.ErrGrantRevoked) {
		t.Errorf("extend the revoked grant: %v", err)
	}
	// The owner's move revokes the grants to auditors of the old group.
	c = f.setGroup(f.alice, f.b)
	if len(c.GrantsRevoked) != 1 || c.GrantsRevoked[0].ID != other {
		t.Fatalf("alice's move revoked %+v", c.GrantsRevoked)
	}
	f.checkNoCrossingGrants()
}

// TestGroupHoldKeepsGrants (group test 6): a group hold that refuses the
// owner's move leaves the grants untouched.
func TestGroupHoldKeepsGrants(t *testing.T) {
	f := newAuditorFixture(t)
	g := f.grant(store.NewGrant{})
	f.hold(store.NewLegalHold{GroupID: &f.a.ID})
	if _, err := f.st.SetUserGroup(f.ctx, f.alice.ID, f.b.ID); err == nil {
		t.Fatal("the held owner moved")
	}
	if r, _ := f.grantState(g); r != "" {
		t.Fatalf("the grant is revoked: %q", r)
	}
	// An auditor owns nothing a hold covers: its move is not refused.
	f.setGroup(f.auda, f.b)
	if r, _ := f.grantState(g); r != store.RevokeGroupChange {
		t.Fatalf("after the auditor's move: %q", r)
	}
}

// TestAccountMoveNarrowsGrants (group test 7, decision 9): an account that
// moves to another owner, or is purged or deleted, leaves the old owner's
// grants; a grant left without accounts is revoked.
func TestAccountMoveNarrowsGrants(t *testing.T) {
	f := newAuditorFixture(t)
	g := f.grant(store.NewGrant{Accounts: []store.GrantAccount{{AccountID: f.a1}, {AccountID: f.a2}}})
	sess := f.session(f.auda, g)
	move := func(id, to int64, otherGroup bool) map[string]any {
		t.Helper()
		e := store.TargetedEntry(store.ActorSession, "account.move")
		e.SetUser(f.root.ID, "root")
		e.SetTarget(store.TargetAccount, fmt.Sprint(id), "")
		if err := f.st.SetAccountOwner(store.WithAudit(f.ctx, e), f.account(id).Ref(), to, otherGroup); err != nil {
			t.Fatal(err)
		}
		return e.Details
	}
	d := move(f.a2, f.bob.ID, false)
	if got := fmt.Sprint(d["grants_changed"]); got != fmt.Sprint([]store.ChangedGrant{{ID: g, Label: "Betriebsprüfung 2022", Revoked: false}}) {
		t.Errorf("grants_changed %s", got)
	}
	if got := scopeIDs(f.scope(f.auda, g)); !slices.Equal(got, []int64{f.a1}) {
		t.Fatalf("after moving a2: %v", got)
	}
	if !f.sessionOK(sess) {
		t.Fatal("a narrowed grant ended the session")
	}
	if e := groupedEntries(t, f.conn, "auditor.grant_change"); len(e) != 1 || groupOf(e[0]) != fmt.Sprint(f.a.ID) || e[0].Details["reason"] != store.RevokeAccountMove {
		t.Errorf("auditor.grant_change %+v", e)
	}
	move(f.a1, f.bob.ID, false)
	if r, _ := f.grantState(g); r != store.RevokeAccountMove {
		t.Fatalf("after moving the last account: %q", r)
	}
	if f.count(`SELECT count(*) FROM sessions WHERE id = $1`, sess) != 0 {
		t.Error("the session of the revoked grant survived")
	}
	// Across groups, with allowOtherGroup.
	g2 := f.grant(store.NewGrant{Accounts: []store.GrantAccount{{AccountID: f.priv}}})
	move(f.priv, f.kim.ID, true)
	if r, _ := f.grantState(g2); r != store.RevokeAccountMove {
		t.Errorf("after the move to kim: %q", r)
	}
	// A purge of the last account: removed accounts stay granted until the
	// purge finishes.
	g3 := f.grant(store.NewGrant{Accounts: []store.GrantAccount{{AccountID: f.infoID}}})
	if _, ok, err := f.st.RemoveAccountLocked(f.ctx, f.account(f.infoID).Ref()); err != nil || !ok {
		t.Fatalf("remove info: %v %v", ok, err)
	}
	if got := scopeIDs(f.scope(f.auda, g3)); !slices.Equal(got, []int64{f.infoID}) {
		t.Fatalf("a removed account left the grant: %v", got)
	}
	preview, err := f.st.PurgePreview(f.ctx, f.infoID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.st.ConfirmPurge(f.ctx, f.account(f.infoID).Ref(), preview.Deleted, nil)
	if err != nil {
		t.Fatal(err)
	}
	for {
		res, err := f.st.PurgeBatch(f.ctx, p.ID, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		if res.Done {
			break
		}
	}
	if _, err := f.st.FinishPurge(f.ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.grantState(g3); r != store.RevokeAccountPurge {
		t.Errorf("after the purge: %q", r)
	}
	// An account without mail is deleted, not removed: the same.
	empty := f.folder(f.alice, "empty", "INBOX")
	g4 := f.grant(store.NewGrant{Accounts: []store.GrantAccount{{AccountID: empty.AccountID}}})
	if res, _, err := f.st.RemoveAccountLocked(f.ctx, f.account(empty.AccountID).Ref()); err != nil || res != store.AccountDeleted {
		t.Fatalf("delete empty: %v %v", res, err)
	}
	if r, _ := f.grantState(g4); r != store.RevokeAccountPurge {
		t.Errorf("after the deletion: %q", r)
	}
	f.checkNoCrossingGrants()
}

// TestAccountMoveWaitsForRevocation (#509 review B2): an account move
// waits for a revocation in flight instead of narrowing that grant: the
// revoked grant keeps the account as history, and the move writes no
// entry about it.
func TestAccountMoveWaitsForRevocation(t *testing.T) {
	f := newAuditorFixture(t)
	g := f.grant(store.NewGrant{Accounts: []store.GrantAccount{{AccountID: f.a1}, {AccountID: f.a2}}})
	revoke, err := f.conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = revoke.Rollback(f.ctx) }()
	if _, err := revoke.Exec(f.ctx, `UPDATE auditor_grants SET revoked_at = now(), revoke_reason = 'owner' WHERE id = $1`, g); err != nil {
		t.Fatal(err)
	}
	ref := f.account(f.a2).Ref()
	done := make(chan error, 1)
	go func() { done <- f.st.SetAccountOwner(f.ctx, ref, f.bob.ID, false) }()
	waitForLockWait(t, connect(t, f.url), done)
	if err := revoke.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM auditor_grant_accounts WHERE grant_id = $1`, g); n != 2 {
		t.Errorf("the revoked grant keeps %d accounts, want both as history", n)
	}
	for _, action := range []string{"auditor.grant_change", "auditor.revoke"} {
		if e := groupedEntries(t, f.conn, action); len(e) != 0 {
			t.Errorf("the move wrote %s %+v", action, e)
		}
	}
}

// TestAuditorEntryGroups (group test 8): every auditor action is recorded
// under the grant's group, whoever acts; an auditor's own steps under the
// auditor's group.
func TestAuditorEntryGroups(t *testing.T) {
	f := newAuditorFixture(t)
	rootb, err := f.st.CreateUserInGroup(f.ctx, store.NewUser{Name: "rootb", PasswordHash: "h", Admin: true, GroupID: f.b.ID})
	if err != nil {
		t.Fatal(err)
	}
	entry := func(actor, action string, u *store.User) context.Context {
		e := store.TargetedEntry(actor, action)
		if u != nil {
			e.SetUser(u.ID, u.Name)
		}
		return store.WithAudit(f.ctx, e)
	}
	g, err := f.st.CreateGrant(entry(store.ActorSession, "auditor.grant", f.alice), f.grantDefaults(store.NewGrant{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.ExtendGrant(entry(store.ActorSession, "auditor.extend", f.alice), g, f.alice.ID, time.Now().AddDate(0, 0, 40)); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RevokeGrant(entry(store.ActorSession, "auditor.revoke", rootb), g, nil, &rootb.ID); err != nil {
		t.Fatal(err)
	}
	g2 := f.grant(store.NewGrant{})
	if err := f.st.RevokeGrant(entry(store.ActorCLI, "auditor.revoke", nil), g2, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"auditor.select", "auditor.denied", "account.list"} {
		e := store.SelfEntry(store.ActorSession, action)
		e.SetUser(f.auda.ID, "auda")
		if err := f.st.Audit(f.ctx, *e); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	a := fmt.Sprint(f.a.ID)
	for _, action := range []string{"auditor.grant", "auditor.extend", "auditor.revoke", "auditor.select", "auditor.denied", "account.list"} {
		rows := groupedEntries(t, f.conn, action)
		if len(rows) == 0 {
			t.Errorf("no %s entry", action)
		}
		for _, r := range rows {
			if groupOf(r) != a {
				t.Errorf("%s in group %s, want %s", action, groupOf(r), a)
			}
		}
	}
	grants := groupedEntries(t, f.conn, "auditor.grant")
	if d := grants[0].Details; d[store.DetailAuditorGrant] != float64(g) || fmt.Sprint(d[store.DetailViaOwners]) != fmt.Sprint([]any{float64(f.alice.ID)}) ||
		len(d["accounts"].([]any)) != 1 || d["label"] != "Betriebsprüfung 2022" {
		t.Errorf("auditor.grant details %v", d)
	}
	var reasons []string
	for _, r := range groupedEntries(t, f.conn, "auditor.revoke") {
		reasons = append(reasons, fmt.Sprint(r.Details["reason"]))
	}
	if !slices.Equal(reasons, []string{store.RevokeOperator, store.RevokeOperator}) {
		t.Errorf("reasons %v", reasons)
	}
	var by []*int64
	rows, err := f.conn.Query(f.ctx, `SELECT revoked_by FROM auditor_grants ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	if by, err = pgx.CollectRows(rows, pgx.RowTo[*int64]); err != nil {
		t.Fatal(err)
	}
	if len(by) != 2 || by[0] == nil || *by[0] != rootb.ID || by[1] != nil {
		t.Errorf("revoked_by %v", by)
	}
	// The owner's Log shows the grant entries of others through
	// auditorView (part 2): the admin of another group and the command
	// line as "operator", without address or browser; alice's own grant
	// entries stay as they are.
	recs, err := f.st.ListAudit(f.ctx, store.AuditPage{Viewer: f.alice.ID, Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, r := range recs {
		seen = append(seen, r.Action+"/"+r.UserName)
		if r.UserName == store.AuditOperator && (r.UserID != nil || r.ClientIP != "" || r.UserAgent != "" || !r.ViaAuditor) {
			t.Errorf("operator entry %+v", r)
		}
	}
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"auditor.extend/alice", "auditor.grant/alice", "auditor.revoke/operator", "auditor.revoke/operator"}) {
		t.Errorf("alice's Log %v", seen)
	}
}

// TestCreateAuditorGroups (group test 11, store part): an auditor is
// created into a group like a member, and counts as a user of it.
func TestCreateAuditorGroups(t *testing.T) {
	f := newAuditorFixture(t)
	if _, err := f.st.CreateUserInGroup(f.ctx, store.NewUser{Name: "aud3", PasswordHash: "h", Auditor: true}); !errors.Is(err, store.ErrGroupRequired) {
		t.Fatalf("without a group: %v", err)
	}
	if f.audb.GroupID != f.b.ID || !f.audb.IsAuditor {
		t.Fatalf("audb %+v", f.audb)
	}
	if err := f.st.DeleteGroup(f.ctx, f.b.ID); !errors.Is(err, store.ErrGroupInUse) {
		t.Fatalf("remove b: %v", err)
	}
	e := store.TargetedEntry(store.ActorCLI, "user.add")
	e.SetTarget(store.TargetUser, "", "aud4")
	if _, err := f.st.CreateUserInGroup(store.WithAudit(f.ctx, e), store.NewUser{Name: "aud4", PasswordHash: "h", Auditor: true, GroupID: f.b.ID}); err != nil {
		t.Fatal(err)
	}
	if add := groupedEntries(t, f.conn, "user.add"); len(add) != 1 || add[0].Details["auditor"] != true || groupOf(add[0]) != fmt.Sprint(f.b.ID) {
		t.Fatalf("user.add %+v", add)
	}
	groups, err := f.st.ListGroups(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.ID == f.b.ID && g.Users != 3 { // kim, audb, aud4
			t.Errorf("group b has %d users", g.Users)
		}
	}
	// With one group, the auditor lands there.
	st := storetest.New(t)
	if _, err := st.CreateUser(f.ctx, "admin", "h", true); err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUserInGroup(f.ctx, store.NewUser{Name: "aud", PasswordHash: "h", Auditor: true})
	if err != nil {
		t.Fatal(err)
	}
	if g, err := st.GetGroupByName(f.ctx, "Default"); err != nil || u.GroupID != g.ID {
		t.Fatalf("auditor in %d, %v", u.GroupID, err)
	}
}

// TestAuditorMigration (group test 13): Down refuses while an auditor
// exists, since dropping the role would make the login a member; without
// one it drops everything, and Up works again.
func TestAuditorMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, "admin", "h", true); err != nil {
		t.Fatal(err)
	}
	aud, err := st.CreateUserInGroup(ctx, store.NewUser{Name: "aud", PasswordHash: "h", Auditor: true})
	if err != nil {
		t.Fatal(err)
	}
	downBlobEncryptionMigration(t, st)
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "auditor users exist") {
		t.Fatalf("down with an auditor: %v", err)
	}
	if err := st.DeleteUser(ctx, aud.ID); err != nil {
		t.Fatal(err)
	}
	downAuditorMigration(t, st)
	c := connect(t, url)
	for _, sql := range []string{
		`SELECT count(*) FROM information_schema.columns WHERE table_name = 'users' AND column_name = 'is_auditor'`,
		`SELECT count(*) FROM information_schema.columns WHERE table_name = 'sessions' AND column_name = 'grant_id'`,
		`SELECT count(*) FROM information_schema.tables WHERE table_name LIKE '%auditor_grant%'`,
		`SELECT count(*) FROM pg_proc WHERE proname LIKE '%grant%' AND proname NOT LIKE 'pg_%' AND proname NOT LIKE 'has_%'`,
	} {
		if n := countOf(t, c, sql); n != 0 {
			t.Errorf("%s: %d after the Down", sql, n)
		}
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUserInGroup(ctx, store.NewUser{Name: "aud", PasswordHash: "h", Auditor: true}); err != nil {
		t.Fatalf("auditor after the Up: %v", err)
	}
}

// TestDeleteAuditorKeepsHistory (group test 14, decision 27): removing an
// auditor revokes its unrevoked grants first, with an entry each, and
// keeps every grant as history without auditor; a removal past the store
// with an unrevoked grant fails on the CHECK.
func TestDeleteAuditorKeepsHistory(t *testing.T) {
	f := newAuditorFixture(t)
	active := f.grant(store.NewGrant{Label: "active"})
	future := f.grant(store.NewGrant{Label: "future", StartsAt: ptr(time.Now().Add(24 * time.Hour)), EndsAt: time.Now().Add(48 * time.Hour)})
	revoked := f.grant(store.NewGrant{Label: "revoked"})
	if err := f.st.RevokeGrant(f.ctx, revoked, &f.alice.ID, &f.alice.ID); err != nil {
		t.Fatal(err)
	}
	f.session(f.auda, active)
	e := store.TargetedEntry(store.ActorSession, "user.remove")
	e.SetUser(f.root.ID, "root")
	e.SetTarget(store.TargetUser, fmt.Sprint(f.auda.ID), "auda")
	if err := f.st.DeleteUser(store.WithAudit(f.ctx, e), f.auda.ID); err != nil {
		t.Fatal(err)
	}
	for g, want := range map[int64]string{active: store.RevokeUserRemoved, future: store.RevokeUserRemoved, revoked: store.RevokeOwner} {
		if r, a := f.grantState(g); r != want || a != nil {
			t.Errorf("grant %d: %q, auditor %v; want %q without auditor", g, r, a, want)
		}
	}
	if n := f.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, f.auda.ID); n != 0 {
		t.Errorf("%d sessions left", n)
	}
	removes := groupedEntries(t, f.conn, "user.remove")
	if len(removes) != 1 || len(removes[0].Details["grants_revoked"].([]any)) != 2 {
		t.Fatalf("user.remove %+v", removes)
	}
	n := 0
	for _, r := range groupedEntries(t, f.conn, "auditor.revoke") {
		if r.Details["reason"] == store.RevokeUserRemoved {
			n++
			if groupOf(r) != fmt.Sprint(f.a.ID) {
				t.Errorf("auditor.revoke in %s", groupOf(r))
			}
		}
	}
	if n != 2 {
		t.Errorf("%d auditor.revoke entries of the removal", n)
	}
	list, err := f.st.OwnerGrants(f.ctx, f.alice.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("alice's grants %+v, %v", list, err)
	}
	for _, g := range list {
		if g.AuditorID != nil || g.Auditor != "" || g.RevokedAt == nil {
			t.Errorf("history %+v", g)
		}
	}
	// Past the store: the CHECK refuses an unrevoked grant without auditor.
	f.grant(store.NewGrant{AuditorID: f.aud2.ID})
	_, err = f.conn.Exec(f.ctx, `DELETE FROM users WHERE id = $1`, f.aud2.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "auditor_grants_auditor_kept" {
		t.Fatalf("raw removal of aud2: %v, want a check violation of auditor_grants_auditor_kept", err)
	}
}
