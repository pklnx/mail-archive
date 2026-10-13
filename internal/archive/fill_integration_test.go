package archive_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
)

var fillCutoff = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func fillMessage(id string) []byte {
	if id == "" {
		return []byte("From: a@example.com\r\nSubject: no id\r\n\r\nNo Message-ID.\r\n")
	}
	return fmt.Appendf(nil, "Message-ID: <%s@example.com>\r\nFrom: a@example.com\r\nSubject: %s\r\n\r\nBody of %s\r\n", id, id, id)
}

// journal stores the journal copy of a message: the same Message-ID, other
// bytes (Postfix adds a Received: line).
func (f *fixture) journal(acc *store.Account, id string) {
	f.t.Helper()
	raw := append([]byte("Received: from mx by archive\r\n"), fillMessage(id)...)
	blob, created, err := f.blobs.Put(bytes.NewReader(raw))
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

func (f *fixture) smtpAccount() *store.Account {
	f.t.Helper()
	acc := &store.Account{Name: "journal", Enabled: true}
	if err := f.store.CreateSMTPAccount(f.ctx, acc, []string{"archive@archive.local"}); err != nil {
		f.t.Fatal(err)
	}
	return acc
}

func (f *fixture) filler() *archive.Filler {
	return &archive.Filler{Store: f.store, Blobs: f.blobs, Sealer: f.sealer, BatchSize: 2, Logger: slog.New(slog.NewTextHandler(f.logs, nil))}
}

func (f *fixture) createFill(acc *store.Account, password string, excluded ...string) *store.Fill {
	f.t.Helper()
	fill := &store.Fill{
		AccountID: acc.ID, Host: f.host, Port: f.port, TLSMode: store.TLSModeNone, Username: "alice@example.com",
		Excluded: excluded, FolderPrefix: "alice@example.com", Cutoff: fillCutoff,
	}
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(f.sealer, id, password) }
	if err := f.store.CreateFill(f.ctx, fill, seal); err != nil {
		f.t.Fatal(err)
	}
	return fill
}

func folderOf(fill *store.Fill, source string) store.FillFolder {
	for _, ff := range fill.Folders {
		if ff.SourceName == source {
			return ff
		}
	}
	return store.FillFolder{}
}

func fillUser(t *testing.T) *imapmemserver.User {
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Sent", "Spam")
	berlin := time.FixedZone("CEST", 2*3600)
	for _, m := range []struct {
		id string
		at time.Time
	}{
		{"m1", fillCutoff.Add(-48 * time.Hour)},             // imported
		{"m2", fillCutoff.Add(-10 * time.Minute)},           // journaled
		{"m3", fillCutoff.Add(30 * time.Minute).In(berlin)}, // in the window: imported
		{"m4", fillCutoff.Add(2 * time.Hour)},               // after the cut-off
		{"m5", fillCutoff.Add(-24 * time.Hour)},             // journaled long before
		{"", fillCutoff.Add(-time.Hour)},                    // no Message-ID: imported
	} {
		imaptest.AppendAt(t, u, "INBOX", fillMessage(m.id), m.at)
	}
	imaptest.AppendAt(t, u, "Sent", fillMessage("s1"), fillCutoff.Add(-5*24*time.Hour))
	imaptest.AppendAt(t, u, "Spam", fillMessage("spam"), fillCutoff.Add(-time.Hour))
	return u
}

func TestFill(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	f.journal(acc, "m2")
	f.journal(acc, "m5")
	fill := f.createFill(acc, "secret", "Spam")
	f.srv.ResetCommands()

	got, err := f.filler().Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.FillOK || got.PasswordEnc != nil || got.FinishedAt == nil {
		t.Fatalf("fill %+v", got)
	}
	inbox, sent := folderOf(got, "INBOX"), folderOf(got, "Sent")
	if want := (store.FillCounts{Scanned: 6, Imported: 3, New: 3, AfterCutoff: 1, Journaled: 2}); inbox.Counts != want || !inbox.Done {
		t.Errorf("INBOX %+v, want %+v", inbox, want)
	}
	if want := (store.FillCounts{Scanned: 1, Imported: 1, New: 1}); sent.Counts != want {
		t.Errorf("Sent %+v", sent.Counts)
	}
	if len(got.Folders) != 2 || inbox.FolderName != "alice@example.com/INBOX" || sent.FolderName != "alice@example.com/Sent" {
		t.Errorf("folders %+v", got.Folders)
	}
	// Only the imported messages were downloaded: UIDs 1, 3 and 6 of INBOX
	// (in batches of two headers) and 1 of Sent.
	if b := f.srv.BodyFetches(); !slices.Equal(b, []string{"1", "3", "6", "1"}) {
		t.Errorf("body fetches %v", b)
	}
	// The journal INBOX is untouched; the fill folders have their own UIDs.
	locs, _ := f.stats()
	if locs["journal"] != 6 {
		t.Errorf("locations %v", locs)
	}

	// A second run of an ended fill does nothing.
	if _, err := f.filler().Run(f.ctx, fill.ID); !errors.Is(err, store.ErrFillNotRunning) {
		t.Errorf("run an ended fill: %v", err)
	}
}

