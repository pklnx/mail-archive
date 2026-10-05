package archive_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestReindexFillsLegacyMessages(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "a", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}}
	if err := st.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	folder, err := st.GetOrCreateFolder(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	raw := "Subject: Alt\r\nContent-Type: text/plain\r\n\r\nDer Wartungsvertrag wurde verlaengert.\r\n"
	blob, _, err := blobs.Put(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	meta := store.MessageMeta{SHA256: blob.SHA256, Size: blob.Size, StoredPath: blob.Path, Subject: "Alt"}
	loc := store.Location{FolderID: folder.ID, UIDValidity: 1, UID: 1}
	if _, err := st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		t.Fatal(err)
	}
	// Simulate a message archived before full-text search existed.
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `UPDATE messages SET body_text = NULL`); err != nil {
		t.Fatal(err)
	}
	search := func() int {
		rows, err := st.SearchMessages(ctx, store.SearchFilter{Query: "Wartungsvertrag", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if n := search(); n != 0 {
		t.Fatalf("found %d before reindex", n)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if n, err := archive.Reindex(ctx, st, blobs, log); err != nil || n != 1 {
		t.Fatalf("Reindex = %d, %v", n, err)
	}
	if n := search(); n != 1 {
		t.Fatalf("found %d after reindex", n)
	}
	if n, err := archive.Reindex(ctx, st, blobs, log); err != nil || n != 0 {
		t.Fatalf("second Reindex = %d, %v", n, err)
	}
}
