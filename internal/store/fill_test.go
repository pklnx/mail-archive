package store_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func newFill(a *store.Account, username string) *store.Fill {
	cutoff := time.Now()
	return &store.Fill{
		AccountID: a.ID, Host: "imap.example.com", Port: 993, TLSMode: store.TLSModeTLS, Username: username, Cutoff: &cutoff,
	}
}

func seal(id int64) ([]byte, error) { return []byte("sealed"), nil }

func TestFills(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	a := createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")
	imap := &store.Account{Name: "mail", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", Enabled: true}
	if err := st.CreateAccount(ctx, imap); err != nil {
		t.Fatal(err)
	}

	// New fills write into JOURNAL: a prefix is refused.
	prefixed := newFill(a, "alice@example.com")
	prefixed.FolderPrefix = "alice"
	if err := st.CreateFill(ctx, prefixed, seal); !errors.Is(err, store.ErrFillPrefix) {
		t.Errorf("prefix accepted: %v", err)
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

	// Every IMAP folder fills the journal folder.
	ff, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if ff.FolderName != store.SMTPFolder || ff.SourceName != "INBOX" {
		t.Fatalf("fill folder %+v", ff)
	}
	if sent, err := st.FillFolder(ctx, f, "Sent"); err != nil || sent.FolderID != ff.FolderID || sent.SourceName != "Sent" {
		t.Fatalf("fill folder Sent %+v, %v", sent, err)
	}
	again, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil || again.FolderID != ff.FolderID {
		t.Fatalf("fill folder again %+v, %v", again, err)
	}

	// A batch and the progress commit together.
	meta := receivedMeta(1)
	ff.LastUID, ff.SourceUIDValidity = 41, 7
	ff.Counts = store.FillCounts{Scanned: 3, Imported: 1, AfterCutoff: 1, Journaled: 1}
	loc := store.Location{InternalDate: time.Now()}
	res, err := st.SaveFillBatch(ctx, ff, []store.MessageMeta{meta}, []store.Location{loc})
	if err != nil || res.New != 1 || ff.Counts.New != 1 {
		t.Fatalf("save: %+v, %+v, %v", res, ff, err)
	}
	// A failing batch changes neither the messages nor the progress.
	bad := *ff
	bad.LastUID, bad.Counts.Scanned, bad.FolderID = 99, 50, -1
	if _, err := st.SaveFillBatch(ctx, &bad, []store.MessageMeta{receivedMeta(2)}, []store.Location{loc}); err == nil {
		t.Fatal("bad batch saved")
	}
	if bad.Counts.Scanned != 50 || bad.Counts.New != 1 {
		t.Errorf("counts not restored: %+v", bad.Counts)
	}
	got, err := st.GetFill(ctx, f.ID)
	if err != nil || len(got.Folders) != 2 {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if g := got.Folders[0]; g.LastUID != 41 || g.SourceUIDValidity != 7 || g.Counts != (store.FillCounts{Scanned: 3, Imported: 1, New: 1, AfterCutoff: 1, Journaled: 1}) {
		t.Errorf("stored progress %+v", g)
	}
	// Skipped-only batches record progress too.
	ff.LastUID, ff.Counts.Scanned, ff.Counts.AfterCutoff = 45, 7, 5
	if _, err := st.SaveFillBatch(ctx, ff, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetFill(ctx, f.ID); got.Folders[0].LastUID != 45 || got.Folders[0].Counts.AfterCutoff != 5 {
		t.Errorf("progress without messages %+v", got.Folders[0])
	}

	// Filled mail does not count as received, nor as the first received.
	if last, _ := st.LastReceived(ctx); !last[a.ID].IsZero() {
		t.Errorf("last received from a fill: %v", last[a.ID])
	}
	if first, err := st.FirstReceived(ctx, a.ID); err != nil || first != nil {
		t.Errorf("first received from a fill: %+v, %v", first, err)
	}
	// The location names the fill.
	d, err := st.GetMessageDetail(ctx, u.ID, meta.SHA256)
	if err != nil || len(d.Locations) != 1 {
		t.Fatalf("detail %+v, %v", d, err)
	}
	if l := d.Locations[0]; l.Folder != store.SMTPFolder || !l.Filled || l.Envelope != nil || l.UID != 1 ||
		l.Fill == nil || l.Fill.ID != f.ID || l.Fill.Username != "alice@example.com" || l.Fill.Host != "imap.example.com" {
		t.Errorf("filled location %+v", l)
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

// TestFillAndReceiveConcurrent checks that a fill and deliveries over SMTP
// share the journal folder: distinct UIDs, a location for every message,
// and last_uid counting them all.
func TestFillAndReceiveConcurrent(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	f := newFill(a, "alice@example.com")
	if err := st.CreateFill(ctx, f, seal); err != nil {
		t.Fatal(err)
	}
	ff, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 20 {
		wg.Go(func() {
			// Every delivery also brings message 7, which the fill imports too.
			for _, sha := range []byte{byte(100 + i), 7} {
				if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(sha), envelope("x@example.com"), present); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Go(func() {
		for b := range 10 {
			metas := []store.MessageMeta{receivedMeta(byte(150 + 3*b)), receivedMeta(byte(151 + 3*b)), receivedMeta(byte(152 + 3*b))}
			if b == 0 {
				metas[1] = receivedMeta(7)
			}
			ff.Counts.Imported += len(metas)
			if _, err := st.SaveFillBatch(ctx, ff, metas, make([]store.Location, len(metas))); err != nil {
				errs <- err
			}
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var locations, distinctUIDs, lastUID, withEnvelope int
	if err := conn.QueryRow(ctx, `SELECT count(*), count(DISTINCT l.uid), max(f.last_uid), count(e.location_id)
		FROM message_locations l JOIN folders f ON f.id = l.folder_id LEFT JOIN envelopes e ON e.location_id = l.id
		WHERE f.account_id = $1`, a.ID).Scan(&locations, &distinctUIDs, &lastUID, &withEnvelope); err != nil {
		t.Fatal(err)
	}
	// 20 delivered, 29 filled, and message 7 once with its envelope, and
	// once more if the fill came first.
	if locations != distinctUIDs || locations != lastUID || withEnvelope != 21 || locations < 50 || locations > 51 {
		t.Errorf("%d locations, %d UIDs, last UID %d, %d with an envelope", locations, distinctUIDs, lastUID, withEnvelope)
	}
	var unlocated int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM messages m
		WHERE NOT EXISTS (SELECT 1 FROM message_locations l WHERE l.message_sha256 = m.sha256)`).Scan(&unlocated); err != nil || unlocated != 0 {
		t.Errorf("%d messages without a location, %v", unlocated, err)
	}
	got, err := st.GetFill(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Folders[0].Counts; c.Imported+c.Present != 30 || c.Imported != locations-21 {
		t.Errorf("counts %+v", c)
	}

	// A fill does not move the time of the last delivery.
	last, err := st.LastReceived(ctx)
	if err != nil || last[a.ID].IsZero() {
		t.Fatalf("last received %v, %v", last, err)
	}
	ff.Counts.Imported++
	if _, err := st.SaveFillBatch(ctx, ff, []store.MessageMeta{receivedMeta(250)}, []store.Location{{}}); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.LastReceived(ctx); !again[a.ID].Equal(last[a.ID]) {
		t.Errorf("last received moved: %v, was %v", again[a.ID], last[a.ID])
	}
}

// TestReceiveAfterFill checks that a delivery of bytes a fill imported
// first gets its own location with an envelope, and that FirstReceived
// only looks at delivered mail.
func TestReceiveAfterFill(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	a := createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")
	f := newFill(a, "alice@example.com")
	if err := st.CreateFill(ctx, f, seal); err != nil {
		t.Fatal(err)
	}
	ff, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	meta := receivedMeta(5)
	meta.Subject = "filled"
	ff.Counts.Imported = 1
	if _, err := st.SaveFillBatch(ctx, ff, []store.MessageMeta{meta}, []store.Location{{InternalDate: time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	// Imported again in a later batch: present.
	ff.Counts.Imported++
	if _, err := st.SaveFillBatch(ctx, ff, []store.MessageMeta{meta}, []store.Location{{}}); err != nil || ff.Counts.Imported != 1 || ff.Counts.Present != 1 {
		t.Fatalf("again: %+v, %v", ff.Counts, err)
	}
	if first, err := st.FirstReceived(ctx, a.ID); err != nil || first != nil {
		t.Fatalf("first received with filled mail only: %+v, %v", first, err)
	}

	res, err := st.ReceiveMessage(ctx, a.ID, meta, envelope("bob@example.com"), present)
	if err != nil || !res.New || res.NewMessage || res.UID != 2 || res.AddedRecipients != 1 {
		t.Fatalf("delivery after the fill %+v, %v", res, err)
	}
	// A retry merges into the delivered location, not the filled one.
	res, err = st.ReceiveMessage(ctx, a.ID, meta, envelope("carol@example.com"), present)
	if err != nil || res.New || res.UID != 2 || res.AddedRecipients != 1 {
		t.Fatalf("second delivery %+v, %v", res, err)
	}
	envs, err := envelopesOf(st, u.ID, meta.SHA256)
	if err != nil || len(envs) != 1 || !slices.Equal(envs[0].RcptTo, []string{"bob@example.com", "carol@example.com"}) {
		t.Fatalf("envelopes %+v, %v", envs, err)
	}
	d, err := st.GetMessageDetail(ctx, u.ID, meta.SHA256)
	if err != nil || len(d.Locations) != 2 {
		t.Fatalf("locations %+v, %v", d, err)
	}
	filled := 0
	for _, l := range d.Locations {
		if l.Filled {
			filled++
		}
	}
	if filled != 1 {
		t.Errorf("filled locations %+v", d.Locations)
	}
	if first, err := st.FirstReceived(ctx, a.ID); err != nil || first == nil || first.Subject != "filled" {
		t.Errorf("first received %+v, %v", first, err)
	}
}
