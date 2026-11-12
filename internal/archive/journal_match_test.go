package archive_test

import (
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
)

// The journaled check of fills and journal imports (#4 of the security
// fixes, advisory GHSA-w6jg-ffvw-q4f5): a mailbox message is left out only
// when the account received a message with the same Message-ID, Date,
// From and Subject over SMTP.

// matchCase is a mailbox message and a message received over SMTP with
// the same Message-ID; the header lines (LF) are the rest of each header.
type matchCase struct {
	name             string
	mailbox, journal string
	journaled        bool
}

const realHeader = "From: Boss <boss@example.com>\nSubject: real contract\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n"

// forgedCases differ in at least one value: each mailbox message is
// imported. The first is the audit's scenario.
var forgedCases = []matchCase{
	{name: "audit scenario", mailbox: realHeader,
		journal: "From: attacker@evil.example\nSubject: spam\nDate: Tue, 2 Jun 2026 12:00:00 +0200\n"},
	{name: "only the Date", mailbox: realHeader,
		journal: "From: Boss <boss@example.com>\nSubject: real contract\nDate: Mon, 1 Jun 2026 12:00:01 +0200\n"},
	{name: "only the From", mailbox: realHeader,
		journal: "From: Boss <attacker@evil.example>\nSubject: real contract\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n"},
	{name: "only the Subject", mailbox: realHeader,
		journal: "From: Boss <boss@example.com>\nSubject: real contract!\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n"},
	{name: "address in the display name", mailbox: realHeader,
		journal: "From: =?UTF-8?Q?=3Cboss@example.com=3E?= <attacker@evil.example>\nSubject: real contract\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n"},
}

// genuineCases are the same message in the forms servers and gateways
// produce, and the cases a missing value decides.
var genuineCases = []matchCase{
	{name: "same header", mailbox: realHeader, journal: realHeader, journaled: true},
	{name: "folded Subject", journaled: true,
		mailbox: "From: Boss <boss@example.com>\nSubject: real\n contract\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n",
		journal: realHeader},
	{name: "Subject B vs Q", journaled: true,
		mailbox: "From: a@example.com\nSubject: =?UTF-8?B?R3LDvMOfZQ==?=\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n",
		journal: "From: a@example.com\nSubject: =?ISO-8859-1?Q?Gr=FC=DFe?=\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n"},
	{name: "Subject Q vs raw UTF-8", journaled: true,
		mailbox: "From: a@example.com\nSubject: =?ISO-8859-1?Q?Gr=FC=DFe?=\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n",
		journal: "From: a@example.com\nSubject: Grüße\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n"},
	{name: "From quoted vs encoded", journaled: true,
		mailbox: "From: \"Doe, John\" <John@Example.COM>\nSubject: s\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n",
		journal: "From: =?UTF-8?Q?Doe=2C_J=C3=B6hn?= <john@example.com>\nSubject: s\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n"},
	{name: "From comment", journaled: true,
		mailbox: "From: john@example.com (John)\nSubject: s\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n",
		journal: "From: \"Doe, John\" <John@Example.COM>\nSubject: s\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n"},
	{name: "Date GMT", journaled: true,
		mailbox: realHeader,
		journal: "From: Boss <boss@example.com>\nSubject: real contract\nDate: 1 Jun 2026 10:00:00 GMT\n"},
	{name: "Date EDT", journaled: true,
		mailbox: realHeader,
		journal: "From: Boss <boss@example.com>\nSubject: real contract\nDate: Mon, 01 Jun 2026 06:00:00 -0400 (EDT)\n"},
	{name: "no Subject", journaled: true,
		mailbox: "From: a@example.com\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n",
		journal: "From: a@example.com\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n"},
	{name: "no Date", mailbox: "From: a@example.com\nSubject: s\n", journal: "From: a@example.com\nSubject: s\n"},
	{name: "unparseable Date",
		mailbox: "From: a@example.com\nSubject: s\nDate: someday\n", journal: "From: a@example.com\nSubject: s\nDate: someday\n"},
	{name: "no From",
		mailbox: "Subject: s\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n", journal: "Subject: s\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n"},
}

