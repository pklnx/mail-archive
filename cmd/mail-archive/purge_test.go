package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
)

// purgeSetup adds anna's account "old" to newArchiveSetup, with a message
// only there and a copy of the message of anna's "mail", and removes it
// with the command.
func newPurgeCLISetup(t *testing.T) (*archiveSetup, *store.Account) {
	t.Helper()
	s := newArchiveSetup(t)
	ctx := context.Background()
	anna := s.accs["anna"]
	old := &store.Account{Name: "old", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: anna.OwnerID}
	if err := s.st.CreateAccount(ctx, old); err != nil {
		t.Fatal(err)
	}
	f, err := s.st.GetOrCreateFolder(ctx, old.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	only, _, err := s.blobs.Put(strings.NewReader("Subject: only in old\r\n\r\nbye\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	shared, _, err := s.blobs.Put(strings.NewReader("Subject: for anna\r\n\r\nhi\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	metas := []store.MessageMeta{{SHA256: only.SHA256, Size: only.Size, StoredPath: only.Path}, {SHA256: shared.SHA256, Size: shared.Size, StoredPath: shared.Path}}
	if _, err := s.st.SaveBatch(ctx, f.ID, 2, metas, []store.Location{{FolderID: f.ID, UID: 1}, {FolderID: f.ID, UID: 2}}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runCmd(t, newAccountCmd(), "", "remove", "old"); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "delete it completely with: account purge old") {
		t.Errorf("remove output:\n%s", out)
	}
	acc, err := s.st.GetAccount(ctx, old.ID)
	if err != nil || acc.RemovedAt == nil {
		t.Fatalf("removed %+v %v", acc, err)
	}
	return s, acc
}

func purgeOut(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newAccountCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := runCmd(t, cmd, stdin, append([]string{"purge"}, args...)...)
	return out.String(), err
}

func TestAccountPurgeCommand(t *testing.T) {
	s, acc := newPurgeCLISetup(t)
	ctx := context.Background()
	t.Setenv("MAIL_ARCHIVE_OPERATOR", "patrick")

	out, err := purgeOut(t, "", "old", "--dry-run")
	if err != nil || !strings.Contains(out, `account "old" (owner anna, imap)`) || !strings.Contains(out, "locations to remove:   2") ||
		!strings.Contains(out, "messages to delete:    1") || !strings.Contains(out, "messages kept:         1") ||
		!regexpMatch(`\s+anna\s+mail\s+1`, out) || !strings.Contains(out, "dry run: nothing was deleted") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if out, err := purgeOut(t, "5\n", "old"); err == nil || !strings.Contains(err.Error(), "not confirmed; nothing was deleted") ||
		!strings.Contains(out, `Type the number of messages to delete (1) to delete account "old" completely:`) {
		t.Fatalf("wrong answer: %v\n%s", err, out)
	}
	if _, err := purgeOut(t, "", "old", "--count", "3"); err == nil || !strings.Contains(err.Error(), "--count 3 does not match the 1 message(s)") {
		t.Fatalf("wrong --count: %v", err)
	}
	if p, _ := s.st.OpenAccountPurge(ctx); p != nil {
		t.Fatalf("a refused purge left %+v", p)
	}
	// Two users have an account "mail"; and it is not removed.
	if _, err := purgeOut(t, "", "mail", "--dry-run"); err == nil || !strings.Contains(err.Error(), "--user") {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := purgeOut(t, "", "mail", "--user", "bob", "--dry-run"); err == nil ||
		!strings.Contains(err.Error(), `account "mail" is not removed; remove it first`) ||
		!strings.Contains(err.Error(), "account remove mail --user bob") {
		t.Errorf("not removed: %v", err)
	}
	// A rule and an active hold are named.
	rule, err := s.st.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: acc.ID, Folder: "INBOX", Years: 6})
	if err != nil {
		t.Fatal(err)
	}
	hold, err := s.st.AddLegalHold(ctx, store.NewLegalHold{Name: "Case A", OwnerID: acc.OwnerID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = purgeOut(t, "1\n", "old")
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf(`account "old" has retention rule #%d (folder "INBOX", 6 years)`, rule)) ||
		!strings.Contains(err.Error(), "retention rule remove ID") ||
		!strings.Contains(err.Error(), fmt.Sprintf(`legal hold #%d "Case A" covers mail of account "old"`, hold)) {
		t.Fatalf("blocked: %v", err)
	}
	if err := s.st.RemoveRetentionRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	if err := s.st.ReleaseLegalHold(ctx, hold, nil, "closed"); err != nil {
		t.Fatal(err)
	}

	n := len(auditLog(t, s.st))
	out, err = purgeOut(t, "1\n", "old")
	if err != nil || !strings.Contains(out, `account "old" deleted completely: 2 location(s) removed, 1 message(s) deleted, 1 kept in other accounts`) {
		t.Fatalf("purge: %v\n%s", err, out)
	}
	var actions []string
	for _, e := range auditLog(t, s.st)[n:] {
		actions = append(actions, e.Action)
		var d map[string]any
		if err := json.Unmarshal(e.Details, &d); err != nil {
			t.Fatal(err)
		}
		if e.Actor != store.ActorCLI || e.TargetType+":"+e.TargetName != "account:old" || d["operator"] != "patrick" {
			t.Errorf("%s: %+v %v", e.Action, e, d)
		}
		switch e.Action {
		case "account.purge":
			if d["deleted"] != float64(1) || d["kept"] != float64(1) || d["locations"] != float64(2) || d["owner"] != "anna" || d["purge_id"] == nil {
				t.Errorf("confirm entry %v", d)
			}
		case "account.purge_finish":
			if d["deleted"] != float64(1) || d["kept"] != float64(1) || d["locations"] != float64(2) || d["kind"] != "imap" {
				t.Errorf("finish entry %v", d)
			}
		}
	}
	if strings.Join(actions, ",") != "account.purge,account.purge_batch,account.purge_finish" {
		t.Errorf("audit %v", actions)
	}
	if _, err := s.st.GetAccount(ctx, acc.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("account still there: %v", err)
	}
	list := captureStdout(t, func() { _ = runCmd(t, newAccountCmd(), "", "list") })
	if strings.Contains(list, "old") {
		t.Errorf("list:\n%s", list)
	}
	code, vout := runVerifyCmd(t)
	if code != 0 || !strings.Contains(vout, "1 file(s) of deleted messages (retention or account purge) wait for their removal") {
		t.Errorf("verify %d:\n%s", code, vout)
	}
}

// A purge confirmed elsewhere: the command resumes it, says when another
// process executes it, and cancels it.
func TestAccountPurgeResumeAndCancel(t *testing.T) {
	s, acc := newPurgeCLISetup(t)
	ctx := context.Background()
	p, err := s.st.ConfirmPurge(ctx, acc.Ref(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	list := captureStdout(t, func() { _ = runCmd(t, newAccountCmd(), "", "list") })
	if !regexpMatch(`old\s+anna\s.*removed \(being deleted\)`, list) {
		t.Errorf("list:\n%s", list)
	}
	status := captureStdout(t, func() { _ = runCmd(t, newStatusCmd(), "") })
	if !strings.Contains(status, fmt.Sprintf(`purge %d of account "old" open`, p.ID)) {
		t.Errorf("status:\n%s", status)
	}
	if out, err := purgeOut(t, "", "old", "--dry-run"); err != nil || !strings.Contains(out, fmt.Sprintf("purge %d of account \"old\" is confirmed", p.ID)) {
		t.Errorf("dry run of an open purge: %v\n%s", err, out)
	}
	// The web server's job executes it (plan review finding 6).
	unlock, ok, err := s.st.TryLockPurge(ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	out, err := purgeOut(t, "", "old")
	if err != nil || !strings.Contains(out, "is being executed by another process") || !strings.Contains(out, "account list") {
		t.Errorf("while another process executes it: %v\n%s", err, out)
	}
	unlock()

	n := len(auditLog(t, s.st))
	out, err = purgeOut(t, "", "old", "--cancel")
	if err != nil || !strings.Contains(out, fmt.Sprintf(`purge %d of account "old" cancelled`, p.ID)) {
		t.Fatalf("cancel: %v\n%s", err, out)
	}
	if got := auditLog(t, s.st)[n:]; len(got) != 1 || got[0].Action != "account.purge_cancel" || got[0].TargetName != "old" {
		t.Errorf("cancel audit %+v", got)
	}
	if _, err := purgeOut(t, "", "old", "--cancel"); err == nil || !strings.Contains(err.Error(), `account "old" has no open purge`) {
		t.Errorf("cancel again: %v", err)
	}

	// Confirmed again, then resumed by the command without a prompt.
	if _, err := s.st.ConfirmPurge(ctx, acc.Ref(), 1, nil); err != nil {
		t.Fatal(err)
	}
	out, err = purgeOut(t, "", "old")
	if err != nil || !strings.Contains(out, "resuming purge") || !strings.Contains(out, `account "old" deleted completely`) {
		t.Fatalf("resume: %v\n%s", err, out)
	}
}

// A released legal hold naming the account keeps it (maintainer decision
// on #155).
func TestAccountPurgeReleasedHold(t *testing.T) {
	s, acc := newPurgeCLISetup(t)
	ctx := context.Background()
	hold, err := s.st.AddLegalHold(ctx, store.NewLegalHold{Name: "Case B", AccountID: &acc.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.ReleaseLegalHold(ctx, hold, nil, "closed"); err != nil {
		t.Fatal(err)
	}
	_, err = purgeOut(t, "", "old", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf(`released legal hold #%d "Case B" names account "old"; its record keeps the account`, hold)) {
		t.Fatalf("released hold: %v", err)
	}
}
