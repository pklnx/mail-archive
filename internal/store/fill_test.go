package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func newFill(a *store.Account, prefix string) *store.Fill {
	return &store.Fill{
		AccountID: a.ID, Host: "imap.example.com", Port: 993, TLSMode: store.TLSModeTLS, Username: "alice@example.com",
		FolderPrefix: prefix, Cutoff: time.Now(),
	}
}

func seal(id int64) ([]byte, error) { return []byte("sealed"), nil }

func TestFills(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	imap := &store.Account{Name: "mail", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", Enabled: true}
	if err := st.CreateAccount(ctx, imap); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"", "/x", "x/", "inbox", "a\nb"} {
		if err := st.CreateFill(ctx, newFill(a, bad), seal); err == nil {
			t.Errorf("prefix %q accepted", bad)
		}
	}
	if err := st.CreateFill(ctx, newFill(imap, "alice"), seal); !errors.Is(err, store.ErrNotSMTP) {
		t.Errorf("imap account: %v", err)
	}

	f := newFill(a, "alice@example.com")
	if err := st.CreateFill(ctx, f, seal); err != nil {
		t.Fatal(err)
	}
	if f.ID == 0 || f.Status != store.FillRunning || string(f.PasswordEnc) != "sealed" {
		t.Fatalf("created %+v", f)
	}
	// One running fill per account.
	if err := st.CreateFill(ctx, newFill(a, "bob@example.com"), seal); !errors.Is(err, store.ErrFillRunning) {
		t.Errorf("second running fill: %v", err)
	}

	// The archive folder is "<prefix>/<source>", with the fill's start as UIDVALIDITY.
	ff, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if ff.FolderName != "alice@example.com/INBOX" || ff.TargetUIDValidity != uint32(f.CreatedAt.Unix()) || ff.TargetLastUID != 0 {
		t.Fatalf("fill folder %+v", ff)
	}
	again, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil || again.FolderID != ff.FolderID {
		t.Fatalf("fill folder again %+v, %v", again, err)
	}

	// A batch and the progress commit together.
	meta := receivedMeta(1)
	ff.LastUID, ff.SourceUIDValidity = 41, 7
	ff.Counts = store.FillCounts{Scanned: 3, Imported: 1, AfterCutoff: 1, Journaled: 1}
	loc := store.Location{FolderID: ff.FolderID, UIDValidity: ff.TargetUIDValidity, UID: 1, InternalDate: time.Now()}
	res, err := st.SaveFillBatch(ctx, ff, 1, []store.MessageMeta{meta}, []store.Location{loc})
	if err != nil || res.New != 1 || ff.Counts.New != 1 || ff.TargetLastUID != 1 {
		t.Fatalf("save: %+v, %+v, %v", res, ff, err)
	}
	// A failing batch changes neither the messages nor the progress.
	bad := *ff
	bad.LastUID, bad.Counts.Scanned = 99, 50
	badLoc := loc
	badLoc.FolderID, badLoc.UID = -1, 2
	if _, err := st.SaveFillBatch(ctx, &bad, 2, []store.MessageMeta{receivedMeta(2)}, []store.Location{badLoc}); err == nil {
		t.Fatal("bad batch saved")
	}
	if bad.Counts.Scanned != 50 || bad.Counts.New != 1 {
		t.Errorf("counts not restored: %+v", bad.Counts)
	}
	got, err := st.GetFill(ctx, f.ID)
	if err != nil || len(got.Folders) != 1 {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if g := got.Folders[0]; g.LastUID != 41 || g.SourceUIDValidity != 7 || g.Counts != (store.FillCounts{Scanned: 3, Imported: 1, New: 1, AfterCutoff: 1, Journaled: 1}) {
		t.Errorf("stored progress %+v", g)
	}
	// Skipped-only batches record progress too.
	ff.LastUID, ff.Counts.Scanned, ff.Counts.AfterCutoff = 45, 7, 5
	if _, err := st.SaveFillBatch(ctx, ff, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetFill(ctx, f.ID); got.Folders[0].LastUID != 45 || got.Folders[0].Counts.AfterCutoff != 5 {
		t.Errorf("progress without messages %+v", got.Folders[0])
	}

	// Fill folders do not count as received mail.
	if last, _ := st.LastReceived(ctx); !last[a.ID].IsZero() {
		t.Errorf("last received from a fill folder: %v", last[a.ID])
	}

	// The database never keeps a password after the end.
	if _, err := conn.Exec(ctx, `UPDATE smtp_fills SET status = 'ok', finished_at = now() WHERE id = $1`, f.ID); err == nil {
		t.Error("an ended fill kept its password")
	}

	// Cancel wins over a later finish; the password is gone.
	if err := st.CancelFill(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishFill(ctx, f.ID, store.FillOK, ""); !errors.Is(err, store.ErrFillNotRunning) {
		t.Errorf("finish after cancel: %v", err)
	}
	if err := st.CancelFill(ctx, f.ID); !errors.Is(err, store.ErrFillNotRunning) {
		t.Errorf("cancel twice: %v", err)
	}
	got, _ = st.GetFill(ctx, f.ID)
	if got.Status != store.FillCancelled || got.PasswordEnc != nil || got.FinishedAt == nil {
		t.Errorf("cancelled %+v", got)
	}
	if err := st.ResumeFill(ctx, f.ID, seal); err != nil {
		t.Fatal(err)
	}
	if err := st.ResumeFill(ctx, f.ID, seal); !errors.Is(err, store.ErrFillNotResumable) {
		t.Errorf("resume a running fill: %v", err)
	}
	if err := st.FinishFill(ctx, f.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.ResumeFill(ctx, f.ID, seal); !errors.Is(err, store.ErrFillNotResumable) {
		t.Errorf("resume an ok fill: %v", err)
	}
	if err := st.ResumeFill(ctx, 9999, seal); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("resume a missing fill: %v", err)
	}
	// The prefix belongs to that fill.
	if err := st.CreateFill(ctx, newFill(a, "alice@example.com"), seal); !errors.Is(err, store.ErrConflict) {
		t.Errorf("prefix in use: %v", err)
	}

	// Removing the account cancels a running fill and wipes its password.
	second := newFill(a, "bob@example.com")
	if err := st.CreateFill(ctx, second, seal); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetAccount(ctx, a.ID)
	if res, err := st.DeleteOrRemoveAccount(ctx, a.Ref()); err != nil || res != store.AccountRemoved {
		t.Fatalf("remove: %v, %v", res, err)
	}
	if got, _ := st.GetFill(ctx, second.ID); got.Status != store.FillCancelled || got.PasswordEnc != nil {
		t.Errorf("fill of a removed account %+v", got)
	}
	fills, err := st.AccountFills(ctx, a.ID)
	if err != nil || len(fills) != 2 || fills[0].ID != second.ID {
		t.Errorf("account fills %+v, %v", fills, err)
	}
}

func TestJournaledMessageIDs(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	other := createSMTPAccount(t, st, nil, "other", "other@example.com")
	if first, err := st.FirstReceived(ctx, a.ID); err != nil || first != nil {
		t.Fatalf("first of an empty account: %+v, %v", first, err)
	}
	m1 := receivedMeta(1)
	m1.MessageID, m1.Subject = "one@example.com", "first"
	m2 := receivedMeta(2)
	m2.MessageID = "two@example.com"
	for _, m := range []store.MessageMeta{m1, m2} {
		if _, err := st.ReceiveMessage(ctx, a.ID, m, envelope("x@example.com"), present); err != nil {
			t.Fatal(err)
		}
	}
	m3 := receivedMeta(3)
	m3.MessageID = "three@example.com"
	if _, err := st.ReceiveMessage(ctx, other.ID, m3, envelope("x@example.com"), present); err != nil {
		t.Fatal(err)
	}
	got, err := st.JournaledMessageIDs(ctx, a.ID, []string{"one@example.com", "three@example.com", "", "none@example.com"})
	if err != nil || len(got) != 1 || !got["one@example.com"] {
		t.Errorf("journaled %v, %v", got, err)
	}
	first, err := st.FirstReceived(ctx, a.ID)
	if err != nil || first == nil || first.Subject != "first" || first.ReceivedAt.IsZero() {
		t.Errorf("first received %+v, %v", first, err)
	}
}
