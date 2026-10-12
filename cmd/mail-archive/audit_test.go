package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
)

func auditLog(t *testing.T, st *store.Store) []store.AuditRecord {
	t.Helper()
	var out []store.AuditRecord
	f := store.AuditFilter{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour)}
	if err := st.ExportAudit(context.Background(), f, func(r store.AuditRecord) error {
		out = append(out, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestCLIAudit runs every command that changes users or accounts, imports,
// exports or backs up, and checks that each writes exactly one entry.
func TestCLIAudit(t *testing.T) {
	s := newArchiveSetup(t)
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	t.Setenv("MAIL_ARCHIVE_OPERATOR", "patrick")
	// Another admin, so that carol can be demoted.
	anna, _ := s.st.GetUserByName(context.Background(), "anna")
	if err := s.st.SetUserAdmin(context.Background(), anna.ID, true); err != nil {
		t.Fatal(err)
	}
	pw := "correct horse battery\n"
	for _, c := range []struct {
		cmd     func() *cobra.Command
		stdin   string
		args    []string
		action  string
		target  string // type:name
		details map[string]any
	}{
		{newUserCmd, pw, []string{"add", "carol", "--password-stdin"}, "user.add", "user:carol", map[string]any{"admin": false}},
		{newUserCmd, "", []string{"lock", "carol"}, "user.change", "user:carol", map[string]any{"locked": true}},
		{newUserCmd, "", []string{"unlock", "carol"}, "user.change", "user:carol", map[string]any{"locked": false}},
		{newUserCmd, "", []string{"promote", "carol"}, "user.change", "user:carol", map[string]any{"admin": true}},
		{newUserCmd, "", []string{"demote", "carol"}, "user.change", "user:carol", map[string]any{"admin": false}},
		{newUserCmd, pw, []string{"set-password", "carol", "--password-stdin"}, "user.password_set", "user:carol", nil},
		{newUserCmd, "", []string{"reset-password", "carol", "--keep-passkeys"}, "user.password_reset", "user:carol", map[string]any{"passkeys_removed": false}},
		{newUserCmd, "", []string{"reset-2fa", "carol"}, "user.2fa_reset", "user:carol", nil},
		{newUserCmd, "", []string{"remove-passkeys", "carol"}, "user.passkeys_removed", "user:carol", map[string]any{"count": float64(0)}},
		{newAccountCmd, "secret\n", []string{"add", "work", "--host", "imap.example", "--username", "u", "--skip-check", "--password-stdin", "--user", "anna"},
			"account.add", "account:work", map[string]any{"kind": "imap"}},
		// rename re-encrypts every stored password, which the fake accounts
		// of the setup do not have; the web test covers renames.
		{newAccountCmd, "", []string{"disable", "work", "--user", "anna"}, "account.change", "account:work", map[string]any{"enabled": false}},
		{newAccountCmd, "", []string{"set-folders", "work", "--exclude", "Spam", "--user", "anna"}, "account.change", "account:work", nil},
		{newAccountCmd, "", []string{"set-schedule", "work", "--sync-interval", "30m", "--user", "anna"}, "account.change", "account:work",
			map[string]any{"fields": []any{"sync_interval"}, "sync_interval": "30m0s"}},
		{newAccountCmd, "other\n", []string{"set-password", "work", "--password-stdin", "--user", "anna"}, "account.change", "account:work",
			map[string]any{"fields": []any{"password"}}},
		{newAccountCmd, "", []string{"add-smtp", "journal", "--address", "archive@example.com", "--user", "anna"}, "account.add", "account:journal",
			map[string]any{"kind": "smtp"}},
		{newAccountCmd, "", []string{"smtp-addresses", "journal", "--add", "more@example.com", "--user", "anna"}, "account.change", "account:journal",
			map[string]any{"added": []any{"more@example.com"}}},
		{newAccountCmd, "", []string{"move", "work", "--user", "anna", "--to", "bob"}, "account.move", "account:work",
			map[string]any{"from_user": "anna", "to_user": "bob"}},
		{newAccountCmd, "", []string{"remove", "work", "--user", "bob"}, "account.remove", "account:work", map[string]any{"result": "deleted"}},
		{newUserCmd, "", []string{"remove", "carol"}, "user.remove", "user:carol", nil},
		{newExportCmd, "", []string{"--format", "mbox", "--out", filepath.Join(t.TempDir(), "x"), "--user", "anna"}, "data.export", "user:anna",
			map[string]any{"format": "mbox", "accounts": float64(2)}},
		{newExportCmd, "", []string{"--format", "mbox", "--out", filepath.Join(t.TempDir(), "y"), "--account", "mail", "--user", "bob"}, "data.export", "account:mail", nil},
		{newBackupCmd, "", []string{"--note-only", "mailarchive-x.dump"}, "data.backup", "", map[string]any{"dump": "mailarchive-x.dump"}},
		// Reading commands are not logged.
		{newUserCmd, "", []string{"list"}, "", "", nil},
		{newAccountCmd, "", []string{"list"}, "", "", nil},
		{newStatusCmd, "", nil, "", "", nil},
	} {
		name := strings.Join(c.args, " ")
		n := len(auditLog(t, s.st))
		if err := captureErr(t, func() error { return runCmd(t, c.cmd(), c.stdin, c.args...) }); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := auditLog(t, s.st)[n:]
		if c.action == "" {
			if len(got) != 0 {
				t.Errorf("%s: logged %+v", name, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%s: %d entries: %+v", name, len(got), got)
		}
		e := got[0]
		if e.Action != c.action || e.Actor != store.ActorCLI || e.UserID != nil || e.ClientIP != "" || e.UserAgent != "" {
			t.Errorf("%s: %+v", name, e)
		}
		if c.target != "" && (e.TargetType+":"+e.TargetName != c.target || e.TargetID == "") {
			t.Errorf("%s: target %s:%s (%s)", name, e.TargetType, e.TargetName, e.TargetID)
		}
		var d map[string]any
		if err := json.Unmarshal(e.Details, &d); err != nil {
			t.Fatal(err)
		}
		if d["operator"] != "patrick" || d["os_user"] == nil || d["os_user"] == "" {
			t.Errorf("%s: who %v", name, d)
		}
		for k, v := range c.details {
			if fmt.Sprint(d[k]) != fmt.Sprint(v) {
				t.Errorf("%s: %s = %v, want %v", name, k, d[k], v)
			}
		}
		if strings.Contains(string(e.Details), "secret") || strings.Contains(string(e.Details), "horse") {
			t.Errorf("%s: a password was recorded: %s", name, e.Details)
		}
	}
}

func captureErr(t *testing.T, fn func() error) error {
	t.Helper()
	var err error
	captureStdout(t, func() { err = fn() })
	return err
}

func TestCLIImportAudit(t *testing.T) {
	s := newArchiveSetup(t)
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	n := len(auditLog(t, s.st))
	cmd := newImportCmd()
	cmd.SetErr(new(strings.Builder))
	if err := captureErr(t, func() error {
		return runCmd(t, cmd, "", "old", "--from", tbFixture, "--format", "mbox", "--user", "anna")
	}); err != nil {
		t.Fatal(err)
	}
	got := auditLog(t, s.st)[n:]
	// The new import account, then the import.
	if len(got) != 2 || got[0].Action != "account.add" || got[1].Action != "data.import" ||
		got[1].TargetName != "old" || got[1].TargetID != got[0].TargetID {
		t.Fatalf("entries %+v", got)
	}
	if !strings.Contains(string(got[1].Details), `"format": "mbox"`) {
		t.Errorf("details %s", got[1].Details)
	}
}

// TestCLIAuditFailClosed: without the log, an export writes nothing.
func TestCLIAuditFailClosed(t *testing.T) {
	s := newArchiveSetup(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("MAIL_ARCHIVE_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	now := time.Now().UTC()
	if _, err := conn.Exec(ctx, fmt.Sprintf("ALTER TABLE audit_log DETACH PARTITION audit_log_%04d_%02d", now.Year(), int(now.Month()))); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "x")
	err = captureErr(t, func() error { return runCmd(t, newExportCmd(), "", "--format", "mbox", "--out", out, "--user", "anna") })
	if err == nil || !strings.Contains(err.Error(), "audit log") {
		t.Fatalf("export without audit log: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("export written: %v", err)
	}
	if err := runUser(t, "", "lock", "anna"); err == nil {
		t.Error("lock without audit log")
	}
	if u, _ := s.st.GetUserByName(ctx, "anna"); u.LockedAt != nil {
		t.Error("anna locked without an entry")
	}
}

func TestAuditCommand(t *testing.T) {
	s := newArchiveSetup(t)
	ctx := context.Background()
	anna, _ := s.st.GetUserByName(ctx, "anna")
	e := store.AuditEntry{Actor: store.ActorSession, Action: "message.search"}
	e.SetUser(anna.ID, anna.Name)
	e.SetTarget(store.TargetUser, "1", "=HYPERLINK(\"http://evil\")")
	e.Detail("q", "-2+3")
	if err := s.st.Audit(ctx, e); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	run := func(args ...string) error {
		t.Helper()
		return captureErr(t, func() error { return runCmd(t, newAuditCmd(), "", args...) })
	}
	today := time.Now().UTC().Format(time.DateOnly)
	csvOut := filepath.Join(dir, "a.csv")
	if err := run("--since", today, "--until", today, "--format", "csv", "--out", csvOut); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(csvOut); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("csv file: %v %v", fi, err)
	}
	f, _ := os.Open(csvOut)
	records, err := csv.NewReader(f).ReadAll()
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Header, the search, and the export itself.
	if len(records) != 3 || strings.Join(records[0], ",") != strings.Join(auditCSVHeader, ",") {
		t.Fatalf("csv: %q", records)
	}
	if records[1][8] != `'=HYPERLINK("http://evil")` || records[1][5] != "message.search" || records[2][5] != "audit.export" {
		t.Errorf("rows %q", records[1:])
	}
	if err := run("--since", today, "--format", "csv", "--out", csvOut); err == nil {
		t.Error("existing file overwritten")
	}

	jsonOut := filepath.Join(dir, "a.jsonl")
	if err := run("--since", today, "--user", "anna", "--action", "search", "--format", "jsonl", "--out", jsonOut); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(jsonOut)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var j auditJSON
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &j) != nil || j.Action != "message.search" || j.User != "anna" ||
		*j.UserID != anna.ID || !strings.Contains(string(j.Details), `"q":"-2+3"`) {
		t.Fatalf("jsonl: %s", data)
	}
	// Periods before the entries are empty.
	empty := filepath.Join(dir, "b.jsonl")
	if err := run("--since", "2020-01-01", "--until", "2020-12-31", "--format", "jsonl", "--out", empty); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(empty); len(data) != 0 {
		t.Errorf("2020: %s", data)
	}
	for _, args := range [][]string{
		{"--since", today, "--format", "xml"},
		{"--since", "yesterday", "--format", "csv"},
		{"--since", today, "--until", "2020-01-01", "--format", "csv"},
		{"--since", today, "--format", "csv", "--action", "nonsense"},
		{"--since", today, "--format", "csv", "--user", "nobody"},
	} {
		if err := run(args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	// Every export, also an empty one, is recorded.
	var exports int
	for _, r := range auditLog(t, s.st) {
		if r.Action == "audit.export" {
			exports++
		}
	}
	if exports != 3 {
		t.Errorf("%d audit.export entries, want 3", exports)
	}
}
