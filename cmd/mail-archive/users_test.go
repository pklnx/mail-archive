package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// runUser runs a user subcommand with stdin as input.
func runUser(t *testing.T, stdin string, args ...string) error {
	t.Helper()
	return runCmd(t, newUserCmd(), stdin, args...)
}

func TestUserCommands(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	ctx := context.Background()

	if err := runUser(t, "short\n", "add", "patrick", "--admin", "--password-stdin"); err == nil || !strings.Contains(err.Error(), "12 characters") {
		t.Fatalf("short password: %v", err)
	}
	if err := runUser(t, "x\n", "add", "Not Valid"); err == nil {
		t.Fatal("invalid name accepted")
	}
	if err := runUser(t, "correct horse battery\n", "add", "Patrick", "--admin", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUserByName(ctx, "patrick")
	if err != nil || !u.IsAdmin || !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Fatalf("created user: %+v, %v", u, err)
	}
	if err := runUser(t, "correct horse battery\n", "add", "patrick", "--password-stdin"); err == nil {
		t.Fatal("duplicate accepted")
	}

	// The only admin can be neither locked nor removed.
	if err := runUser(t, "", "lock", "patrick"); err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Fatalf("lock last admin: %v", err)
	}
	if err := runUser(t, "", "remove", "patrick"); err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Fatalf("remove last admin: %v", err)
	}

	if err := runUser(t, "another horse battery\n", "add", "alice", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if err := runUser(t, "", "lock", "alice"); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.GetUserByName(ctx, "alice"); a.LockedAt == nil {
		t.Fatal("alice not locked")
	}
	if err := runUser(t, "", "unlock", "ALICE"); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetUserByName(ctx, "alice")
	if err := runUser(t, "a third horse battery\n", "set-password", "alice", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if after, _ := st.GetUserByName(ctx, "alice"); after.PasswordHash == before.PasswordHash || after.LockedAt != nil {
		t.Fatalf("set-password: %+v", after)
	}
	if err := runUser(t, "", "list"); err != nil {
		t.Fatal(err)
	}
	if err := runUser(t, "", "remove", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := runUser(t, "", "remove", "alice"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("remove twice: %v", err)
	}
}

// runCmd runs a command (account, sync, status, user) with stdin as input.
func runCmd(t *testing.T, root *cobra.Command, stdin string, args ...string) error {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString(stdin)
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; _ = r.Close() }()
	root.SetArgs(args)
	root.SilenceUsage, root.SilenceErrors = true, true
	return root.ExecuteContext(context.Background())
}

func TestAccountOwners(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	ctx := context.Background()
	add := func(name string, extra ...string) error {
		args := append([]string{"add", name, "--host", "h", "--username", "u", "--skip-check", "--password-stdin"}, extra...)
		return runCmd(t, newAccountCmd(), "pw\n", args...)
	}

	// Without users the account waits for the first user.
	if err := add("early"); err != nil {
		t.Fatal(err)
	}
	if err := runUser(t, "correct horse battery\n", "add", "patrick", "--admin", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	patrick, _ := st.GetUserByName(ctx, "patrick")
	if l, _ := st.ListOwnedAccounts(ctx, patrick.ID); len(l) != 1 {
		t.Fatalf("first user owns %d accounts", len(l))
	}

	// With one user, new accounts are theirs.
	if err := add("personal"); err != nil {
		t.Fatal(err)
	}
	if err := runUser(t, "another horse battery\n", "add", "kim", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	// With several users, --user is required.
	if err := add("work"); err == nil || !strings.Contains(err.Error(), "--user") {
		t.Fatalf("add without --user: %v", err)
	}
	if err := add("personal", "--user", "kim"); err != nil {
		t.Fatal(err)
	}
	if err := add("personal", "--user", "kim"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate for kim: %v", err)
	}

	// An ambiguous name needs --user; a unique one does not.
	if err := runCmd(t, newAccountCmd(), "", "disable", "personal"); err == nil || !strings.Contains(err.Error(), "(patrick, kim)") {
		t.Fatalf("ambiguous: %v", err)
	}
	if err := runCmd(t, newAccountCmd(), "", "disable", "personal", "--user", "kim"); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, newAccountCmd(), "", "disable", "early"); err != nil {
		t.Fatal(err)
	}
	kim, _ := st.GetUserByName(ctx, "kim")
	if a, _ := st.GetOwnedAccount(ctx, kim.ID, "personal"); a.Enabled {
		t.Fatal("kim's account still enabled")
	}
	if a, _ := st.GetOwnedAccount(ctx, patrick.ID, "personal"); !a.Enabled {
		t.Fatal("patrick's account disabled")
	}

	// Moving: name conflicts are refused.
	if err := runCmd(t, newAccountCmd(), "", "move", "personal", "--user", "patrick", "--to", "kim"); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("move onto a taken name: %v", err)
	}
	if err := runCmd(t, newAccountCmd(), "", "move", "early", "--to", "kim"); err != nil {
		t.Fatal(err)
	}
	if l, _ := st.ListOwnedAccounts(ctx, kim.ID); len(l) != 2 {
		t.Fatalf("kim owns %d accounts", len(l))
	}

	// Users with accounts cannot be removed.
	if err := runUser(t, "", "remove", "kim"); err == nil || !strings.Contains(err.Error(), "owns 2 account") {
		t.Fatalf("remove an owner: %v", err)
	}
	for _, args := range [][]string{{"list"}, {"list", "--user", "kim"}} {
		if err := runCmd(t, newAccountCmd(), "", args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := runCmd(t, newStatusCmd(), "", "--user", "kim"); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, newAccountCmd(), "", "list", "--user", "nobody"); err == nil {
		t.Fatal("unknown --user accepted")
	}
}
