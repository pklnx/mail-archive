package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestSMTPAccountCommands(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv(config.EnvDatabaseURL, url)
	key, _ := crypto.GenerateKey()
	t.Setenv(config.EnvSecretKey, key)
	t.Setenv(config.EnvDataDir, t.TempDir())
	ctx := context.Background()
	account := func(args ...string) error { return runCmd(t, newAccountCmd(), "", args...) }

	out := captureStdout(t, func() {
		if err := account("add-smtp", "journal", "--address", "Archive@Archive.test"); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, `smtp account "journal" added`) || !strings.Contains(out, config.EnvSMTPListen) {
		t.Errorf("add-smtp output:\n%s", out)
	}
	accs, err := st.ListAccountsByName(ctx, "journal")
	if err != nil || len(accs) != 1 || accs[0].Kind != store.KindSMTP || !accs[0].Enabled {
		t.Fatalf("created: %+v, %v", accs, err)
	}
	if err := account("add-smtp", "second", "--address", "archive@archive.test"); err == nil || !strings.Contains(err.Error(), "another smtp account") {
		t.Errorf("address in use: %v", err)
	}
	if err := account("add-smtp", "journal", "--address", "x@archive.test"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("name in use: %v", err)
	}
	if err := account("add-smtp", "bad", "--address", "a+b@archive.test"); err == nil {
		t.Error("address with extension accepted")
	}

	out = captureStdout(t, func() {
		if err := account("smtp-addresses", "journal", "--add", "journal@archive.test"); err != nil {
			t.Error(err)
		}
	})
	if out != "archive@archive.test\njournal@archive.test\n" {
		t.Errorf("addresses:\n%s", out)
	}

	// Enable and disable work; server, folders and sync do not.
	if err := account("disable", "journal"); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.GetAccount(ctx, accs[0].ID); a.Enabled {
		t.Error("still enabled")
	}
	if err := account("enable", "journal"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"set-folders", "journal", "--exclude", "x"}, {"folders", "journal"}} {
		if err := account(args...); err == nil || !strings.Contains(err.Error(), "smtp account") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if err := runCmd(t, newSyncCmd(), "", "--account", "journal"); err == nil || !strings.Contains(err.Error(), "smtp account") {
		t.Errorf("sync --account: %v", err)
	}

	out = captureStdout(t, func() {
		if err := account("list"); err != nil {
			t.Error(err)
		}
	})
	if !regexpMatch(`journal\s+-\s+-\s+smtp\s+archive@archive.test, journal@archive.test\s+enabled`, out) {
		t.Errorf("list:\n%s", out)
	}
	out = captureStdout(t, func() {
		if err := runCmd(t, newStatusCmd(), ""); err != nil {
			t.Error(err)
		}
	})
	if !regexpMatch(`journal\s+complete \(SMTP\)\s+-\s+true\s+1\s+0\s+-\s+never\s+-`, out) {
		t.Errorf("status:\n%s", out)
	}
}

var printedSecret = regexp.MustCompile(`(?m)^secret:\s+(mas_[a-z2-7]{52})$`)

// TestSMTPSecretCommand: account smtp-secret lists, creates with --grace
// and revokes, and writes the audit entries; user lock and account move
// say which secrets keep working (#395).
func TestSMTPSecretCommand(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv(config.EnvDatabaseURL, url)
	t.Setenv(config.EnvDataDir, t.TempDir())
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	// carol stays an admin who can log in while alice and bob are locked.
	for _, name := range []string{"bob", "carol"} {
		if _, err := st.CreateUser(ctx, name, "h", true); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, error) {
		t.Helper()
		var err error
		out := captureStdout(t, func() { err = runCmd(t, newAccountCmd(), "", args...) })
		return out, err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}

	out := must("add-smtp", "journal", "--address", "journal@archive.test", "--user", "alice")
	acc, err := st.GetOwnedAccount(ctx, alice.ID, "journal")
	if err != nil {
		t.Fatal(err)
	}
	username := auth.SMTPUsername(acc.ID)
	if !strings.Contains(out, "user name "+username) || !strings.Contains(out, "account smtp-secret journal --user alice --new") {
		t.Errorf("add-smtp hint:\n%s", out)
	}
	if out := must("smtp-secret", "journal", "--user", "alice"); !strings.Contains(out, "user name: "+username) || !strings.Contains(out, "no SMTP secrets") {
		t.Fatalf("empty list:\n%s", out)
	}

	n := len(auditLog(t, st))
	out = must("smtp-secret", "journal", "--user", "alice", "--new")
	m := printedSecret.FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "user name: "+username) || !strings.Contains(out, "[archive.example]:587 "+username+":"+m[1]) {
		t.Fatalf("--new:\n%s", out)
	}
	first := m[1]
	l, err := st.SMTPLogin(ctx, auth.HashSMTPSecret(first), time.Now())
	if err != nil || l.AccountID != acc.ID {
		t.Fatalf("login with the printed secret: %+v %v", l, err)
	}
	firstID := l.CredentialID

	// The default grace keeps the first; --grace 0 revokes the current.
	out = must("smtp-secret", "journal", "--user", "alice", "--new", "--grace", "2d")
	if !strings.Contains(out, fmt.Sprintf("secret %d stays usable until", firstID)) {
		t.Fatalf("--grace 2d:\n%s", out)
	}
	second := printedSecret.FindStringSubmatch(out)[1]
	out = must("smtp-secret", "journal", "--user", "alice", "--new", "--grace", "0")
	if strings.Count(out, " revoked\n") != 2 {
		t.Fatalf("--grace 0:\n%s", out)
	}
	third := printedSecret.FindStringSubmatch(out)[1]
	l3, err := st.SMTPLogin(ctx, auth.HashSMTPSecret(third), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	out = must("smtp-secret", "journal", "--user", "alice")
	for _, s := range []string{first, second, third} {
		if strings.Contains(out, s) {
			t.Fatalf("list shows a secret:\n%s", out)
		}
	}
	if strings.Count(out, "revoked") != 2 || strings.Count(out, "active") != 1 || !strings.Contains(out, "mas_…"+third[52:]) {
		t.Fatalf("list:\n%s", out)
	}

	for _, bad := range [][]string{
		{"--new", "--grace", "31d"}, {"--new", "--grace", "7"}, {"--new", "--grace", "1w"}, {"--grace", "7d"},
		{"--new", "--revoke", "1"}, {"--revoke", strconv.FormatInt(firstID, 10)},
	} {
		if _, err := run(append([]string{"smtp-secret", "journal", "--user", "alice"}, bad...)...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if err := st.CreateAccount(ctx, &store.Account{Name: "mail", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, Enabled: true, OwnerID: &alice.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := run("smtp-secret", "mail", "--user", "alice"); err == nil || !strings.Contains(err.Error(), "not an smtp account") {
		t.Errorf("imap account: %v", err)
	}

	// Locking alice says the current secret keeps working.
	out = captureStdout(t, func() {
		if err := runUser(t, "", "lock", "alice"); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, `smtp account "journal" has 1 usable SMTP secret(s), which "alice" may know`) ||
		!strings.Contains(out, "account smtp-secret journal --user alice --new --grace 0") {
		t.Errorf("lock:\n%s", out)
	}
	// Moving the account says the previous owner may know it.
	out = must("move", "journal", "--user", "alice", "--to", "bob")
	if !strings.Contains(out, `which the previous owner "alice" may know`) || !strings.Contains(out, "--user bob --new --grace 0") {
		t.Errorf("move:\n%s", out)
	}

	out = must("smtp-secret", "journal", "--user", "bob", "--revoke", strconv.FormatInt(l3.CredentialID, 10))
	if !strings.Contains(out, "revoked") {
		t.Fatalf("--revoke:\n%s", out)
	}
	if _, err := st.SMTPLogin(ctx, auth.HashSMTPSecret(third), time.Now()); !errors.Is(err, store.ErrSMTPCredentialRevoked) {
		t.Errorf("after --revoke: %v", err)
	}
	// With nothing usable left, lock and move say nothing about SMTP.
	out = captureStdout(t, func() {
		if err := runUser(t, "", "lock", "bob"); err != nil {
			t.Error(err)
		}
	})
	if strings.Contains(out, "SMTP") {
		t.Errorf("lock without secrets:\n%s", out)
	}

	var creates, revokes int
	for _, e := range auditLog(t, st)[n:] {
		if strings.Contains(string(e.Details), first) || strings.Contains(string(e.Details), third) {
			t.Fatalf("a secret in the audit log: %s", e.Details)
		}
		switch e.Action {
		case "smtp_credential.create":
			creates++
			if e.Actor != store.ActorCLI || e.TargetName != "journal" || !strings.Contains(string(e.Details), `"grace_days"`) {
				t.Errorf("create entry %+v %s", e, e.Details)
			}
		case "smtp_credential.revoke":
			revokes++
			if !strings.Contains(string(e.Details), fmt.Sprintf(`"credential_id": %d`, l3.CredentialID)) {
				t.Errorf("revoke entry %s", e.Details)
			}
		}
	}
	if creates != 3 || revokes != 1 {
		t.Errorf("entries: %d creates, %d revokes", creates, revokes)
	}
}