// mailboxCopy is the mailbox message of case i, CRLF.
func (c matchCase) mailboxCopy(i int) []byte {
	return crlf(fmt.Sprintf("Message-ID: <case%d@example.com>\n%s\nBody of case %d\n", i, c.mailbox, i))
}

// journalCopy is what the account received over SMTP for case i: a
// Received: line more and other body bytes, so never the same file.
func (c matchCase) journalCopy(i int) []byte {
	return crlf(fmt.Sprintf("Received: from mx by archive\nMessage-ID: <case%d@example.com>\n%s\nJournal copy of case %d\n", i, c.journal, i))
}

func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

func countJournaled(cases []matchCase) int {
	n := 0
	for _, c := range cases {
		if c.journaled {
			n++
		}
	}
	return n
}

// filledSHAs returns the messages the fills imported into the journal
// folder of acc.
func (f *fixture) filledSHAs(acc *store.Account) map[string]bool {
	f.t.Helper()
	conn, err := pgx.Connect(f.ctx, f.dbURL)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = conn.Close(f.ctx) }()
	rows, err := conn.Query(f.ctx, `SELECT l.message_sha256 FROM message_locations l JOIN folders fo ON fo.id = l.folder_id
		WHERE fo.account_id = $1 AND fo.name = $2 AND l.fill_id IS NOT NULL`, acc.ID, store.SMTPFolder)
	if err != nil {
		f.t.Fatal(err)
	}
	shas, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]bool{}
	for _, s := range shas {
		out[s] = true
	}
	return out
}

// runMatchFill delivers the journal copies, fills the mailbox copies (one
// header per batch) and checks each case.
func runMatchFill(t *testing.T, cases []matchCase) {
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX")
	for i, c := range cases {
		imaptest.AppendAt(t, u, "INBOX", c.mailboxCopy(i), fillCutoff.Add(-48*time.Hour))
	}
	f := newFixture(t, u)
	acc := f.smtpAccount()
	for i, c := range cases {
		f.deliver(acc, c.journalCopy(i))
	}
	fill := f.createFill(acc, "secret")
	f.srv.ResetCommands()
	filler := f.filler()
	filler.BatchSize = 1
	got, err := filler.Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	journaled := countJournaled(cases)
	inbox := folderOf(got, "INBOX")
	want := store.FillCounts{Scanned: len(cases), Imported: len(cases) - journaled, New: len(cases) - journaled, Journaled: journaled}
	if got.Status != store.FillOK || inbox.Counts != want {
		t.Errorf("fill %s %+v, want %+v", got.Status, inbox.Counts, want)
	}
	filled := f.filledSHAs(acc)
	var fetched []string
	for i, c := range cases {
		if imported := filled[shaOf(c.mailboxCopy(i))]; imported == c.journaled {
			t.Errorf("%s: imported %v, want %v", c.name, imported, !c.journaled)
		}
		if !c.journaled {
			fetched = append(fetched, strconv.Itoa(i+1))
		}
	}
	// Only the imported messages were downloaded.
	if b := f.srv.BodyFetches(); !slices.Equal(b, fetched) {
		t.Errorf("body fetches %v, want %v", b, fetched)
	}
}

// deliver stores raw as received over SMTP, with an envelope.
func (f *fixture) deliver(acc *store.Account, raw []byte) {
	f.t.Helper()
	blob, created, err := f.blobs.Put(strings.NewReader(string(raw)))
	if err != nil {
		f.t.Fatal(err)
	}
	meta, err := archive.BuildMeta(f.ctx, f.store, f.blobs, blob, created)
	if err != nil {
		f.t.Fatal(err)
	}
	env := store.Envelope{MailFrom: "a@example.com", RcptTo: []string{"alice@example.com"}, ReceivedAt: time.Now(), HELO: "mx"}
	if _, err := f.store.ReceiveMessage(f.ctx, acc.ID, meta, env, func() bool { return true }); err != nil {
		f.t.Fatal(err)
	}
}

// TestFillForgedMessageID is the regression test: a message received over
// SMTP with the Message-ID of a mailbox message but another Date, From or
// Subject does not keep that message out of the archive.
func TestFillForgedMessageID(t *testing.T) {
	runMatchFill(t, forgedCases)
}

