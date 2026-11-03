package main

import (
	"context"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestGroupCommands (#470): group add, list, rename and remove; user add
// --group (needed with several groups), user set-group (prints the ended
// shares), the GROUP columns of user list and account list, and account
// move --other-group.
func TestGroupCommands(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	ctx := context.Background()
	group := func(args ...string) error { return runCmd(t, newGroupCmd(), "", args...) }
	out := func(fn func() error) string {
		t.Helper()
		var err error
		s := captureStdout(t, func() { err = fn() })
		if err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
		return s
	}

	if err := runUser(t, "correct horse battery\n", "add", "anna", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if err := runUser(t, "correct horse battery\n", "add", "ben", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if s := out(func() error { return group("add", "Meier") }); !strings.Contains(s, `group "Meier" created`) {
		t.Errorf("add: %s", s)
	}
	if err := group("add", "meier"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate: %v", err)
	}
	// With two groups, a new user needs --group, before the password is read.
	if err := runUser(t, "", "add", "carl"); err == nil || !strings.Contains(err.Error(), "--group") {
		t.Fatalf("add without --group: %v", err)
	}
	if err := runUser(t, "correct horse battery\n", "add", "carl", "--group", "nosuch", "--password-stdin"); err == nil || !strings.Contains(err.Error(), `group "nosuch" not found`) {
		t.Fatalf("unknown group: %v", err)
	}
	if err := runUser(t, "correct horse battery\n", "add", "meier.anna", "--group", "MEIER", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	meier, err := st.GetGroupByName(ctx, "Meier")
	if err != nil {
		t.Fatal(err)
	}
	if u, err := st.GetUserByName(ctx, "meier.anna"); err != nil || u.GroupID != meier.ID {
		t.Fatalf("meier.anna %+v, %v", u, err)
	}

	// anna's account is shared with ben; moving ben ends it.
	anna, _ := st.GetUserByName(ctx, "anna")
	ben, _ := st.GetUserByName(ctx, "ben")
	acc := &store.Account{Name: "office", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &anna.ID}
	if err := st.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ShareAccount(ctx, acc.Ref(), ben.ID); err != nil {
		t.Fatal(err)
	}
	s := out(func() error { return runUser(t, "", "set-group", "ben", "Meier") })
	if !strings.Contains(s, `"ben" moved from group "Default" to "Meier"`) || !strings.Contains(s, "share ended: anna/office with ben") {
		t.Errorf("set-group: %s", s)
	}
	if s := out(func() error { return runUser(t, "", "set-group", "ben", "meier") }); !strings.Contains(s, "already is in group") {
		t.Errorf("set-group again: %s", s)
	}
	if err := runUser(t, "", "set-group", "ben", "nosuch"); err == nil {
		t.Fatal("set-group to an unknown group")
	}
	if s := out(func() error { return runUser(t, "", "list") }); !regexpMatch(`(?m)^ben\s+Meier\s+user`, s) || !regexpMatch(`(?m)^anna\s+Default\s+user`, s) {
		t.Errorf("user list:\n%s", s)
	}
	if s := out(func() error { return group("list") }); !regexpMatch(`(?m)^Default\s+1\s+1\s`, s) || !regexpMatch(`(?m)^Meier\s+2\s+0\s`, s) {
		t.Errorf("group list:\n%s", s)
	}

	// account move across groups needs --other-group.
	if err := runCmd(t, newAccountCmd(), "", "move", "office", "--to", "ben"); err == nil || !strings.Contains(err.Error(), "--other-group") {
		t.Fatalf("move across groups: %v", err)
	}
	if _, err := st.SetUserGroup(ctx, ben.ID, meier.ID); err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUserInGroup(ctx, store.NewUser{Name: "dora", PasswordHash: "h", GroupID: anna.GroupID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ShareAccount(ctx, acc.Ref(), other.ID); err != nil {
		t.Fatal(err)
	}
	s = out(func() error { return runCmd(t, newAccountCmd(), "", "move", "office", "--to", "ben", "--other-group") })
	if !strings.Contains(s, `account "office" now belongs to "ben"`) || !strings.Contains(s, `share with "dora" ended`) {
		t.Errorf("move --other-group: %s", s)
	}
	if s := out(func() error { return runCmd(t, newAccountCmd(), "", "list") }); !regexpMatch(`(?m)^office\s+ben\s+Meier\s`, s) {
		t.Errorf("account list:\n%s", s)
	}

	// rename and remove.
	if s := out(func() error { return group("rename", "meier", "Meier GmbH") }); !strings.Contains(s, `group "Meier" is now "Meier GmbH"`) {
		t.Errorf("rename: %s", s)
	}
	if err := group("remove", "Meier GmbH"); err == nil || !strings.Contains(err.Error(), "still has users") {
		t.Fatalf("remove with users: %v", err)
	}
	if err := group("add", "Empty"); err != nil {
		t.Fatal(err)
	}
	if s := out(func() error { return group("remove", "empty") }); !strings.Contains(s, `group "Empty" removed`) {
		t.Errorf("remove: %s", s)
	}
	// Every command wrote its entry, under the group it is about.
	conn := connectDB(t, url)
	for action, want := range map[string]int{"group.add": 2, "group.rename": 1, "group.remove": 1, "user.group_change": 1, "account.move": 1} {
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = $1 AND group_id IS NOT NULL`, action).Scan(&n); err != nil || n != want {
			t.Errorf("%s: %d entries with a group, want %d (%v)", action, n, want, err)
		}
	}
}
