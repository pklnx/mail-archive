package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/archive/gobdtest"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/gobd"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// gobdCLI is the fixture of internal/archive/gobdtest behind the command
// line.
func gobdCLI(t *testing.T) *gobdtest.Fixture {
	t.Helper()
	f := gobdtest.New(t)
	t.Setenv(config.EnvDatabaseURL, f.URL)
	t.Setenv(config.EnvDataDir, f.DataDir)
	t.Setenv(config.EnvLogLevel, "error")
	return f
}

// fixedGoBD makes every export of the test deterministic, and calls
// progress (if not nil) after each message file.
func fixedGoBD(t *testing.T, progress func(done int64) error) {
	t.Helper()
	gobdTestHook = func(o *archive.GoBDOptions) {
		o.Now = func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) }
		o.NewID = func() string { return "00112233445566778899aabbccddeeff" }
		o.Progress = progress
	}
	t.Cleanup(func() { gobdTestHook = nil })
}

func runExportGoBDCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	cmd := newExportCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := runCmd(t, cmd, "", append([]string{"--format", "gobd"}, args...)...)
	var exitErr *exitError
	if err != nil && !errors.As(err, &exitErr) {
		// main prints these.
		fmt.Fprintln(&errOut, "error:", err)
	}
	return exitCodeOf(err), out.String(), errOut.String()
}

// entries returns the audit entries of the GoBD export, oldest first.
func exportEntries(t *testing.T, st *store.Store) []store.AuditRecord {
	t.Helper()
	var out []store.AuditRecord
	for _, r := range auditLog(t, st) {
		if r.Action == "data.export" || r.Action == "data.export_done" {
			out = append(out, r)
		}
	}
	return out
}

func details(t *testing.T, r store.AuditRecord) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Details, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func gid(g *store.Group) *int64 { return &g.ID }

func sameGroup(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func absent(t *testing.T, out string) {
	t.Helper()
	for _, p := range []string{out, out + gobd.PartialSuffix} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists: %v", p, err)
		}
	}
}

// TestExportGoBDRefusesMixedGroups (T-G1, T-G2): a selection across groups
// is refused with exit 2 and an error naming each and its group, before
// any file or audit entry.
func TestExportGoBDRefusesMixedGroups(t *testing.T) {
	f := gobdCLI(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "x")
	code, _, stderr := runExportGoBDCmd(t, "--out", out, "--user", "anna", "--user", "kim", "--group", "Meier GmbH")
	if code != 2 || !strings.Contains(stderr, "anna (Meier GmbH)") || !strings.Contains(stderr, "kim (Schulz KG)") ||
		!strings.Contains(stderr, "one export covers one customer group") {
		t.Errorf("anna and kim: exit %d\n%s", code, stderr)
	}
	absent(t, out)
	code, _, stderr = runExportGoBDCmd(t, "--out", out, "--account", "anna/work", "--account", "old-imap", "--group", "Meier GmbH")
	if code != 2 || !strings.Contains(stderr, `account "old-imap" (no group)`) {
		t.Errorf("anna/work and old-imap: exit %d\n%s", code, stderr)
	}
	absent(t, out)
	if e := exportEntries(t, f.St); len(e) != 0 {
		t.Errorf("audit entries of refused exports: %v", e)
	}
}