// TestFillGenuineJournalCopy: the journal copy of a mailbox message is
// recognised despite header variations, so it is not stored twice; a
// missing Date or From imports it.
func TestFillGenuineJournalCopy(t *testing.T) {
	runMatchFill(t, genuineCases)
}

// runMatchImport delivers the journal copies, imports the mailbox copies
// with import --journal and checks each case, also after the sweep: no
// file of a stored message goes.
func runMatchImport(t *testing.T, cases []matchCase) {
	e := newArchiveEnv(t)
	acc := e.smtpAccount("journal")
	files := map[string]string{}
	for i, c := range cases {
		e.deliver(acc, c.journalCopy(i))
		files[fmt.Sprintf("%02d.eml", i)] = string(c.mailboxCopy(i))
	}
	dir := t.TempDir()
	writeEML(t, dir, files, time.Now())
	res := e.importJournal(acc, e.smtpImport(acc, "mailbox", "eml"), dir)
	journaled := countJournaled(cases)
	if tot := res.Total(); res.Status != "ok" || tot.Read != len(cases) || tot.Journaled != journaled || tot.Added != len(cases)-journaled {
		t.Errorf("import %s %+v", res.Status, tot)
	}
	if _, err := archive.SweepBlobs(e.ctx, e.st, e.blobs, archive.SweepOptions{Logger: quiet}); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		sha := shaOf(c.mailboxCopy(i))
		got := strings.Join(e.origin(acc, sha), ",")
		switch {
		case c.journaled && (got != "" || e.blobs.Exists(sha)):
			t.Errorf("%s: journaled copy stored: %q, file there %v", c.name, got, e.blobs.Exists(sha))
		case !c.journaled && (got != "import:mailbox" || !e.blobs.Exists(sha)):
			t.Errorf("%s: %q, file there %v", c.name, got, e.blobs.Exists(sha))
		}
	}
	vres, err := archive.Verify(e.ctx, e.st, e.blobs, archive.VerifyOptions{LockTimeout: time.Second, Logger: quiet})
	if err != nil || vres.Problems() != 0 {
		t.Errorf("verify %+v, %v", vres, err)
	}
}

// TestImportJournalForgedMessageID is the regression test for import
// --journal: the mailbox message is stored, and its file is not removed.
func TestImportJournalForgedMessageID(t *testing.T) {
	runMatchImport(t, forgedCases)
}

// TestImportJournalGenuineJournalCopy: as TestFillGenuineJournalCopy, for
// import --journal; the files of journaled copies are removed.
func TestImportJournalGenuineJournalCopy(t *testing.T) {
	runMatchImport(t, genuineCases)
}

// TestImportJournalCandidateCap: more candidates than the cap (ten per
// message of a batch) are cut with a warning; the message is then stored
// or left out, never lost.
func TestImportJournalCandidateCap(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.smtpAccount("journal")
	head := "Message-ID: <flood@example.com>\nFrom: a@example.com\nDate: Mon, 1 Jun 2026 12:00:00 +0200\n"
	for i := range 11 {
		e.deliver(acc, crlf(fmt.Sprintf("Received: from mx\n%sSubject: s%d\n\nflood\n", head, i)))
	}
	dir := t.TempDir()
	writeEML(t, dir, map[string]string{"a.eml": head + "Subject: s0\n\nmailbox\n"}, time.Now())
	logs := &logBuffer{}
	im := e.importer()
	im.BatchSize, im.Logger = 1, slog.New(slog.NewTextHandler(logs, nil))
	folders, _, err := archive.JournalImportFolders("eml", dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := im.ImportJournal(e.ctx, acc, e.smtpImport(acc, "mailbox", "eml"), folders)
	if err != nil {
		t.Fatal(err)
	}
	if tot := res.Total(); tot.Read != 1 || tot.Journaled+tot.Added != 1 {
		t.Errorf("import %+v", tot)
	}
	if !strings.Contains(logs.String(), "too many journaled messages") {
		t.Errorf("no warning in %s", logs.String())
	}
}
