package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// runUser runs a user subcommand with stdin as input.
func runUser(t *testing.T, stdin string, args ...string) error {
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

	cmd := newUserCmd()
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	return cmd.ExecuteContext(context.Background())
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
