package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/keepia/archive/internal/store"
)

func runOut(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := runCmd(t, cmd, "", args...)
	return out.String(), err
}

func TestRetentionCommands(t *testing.T) {
	setup := newArchiveSetup(t)

	if out, err := runOut(t, newRetentionCmd(), "rule", "list"); err != nil || !strings.Contains(out, "no retention rules") {
		t.Fatalf("empty list: %v\n%s", err, out)
	}
	// Two users have an account "mail": --user picks one.
	if _, err := runOut(t, newRetentionCmd(), "rule", "add", "--account", "mail", "--years", "8"); err == nil ||
		!strings.Contains(err.Error(), "--user") {
		t.Fatalf("ambiguous account: %v", err)
	}
	if _, err := runOut(t, newRetentionCmd(), "rule", "add", "--account", "mail", "--user", "anna", "--years", "2"); err == nil ||
		!strings.Contains(err.Error(), "--force") {
		t.Fatalf("short rule without --force: %v", err)
	}
	out, err := runOut(t, newRetentionCmd(), "rule", "add", "--account", "mail", "--user", "anna", "--years", "8", "--note", "§147 AO")
	if err != nil || !strings.Contains(out, `rule 1: account "mail" keeps mail for 8 years`) {
		t.Fatalf("add: %v\n%s", err, out)
	}
	out, err = runOut(t, newRetentionCmd(), "rule", "add", "--account", "mail", "--user", "bob", "--folder", "News", "--years", "1", "--force")
	if err != nil || !strings.Contains(out, "rule 2") || !strings.Contains(out, `has no folder "News" yet`) {
		t.Fatalf("folder rule: %v\n%s", err, out)
	}
	out, err = runOut(t, newRetentionCmd(), "rule", "list")
	if err != nil || !regexpMatch(`1\s+anna\s+mail\s+\(all\)\s+8\s+Europe/Berlin\s+\S+\s+§147 AO`, out) || !strings.Contains(out, "News") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if out, err := runOut(t, newRetentionCmd(), "rule", "remove", "2"); err != nil || !strings.Contains(out, "rule 2 removed") {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if _, err := runOut(t, newRetentionCmd(), "rule", "remove", "2"); err == nil || !strings.Contains(err.Error(), "no rule 2") {
		t.Fatalf("remove again: %v", err)
	}

	if _, err := runOut(t, newHoldCmd(), "add", "everything", "--reason", "audit"); err == nil || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("hold without scope: %v", err)
	}
	if out, err := runOut(t, newHoldCmd(), "add", "Tax audit", "--reason", "Betriebsprüfung", "--user", "anna", "--until", "2026-01-01"); err != nil ||
		!strings.Contains(out, "legal hold 1 added") {
		t.Fatalf("hold: %v\n%s", err, out)
	}
	out, err = runOut(t, newHoldCmd(), "add", "Anna", "--reason", "lawsuit", "--query", "anna")
	if err != nil || !strings.Contains(out, "legal hold 2 added") || !strings.Contains(out, "the filter matched 0 message(s)") {
		t.Fatalf("filter hold: %v\n%s", err, out)
	}
	out, err = runOut(t, newHoldCmd(), "list")
	if err != nil || !strings.Contains(out, "user anna, before 2026-01-02") || !strings.Contains(out, `filter "anna" (0 matched)`) {
		t.Fatalf("hold list: %v\n%s", err, out)
	}
	if _, err := runOut(t, newHoldCmd(), "release", "1"); err == nil {
		t.Fatal("release without --note")
	}
	if out, err := runOut(t, newHoldCmd(), "release", "1", "--note", "closed"); err != nil || !strings.Contains(out, "released") {
		t.Fatalf("release: %v\n%s", err, out)
	}
	if out, _ := runOut(t, newHoldCmd(), "list"); strings.Contains(out, "Tax audit") {
		t.Fatalf("released hold listed:\n%s", out)
	}
	if out, _ := runOut(t, newHoldCmd(), "list", "--released"); !strings.Contains(out, "released ") || !strings.Contains(out, "closed") {
		t.Fatalf("list --released:\n%s", out)
	}

	// A hold on a customer group (#470): listed with its group, its
	// entries recorded under it; the group then stays.
	if err := runCmd(t, newGroupCmd(), "", "add", "Meier"); err != nil {
		t.Fatal(err)
	}
	if _, err := runOut(t, newHoldCmd(), "add", "x", "--reason", "y", "--group", "nosuch"); err == nil || !strings.Contains(err.Error(), `group "nosuch" not found`) {
		t.Fatalf("hold on an unknown group: %v", err)
	}
	if _, err := runOut(t, newHoldCmd(), "add", "x", "--reason", "y", "--group", "Meier", "--user", "anna"); err == nil ||
		!strings.Contains(err.Error(), "not in the hold's group") {
		t.Fatalf("hold on anna in Meier: %v", err)
	}
	out, err = runOut(t, newHoldCmd(), "add", "Meier audit", "--reason", "Betriebsprüfung", "--group", "meier")
	if err != nil || !strings.Contains(out, "legal hold 3 added") {
		t.Fatalf("group hold: %v\n%s", err, out)
	}
	if out, _ := runOut(t, newHoldCmd(), "list"); !regexpMatch(`(?m)^3\s+Meier audit\s+group Meier\s`, out) {
		t.Fatalf("hold list with a group hold:\n%s", out)
	}
	if out, err := runOut(t, newHoldCmd(), "release", "3", "--note", "closed"); err != nil || !strings.Contains(out, "released") {
		t.Fatalf("release the group hold: %v\n%s", err, out)
	}
	if err := runCmd(t, newGroupCmd(), "", "remove", "Meier"); err == nil || !strings.Contains(err.Error(), "a legal hold names it") {
		t.Fatalf("remove a group a hold names: %v", err)
	}
	var n int
	if err := connectDB(t, setup.url).QueryRow(context.Background(), `SELECT count(*) FROM audit_log a JOIN customer_groups g ON g.id = a.group_id
		WHERE a.action IN ('hold.add', 'hold.release') AND a.details->>'name' = 'Meier audit' AND g.name = 'Meier'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("group hold entries under Meier: %d (%v), want 2", n, err)
	}

	status := captureStdout(t, func() { _ = runCmd(t, newStatusCmd(), "") })
	if !strings.Contains(status, "retention: 1 rule(s), 1 active legal hold(s)") {
		t.Errorf("status:\n%s", status)
	}
	// A user named by a hold cannot be removed.
	if err := runCmd(t, newUserCmd(), "", "remove", "anna"); err == nil {
		t.Error("removed a user with accounts and holds")
	}
	audit := captureStdout(t, func() {
		_ = runCmd(t, newAuditCmd(), "", "--since", "2000-01-01", "--format", "csv", "--action", "retention")
	})
	for _, a := range []string{"retention.rule_add", "retention.rule_remove", "hold.add", "hold.release"} {
		if !strings.Contains(audit, a) {
			t.Errorf("audit lacks %s:\n%s", a, audit)
		}
	}
}

// hold list says who added each hold (#538): the operator reviews the
// filter holds admins added in the web UI, and the unknown ones like them.
// The web sets created_by, the command line never does; without it the
// hold.add audit entry tells, and without that the creator is unknown.
func TestHoldListCreatedBy(t *testing.T) {
	s := newArchiveSetup(t)
	ctx := context.Background()
	anna := *s.accs["anna"].OwnerID
	if out, err := runOut(t, newHoldCmd(), "add", "operator hold", "--reason", "r", "--user", "anna", "--query", "anna"); err != nil ||
		!strings.Contains(out, "legal hold 1 added") {
		t.Fatalf("hold add: %v\n%s", err, out)
	}
	web := func(name string, by *store.User) {
		t.Helper()
		e := store.TargetedEntry(store.ActorSession, "hold.add")
		e.UserID, e.UserName = &by.ID, by.Name
		if _, err := s.st.AddLegalHold(store.WithAudit(ctx, e), store.NewLegalHold{
			Name: name, Reason: "r", OwnerID: &anna, Filter: &store.HoldFilter{Query: "anna"}, CreatedBy: &by.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"admin", "eve"} {
		u, err := s.st.CreateUser(ctx, name, "h", true)
		if err != nil {
			t.Fatal(err)
		}
		web(name+"'s hold", u)
		if name == "eve" {
			if err := s.st.DeleteUser(ctx, u.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := connectDB(t, s.url).Exec(ctx, `INSERT INTO legal_holds (name, reason, owner_id) VALUES ('by hand', 'r', $1)`, anna); err != nil {
		t.Fatal(err)
	}
	out, err := runOut(t, newHoldCmd(), "list")
	if err != nil {
		t.Fatalf("hold list: %v\n%s", err, out)
	}
	if !regexpMatch(`(?m)^ID\s+NAME\s+SCOPE\s+CREATED\s+CREATED BY\s+STATE\s+REASON$`, out) {
		t.Errorf("header:\n%s", out)
	}
	for name, by := range map[string]string{
		"operator hold": "cli", "admin's hold": "admin", "eve's hold": `eve \(removed\)`, "by hand": "unknown",
	} {
		if !regexpMatch(`(?m)^\d+\s+`+name+`\s.*\d{4}-\d\d-\d\d  +`+by+`  +active  +r$`, out) {
			t.Errorf("%s not created by %s:\n%s", name, by, out)
		}
	}
}

func TestDeletionCommands(t *testing.T) {
	s := newArchiveSetup(t)
	ctx := context.Background()
	acc := s.accs["anna"]
	f, err := s.st.GetOrCreateFolder(ctx, acc.ID, "Old")
	if err != nil {
		t.Fatal(err)
	}
	sent := time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC)
	b, _, err := s.blobs.Put(strings.NewReader("Subject: =cmd\r\n\r\nold\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.SaveBatch(ctx, f.ID, 1, []store.MessageMeta{{SHA256: b.SHA256, Size: b.Size, StoredPath: b.Path, Subject: "=cmd", SentAt: &sent}},
		[]store.Location{{FolderID: f.ID, UID: 1, InternalDate: sent}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runOut(t, newRetentionCmd(), "rule", "add", "--account", "mail", "--user", "anna", "--folder", "Old", "--years", "1", "--force"); err != nil {
		t.Fatal(err)
	}

	out, err := runOut(t, newRetentionCmd(), "preview")
	if err != nil || !strings.Contains(out, "deletion run 1: 1 message(s)") || !regexpMatch(`anna\s+mail\s+Old\s+2011\s+1`, out) {
		t.Fatalf("preview: %v\n%s", err, out)
	}
	if _, err := runOut(t, newRetentionCmd(), "preview"); err == nil || !strings.Contains(err.Error(), "run 1") {
		t.Fatalf("second preview: %v", err)
	}
	out, err = runOut(t, newRetentionCmd(), "show", "1")
	if err != nil || !strings.Contains(out, "proposed") || !strings.Contains(out, "anna/mail/Old") || !strings.Contains(out, b.SHA256) {
		t.Fatalf("show: %v\n%s", err, out)
	}
	out, err = runOut(t, newRetentionCmd(), "show", "1", "--format", "csv")
	if err != nil || !strings.Contains(out, "'=cmd") || !strings.HasPrefix(out, "sha256,state,") {
		t.Fatalf("show csv: %v\n%s", err, out)
	}

	// The wrong number confirms nothing.
	cmd := newRetentionCmd()
	cmd.SetIn(strings.NewReader("7\n"))
	if _, err := runOut(t, cmd, "confirm", "1"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("wrong number: %v", err)
	}
	cmd = newRetentionCmd()
	cmd.SetIn(strings.NewReader("1\n"))
	out, err = runOut(t, cmd, "confirm", "1")
	if err != nil || !strings.Contains(out, "deletion run 1 done: 1 deleted, 0 kept") {
		t.Fatalf("confirm: %v\n%s", err, out)
	}
	if _, err := runOut(t, newRetentionCmd(), "confirm", "1", "--yes"); err == nil || !strings.Contains(err.Error(), "is done") {
		t.Fatalf("confirm a done run: %v", err)
	}
	if out, err := runOut(t, newRetentionCmd(), "runs"); err != nil || !strings.Contains(out, "deletion run 1: done") {
		t.Fatalf("runs: %v\n%s", err, out)
	}
	status := captureStdout(t, func() { _ = runCmd(t, newStatusCmd(), "") })
	if !strings.Contains(status, "1 file(s) of deleted messages to remove") {
		t.Errorf("status:\n%s", status)
	}
	// Within the default grace of 7 days nothing is removed.
	if out, err := runOut(t, newRetentionCmd(), "sweep"); err != nil || !strings.Contains(out, "removed 0 file(s)") || !s.blobs.Exists(b.SHA256) {
		t.Fatalf("sweep: %v\n%s", err, out)
	}

	// A new run, cancelled.
	if _, err := runOut(t, newRetentionCmd(), "preview"); err != nil {
		t.Fatal(err)
	}
	if out, err := runOut(t, newRetentionCmd(), "cancel", "2"); err != nil || !strings.Contains(out, "cancelled") {
		t.Fatalf("cancel: %v\n%s", err, out)
	}
	audit := captureStdout(t, func() {
		_ = runCmd(t, newAuditCmd(), "", "--since", "2000-01-01", "--format", "csv", "--action", "retention")
	})
	for _, a := range []string{"retention.preview", "retention.confirm", "retention.batch", "retention.finish", "retention.cancel"} {
		if !strings.Contains(audit, a) {
			t.Errorf("audit lacks %s:\n%s", a, audit)
		}
	}
}

// Every period ends at 1 January 2026 in its rule's zone; its last day is
// 31 December 2025 whatever the zone, not 1 January west of UTC (#406).
func TestRetentionEndDay(t *testing.T) {
	for _, c := range []struct{ zone, end string }{
		{"Europe/Berlin", "2025-12-31T23:00:00Z"},
		{"America/New_York", "2026-01-01T05:00:00Z"},
		{"Pacific/Pago_Pago", "2026-01-01T11:00:00Z"},
		{"Asia/Tokyo", "2025-12-31T15:00:00Z"},
		{"Pacific/Kiritimati", "2025-12-31T10:00:00Z"},
	} {
		end, err := time.Parse(time.RFC3339, c.end)
		if err != nil {
			t.Fatal(err)
		}
		if got := retentionEndDay(end); got != "2025-12-31" {
			t.Errorf("%s (%s): %s", c.zone, c.end, got)
		}
	}
}