// TestExportGoBDGroups (T-G3, T-G4): one group or no group works and
// passes export verify; --group is required with two groups and checked.
func TestExportGoBDGroups(t *testing.T) {
	f := gobdCLI(t)
	fixedGoBD(t, nil)
	dir := t.TempDir()

	out := filepath.Join(dir, "meier")
	code, stdout, stderr := runExportGoBDCmd(t, "--out", out, "--user", "anna", "--user", "ben", "--group", "Meier GmbH")
	if code != 0 {
		t.Fatalf("anna and ben: exit %d\n%s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "exported 3 message(s), 4 location(s)") || !strings.Contains(stdout, "customer group Meier GmbH") {
		t.Errorf("output:\n%s", stdout)
	}
	// The real export through export verify, with the expected group.
	vcode, vout := runExportVerifyCmd(t, out, "--expect-group", "meier gmbh")
	if vcode != 0 || !strings.Contains(vout, "no problems") || !strings.Contains(vout, "owners: anna, ben") {
		t.Errorf("verify: exit %d\n%s", vcode, vout)
	}
	st, err := os.ReadFile(filepath.Join(out, gobd.StatementFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{fmt.Sprintf("group-id %d\n", f.A.ID), "group \"Meier GmbH\"\n", "owners [\"anna\",\"ben\"]\n"} {
		if !bytes.Contains(st, []byte(want)) {
			t.Errorf("statement lacks %q", want)
		}
	}
	// The ZIP of the same selection passes too.
	zout := filepath.Join(dir, "meier.zip")
	if code, _, stderr := runExportGoBDCmd(t, "--out", zout, "--user", "anna", "--user", "ben", "--group", "Meier GmbH"); code != 0 {
		t.Fatalf("zip: exit %d\n%s", code, stderr)
	}
	if vcode, vout := runExportVerifyCmd(t, zout, "--expect-group", "Meier GmbH"); vcode != 0 {
		t.Errorf("verify zip: exit %d\n%s", vcode, vout)
	}

	none := filepath.Join(dir, "none")
	if code, _, stderr := runExportGoBDCmd(t, "--out", none, "--account", "old-imap", "--without-group"); code != 0 {
		t.Fatalf("old-imap: exit %d\n%s", code, stderr)
	}
	if vcode, vout := runExportVerifyCmd(t, none, "--expect-group", "none"); vcode != 0 || !strings.Contains(vout, "owners: none") {
		t.Errorf("verify none: exit %d\n%s", vcode, vout)
	}

	// T-G4.
	code, _, stderr = runExportGoBDCmd(t, "--out", filepath.Join(dir, "a"), "--user", "anna")
	if code != 2 || !strings.Contains(stderr, "name the customer with --group or --without-group") {
		t.Errorf("no group named: exit %d\n%s", code, stderr)
	}
	code, _, stderr = runExportGoBDCmd(t, "--out", filepath.Join(dir, "b"), "--user", "kim", "--group", "Meier GmbH")
	if code != 2 || !strings.Contains(stderr, "the selection is in Schulz KG, not Meier GmbH") {
		t.Errorf("kim as Meier GmbH: exit %d\n%s", code, stderr)
	}
	code, _, stderr = runExportGoBDCmd(t, "--out", filepath.Join(dir, "c"), "--user", "anna", "--group", "Meier GmbH", "--without-group")
	if code != 2 || !strings.Contains(stderr, "exclude each other") {
		t.Errorf("both flags: exit %d\n%s", code, stderr)
	}
	code, _, stderr = runExportGoBDCmd(t, "--out", filepath.Join(dir, "d"), "--user", "anna", "--group", "Müller")
	if code != 2 || !strings.Contains(stderr, `group "Müller" not found`) {
		t.Errorf("unknown group: exit %d\n%s", code, stderr)
	}
	// G1: --group alone exports the whole group.
	whole := filepath.Join(dir, "whole")
	if code, _, stderr := runExportGoBDCmd(t, "--out", whole, "--group", "Meier GmbH"); code != 0 {
		t.Fatalf("whole group: exit %d\n%s", code, stderr)
	}
	if vcode, vout := runExportVerifyCmd(t, whole); vcode != 0 || !strings.Contains(vout, "owners: anna, ben") {
		t.Errorf("verify whole group: exit %d\n%s", vcode, vout)
	}

	// With one group both flags are optional: move kim, remove group B.
	if _, err := f.St.SetUserGroup(f.Ctx, f.Kim.ID, f.A.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.St.DeleteGroup(f.Ctx, f.B.ID); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runExportGoBDCmd(t, "--out", filepath.Join(dir, "one"), "--user", "kim"); code != 0 {
		t.Errorf("one group, no flag: exit %d\n%s", code, stderr)
	}
}

// TestExportGoBDFlags: flag errors exit 2 and run nothing.
func TestExportGoBDFlags(t *testing.T) {
	gobdCLI(t)
	out := filepath.Join(t.TempDir(), "x")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--group", "Meier GmbH"}, "--out is required"},
		{[]string{"--out", out}, "choose what to export"},
		{[]string{"--out", out, "--without-group"}, "name the accounts with --account"},
		{[]string{"--out", out, "--user", "anna", "--hold", "1"}, "choose one of"},
		{[]string{"--out", out, "--account", "anna/work", "--account", "ben/work", "--folder", "INBOX"}, "exactly one --account"},
		{[]string{"--out", out, "--user", "nobody", "--group", "Meier GmbH"}, `user "nobody" not found`},
		{[]string{"--out", out, "--account", "work", "--group", "Meier GmbH"}, "several users have an account"},
		{[]string{"--out", out, "--user", "anna", "--group", "Meier GmbH", "--time-zone", "Mars/Olympus"}, "unknown time zone"},
		{[]string{"--out", out, "--user", "anna", "--group", "Meier GmbH", "--since", "2025-02-30"}, "--since"},
		{[]string{"--out", out + ".zip", "--user", "anna", "--group", "Meier GmbH", "--resume"}, "only a directory export can be resumed"},
		{[]string{"--out", out, "--user", "anna", "--group", "Meier GmbH", "--resume"}, "no interrupted export"},
	} {
		code, _, stderr := runExportGoBDCmd(t, c.args...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, want %q\n%s", c.args, code, c.want, stderr)
		}
	}
	absent(t, out)
	// mbox and maildir keep their rules.
	err := runCmd(t, newExportCmd(), "", "--format", "mbox", "--out", out, "--user", "anna", "--since", "2025-01-01")
	if err == nil || !strings.Contains(err.Error(), "only for --format gobd") {
		t.Errorf("mbox with --since: %v", err)
	}
	err = runCmd(t, newExportCmd(), "", "--format", "mbox", "--out", out, "--user", "anna", "--user", "ben")
	if err == nil || !strings.Contains(err.Error(), "one value") {
		t.Errorf("mbox with two users: %v", err)
	}
}

// TestExportGoBDMoveDuringExport (T-G6): a group move that commits while
// the export runs changes nothing in it; it is reported, recorded and
// exits 3. The move commits while the export is still in its snapshot,
// so the export holds no lock on the user.
func TestExportGoBDMoveDuringExport(t *testing.T) {
	f := gobdCLI(t)
	dir := t.TempDir()
	fixedGoBD(t, nil)
	before := filepath.Join(dir, "before")
	if code, _, stderr := runExportGoBDCmd(t, "--out", before, "--user", "anna", "--user", "ben", "--group", "Meier GmbH"); code != 0 {
		t.Fatalf("without a move: exit %d\n%s", code, stderr)
	}
	moved := false
	fixedGoBD(t, func(done int64) error {
		if done != 1 || moved {
			return nil
		}
		moved = true
		ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
		defer cancel()
		_, err := f.St.SetUserGroup(ctx, f.Ben.ID, f.B.ID)
		return err
	})
	out := filepath.Join(dir, "during")
	code, _, stderr := runExportGoBDCmd(t, "--out", out, "--user", "anna", "--user", "ben", "--group", "Meier GmbH")
	if code != exitGroupMoved || !strings.Contains(stderr, "ben moved from Meier GmbH to Schulz KG") ||
		!strings.Contains(stderr, "complete as of the snapshot") {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if diff := treeDiffCLI(t, before, out); diff != "" {
		t.Errorf("the move changed the export: %s", diff)
	}
	entries := exportEntries(t, f.St)
	last := entries[len(entries)-1]
	if last.Action != "data.export_done" || !sameGroup(last.GroupID, gid(f.A)) {
		t.Fatalf("done entry %+v", last)
	}
	if got := details(t, last)["group_moved_after_snapshot"]; fmt.Sprint(got) != "[ben]" {
		t.Errorf("group_moved_after_snapshot %v", got)
	}

	// Rev. 4: one user who moves; both entries keep the snapshot's group.
	if _, err := f.St.SetUserGroup(f.Ctx, f.Ben.ID, f.A.ID); err != nil {
		t.Fatal(err)
	}
	moved = false
	out = filepath.Join(dir, "single")
	code, _, stderr = runExportGoBDCmd(t, "--out", out, "--user", "ben", "--group", "Meier GmbH")
	if code != exitGroupMoved {
		t.Fatalf("single: exit %d\n%s", code, stderr)
	}
	entries = exportEntries(t, f.St)
	for _, e := range entries[len(entries)-2:] {
		if !sameGroup(e.GroupID, gid(f.A)) || e.TargetType != store.TargetUser || e.TargetName != "ben" {
			t.Errorf("%s: group %v, target %s %s", e.Action, e.GroupID, e.TargetType, e.TargetName)
		}
	}
}

func treeDiffCLI(t *testing.T, a, b string) string {
	t.Helper()
	read := func(root string) map[string]string {
		out := map[string]string{}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(root, p)
				data, _ := os.ReadFile(p)
				out[rel] = string(data)
			}
			return err
		})
		return out
	}
	ta, tb := read(a), read(b)
	if len(ta) != len(tb) {
		return fmt.Sprintf("%d files against %d", len(ta), len(tb))
	}
	for k, v := range ta {
		if tb[k] != v {
			return "differs: " + k
		}
	}
	return ""
}

var errStopExport = errors.New("stopped by the test")

// TestExportGoBDResumeAfterMove (T-G8, Rev. 4): a resume after the
// selection's group changed is refused by the state file's check alone,
// and the interrupted export stays.
func TestExportGoBDResumeAfterMove(t *testing.T) {
	f := gobdCLI(t)
	f.Add(t, "ben/work/INBOX", "Zweiter Beleg", time.Date(2025, 2, 3, 9, 0, 0, 0, time.UTC))
	fixedGoBD(t, func(done int64) error {
		if done == 1 {
			return errStopExport
		}
		return nil
	})
	out := filepath.Join(t.TempDir(), "x")
	code, _, stderr := runExportGoBDCmd(t, "--out", out, "--user", "ben", "--group", "Meier GmbH")
	if code != 1 || !strings.Contains(stderr, "stopped by the test") {
		t.Fatalf("first run: exit %d\n%s", code, stderr)
	}
	entries := exportEntries(t, f.St)
	if last := entries[len(entries)-1]; last.Action != "data.export_done" || details(t, last)["aborted"] == nil {
		t.Errorf("the interrupted run's end: %+v", last)
	}
	if _, err := f.St.SetUserGroup(f.Ctx, f.Ben.ID, f.B.ID); err != nil {
		t.Fatal(err)
	}
	fixedGoBD(t, nil)
	code, _, stderr = runExportGoBDCmd(t, "--out", out, "--user", "ben", "--group", "Meier GmbH", "--resume")
	if code != 2 || strings.TrimSpace(stderr) != "error: "+archive.ErrGroupChanged.Error() {
		t.Errorf("resume after the move: exit %d\n%s", code, stderr)
	}
	if _, err := os.Stat(out + gobd.PartialSuffix); err != nil {
		t.Errorf("the interrupted export is gone: %v", err)
	}
	// Back in the group, the resume completes.
	if _, err := f.St.SetUserGroup(f.Ctx, f.Ben.ID, f.A.ID); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runExportGoBDCmd(t, "--out", out, "--user", "ben", "--group", "Meier GmbH", "--resume"); code != 0 {
		t.Fatalf("resume: exit %d\n%s", code, stderr)
	}
	if vcode, vout := runExportVerifyCmd(t, out); vcode != 0 {
		t.Errorf("verify the resumed export: exit %d\n%s", vcode, vout)
	}
}

// TestExportGoBDAudit (T-G12, decision 19): both entries are recorded
// under the export's group, set explicitly; details never carry a group;
// a member's own Log lists neither; the audit export by group finds them.
func TestExportGoBDAudit(t *testing.T) {
	f := gobdCLI(t)
	fixedGoBD(t, nil)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if code, _, stderr := runExportGoBDCmd(t, args...); code != 0 {
			t.Fatalf("%v: exit %d\n%s", args, code, stderr)
		}
	}
	run("--out", filepath.Join(dir, "ab"), "--user", "anna", "--user", "ben", "--group", "Meier GmbH", "--requested-by", "Steuerberater Huber")
	run("--out", filepath.Join(dir, "kim"), "--user", "kim", "--group", "Schulz KG")
	run("--out", filepath.Join(dir, "old"), "--account", "old-imap", "--without-group")

	entries := exportEntries(t, f.St)
	if len(entries) != 6 {
		t.Fatalf("%d entries", len(entries))
	}
	want := []struct {
		group  *int64
		target string
	}{{gid(f.A), ""}, {gid(f.A), ""}, {gid(f.B), "kim"}, {gid(f.B), "kim"}, {nil, "old-imap"}, {nil, "old-imap"}}
	for i, e := range entries {
		if !sameGroup(e.GroupID, want[i].group) || e.TargetName != want[i].target {
			t.Errorf("entry %d %s: group %v, target %q", i, e.Action, e.GroupID, e.TargetName)
		}
		d := details(t, e)
		for k, v := range d {
			if strings.Contains(k, "group") && k != "group_moved_after_snapshot" {
				t.Errorf("entry %d %s: detail %s", i, e.Action, k)
			}
			if s, ok := v.(string); ok && (strings.Contains(s, "Meier") || strings.Contains(s, "Schulz")) {
				t.Errorf("entry %d %s: %s names a group: %s", i, e.Action, k, s)
			}
		}
	}
	first, done := details(t, entries[0]), details(t, entries[1])
	if first["requested_by"] != "Steuerberater Huber" || fmt.Sprint(first["owners"]) != "[anna ben]" || first["format"] != "gobd" {
		t.Errorf("data.export details %v", first)
	}
	for _, k := range []string{"manifest_sha256", "statement_sha256", "messages", "complete", "id"} {
		if done[k] == nil {
			t.Errorf("data.export_done lacks %s: %v", k, done)
		}
	}
	sums, err := os.ReadFile(filepath.Join(dir, "ab", gobd.SumsFile))
	if err != nil {
		t.Fatal(err)
	}
	if done["manifest_sha256"] != shaHex(sums) {
		t.Errorf("manifest_sha256 %v is not the digest of SHA256SUMS", done["manifest_sha256"])
	}

	// ./ma audit --group "Meier GmbH" holds A's two entries, not kim's.
	auditOut := filepath.Join(dir, "audit.jsonl")
	if err := runCmd(t, newAuditCmd(), "", "--since", time.Now().UTC().Format(time.DateOnly), "--group", "Meier GmbH",
		"--action", "export", "--format", "jsonl", "--out", auditOut); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(auditOut)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(data, []byte(`"action":"data.export`)); n != 2 || bytes.Contains(data, []byte(`"kim"`)) {
		t.Errorf("audit export of Meier GmbH: %d export entries\n%s", n, data)
	}

	// Kim's own Log (GET /api/audit as kim, not an admin) lists neither.
	page, err := f.St.ListAudit(f.Ctx, store.AuditPage{Viewer: f.Kim.ID, Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page {
		if strings.HasPrefix(r.Action, "data.export") {
			t.Errorf("kim's Log lists %s", r.Action)
		}
	}
}

// TestExportGoBDUnownedGetsOwner (decision 19): an export of an account
// without an owner records data.export_done without a group even when the
// account gets an owner during the export.
func TestExportGoBDUnownedGetsOwner(t *testing.T) {
	f := gobdCLI(t)
	fixedGoBD(t, func(done int64) error {
		if done == 1 {
			ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
			defer cancel()
			_, err := f.Conn.Exec(ctx, "UPDATE accounts SET owner_id = $1 WHERE id = $2", f.Kim.ID, f.OldIMAP.ID)
			return err
		}
		return nil
	})
	code, _, stderr := runExportGoBDCmd(t, "--out", filepath.Join(t.TempDir(), "x"), "--account", "old-imap", "--without-group")
	if code != exitGroupMoved || !strings.Contains(stderr, "account old-imap moved from no group to Schulz KG") {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	entries := exportEntries(t, f.St)
	last := entries[len(entries)-1]
	if last.Action != "data.export_done" || last.GroupID != nil || last.TargetName != "old-imap" {
		t.Errorf("done entry: group %v, target %q", last.GroupID, last.TargetName)
	}
}

// TestExportGoBDDetails: --hold, --grant (auditor_grant), the missing
// exit code, the plaintext warning.
func TestExportGoBDDetails(t *testing.T) {
	f := gobdCLI(t)
	fixedGoBD(t, nil)
	dir := t.TempDir()
	aud, err := f.St.CreateUserInGroup(f.Ctx, store.NewUser{Name: "pruefer", PasswordHash: "h", GroupID: f.A.ID, Auditor: true})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := f.St.CreateGrant(f.Ctx, store.NewGrant{OwnerID: f.Anna.ID, AuditorID: aud.ID, Label: "Prüfung 2024",
		Accounts: []store.GrantAccount{{AccountID: f.AnnaWork.ID}}, EndsAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runExportGoBDCmd(t, "--out", filepath.Join(dir, "grant"), "--grant", fmt.Sprint(grant), "--group", "Meier GmbH"); code != 0 {
		t.Fatalf("grant: exit %d\n%s", code, stderr)
	}
	entries := exportEntries(t, f.St)
	if d := details(t, entries[0]); d["auditor_grant"] != float64(grant) || entries[0].TargetName != "anna" {
		t.Errorf("grant entry: %v, target %q", d, entries[0].TargetName)
	}
	hold, err := f.St.AddLegalHold(f.Ctx, store.NewLegalHold{Name: "h", Reason: "r", OwnerID: &f.Ben.ID})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runExportGoBDCmd(t, "--out", filepath.Join(dir, "hold"), "--hold", fmt.Sprint(hold), "--group", "Meier GmbH"); code != 0 {
		t.Fatalf("hold: exit %d\n%s", code, stderr)
	}

	// Encryption at rest on and the export inside the data directory.
	ring := blobcrypt.KeyringPath(f.DataDir)
	if err := os.MkdirAll(filepath.Dir(ring), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ring, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runExportGoBDCmd(t, "--out", filepath.Join(f.DataDir, "export", "x"), "--user", "ben", "--group", "Meier GmbH")
	if code != 0 || !strings.Contains(stderr, "the export is not encrypted") {
		t.Errorf("inside the data directory: exit %d\n%s", code, stderr)
	}
	if _, _, stderr := runExportGoBDCmd(t, "--out", filepath.Join(dir, "outside"), "--user", "ben", "--group", "Meier GmbH"); strings.Contains(stderr, "not encrypted") {
		t.Errorf("warning outside the data directory:\n%s", stderr)
	}

	// A missing file: exit 1, listed on stderr.
	m, err := f.Blobs.OpenMessage(f.BenMsg) // the file as it is on disk, in either form
	if err != nil {
		t.Fatal(err)
	}
	rel := m.Path()
	_ = m.Close()
	if err := os.Remove(filepath.Join(f.DataDir, rel)); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runExportGoBDCmd(t, "--out", filepath.Join(dir, "missing"), "--user", "ben", "--group", "Meier GmbH")
	if code != 1 || !strings.Contains(stderr, "missing "+f.BenMsg+": file missing") {
		t.Errorf("missing: exit %d\n%s", code, stderr)
	}
	entries = exportEntries(t, f.St)
	if d := details(t, entries[len(entries)-1]); d["complete"] != false || d["missing"] != float64(1) {
		t.Errorf("done entry of an incomplete export: %v", d)
	}
}

// TestExportGoBDUnownedFolder: --folder narrows an account without an
// owner, so the files match what the signed statement names (review A,
// #512).
func TestExportGoBDUnownedFolder(t *testing.T) {
	f := gobdCLI(t)
	fixedGoBD(t, nil)
	sent := f.Add(t, "old-imap/Sent", "Gesendet", time.Date(2019, 7, 1, 9, 0, 0, 0, time.UTC))
	out := filepath.Join(t.TempDir(), "x")
	if code, _, stderr := runExportGoBDCmd(t, "--out", out, "--account", "old-imap", "--folder", "INBOX", "--without-group"); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	locs, err := os.ReadFile(filepath.Join(out, gobd.LocationsFile))
	if err != nil {
		t.Fatal(err)
	}
	if rows := strings.Count(string(locs), "\r\n") - 1; rows != 1 || !strings.Contains(string(locs), f.OldMsg) || strings.Contains(string(locs), sent) {
		t.Errorf("old-imap --folder INBOX:\n%s", locs)
	}
	if _, err := os.Stat(filepath.Join(out, "mail", "2019", "07", sent+".eml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Sent's message was exported: %v", err)
	}
}

// TestExportGoBDEncryptionNeedsKey: the GoBD export goes through the
// readers' start-up rule of encryption at rest, as mbox and maildir do:
// with encryption on and no archive key it refuses before the audit entry
// and writes nothing; with the key it exports and the export verifies.
func TestExportGoBDEncryptionNeedsKey(t *testing.T) {
	f := gobdCLI(t)
	fixedGoBD(t, nil)
	kek := enableForTest(t, f.St, store.BlobOn)
	dir := t.TempDir()
	out := filepath.Join(dir, "nokey")
	code, _, stderr := runExportGoBDCmd(t, "--out", out, "--user", "ben", "--group", "Meier GmbH")
	if code == 0 || !strings.Contains(stderr, archive.ErrNoArchiveKey.Error()) {
		t.Fatalf("without the key: exit %d\n%s", code, stderr)
	}
	absent(t, out)
	if n := len(exportEntries(t, f.St)); n != 0 {
		t.Errorf("without the key: %d audit entries", n)
	}
	t.Setenv(config.EnvArchiveKey, blobcrypt.EncodeKEK(kek))
	out = filepath.Join(dir, "key")
	if code, _, stderr := runExportGoBDCmd(t, "--out", out, "--user", "ben", "--group", "Meier GmbH"); code != 0 {
		t.Fatalf("with the key: exit %d\n%s", code, stderr)
	}
	res, err := gobd.Verify(out, gobd.VerifyOptions{})
	if err != nil || res.Problems() != 0 {
		t.Fatalf("verify: %+v %v", res, err)
	}
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// grantPage is one page of ListAudit over the last hour, cut down to the
// GoBD export's entries.
func grantPage(t *testing.T, f *gobdtest.Fixture, p store.AuditPage) []store.AuditRecord {
	t.Helper()
	p.Since, p.Until, p.Limit = time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 500
	recs, err := f.St.ListAudit(f.Ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditRecord
	for _, r := range recs {
		if r.Action == "data.export" || r.Action == "data.export_done" {
			out = append(out, r)
		}
	}
	return out
}

// TestExportGoBDGrant (#393, part 4): --grant exports exactly the grant's
// accounts, folders and period. The owner's account the grant does not
// name is dropped (Restrict, #439), so are the folder it does not name,
// the messages outside its period and the other owner's place of a shared
// message. Both entries are targeted at the owner, recorded in the owner's
// group and carry auditor_grant and via_owners: the export is in the
// owner's Log, as the operator's with only the grant, and in the admin's
// view of the grant; a member of the group and a user of another group
// see it in neither.
func TestExportGoBDGrant(t *testing.T) {
	f := gobdCLI(t)
	fixedGoBD(t, nil)
	day := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 9, 0, 0, 0, time.UTC) }
	f.Account(t, f.Anna, "privat")
	private := f.Add(t, "anna/privat/INBOX", "Privat", day(2024, 5, 1))
	archived := f.Add(t, "anna/work/Archiv", "Nur Archiv", day(2024, 4, 1))
	before := f.Add(t, "anna/work/INBOX", "Vorjahr", day(2023, 6, 1))
	after := f.Add(t, "anna/work/INBOX", "Neujahr", day(2025, 1, 1))
	aud, err := f.St.CreateUserInGroup(f.Ctx, store.NewUser{Name: "pruefer", PasswordHash: "h", GroupID: f.A.ID, Auditor: true})
	if err != nil {
		t.Fatal(err)
	}
	from, until := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	grant, err := f.St.CreateGrant(f.Ctx, store.NewGrant{OwnerID: f.Anna.ID, AuditorID: aud.ID, Label: "Betriebsprüfung 2024",
		Accounts:   []store.GrantAccount{{AccountID: f.AnnaWork.ID, Folders: []int64{f.Folders["anna/work/INBOX"]}}},
		PeriodFrom: &from, PeriodUntil: &until, EndsAt: time.Now().Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "grant")
	code, stdout, stderr := runExportGoBDCmd(t, "--out", out, "--grant", fmt.Sprint(grant), "--group", "Meier GmbH")
	if code != 0 || !strings.Contains(stdout, "exported 2 message(s), 2 location(s)") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	msgs, err := os.ReadFile(filepath.Join(out, gobd.MessagesFile))
	if err != nil {
		t.Fatal(err)
	}
	locs, err := os.ReadFile(filepath.Join(out, gobd.LocationsFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{f.Shared, f.AnnaMsg} {
		if !strings.Contains(string(msgs), sha) {
			t.Errorf("granted message %s not exported", sha)
		}
	}
	for name, sha := range map[string]string{"the ungranted account": private, "the ungranted folder": archived,
		"before the period": before, "after the period": after, "ben's": f.BenMsg, "kim's": f.KimMsg, "old-imap's": f.OldMsg} {
		if strings.Contains(string(msgs), sha) || strings.Contains(string(locs), sha) {
			t.Errorf("message %s exported", name)
		}
	}
	rows := strings.Split(strings.TrimSuffix(string(locs), "\r\n"), "\r\n")[1:]
	for _, row := range rows {
		if !strings.Contains(row, `;"anna";"work";`) || !strings.Contains(row, `;"INBOX";`) {
			t.Errorf("location outside the grant: %s", row)
		}
	}
	if len(rows) != 2 {
		t.Errorf("locations:\n%s", locs)
	}
	if vcode, vout := runExportVerifyCmd(t, out, "--expect-group", "meier gmbh"); vcode != 0 || !strings.Contains(vout, "owners: anna\n") {
		t.Errorf("verify: exit %d\n%s", vcode, vout)
	}

	// Both entries: the owner, the owner's group, the grant and its owner.
	entries := exportEntries(t, f.St)
	if len(entries) != 2 {
		t.Fatalf("entries: %v", entries)
	}
	for _, e := range entries {
		d := details(t, e)
		via, _ := d[store.DetailViaOwners].([]any)
		if e.TargetType != "user" || e.TargetID != fmt.Sprint(f.Anna.ID) || !sameGroup(e.GroupID, gid(f.A)) ||
			d[store.DetailAuditorGrant] != float64(grant) || len(via) != 1 || via[0] != float64(f.Anna.ID) {
			t.Errorf("%s: target %s %s, group %v, details %v", e.Action, e.TargetType, e.TargetID, e.GroupID, d)
		}
	}

	// The owner's Log: both, as the operator's, with the grant only.
	own := grantPage(t, f, store.AuditPage{Viewer: f.Anna.ID})
	if len(own) != 2 {
		t.Fatalf("owner's Log: %v", own)
	}
	for _, r := range own {
		if !r.ViaShare || !r.ViaAuditor || r.UserName != store.AuditOperator || string(r.Details) != fmt.Sprintf(`{"auditor_grant":%d}`, grant) {
			t.Errorf("owner's Log %s: via share %v, via auditor %v, user %q, details %s", r.Action, r.ViaShare, r.ViaAuditor, r.UserName, r.Details)
		}
	}
	if mine := grantPage(t, f, store.AuditPage{Viewer: f.Anna.ID, GrantID: &grant}); len(mine) != 2 {
		t.Errorf("owner's Log of the grant: %v", mine)
	}
	// The admin's view of the grant: both, in full.
	admin := grantPage(t, f, store.AuditPage{Viewer: f.Anna.ID, All: true, GrantID: &grant, GrantOwner: f.Anna.ID})
	if len(admin) != 2 || details(t, admin[0])["out"] != out {
		t.Errorf("admin's view of the grant: %v", admin)
	}
	// Nobody else.
	for _, u := range []*store.User{f.Ben, f.Kim, aud} {
		if got := grantPage(t, f, store.AuditPage{Viewer: u.ID}); len(got) != 0 {
			t.Errorf("%s's Log: %v", u.Name, got)
		}
		if got := grantPage(t, f, store.AuditPage{Viewer: u.ID, GrantID: &grant}); len(got) != 0 {
			t.Errorf("%s's Log of the grant: %v", u.Name, got)
		}
	}
}

// TestExportGoBDGrantStates (#393, part 4, I6): a grant that has not
// started and one that has ended are exported (the operator exports on the
// owner's request); a revoked grant, also one revoked by a group move, is
// refused with exit 2 before any file or entry; a grant's export is in its
// owner's group, so claiming another group is refused.
func TestExportGoBDGrantStates(t *testing.T) {
	f := gobdCLI(t)
	fixedGoBD(t, nil)
	aud, err := f.St.CreateUserInGroup(f.Ctx, store.NewUser{Name: "pruefer", PasswordHash: "h", GroupID: f.A.ID, Auditor: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	newGrant := func(starts time.Time) int64 {
		t.Helper()
		id, err := f.St.CreateGrant(f.Ctx, store.NewGrant{OwnerID: f.Anna.ID, AuditorID: aud.ID, Label: "Prüfung",
			Accounts: []store.GrantAccount{{AccountID: f.AnnaWork.ID}}, StartsAt: &starts, EndsAt: starts.Add(48 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	scheduled := newGrant(now.Add(24 * time.Hour))
	expired := newGrant(now.Add(-24 * time.Hour))
	if _, err := f.Conn.Exec(f.Ctx, "UPDATE auditor_grants SET ends_at = now() - interval '1 hour' WHERE id = $1", expired); err != nil {
		t.Fatal(err)
	}
	revoked := newGrant(now.Add(-time.Hour))
	if err := f.St.RevokeGrant(f.Ctx, revoked, &f.Anna.ID, &f.Anna.ID); err != nil {
		t.Fatal(err)
	}
	all, err := f.St.AllGrants(f.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range all {
		if g.Active {
			t.Fatalf("grant %d (%s) is active", g.ID, g.Label)
		}
	}

	dir := t.TempDir()
	for name, id := range map[string]int64{"scheduled": scheduled, "expired": expired} {
		code, stdout, stderr := runExportGoBDCmd(t, "--out", filepath.Join(dir, name), "--grant", fmt.Sprint(id), "--group", "Meier GmbH")
		if code != 0 || !strings.Contains(stdout, "exported 2 message(s), 3 location(s)") {
			t.Errorf("%s grant: exit %d\n%s%s", name, code, stdout, stderr)
		}
	}
	exported := len(exportEntries(t, f.St))
	if exported != 4 {
		t.Errorf("entries of two exports: %d", exported)
	}

	refused := func(name string, id int64, group, want string) {
		t.Helper()
		out := filepath.Join(dir, name)
		code, _, stderr := runExportGoBDCmd(t, "--out", out, "--grant", fmt.Sprint(id), "--group", group)
		if code != 2 || !strings.Contains(stderr, want) {
			t.Errorf("%s: exit %d, want %q\n%s", name, code, want, stderr)
		}
		absent(t, out)
		if n := len(exportEntries(t, f.St)); n != exported {
			t.Errorf("%s: %d entries, want %d", name, n, exported)
		}
	}
	refused("revoked", revoked, "Meier GmbH", fmt.Sprintf("grant %d was revoked", revoked))
	// Moving the auditor to another group revokes the grant.
	if _, err := f.St.SetUserGroup(f.Ctx, aud.ID, f.B.ID); err != nil {
		t.Fatal(err)
	}
	refused("moved", scheduled, "Meier GmbH", fmt.Sprintf("grant %d was revoked", scheduled))
	other := newGrantOf(t, f, f.Kim, f.KimMail)
	refused("other group", other, "Meier GmbH", "the selection is in Schulz KG, not Meier GmbH")
}

// newGrantOf creates an active grant of owner's account to a new auditor
// of the owner's group.
func newGrantOf(t *testing.T, f *gobdtest.Fixture, owner *store.User, acc *store.Account) int64 {
	t.Helper()
	aud, err := f.St.CreateUserInGroup(f.Ctx, store.NewUser{Name: owner.Name + ".pruefer", PasswordHash: "h", GroupID: owner.GroupID, Auditor: true})
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.St.CreateGrant(f.Ctx, store.NewGrant{OwnerID: owner.ID, AuditorID: aud.ID, Label: "Prüfung",
		Accounts: []store.GrantAccount{{AccountID: acc.ID}}, EndsAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
