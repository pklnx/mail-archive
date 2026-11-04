package store_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

var testKeyring = store.KeyringCopy{Keyring: []byte(`{"format":"x"}`), ID: strings.Repeat("ab", 16), Generation: 1, CurrentKey: 0xdeadbeef}

func TestBlobEncryptionState(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if row, err := st.BlobEncryption(ctx); err != nil || row != nil {
		t.Fatalf("new archive: %+v %v", row, err)
	}
	if err := st.SetBlobEncryption(ctx, "off", testKeyring); err == nil {
		t.Fatal("a state that is none accepted")
	}
	if err := st.SetBlobEncryption(ctx, store.BlobEncrypting, testKeyring); err != nil {
		t.Fatal(err)
	}
	row, err := st.BlobEncryption(ctx)
	if err != nil || row.State != store.BlobEncrypting || row.Generation != 1 || row.CurrentKey != 0xdeadbeef ||
		row.KeyringID != testKeyring.ID || string(row.Keyring) != string(testKeyring.Keyring) {
		t.Fatalf("row %+v %v", row, err)
	}
	if err := st.SetBlobEncryption(ctx, store.BlobOn, store.KeyringCopy{}); err != nil {
		t.Fatal(err)
	}
	if row, _ = st.BlobEncryption(ctx); row.State != store.BlobOn || row.Generation != 2 || string(row.Keyring) != string(testKeyring.Keyring) {
		t.Fatalf("on: %+v", row)
	}

	// The keyring copy: compare-and-swap on its generation; a new current
	// key is a key change and increments the generation.
	k := testKeyring
	k.Generation, k.Keyring = 2, []byte(`{"format":"y"}`)
	if err := st.UpdateBlobKeyring(ctx, 5, k); !errors.Is(err, store.ErrKeyringChanged) {
		t.Fatalf("stale update: %v", err)
	}
	if err := st.UpdateBlobKeyring(ctx, 1, k); err != nil {
		t.Fatal(err)
	}
	if row, _ = st.BlobEncryption(ctx); row.Generation != 2 || row.KeyringGeneration != 2 || string(row.Keyring) != `{"format":"y"}` {
		t.Fatalf("same current key: %+v", row)
	}
	k.Generation, k.CurrentKey = 3, 7
	if err := st.UpdateBlobKeyring(ctx, 2, k); err != nil {
		t.Fatal(err)
	}
	if row, _ = st.BlobEncryption(ctx); row.Generation != 3 || row.CurrentKey != 7 {
		t.Fatalf("new current key: %+v", row)
	}

	if err := st.ClearBlobEncryption(ctx); err != nil {
		t.Fatal(err)
	}
	if row, err := st.BlobEncryption(ctx); err != nil || row != nil {
		t.Fatalf("off: %+v %v", row, err)
	}
	if err := st.UpdateBlobKeyring(ctx, 3, k); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update without a row: %v", err)
	}
}

// Every transaction that inserts messages compares the generation a writer
// started with: SaveBatch (sync, import), the journal batches (fill and
// journal import share saveIntoJournal) and ReceiveMessage (SMTP), so a
// writer that missed a change cannot commit (revision 5, B1 (c)).
func TestBlobGenerationPin(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	imap := &store.Account{Name: "imap", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &u.ID}
	if err := st.CreateAccount(ctx, imap); err != nil {
		t.Fatal(err)
	}
	folder, err := st.GetOrCreateFolder(ctx, imap.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	smtp := createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")
	journal, err := st.JournalFolderID(ctx, smtp.ID)
	if err != nil {
		t.Fatal(err)
	}
	imp := &store.SMTPImport{AccountID: smtp.ID, Label: "piler", Format: "eml", Source: "/import/piler"}
	if err := st.CreateSMTPImport(ctx, imp); err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	paths := map[string]func(sha byte) error{
		"sync batch": func(sha byte) error {
			_, err := st.SaveBatch(ctx, folder.ID, uint32(sha), []store.MessageMeta{receivedMeta(sha)},
				[]store.Location{{FolderID: folder.ID, UID: uint32(sha), InternalDate: date}})
			return err
		},
		"journal batch": func(sha byte) error {
			_, _, err := st.SaveJournalImportBatch(ctx, journal, imp.ID, []store.MessageMeta{receivedMeta(sha)}, []store.Location{{InternalDate: date}})
			return err
		},
		"smtp delivery": func(sha byte) error {
			_, err := st.ReceiveMessage(ctx, smtp.ID, receivedMeta(sha), envelope("x@example.com"), present)
			return err
		},
	}

	var changes atomic.Int32
	st.PinBlobGeneration(0, func() { changes.Add(1) })
	sha := byte(1)
	for name, insert := range paths {
		if err := insert(sha); err != nil {
			t.Fatalf("%s while the state is unchanged: %v", name, err)
		}
		sha++
	}
	if err := st.SetBlobEncryption(ctx, store.BlobOn, testKeyring); err != nil {
		t.Fatal(err)
	}
	for name, insert := range paths {
		if err := insert(sha); !errors.Is(err, store.ErrBlobStateChanged) {
			t.Fatalf("%s after the change: %v", name, err)
		}
		if ok, err := st.MessageExists(ctx, receivedMeta(sha).SHA256); err != nil || ok {
			t.Fatalf("%s committed the row: %v %v", name, ok, err)
		}
		sha++
	}
	if n := changes.Load(); n != 1 {
		t.Fatalf("onChange ran %d times, want once", n)
	}
	// Pinned to the current generation, inserts work again.
	row, err := st.BlobEncryption(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.PinBlobGeneration(row.Generation, nil)
	for name, insert := range paths {
		if err := insert(sha); err != nil {
			t.Fatalf("%s pinned to the new generation: %v", name, err)
		}
		sha++
	}
}

// 00037 Down refuses while encryption is on or being changed: older
// versions cannot read encrypted files.
func TestBlobEncryptionMigrationDown(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetBlobEncryption(ctx, store.BlobOn, testKeyring); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "run `encryption disable` first") {
		t.Fatalf("down while on: %v", err)
	}
	if err := st.ClearBlobEncryption(ctx); err != nil {
		t.Fatal(err)
	}
	downBlobEncryptionMigration(t, st)
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if row, err := st.BlobEncryption(ctx); err != nil || row != nil {
		t.Fatalf("after up again: %+v %v", row, err)
	}
}

// downBlobEncryptionMigration rolls back 00037_blob_encryption.
func downBlobEncryptionMigration(t *testing.T, st *store.Store) {
	t.Helper()
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00037_blob_encryption") {
		t.Fatalf("down: %q %v", m, err)
	}
}
