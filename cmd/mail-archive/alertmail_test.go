package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/keepia/archive/internal/alertmail"
	"github.com/keepia/archive/internal/alertmail/mailtest"
	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/crypto"
	"github.com/keepia/archive/internal/imaptest"
	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/store/storetest"
)

// useRelay points the alert mail settings at relay: the URL, a password
// file outside the data directory and the sender. Mail goes to the relay
// (its address and certificate) through newMailSender.
func useRelay(t *testing.T, relay *mailtest.Server) {
	t.Helper()
	pw := filepath.Join(t.TempDir(), "mail.password")
	if err := os.WriteFile(pw, []byte(mailtest.Password+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvMailURL, "smtp://"+mailtest.User+"@"+mailtest.Host+":"+strconv.Itoa(relay.Port()))
	t.Setenv(config.EnvMailPasswordFile, pw)
	t.Setenv(config.EnvMailFrom, "Mail Archive <archive@relay.test>")
	old := newMailSender
	newMailSender = func(cfg *config.Config) alertmail.Mailer {
		s := relay.Sender()
		s.Cfg = cfg.Mail
		return s
	}
	t.Cleanup(func() { newMailSender = old })
}

func runMailTest(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := newNotifyCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := runCmd(t, cmd, "", append([]string{"mail-test"}, args...)...)
	if err != nil {
		out.WriteString(err.Error())
	}
	return exitCodeOf(err), out.String()
}

// TestNotifyMailTestCommand: `notify mail-test --to` sends one mail
// through the configured relay and prints the reply code; a refusal
// prints its code without the relay's text or the password.
func TestNotifyMailTestCommand(t *testing.T) {
	t.Setenv(config.EnvDatabaseURL, "postgres://unused")
	t.Setenv(config.EnvDataDir, t.TempDir())
	t.Setenv(config.EnvMailURL, "")
	if code, out := runMailTest(t, "--to", "admin@example.com"); code == 0 || !strings.Contains(out, "is not set") {
		t.Fatalf("without a URL: %d %s", code, out)
	}
	relay := mailtest.Start(t, mailtest.StartTLS)
	useRelay(t, relay)
	if code, out := runMailTest(t, "--to", "two@example.com, three@example.com"); code == 0 || !strings.Contains(out, "one mail address") {
		t.Fatalf("two addresses: %d %s", code, out)
	}
	code, out := runMailTest(t, "--to", "admin@example.com")
	if code != 0 || !strings.Contains(out, "SMTP 250 2.0.0, test mail sent") || len(relay.Mails()) != 1 || relay.Mails()[0].To[0] != "admin@example.com" {
		t.Fatalf("ok: %d %s %d", code, out, len(relay.Mails()))
	}
	relay.On("rcpt", func(string) error { return mailtest.Error(550, 5, 1, 1, "zqreplyzq unknown") })
	code, out = runMailTest(t, "--to", "admin@example.com")
	if code == 0 || !strings.Contains(out, "550 5.1.1") || strings.Contains(out, "zqreplyzq") || strings.Contains(out, mailtest.Password) {
		t.Fatalf("refused: %d %s", code, out)
	}
}

// TestSyncSendsAlertMail: a cron setup without serve and without a
// webhook gets the storage alert of `./ma sync` by mail at its end.
func TestSyncSendsAlertMail(t *testing.T) {
	user := imapmemserver.NewUser("alice", "pw")
	imaptest.CreateMailboxes(t, user, "INBOX")
	for i := range 3 {
		imaptest.Append(t, user, "INBOX", []byte(fmt.Sprintf("Subject: s%d\r\n\r\nbody %d\r\n", i, i)))
	}
	host, port := imaptest.Start(t, user)
	st, url := storetest.NewWithURL(t)
	t.Setenv(config.EnvDatabaseURL, url)
	t.Setenv(config.EnvDataDir, t.TempDir())
	t.Setenv(config.EnvLogLevel, "error")
	t.Setenv(config.EnvStorageQuota, "1")
	t.Setenv(config.EnvMinFreeDisk, "0")
	t.Setenv(config.EnvNotifyWebhookURL, "")
	keyStr, _ := crypto.GenerateKey()
	t.Setenv(config.EnvSecretKey, keyStr)
	key, _ := crypto.ParseKey(keyStr)
	sealer, _ := crypto.NewSealer(key)
	ctx := context.Background()
	admin, err := st.CreateUser(ctx, "boss", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.SetNotifyEmail(ctx, admin.ID, store.NotifyCodeRequest{Email: "boss@example.com", CodeHash: store.NotifyCodeHash(admin.ID, "ABCDEF"), Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmNotifyEmail(ctx, admin.ID, store.NotifyCodeHash(admin.ID, "ABCDEF"), now); err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "work", Host: host, Port: port, TLSMode: store.TLSModeNone, Username: "alice", Enabled: true, OwnerID: &admin.ID}
	seal := func(id int64) ([]byte, error) { return archive.SealPassword(sealer, id, "pw") }
	if err := st.CreateAccountSealed(ctx, acc, seal); err != nil {
		t.Fatal(err)
	}
	relay := mailtest.Start(t, mailtest.StartTLS)
	useRelay(t, relay)

	captureStdout(t, func() { err = runCmd(t, newSyncCmd(), "") })
	if exitCodeOf(err) != exitPaused {
		t.Fatalf("sync: %v", err)
	}
	got := relay.Mails()
	if len(got) != 1 || got[0].To[0] != "boss@example.com" || !strings.Contains(string(got[0].Data), "Mail archive: storage ") {
		t.Fatalf("mails: %d %+v", len(got), got)
	}
}
