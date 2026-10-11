package main

import (
	"context"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestSMTPAccountCommands(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", t.TempDir())
	ctx := context.Background()
	account := func(args ...string) error { return runCmd(t, newAccountCmd(), "", args...) }

	out := captureStdout(t, func() {
		if err := account("add-smtp", "journal", "--address", "Archive@Archive.test"); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, `smtp account "journal" added`) || !strings.Contains(out, "MAIL_ARCHIVE_SMTP_LISTEN") {
		t.Errorf("add-smtp output:\n%s", out)
	}
	accs, err := st.ListAccountsByName(ctx, "journal")
	if err != nil || len(accs) != 1 || accs[0].Kind != store.KindSMTP || !accs[0].Enabled {
		t.Fatalf("created: %+v, %v", accs, err)
	}
	if err := account("add-smtp", "second", "--address", "archive@archive.test"); err == nil || !strings.Contains(err.Error(), "another smtp account") {
		t.Errorf("address in use: %v", err)
	}
	if err := account("add-smtp", "journal", "--address", "x@archive.test"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("name in use: %v", err)
	}
	if err := account("add-smtp", "bad", "--address", "a+b@archive.test"); err == nil {
		t.Error("address with extension accepted")
	}

	out = captureStdout(t, func() {
		if err := account("smtp-addresses", "journal", "--add", "journal@archive.test"); err != nil {
			t.Error(err)
		}
	})
	if out != "archive@archive.test\njournal@archive.test\n" {
		t.Errorf("addresses:\n%s", out)
	}

	// Enable and disable work; server, folders and sync do not.
	if err := account("disable", "journal"); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.GetAccount(ctx, accs[0].ID); a.Enabled {
		t.Error("still enabled")
	}
	if err := account("enable", "journal"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"set-folders", "journal", "--exclude", "x"}, {"folders", "journal"}} {
		if err := account(args...); err == nil || !strings.Contains(err.Error(), "smtp account") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if err := runCmd(t, newSyncCmd(), "", "--account", "journal"); err == nil || !strings.Contains(err.Error(), "smtp account") {
		t.Errorf("sync --account: %v", err)
	}

	out = captureStdout(t, func() {
		if err := account("list"); err != nil {
			t.Error(err)
		}
	})
	if !regexpMatch(`journal\s+-\s+smtp\s+archive@archive.test, journal@archive.test\s+enabled`, out) {
		t.Errorf("list:\n%s", out)
	}
	out = captureStdout(t, func() {
		if err := runCmd(t, newStatusCmd(), ""); err != nil {
			t.Error(err)
		}
	})
	if !regexpMatch(`journal\s+complete \(SMTP\)\s+-\s+true\s+1\s+0\s+-\s+never\s+-`, out) {
		t.Errorf("status:\n%s", out)
	}
}