func TestFillResumeAfterCancel(t *testing.T) {
	u := fillUser(t)
	f := newFixture(t, u)
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret", "Spam", "Sent")
	filler := f.filler()
	batches := 0
	filler.Progress = func(store.FillFolder) {
		batches++
		if batches == 1 {
			if err := f.store.CancelFill(f.ctx, fill.ID); err != nil {
				t.Error(err)
			}
		}
	}
	got, err := filler.Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.FillCancelled || got.PasswordEnc != nil {
		t.Fatalf("cancelled fill %+v", got)
	}
	if c := folderOf(got, "INBOX"); c.LastUID != 2 || c.Counts.Scanned != 2 || c.Done {
		t.Fatalf("progress after cancel %+v", c)
	}

	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(f.sealer, id, "secret") }
	if err := f.store.ResumeFill(f.ctx, fill.ID, seal); err != nil {
		t.Fatal(err)
	}
	f.srv.ResetCommands()
	filler.Progress = nil
	if got, err = filler.Run(f.ctx, fill.ID); err != nil {
		t.Fatal(err)
	}
	inbox := folderOf(got, "INBOX")
	if want := (store.FillCounts{Scanned: 6, Imported: 5, New: 5, AfterCutoff: 1}); got.Status != store.FillOK || inbox.Counts != want {
		t.Errorf("resumed %s %+v, want %+v", got.Status, inbox.Counts, want)
	}
	// Nothing committed before the cancel is fetched again.
	if b := f.srv.BodyFetches(); !slices.Equal(b, []string{"3", "5:6"}) {
		t.Errorf("body fetches after resume %v", b)
	}
	if locs, _ := f.stats(); locs["journal"] != 5 {
		t.Errorf("locations %v", locs)
	}
}

func TestFillRescansAfterUIDValidityChange(t *testing.T) {
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Box")
	for i := range 4 {
		imaptest.AppendAt(t, u, "Box", fillMessage(fmt.Sprintf("b%d", i)), fillCutoff.Add(-time.Hour))
	}
	f := newFixture(t, u)
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret")
	filler := f.filler()
	filler.Progress = func(ff store.FillFolder) {
		if ff.SourceName == "Box" && ff.LastUID == 2 {
			_ = f.store.CancelFill(f.ctx, fill.ID)
		}
	}
	if _, err := filler.Run(f.ctx, fill.ID); err != nil {
		t.Fatal(err)
	}
	// The server renumbers the folder: same messages, new UIDVALIDITY.
	if err := u.Delete("Box"); err != nil {
		t.Fatal(err)
	}
	imaptest.CreateMailboxes(t, u, "Box")
	for i := range 4 {
		imaptest.AppendAt(t, u, "Box", fillMessage(fmt.Sprintf("b%d", i)), fillCutoff.Add(-time.Hour))
	}
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(f.sealer, id, "secret") }
	if err := f.store.ResumeFill(f.ctx, fill.ID, seal); err != nil {
		t.Fatal(err)
	}
	filler.Progress = nil
	got, err := filler.Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	box := folderOf(got, "Box")
	if box.Counts.Imported != 4 || box.Counts.Present != 2 || got.Status != store.FillOK {
		t.Errorf("after the renumbering %s %+v", got.Status, box.Counts)
	}
	if locs, _ := f.stats(); locs["journal"] != 4 {
		t.Errorf("locations %v", locs)
	}
}

func TestFillFailsAndWipesThePassword(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	fill := f.createFill(acc, "wrong password")
	got, err := f.filler().Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.FillFailed || got.Error == "" || got.PasswordEnc != nil {
		t.Errorf("failed fill %+v", got)
	}
}

func TestFillHoldsTheSyncLock(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret")
	unlock, ok, err := f.store.TryLockSyncForWrite(f.ctx, acc.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := f.filler().Run(f.ctx, fill.ID); !errors.Is(err, archive.ErrFillBusy) {
		t.Errorf("run while locked: %v", err)
	}
	// Journaling goes on while the lock is held.
	f.journal(acc, "during")
	unlock()
}
