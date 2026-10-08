package archive_test

import (
	"context"
	"errors"
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

type reindexEnv struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	conn   *pgx.Conn
	blobs  *blobstore.Store
	owner  int64
	folder int64
	uid    uint32
	log    *slog.Logger
}

func newReindexEnv(t *testing.T) *reindexEnv {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := st.CreateUser(ctx, "owner", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "a", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &owner.ID}
	if err := st.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	folder, err := st.GetOrCreateFolder(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	return &reindexEnv{
		t: t, ctx: ctx, st: st, conn: conn, blobs: blobs, owner: owner.ID, folder: folder.ID,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// store saves a message the way sync does, at the current index version.
func (e *reindexEnv) store(raw string) string {
	e.t.Helper()
	blob, _, err := e.blobs.Put(strings.NewReader(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	meta, err := archive.BuildMeta(e.ctx, e.st, e.blobs, blob, true)
	if err != nil {
		e.t.Fatal(err)
	}
	e.uid++
	loc := store.Location{FolderID: e.folder, UIDValidity: 1, UID: e.uid}
	if _, err := e.st.SaveBatch(e.ctx, e.folder, e.uid, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		e.t.Fatal(err)
	}
	return blob.SHA256
}

// legacy turns a stored message into one archived by an older version.
func (e *reindexEnv) legacy(sha string) {
	e.t.Helper()
	_, err := e.conn.Exec(e.ctx, `UPDATE messages SET body_text = NULL, to_addr = NULL, cc_addr = NULL,
		attachment_names = NULL, has_attachment = false, index_version = 0 WHERE sha256 = $1`, sha)
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *reindexEnv) search(f store.SearchFilter) int {
	e.t.Helper()
	f.Owner, f.Limit = e.owner, 10
	rows, err := e.st.SearchMessages(e.ctx, f)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(rows)
}

func (e *reindexEnv) reindex() int {
	e.t.Helper()
	n, err := archive.Reindex(e.ctx, e.st, e.blobs, e.log)
	if err != nil {
		e.t.Fatalf("Reindex = %d, %v", n, err)
	}
	return n
}

const legacyRaw = "Subject: Alt\r\nTo: Finanzamt Koeln <poststelle@fa.example>\r\nCc: berater@example.com\r\n" +
	"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
	"--b\r\nContent-Type: text/plain\r\n\r\nDer Wartungsvertrag wurde verlaengert.\r\n" +
	"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"Bescheid.pdf\"\r\n\r\nJVBE\r\n--b--\r\n"

func TestBuildMetaFillsIndexData(t *testing.T) {
	e := newReindexEnv(t)
	blob, _, err := e.blobs.Put(strings.NewReader(legacyRaw))
	if err != nil {
		t.Fatal(err)
	}
	m, err := archive.BuildMeta(e.ctx, e.st, e.blobs, blob, true)
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Alt" || m.To != "Finanzamt Koeln <poststelle@fa.example>" || m.Cc != "berater@example.com" ||
		!strings.Contains(m.BodyText, "Wartungsvertrag") || !m.HasAttachment ||
		len(m.AttachmentNames) != 1 || m.AttachmentNames[0] != "Bescheid.pdf" {
		t.Errorf("BuildMeta = %+v", m)
	}
}

func TestReindexFillsLegacyMessages(t *testing.T) {
	e := newReindexEnv(t)
	old := e.store(legacyRaw)
	e.legacy(old)
	e.store("Subject: Neu\r\nTo: poststelle@fa.example\r\n\r\nAktuell.\r\n")

	filters := []store.SearchFilter{
		{Query: "Wartungsvertrag"},
		{To: "Finanzamt"},
		{To: "berater@"},
		{Attachment: "bescheid"},
		{HasAttachment: true},
	}
	for _, f := range filters {
		if n := e.search(f); n != 0 {
			t.Fatalf("%+v: found %d before reindex", f, n)
		}
	}
	if pending, err := e.st.HasUnindexed(e.ctx); err != nil || !pending {
		t.Fatalf("HasUnindexed = %v, %v", pending, err)
	}

	if n := e.reindex(); n != 1 {
		t.Fatalf("Reindex updated %d, want 1 (the current row is skipped)", n)
	}
	for _, f := range filters {
		if n := e.search(f); n != 1 {
			t.Errorf("%+v: found %d after reindex", f, n)
		}
	}
	if n := e.search(store.SearchFilter{To: "poststelle@fa.example"}); n != 2 {
		t.Errorf("to: found %d of old and current message", n)
	}
	if pending, err := e.st.HasUnindexed(e.ctx); err != nil || pending {
		t.Fatalf("HasUnindexed after reindex = %v, %v", pending, err)
	}
	if n := e.reindex(); n != 0 {
		t.Fatalf("second Reindex updated %d", n)
	}
}

func TestReindexSurvivesMissingFile(t *testing.T) {
	e := newReindexEnv(t)
	gone := e.store(legacyRaw)
	e.legacy(gone)
	if _, err := e.conn.Exec(e.ctx, `UPDATE messages SET stored_path = 'missing.eml' WHERE sha256 = $1`, gone); err != nil {
		t.Fatal(err)
	}
	other := e.store("Subject: x\r\nTo: zweiter@example.com\r\n\r\nx\r\n")
	e.legacy(other)

	if n := e.reindex(); n != 2 {
		t.Fatalf("Reindex updated %d, want 2", n)
	}
	var version int16
	var body string
	if err := e.conn.QueryRow(e.ctx, `SELECT index_version, body_text FROM messages WHERE sha256 = $1`, gone).Scan(&version, &body); err != nil {
		t.Fatal(err)
	}
	if version != store.IndexVersion || body != "" {
		t.Errorf("missing file: version %d, body %q", version, body)
	}
	if n := e.search(store.SearchFilter{To: "zweiter"}); n != 1 {
		t.Errorf("the message after the missing one was not indexed")
	}
	if n := e.reindex(); n != 0 {
		t.Fatalf("second Reindex updated %d", n)
	}
}

func TestReindexRunsOnce(t *testing.T) {
	e := newReindexEnv(t)
	e.legacy(e.store(legacyRaw))
	unlock, ok, err := e.st.TryLockReindex(e.ctx)
	if err != nil || !ok {
		t.Fatalf("TryLockReindex = %v, %v", ok, err)
	}
	if _, err := archive.Reindex(e.ctx, e.st, e.blobs, e.log); !errors.Is(err, archive.ErrReindexRunning) {
		t.Fatalf("Reindex while locked: %v", err)
	}
	unlock()
	if n := e.reindex(); n != 1 {
		t.Fatalf("Reindex after unlock updated %d", n)
	}
}

func TestSetIndexDataKeepsNewerRows(t *testing.T) {
	e := newReindexEnv(t)
	// A sync stored this message at the current version while a reindex
	// listed it as pending: the reindex's update must not touch it.
	sha := e.store(legacyRaw)
	n, err := e.st.SetIndexData(e.ctx, []store.Indexed{{SHA256: sha, IndexData: store.IndexData{BodyText: "stale"}}})
	if err != nil || n != 0 {
		t.Fatalf("SetIndexData = %d, %v", n, err)
	}
	if got := e.search(store.SearchFilter{Attachment: "Bescheid"}); got != 1 {
		t.Errorf("current row was overwritten")
	}
	// A sync that stores the message again while it is pending does not
	// downgrade or fill it; ON CONFLICT ignores the row and reindex fills it.
	e.legacy(sha)
	e.store(legacyRaw)
	if n := e.reindex(); n != 1 {
		t.Fatalf("Reindex updated %d", n)
	}
}
