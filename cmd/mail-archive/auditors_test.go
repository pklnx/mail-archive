package main

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestAuditorCommands (#393 part 2; group test 11, CLI): user add
// --auditor generates the password and needs --group with several groups,
// user list shows the role, auditor list shows every grant with its group,
// account move prints the grants it changed, user set-group the grants it
// revoked, user remove those of a removed auditor, and auditor revoke ends
// a grant.
func TestAuditorCommands(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	ctx := context.Background()
	out := func(fn func() error) string {
		t.Helper()
		var err error
		s := captureStdout(t, func() { err = fn() })
		if err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
		return s
	}
	if err := runUser(t, "", "add", "aud0", "--auditor"); err == nil || !strings.Contains(err.Error(), "first user cannot be an auditor") {
		t.Fatalf("first user: %v", err)
	}
	for _, name := range []string{"anna", "ben"} {
		if err := runUser(t, "correct horse battery\n", "add", name, "--password-stdin"); err != nil {
			t.Fatal(err)
		}
	}
	if err := runUser(t, "", "add", "x", "--auditor", "--admin"); err == nil || !strings.Contains(err.Error(), "cannot be an admin") {
		t.Fatalf("admin auditor: %v", err)
	}
	s := out(func() error { return runUser(t, "", "add", "stb-meier", "--auditor") })
	m := regexp.MustCompile(`(?s)auditor "stb-meier" created\npassword for "stb-meier" \(shown only now; to be changed at the first login\):\n(\S{12,})\n$`).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("add --auditor printed %q", s)
	}
	aud, err := st.GetUserByName(ctx, "stb-meier")
	if err != nil || !aud.IsAuditor || !aud.MustChangePassword {
		t.Fatalf("stb-meier %+v, %v", aud, err)
	}
	if s := out(func() error { return runUser(t, "", "list") }); !regexp.MustCompile(`(?m)^stb-meier\s+Default\s+auditor\s+must change password\s`).MatchString(s) {
		t.Errorf("user list: %s", s)
	}
	if err := runCmd(t, newGroupCmd(), "", "add", "Meier"); err != nil {
		t.Fatal(err)
	}
	if err := runUser(t, "", "add", "stb-kunde", "--auditor"); err == nil || !strings.Contains(err.Error(), "--group") {
		t.Fatalf("auditor without --group: %v", err)
	}
	anna, _ := st.GetUserByName(ctx, "anna")
	accounts := map[string]int64{}
	for _, name := range []string{"office", "private"} {
		a := &store.Account{Name: name, OwnerID: &anna.ID, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, Enabled: true}
		if err := st.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
		accounts[name] = a.ID
	}
	grant := func(label string, ids ...int64) int64 {
		t.Helper()
		var ga []store.GrantAccount
		for _, id := range ids {
			ga = append(ga, store.GrantAccount{AccountID: id})
		}
		id, err := st.CreateGrant(ctx, store.NewGrant{OwnerID: anna.ID, AuditorID: aud.ID, Label: label, Accounts: ga, EndsAt: time.Now().AddDate(0, 0, 10)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	g1 := grant("Prüfung 2024", accounts["office"], accounts["private"])
	g2 := grant("Prüfung 2025", accounts["private"])
	g3 := grant("Prüfung 2026", accounts["office"])
	s = out(func() error { return runCmd(t, newAuditorCmd(), "", "list") })
	for _, id := range []int64{g1, g2, g3} {
		if !regexp.MustCompile(`(?m)^` + strconv.FormatInt(id, 10) + `\s+Default\s+anna\s+stb-meier\s+Prüfung 20\d\d\s+\S+\s+\S+\s+active$`).MatchString(s) {
			t.Errorf("auditor list has no active grant %d:\n%s", id, s)
		}
	}
	if !strings.HasPrefix(s, "ID ") || !strings.Contains(s, "GROUP") {
		t.Errorf("auditor list header: %s", s)
	}
	// private moves to ben: g1 loses it, g2 is left without accounts.
	s = out(func() error { return runCmd(t, newAccountCmd(), "", "move", "private", "--to", "ben") })
	if !strings.Contains(s, `auditor grant `+strconv.FormatInt(g1, 10)+` ("Prüfung 2024") no longer includes the account`) ||
		!strings.Contains(s, `auditor grant `+strconv.FormatInt(g2, 10)+` ("Prüfung 2025") revoked: it has no account left`) {
		t.Errorf("account move printed:\n%s", s)
	}
	if err := runCmd(t, newAuditorCmd(), "", "revoke", "nope"); err == nil {
		t.Error("revoke nope")
	}
	if err := runCmd(t, newAuditorCmd(), "", "revoke", "999999"); err == nil || !strings.Contains(err.Error(), "no auditor grant 999999") {
		t.Errorf("revoke unknown: %v", err)
	}
	if s := out(func() error { return runCmd(t, newAuditorCmd(), "", "revoke", strconv.FormatInt(g3, 10)) }); !strings.Contains(s, "revoked") {
		t.Errorf("revoke: %s", s)
	}
	if err := runCmd(t, newAuditorCmd(), "", "revoke", strconv.FormatInt(g3, 10)); err == nil || !strings.Contains(err.Error(), "revoked already") {
		t.Errorf("revoke twice: %v", err)
	}
	s = out(func() error { return runCmd(t, newAuditorCmd(), "", "list") })
	if !regexp.MustCompile(`(?m)^`+strconv.FormatInt(g3, 10)+`\s.*revoked \(operator\)$`).MatchString(s) ||
		!regexp.MustCompile(`(?m)^`+strconv.FormatInt(g2, 10)+`\s.*revoked \(account_move\)$`).MatchString(s) {
		t.Errorf("auditor list after revocations:\n%s", s)
	}
	// The auditor moves to Meier: g1 is revoked, and printed.
	s = out(func() error { return runUser(t, "", "set-group", "stb-meier", "Meier") })
	if !strings.Contains(s, `auditor grant revoked: `+strconv.FormatInt(g1, 10)+` "Prüfung 2024" of anna to stb-meier`) {
		t.Errorf("set-group printed:\n%s", s)
	}
	// Removing an auditor with an unrevoked grant names it.
	if _, err := st.SetUserGroup(ctx, aud.ID, anna.GroupID); err != nil {
		t.Fatal(err)
	}
	g4 := grant("Prüfung 2027", accounts["office"])
	s = out(func() error { return runUser(t, "", "remove", "stb-meier") })
	if !strings.Contains(s, `auditor grant `+strconv.FormatInt(g4, 10)+` ("Prüfung 2027" of anna) revoked`) {
		t.Errorf("remove printed:\n%s", s)
	}
	s = out(func() error { return runCmd(t, newAuditorCmd(), "", "list") })
	if !regexp.MustCompile(`(?m)^` + strconv.FormatInt(g4, 10) + `\s+Default\s+anna\s+-\s.*revoked \(user_removed\)$`).MatchString(s) {
		t.Errorf("auditor list after the removal:\n%s", s)
	}
	for action, want := range map[string]int{"auditor.revoke": 4} {
		var n int
		recs, err := st.ListAudit(ctx, store.AuditPage{Viewer: anna.ID, All: true, Actions: []string{action}, Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		n = len(recs)
		if n != want {
			t.Errorf("%s: %d entries, want %d", action, n, want)
		}
	}
}
