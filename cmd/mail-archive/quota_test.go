package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// group set-quota: a size or none, refused above the instance quota and
// for a bad size, audited as group.quota targeted at the group with the
// old and new value; group list shows QUOTA, USED and STATE.
func TestGroupSetQuota(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	t.Setenv("MAIL_ARCHIVE_STORAGE_QUOTA", "100GiB")
	ctx := context.Background()
	group := func(args ...string) (string, error) {
		t.Helper()
		cmd := newGroupCmd()
		var b bytes.Buffer
		cmd.SetOut(&b)
		err := runCmd(t, cmd, "", args...)
		return b.String(), err
	}
	admin, err := st.CreateUser(ctx, "root", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := group("add", "Meier"); err != nil {
		t.Fatal(err)
	}
	meier, err := st.GetGroupByName(ctx, "Meier")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := group("set-quota", "Meier", "101GiB"); err == nil || !strings.Contains(err.Error(), "above the instance quota") {
		t.Fatalf("above the instance quota: %v", err)
	}
	for _, bad := range []string{"0", "lots", "1.5T"} {
		if _, err := group("set-quota", "Meier", bad); err == nil || !strings.Contains(err.Error(), "invalid size") {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if _, err := group("set-quota", "nosuch", "1G"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown group: %v", err)
	}
	out, err := group("set-quota", "meier", "50GiB")
	if err != nil || !strings.Contains(out, `group "Meier": quota 50.0 GiB (was none)`) {
		t.Fatalf("set: %q, %v", out, err)
	}
	if out, err := group("list"); err != nil || !regexpMatch(`(?m)^NAME\s+USERS\s+ACCOUNTS\s+QUOTA\s+USED\s+STATE\s+CREATED$`, out) ||
		!regexpMatch(`(?m)^Meier\s+0\s+0\s+50.0 GiB\s+-\s+ok\s`, out) || !regexpMatch(`(?m)^Default\s+1\s+0\s+-\s+-\s+ok\s`, out) {
		t.Fatalf("list: %q, %v", out, err)
	}
	if out, err := group("set-quota", "Meier", "none"); err != nil || !strings.Contains(out, "quota none (was 50.0 GiB)") {
		t.Fatalf("none: %q, %v", out, err)
	}
	if out, err := group("set-quota", "Meier", "NONE"); err != nil || !strings.Contains(out, "already has the quota none") {
		t.Fatalf("none again: %q, %v", out, err)
	}
	recs, err := st.ListAudit(ctx, store.AuditPage{Viewer: admin.ID, All: true, Actions: []string{"group.quota"},
		Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("%d group.quota entries, want 3", len(recs))
	}
	seen := map[string]bool{}
	for _, r := range recs {
		var d map[string]any
		if err := json.Unmarshal(r.Details, &d); err != nil {
			t.Fatal(err)
		}
		if r.GroupID == nil || *r.GroupID != meier.ID || d["group"] != "Meier" {
			t.Fatalf("entry %+v %s", r, r.Details)
		}
		from, hasFrom := d["from"]
		to, hasTo := d["to"]
		if !hasFrom || !hasTo {
			t.Fatalf("entry details %s", r.Details)
		}
		seen[fmt.Sprint(from, "->", to)] = true
	}
	want := fmt.Sprint(nil, "->", float64(50<<30))
	back := fmt.Sprint(float64(50<<30), "->", nil)
	if !seen[want] || !seen[back] || !seen[fmt.Sprint(nil, "->", nil)] {
		t.Fatalf("entries %v", seen)
	}
}

// A paused import exits with 75 and says which limit was reached and how
// much it stored; the next run is refused at the start, also with 75.
func TestImportPausedExit(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", t.TempDir())
	t.Setenv("MAIL_ARCHIVE_LOG_LEVEL", "error")
	t.Setenv("MAIL_ARCHIVE_STORAGE_QUOTA", "1")
	t.Setenv("MAIL_ARCHIVE_MIN_FREE_DISK", "0")
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	if _, err := st.CreateUser(context.Background(), "anna", "h", false); err != nil {
		t.Fatal(err)
	}
	run := func() error {
		cmd := newImportCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		return runCmd(t, cmd, "", "old", "--from", tbFixture, "--format", "mbox")
	}
	err := run()
	var ex *exitError
	if !errors.As(err, &ex) || ex.exitCode() != exitPaused ||
		!regexpMatch(`^storage quota of the instance reached \(MAIL_ARCHIVE_STORAGE_QUOTA\) after [1-9][0-9]* messages; run the same command again`, ex.msg) {
		t.Fatalf("paused import: %v", err)
	}
	err = run()
	if !errors.As(err, &ex) || ex.exitCode() != exitPaused || !strings.Contains(ex.msg, "; run it once space is freed") {
		t.Fatalf("import while paused: %v", err)
	}
}

// A sync that the gate pauses (within one message of the limit) prints
// "paused" with the limit and exits
// with 75; the next run is refused at the start, also with 75, and
// fetches nothing.
func TestSyncPausedExit(t *testing.T) {
	user := imapmemserver.NewUser("alice", "pw")
	imaptest.CreateMailboxes(t, user, "INBOX")
	for i := range 250 { // more than two batches of 100
		imaptest.Append(t, user, "INBOX", []byte(fmt.Sprintf("Subject: m%d\r\n\r\nbody %d\r\n", i, i)))
	}
	host, port := imaptest.Start(t, user)
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", t.TempDir())
	t.Setenv("MAIL_ARCHIVE_LOG_LEVEL", "error")
	t.Setenv("MAIL_ARCHIVE_STORAGE_QUOTA", "1")
	t.Setenv("MAIL_ARCHIVE_MIN_FREE_DISK", "0")
	keyStr, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", keyStr)
	key, _ := crypto.ParseKey(keyStr)
	sealer, _ := crypto.NewSealer(key)
	acc := &store.Account{Name: "work", Host: host, Port: port, TLSMode: store.TLSModeNone, Username: "alice", Enabled: true}
	seal := func(id int64) ([]byte, error) { return archive.SealPassword(sealer, id, "pw") }
	if err := st.CreateAccountSealed(context.Background(), acc, seal); err != nil {
		t.Fatal(err)
	}
	var err error
	out := captureStdout(t, func() { err = runCmd(t, newSyncCmd(), "") })
	if exitCodeOf(err) != exitPaused || !regexpMatch(`(?m)^work\s+fetched=1\s+new=1\s+paused: storage quota of the instance reached`, out) {
		t.Fatalf("paused sync: %v\n%s", err, out)
	}
	out = captureStdout(t, func() { err = runCmd(t, newSyncCmd(), "") })
	if exitCodeOf(err) != exitPaused || !regexpMatch(`(?m)^work\s+fetched=0\s+new=0\s+paused:`, out) {
		t.Fatalf("sync while paused: %v\n%s", err, out)
	}
}
