package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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
	newArchiveSetup(t)

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
