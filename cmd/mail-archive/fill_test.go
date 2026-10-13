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

	"github.com/pklnx/mail-archive/internal/archive"
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
	if p := archive.DefaultFillPrefix(" /alice@example.com/\n"); p != "alice@example.com" {
		t.Errorf("prefix %q", p)
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
		{append([]string{"fill", "journal", "--prefix", "INBOX"}, source...), "--prefix"},
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
	if !regexpMatch(`fill \d+: ok, alice@example.com@`, out) || !regexpMatch(`INBOX\s+3\s+3\s+3\s+0\s+0\s+0\s+0`, out) {
		t.Errorf("fill output:\n%s", out)
	}
	accs, _ := st.ListAccountsByName(ctx, "journal")
	fills, err := st.AccountFills(ctx, accs[0].ID)
	if err != nil || len(fills) != 1 || fills[0].Status != store.FillOK || fills[0].FolderPrefix != "alice@example.com" || fills[0].PasswordEnc != nil {
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
	if started["host"] != host || started["username"] != "alice@example.com" || started["prefix"] != "alice@example.com" {
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
