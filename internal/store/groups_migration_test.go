package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestGroupsMigration (#470, test 13): 00034 puts every user into
// "Default" and keeps the shares and the old audit rows as they were; Down
// works with one group and refuses with two; Down keeps audit_log.group_id,
// and a later Up never hands out a group id that the log still holds.
func TestGroupsMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	q := func(sql string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := conn.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// A first Up starts the ids at 1.
	if g, err := st.GetGroupByName(ctx, "Default"); err != nil || g.ID != 1 {
		t.Fatalf("Default after the first Up: %+v, %v", g, err)
	}

	// Down with one group works; audit_log.group_id stays.
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, "bob", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "info", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &alice.ID}
	if err := st.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ShareAccount(ctx, acc.Ref(), bob.ID); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateGroup(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	downGroupHoldsMigration(t, st)
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "several customer groups exist") {
		t.Fatalf("down with two groups: %v", err)
	}
	// Raw SQL: the store's group queries need the current schema
	// (customer_groups.quota_bytes, 00041).
	exec(`DELETE FROM customer_groups WHERE id = $1`, b.ID)
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00034_customer_groups") {
		t.Fatalf("down: %q %v", m, err)
	}
	if n := q(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'users' AND column_name = 'group_id'`); n != 0 {
		t.Fatal("users.group_id survived the Down")
	}
	if n := q(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'audit_log' AND column_name = 'group_id'`); n == 0 {
		t.Fatal("audit_log.group_id did not survive the Down")
	}
	// Entries from groups 2 and 3, since deleted, and older ones without a
	// group, all written before the re-Up.
	exec(`INSERT INTO audit_log (actor, action, group_id) VALUES ('cli', 'user.change', 2), ('cli', 'user.change', 3), ('cli', 'data.backup', NULL)`)

	// Up: everyone in Default, which gets an id above every one the log
	// holds; shares and audit rows unchanged.
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	def, err := st.GetGroupByName(ctx, "Default")
	if err != nil {
		t.Fatal(err)
	}
	if def.ID != 4 {
		t.Fatalf("Default got id %d, want 4 (above the log's 3)", def.ID)
	}
	if n := q(`SELECT count(*) FROM users WHERE group_id = $1`, def.ID); n != 2 {
		t.Fatalf("%d users in Default", n)
	}
	if n := q(`SELECT count(*) FROM account_shares`); n != 1 {
		t.Fatalf("%d shares after the Up", n)
	}
	if n := q(`SELECT count(*) FROM audit_log WHERE group_id IS NULL AND action = 'data.backup'`); n != 1 {
		t.Fatal("the old entry without a group changed")
	}
	if got := scopeIDs(storetest.Viewer(t, st, bob.ID)); len(got) != 1 || got[0] != acc.ID {
		t.Fatalf("bob reads %v after the Up", got)
	}
	nb, err := st.CreateGroup(ctx, "New")
	if err != nil {
		t.Fatal(err)
	}
	if nb.ID <= 3 {
		t.Fatalf("new group id %d reuses an id of the log", nb.ID)
	}
	if n := q(`SELECT count(*) FROM audit_log WHERE group_id = $1 AND action <> 'group.add'`, nb.ID); n != 0 {
		t.Fatalf("%d old entries match the new group", n)
	}
}
