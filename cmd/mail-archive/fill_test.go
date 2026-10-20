package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestParseCutoff(t *testing.T) {
	now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in   string
		want time.Time
	}{
		{"", now},
		{"2026-06-01", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
		{"2026-06-01T12:00:00+02:00", time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)},
	} {
		got, err := parseCutoff(c.in, now)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("%q: %v, %v", c.in, got, err)
		}
	}
	if _, err := parseCutoff("June 1", now); err == nil {
		t.Error("accepted June 1")
	}
}

func TestAccountFillCommand(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", t.TempDir())
	t.Setenv("MAIL_ARCHIVE_LOG_LEVEL", "error")
	ctx := context.Background()

	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Spam")
	at := time.Now().Add(-48 * time.Hour)
	for i := range 3 {
		imaptest.AppendAt(t, u, "INBOX", fmt.Appendf(nil, "Message-ID: <m%d@example.com>\r\nSubject: m%d\r\n\r\nbody\r\n", i, i), at)
	}
	imaptest.AppendAt(t, u, "Spam", []byte("Subject: spam\r\n\r\nbuy\r\n"), at)
	host, port := imaptest.StartWithRoles(t, map[string]imap.MailboxAttr{"INBOX": "", "Spam": imap.MailboxAttrJunk}, u)

	account := func(stdin string, args ...string) error { return runCmd(t, newAccountCmd(), stdin, args...) }
	if err := account("", "add-smtp", "journal", "--address", "archive@archive.test"); err != nil {
		t.Fatal(err)
	}
	if err := account("pw\n", "add", "mail", "--host", "h", "--username", "u", "--skip-check", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	source := []string{"--host", host, "--port", strconv.Itoa(port), "--tls", "none", "--username", "alice@example.com", "--password-stdin"}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"fill", "journal", "--resume", "--cancel"}, "exclude each other"},
		{[]string{"fill", "journal", "--resume", "--host", "h"}, "no other settings"},
		{[]string{"fill", "journal", "--host", "h"}, "--host and --username"},
		{append([]string{"fill", "mail"}, source...), "not an smtp account"},
		{append([]string{"fill", "journal", "--before", "June"}, source...), "--before"},
		{append([]string{"fill", "journal", "--prefix", "alice"}, source...), "--prefix is no longer supported: fills write into JOURNAL"},
		{append([]string{"fill", "journal"}, source...), "Spam"},
		{[]string{"fill", "journal", "--cancel"}, "no fill"},
		{[]string{"fill", "journal", "--resume"}, "no fill to resume"},
	} {
		if err := account("secret\n", c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
	if err := account("wrong\n", append([]string{"fill", "journal", "--exclude", "Spam"}, source...)...); err == nil || !strings.Contains(err.Error(), "login") {
		t.Errorf("wrong password: %v", err)
	}

	out := captureStdout(t, func() {
		if err := account("secret\n", append([]string{"fill", "journal", "--exclude", "Spam"}, source...)...); err != nil {
			t.Error(err)
		}
	})
	if !regexpMatch(`fill \d+: ok, alice@example.com@\S+ into JOURNAL,`, out) || !regexpMatch(`INBOX\s+3\s+3\s+3\s+0\s+0\s+0\s+0`, out) {
		t.Errorf("fill output:\n%s", out)
	}
	accs, _ := st.ListAccountsByName(ctx, "journal")
	fills, err := st.AccountFills(ctx, accs[0].ID)
	if err != nil || len(fills) != 1 || fills[0].Status != store.FillOK || fills[0].FolderPrefix != "" || fills[0].Folders[0].FolderName != "JOURNAL" || fills[0].PasswordEnc != nil {
		t.Fatalf("fills %+v, %v", fills, err)
	}
	if err := account("", "fill", "journal", "--resume"); err == nil || !strings.Contains(err.Error(), "ended well") {
		t.Errorf("resume an ok fill: %v", err)
	}

	// The audit log has the start and the result.
	var actions []string
	var started, done map[string]any
	f := store.AuditFilter{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour)}
	_ = st.ExportAudit(ctx, f, func(r store.AuditRecord) error {
		actions = append(actions, r.Action)
		switch r.Action {
		case "data.fill":
			started = jsonMap(t, r.Details)
		case "data.fill_done":
			done = jsonMap(t, r.Details)
		}
		return nil
	})
	if started["host"] != host || started["username"] != "alice@example.com" || started["prefix"] != nil {
		t.Errorf("data.fill %v (actions %v)", started, actions)
	}
	if done["status"] != "ok" || done["imported"] != float64(3) {
		t.Errorf("data.fill_done %v", done)
	}

	out = captureStdout(t, func() {
		if err := account("", "fill", "journal"); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "fill 1: ok") {
		t.Errorf("list:\n%s", out)
	}
}

func jsonMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAccountFillRecurring(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", t.TempDir())
	t.Setenv("MAIL_ARCHIVE_LOG_LEVEL", "error")
	t.Setenv("MAIL_ARCHIVE_SYNC_INTERVAL_MIN", "1h")
	ctx := context.Background()

	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Spam")
	at := time.Now().Add(-48 * time.Hour)
	for i := range 3 {
		imaptest.AppendAt(t, u, "INBOX", fmt.Appendf(nil, "Message-ID: <m%d@example.com>\r\nSubject: m%d\r\n\r\nbody\r\n", i, i), at)
	}
	imaptest.AppendAt(t, u, "Spam", []byte("Subject: spam\r\n\r\nbuy\r\n"), at)
	host, port := imaptest.StartWithRoles(t, map[string]imap.MailboxAttr{"INBOX": "", "Spam": imap.MailboxAttrJunk}, u)
	var stderr strings.Builder
	account := func(stdin string, args ...string) error {
		cmd := newAccountCmd()
		cmd.SetErr(&stderr)
		return runCmd(t, cmd, stdin, args...)
	}
	if err := account("", "add-smtp", "journal", "--address", "archive@archive.test"); err != nil {
		t.Fatal(err)
	}
	source := []string{"--host", host, "--port", strconv.Itoa(port), "--tls", "none", "--username", "alice@example.com", "--password-stdin", "--exclude", "Spam"}

	for _, c := range []struct {
		args []string
		want string
	}{
		{append([]string{"fill", "journal", "--keep-running", "--before", "2026-06-01"}, source...), "--before and --keep-running exclude each other"},
		{append([]string{"fill", "journal", "--interval", "12h"}, source...), "--interval needs --keep-running"},
		{append([]string{"fill", "journal", "--fill", "1"}, source...), "a new fill takes no --fill"},
		{append([]string{"fill", "journal", "--keep-running", "--interval", "manual"}, source...), "--interval"},
		{append([]string{"fill", "journal", "--keep-running", "--interval", "30m"}, source...), "below the server's minimum"},
		{[]string{"fill", "journal", "--resume", "--keep-running"}, "exclude each other"},
		{[]string{"fill", "journal", "--cancel", "--interval", "6h"}, "no other settings"},
		{[]string{"fill", "journal", "--interval", "6h"}, "no fill of \"journal\" keeps running"},
		{[]string{"fill", "journal", "--keep-running"}, "has no fill to switch on"},
	} {
		if err := account("secret\n", c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}

	// A recurring fill: its first run here, then the web server's.
	out := captureStdout(t, func() {
		if err := account("secret\n", append([]string{"fill", "journal", "--keep-running", "--interval", "12h"}, source...)...); err != nil {
			t.Error(err)
		}
	})
	if !regexpMatch(`fill 1: ok, alice@example.com@\S+ into JOURNAL, two-hour rule,`, out) || !strings.Contains(out, "keeps running every 12h") ||
		!strings.Contains(stderr.String(), "the web server runs it every 12h") {
		t.Errorf("recurring fill output:\n%s\n%s", out, stderr.String())
	}
	accs, _ := st.ListAccountsByName(ctx, "journal")
	acc := accs[0]
	fills, err := st.AccountFills(ctx, acc.ID)
	if err != nil || len(fills) != 1 || !fills[0].KeepRunning || fills[0].Cutoff != nil || fills[0].PasswordEnc == nil ||
		*fills[0].Interval != 12*time.Hour {
		t.Fatalf("fills %+v, %v", fills, err)
	}
	out = captureStdout(t, func() {
		if err := account("", "fill", "journal", "--interval", "24h"); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "fill 1: keeps running every 24h") {
		t.Errorf("interval output %q", out)
	}

	// A second, one-time fill, switched on: two recurring fills now.
	if err := captureErr(t, func() error { return account("secret\n", append([]string{"fill", "journal"}, source...)...) }); err != nil {
		t.Fatal(err)
	}
	if err := captureErr(t, func() error { return account("secret\n", "fill", "journal", "--keep-running", "--password-stdin") }); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--cancel"}, {"--interval", "6h"}} {
		if err := account("", append([]string{"fill", "journal"}, args...)...); err == nil || !strings.Contains(err.Error(), "(2, 1); choose one with --fill") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if err := account("", "fill", "journal", "--cancel", "--fill", "99"); err == nil || !strings.Contains(err.Error(), "has no fill 99") {
		t.Errorf("--fill 99: %v", err)
	}
	out = captureStdout(t, func() {
		if err := account("", "fill", "journal", "--cancel", "--fill", "2"); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "fill 2 stopped") {
		t.Errorf("stop output %q", out)
	}
	if f, _ := st.GetFill(ctx, 2); f.KeepRunning || f.PasswordEnc != nil || f.Status != store.FillCancelled {
		t.Errorf("stopped fill %+v", f)
	}
	if err := captureErr(t, func() error {
		return account("secret\n", "fill", "journal", "--resume", "--fill", "2", "--password-stdin")
	}); err != nil {
		t.Fatal(err)
	}
	if f, _ := st.GetFill(ctx, 2); !f.KeepRunning || f.Status != store.FillOK {
		t.Errorf("resumed fill %+v", f)
	}
	out = captureStdout(t, func() {
		if err := account("", "fill", "journal"); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "keeps running every 24h") || !strings.Contains(out, "health ok") {
		t.Errorf("list:\n%s", out)
	}

	// Moving the account warns: the fills go on importing the same mailboxes.
	// (The first user took over the account.)
	for _, name := range []string{"anna", "bob"} {
		if _, err := st.CreateUser(ctx, name, "h", false); err != nil {
			t.Fatal(err)
		}
	}
	stderr.Reset()
	if err := captureErr(t, func() error { return account("", "move", "journal", "--to", "bob") }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning: fill 1 keeps importing alice@example.com into \"journal\", now for \"bob\"") {
		t.Errorf("move output %q", stderr.String())
	}

	// The audit log has the changes, without a password, and no run of a
	// recurring fill.
	var entries []map[string]any
	var actions []string
	f := store.AuditFilter{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour)}
	_ = st.ExportAudit(ctx, f, func(r store.AuditRecord) error {
		if strings.HasPrefix(r.Action, "data.fill") {
			actions = append(actions, r.Action)
			entries = append(entries, jsonMap(t, r.Details))
		}
		if strings.Contains(string(r.Details), "secret") {
			t.Errorf("%s names the password: %s", r.Action, r.Details)
		}
		return nil
	})
	want := []string{"data.fill", "data.fill_change", "data.fill", "data.fill_done", "data.fill_change", "data.fill_cancel", "data.fill_resume"}
	if strings.Join(actions, " ") != strings.Join(want, " ") {
		t.Fatalf("audit %v, want %v", actions, want)
	}
	if d := entries[0]; d["keep_running"] != true || d["interval"] != "12h0m0s" || d["before"] != nil {
		t.Errorf("data.fill %v", d)
	}
	if d := entries[1]; fmt.Sprint(d["interval"]) != "map[from:12h0m0s to:24h0m0s]" || d["fill"] != float64(1) {
		t.Errorf("interval change %v", d)
	}
	if d := entries[4]; fmt.Sprint(d["keep_running"]) != "map[from:false to:true]" || fmt.Sprint(d["interval"]) != "map[from:<nil> to:default]" || d["fill"] != float64(2) {
		t.Errorf("switch on %v", d)
	}
}
