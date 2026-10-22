package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

const tbFixture = "../../internal/archive/testdata/import/thunderbird"

// captureStdout runs fn with os.Stdout redirected.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		done <- b.String()
	}()
	fn()
	os.Stdout = old
	_ = w.Close()
	return <-done
}

func TestImportCommand(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", t.TempDir())
	t.Setenv("MAIL_ARCHIVE_LOG_LEVEL", "error")
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	ctx := context.Background()
	anna, err := st.CreateUser(ctx, "anna", "h", false)
	if err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := newImportCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := runCmd(t, cmd, "", args...)
		return out.String(), err
	}

	// Argument errors.
	for _, args := range [][]string{
		{"old", "--from", tbFixture},
		{"old", "--format", "mbox"},
		{"old", "--from", tbFixture, "--format", "pst"},
		{"a/b", "--from", tbFixture, "--format", "mbox"},
		{"old", "--from", tbFixture, "--format", "mbox", "--max-message-size", "lots"},
		{"old", "--from", tbFixture, "--format", "maildir"},
		{"old", "--from", tbFixture, "--format", "mbox", "--folder", "bad\x01name"},
	} {
		if _, err := run(args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	// The hint for Docker Compose.
	t.Setenv("MAIL_ARCHIVE_COMMAND", "./ma")
	if _, err := run("old", "--from", "./import/missing.mbox", "--format", "mbox"); err == nil || !strings.Contains(err.Error(), "--from /import/") {
		t.Errorf("missing file under ./ma: %v", err)
	}
	t.Setenv("MAIL_ARCHIVE_COMMAND", "")

	out, err := run("old", "--from", tbFixture, "--format", "mbox", "--dry-run")
	if err != nil || !strings.Contains(out, "4 message(s) in 2 folder(s); nothing was stored") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if accs, _ := st.ListAccounts(ctx); len(accs) != 0 {
		t.Fatalf("dry run created %+v", accs)
	}

	out, err = run("old", "--from", tbFixture, "--format", "mbox")
	if err != nil || !strings.Contains(out, "ok: read 4, added 4 (4 new to the archive)") {
		t.Fatalf("import: %v\n%s", err, out)
	}
	acc, err := st.GetOwnedAccount(ctx, anna.ID, "old")
	if err != nil || acc.Kind != store.KindImport {
		t.Fatalf("account %+v %v", acc, err)
	}
	out, err = run("old", "--from", tbFixture, "--format", "mbox")
	if err != nil || !strings.Contains(out, "added 0 (0 new to the archive), 4 already there") {
		t.Fatalf("again: %v\n%s", err, out)
	}
	if _, err := run("old", "--from", tbFixture, "--format", "mbox", "--max-message-size", "200"); err == nil {
		t.Error("partial import exits 0")
	}

	// IMAP accounts take no import; account commands refuse import accounts.
	imap := &store.Account{Name: "server", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", OwnerID: &anna.ID} // no password: nothing to decrypt
	if err := st.CreateAccount(ctx, imap); err != nil {
		t.Fatal(err)
	}
	if _, err := run("server", "--from", tbFixture, "--format", "mbox"); err == nil || !strings.Contains(err.Error(), "IMAP account") {
		t.Errorf("import into IMAP account: %v", err)
	}
	for _, args := range [][]string{{"enable", "old"}, {"disable", "old"}, {"set-folders", "old"}, {"folders", "old"}, {"set-password", "old", "--password-stdin"}} {
		if err := runCmd(t, newAccountCmd(), "pw\n", args...); err == nil || !strings.Contains(err.Error(), "import account") {
			t.Errorf("account %v: %v", args, err)
		}
	}
	if err := runCmd(t, newSyncCmd(), "", "--account", "old"); err == nil || !strings.Contains(err.Error(), "run import again") {
		t.Errorf("sync --account: %v", err)
	}
	list := captureStdout(t, func() {
		if err := runCmd(t, newAccountCmd(), "", "list"); err != nil {
			t.Error(err)
		}
	})
	if !regexpMatch(`old\s+anna\s+import\s+-\s+import`, list) {
		t.Errorf("account list:\n%s", list)
	}
	status := captureStdout(t, func() {
		if err := runCmd(t, newStatusCmd(), ""); err != nil {
			t.Error(err)
		}
	})
	if !regexpMatch(`old\s+import\s+anna\s+import\s+2\s+4\s`, status) {
		t.Errorf("status:\n%s", status)
	}
	if err := runCmd(t, newAccountCmd(), "", "rename", "old", "older"); err != nil {
		t.Errorf("rename: %v", err)
	}
	if err := runCmd(t, newAccountCmd(), "", "remove", "older"); err != nil {
		t.Errorf("remove: %v", err)
	}
	if _, err := run("older", "--from", tbFixture, "--format", "mbox"); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Errorf("import into removed account: %v", err)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"256MiB": 256 << 20, "100MB": 100e6, "10M": 10 << 20, "1048576": 1 << 20, "2 GiB": 2 << 30} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0", "-1", "lots", "5TB", "99999GiB"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func regexpMatch(re, s string) bool { return regexp.MustCompile(re).MatchString(s) }

func TestImportEMLCommand(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", t.TempDir())
	t.Setenv("MAIL_ARCHIVE_LOG_LEVEL", "error")
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, "anna", "h", false); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, content := range map[string]string{
		"1.eml": "Subject: one\n\n1\n", "2.eml": "Subject: two\n\n2\n", "empty.eml": "", "export.zip": "PK\x03\x04",
	} {
		if err := os.WriteFile(dir+"/"+name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := newImportCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := runCmd(t, cmd, "", args...)
		return out.String(), err
	}

	if _, err := run("piler", "--from", dir, "--format", "eml"); err == nil || !strings.Contains(err.Error(), "need --folder NAME") {
		t.Fatalf("without --folder: %v", err)
	}
	if _, err := run("piler", "--from", dir+"/export.zip", "--format", "eml"); err == nil || !strings.Contains(err.Error(), "unpack it first") {
		t.Fatalf("zip: %v", err)
	}
	out, err := run("piler", "--from", dir, "--format", "eml", "--folder", "piler", "--dry-run")
	if err != nil || !strings.Contains(out, "3 message(s) in 1 folder(s); nothing was stored") ||
		!strings.Contains(out, "skipped export.zip: compressed archive; unpack it first") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	out, err = run("piler", "--from", dir, "--format", "eml", "--folder", "piler")
	if code := exitCodeOf(err); code != 1 || !strings.Contains(out, "partial: read 3, added 2 (2 new to the archive), 0 already there, 1 skipped") {
		t.Fatalf("import: exit %d %v\n%s", code, err, out)
	}
}

func TestImportJournalCommand(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	dataDir := t.TempDir()
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", dataDir)
	t.Setenv("MAIL_ARCHIVE_LOG_LEVEL", "error")
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	ctx := context.Background()
	anna, err := st.CreateUser(ctx, "anna", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	journal := &store.Account{Name: "alice@example.com", Enabled: true, OwnerID: &anna.ID}
	if err := st.CreateSMTPAccount(ctx, journal, []string{"journal@archive.test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAccount(ctx, &store.Account{Kind: store.KindImport, Name: "piler", OwnerID: &anna.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAccount(ctx, &store.Account{Name: "server", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", OwnerID: &anna.ID}); err != nil {
		t.Fatal(err)
	}
	// tb2 of the fixture was journaled, with other bytes.
	blobs, err := openBlobs(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	blob, created, err := blobs.Put(strings.NewReader("Received: by mx\r\nMessage-ID: <tb2@example.com>\r\nSubject: Second message\r\n\r\nx\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := archive.BuildMeta(ctx, st, blobs, blob, created)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReceiveMessage(ctx, journal.ID, meta, store.Envelope{MailFrom: "a@example.com", RcptTo: []string{"b@example.com"}, ReceivedAt: time.Now()},
		func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	// A disabled account still takes the import: disabling stops receiving.
	conn := connectDB(t, url)
	if _, err := conn.Exec(ctx, "UPDATE accounts SET enabled = FALSE WHERE id = $1", journal.ID); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := newImportCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := runCmd(t, cmd, "", args...)
		return out.String(), err
	}
	accounts := func() int {
		t.Helper()
		accs, err := st.ListAccounts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return len(accs)
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"alice@example.com", "--journal", "--folder", "x"}, "--folder is not allowed with --journal"},
		{[]string{"other", "--label", "x"}, "--label needs --journal"},
		{[]string{"alice@example.com"}, `"alice@example.com" is an smtp account; add --journal`},
		{[]string{"piler", "--journal"}, `"piler" is an import account`},
		{[]string{"server", "--journal"}, `"server" is an IMAP account`},
		{[]string{"missing", "--journal"}, `no smtp account "missing"`},
		{[]string{"missing", "--journal", "--dry-run"}, `no smtp account "missing"`},
		{[]string{"alice@example.com", "--journal", "--label", "bad\x01label"}, "--label"},
		{[]string{"alice@example.com", "--journal", "--label", ""}, "--label"},
	} {
		args := append(c.args, "--from", tbFixture, "--format", "mbox")
		if _, err := run(args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.args, err, c.want)
		}
	}
	if n := accounts(); n != 3 {
		t.Fatalf("%d accounts", n)
	}

	out, err := run("alice@example.com", "--journal", "--user", "anna", "--from", tbFixture, "--format", "mbox", "--label", "thunderbird", "--dry-run")
	if err != nil || !strings.Contains(out, "4 message(s) in 2 folder(s); nothing was stored") || !strings.Contains(out, `into JOURNAL of smtp account "alice@example.com"`) {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	var imports int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM smtp_imports").Scan(&imports); err != nil || imports != 0 {
		t.Fatalf("dry run recorded %d imports, %v", imports, err)
	}

	out, err = run("alice@example.com", "--journal", "--from", tbFixture, "--format", "mbox", "--label", "thunderbird")
	if err != nil || !regexpMatch(`FOLDER\s+READ\s+ADDED\s+NEW\s+PRESENT\s+JOURNALED\s+SKIPPED`, out) ||
		!strings.Contains(out, "ok: read 4, added 3 (3 new to the archive), 0 already there, 1 already journaled, 0 skipped") {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if n := accounts(); n != 3 {
		t.Fatalf("%d accounts after the import", n)
	}
	var importID int64
	var label, source string
	if err := conn.QueryRow(ctx, "SELECT id, label, source FROM smtp_imports WHERE account_id = $1", journal.ID).Scan(&importID, &label, &source); err != nil ||
		label != "thunderbird" || source != tbFixture {
		t.Fatalf("import row %d %q %q, %v", importID, label, source, err)
	}
	var details []byte
	if err := conn.QueryRow(ctx, "SELECT details FROM audit_log WHERE action = 'data.import' AND target_id = $1", strconv.FormatInt(journal.ID, 10)).Scan(&details); err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(details, &d); err != nil {
		t.Fatal(err)
	}
	if d["format"] != "mbox" || d["source"] != tbFixture || d["folders"] != float64(2) || d["into"] != "JOURNAL" ||
		d["label"] != "thunderbird" || d["import"] != float64(importID) {
		t.Errorf("audit details %s", details)
	}

	// Again, with the default label: nothing added.
	out, err = run("alice@example.com", "--journal", "--from", tbFixture, "--format", "mbox")
	if err != nil || !strings.Contains(out, "added 0 (0 new to the archive), 3 already there, 1 already journaled") {
		t.Fatalf("again: %v\n%s", err, out)
	}
	if err := conn.QueryRow(ctx, "SELECT label FROM smtp_imports ORDER BY id DESC LIMIT 1").Scan(&label); err != nil || label != tbFixture {
		t.Errorf("default label %q, %v", label, err)
	}
}

func connectDB(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}
