package archive_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

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

const replyRaw = "Message-ID: <r@x>\r\nIn-Reply-To: <o@x>\r\nReferences: <o@x>\r\nSubject: Re: Alt\r\n\r\nJa.\r\n"

// Rows extracted by the previous version (2) get the links between replies.
func TestReindexFillsThreadsAfterVersionBump(t *testing.T) {
	e := newReindexEnv(t)
	sha := e.store(replyRaw)
	e.store("Message-ID: <n@x>\r\nSubject: neu\r\n\r\nx\r\n") // current: not updated
	if _, err := e.conn.Exec(e.ctx, `UPDATE messages SET in_reply_to = NULL, reference_ids = '{}', thread_id = NULL,
		index_version = 2 WHERE sha256 = $1`, sha); err != nil {
		t.Fatal(err)
	}
	if n := e.reindex(); n != 1 {
		t.Fatalf("Reindex updated %d, want 1", n)
	}
	var inReplyTo, threadID string
	var refs []string
	if err := e.conn.QueryRow(e.ctx, `SELECT in_reply_to, reference_ids, thread_id FROM messages WHERE sha256 = $1`, sha).
		Scan(&inReplyTo, &refs, &threadID); err != nil {
		t.Fatal(err)
	}
	if inReplyTo != "o@x" || len(refs) != 1 || refs[0] != "o@x" || threadID != "o@x" {
		t.Errorf("in_reply_to %q, references %v, thread %q", inReplyTo, refs, threadID)
	}
}

// Messages stored before the conversation migration get their links from
// reindex after it.
func TestReindexAfterConversationMigration(t *testing.T) {
	e := newReindexEnv(t)
	if name, err := e.st.MigrateDown(e.ctx); err != nil || !strings.Contains(name, "conversations") {
		t.Fatalf("MigrateDown = %q, %v", name, err)
	}
	blob, _, err := e.blobs.Put(strings.NewReader(replyRaw))
	if err != nil {
		t.Fatal(err)
	}
	// What the previous version stored: no thread columns yet.
	if _, err := e.conn.Exec(e.ctx, `INSERT INTO messages (sha256, size, stored_path, message_id, subject, body_text, index_version)
		VALUES ($1, $2, $3, 'r@x', 'Re: Alt', 'Ja.', 2)`, blob.SHA256, blob.Size, blob.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := e.conn.Exec(e.ctx, `INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid)
		VALUES ($1, $2, 1, 1)`, blob.SHA256, e.folder); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Migrate(e.ctx); err != nil {
		t.Fatal(err)
	}
	if pending, err := e.st.HasUnindexed(e.ctx); err != nil || !pending {
		t.Fatalf("HasUnindexed = %v, %v", pending, err)
	}
	if n := e.reindex(); n != 1 {
		t.Fatalf("Reindex updated %d", n)
	}
	if n := e.search(store.SearchFilter{Thread: "o@x"}); n != 1 {
		t.Errorf("thread o@x lists %d messages", n)
	}
}

// A reindex cancelled in the middle leaves the rest pending; the next run
// finishes it.
func TestReindexContinuesAfterCancel(t *testing.T) {
	e := newReindexEnv(t)
	for i := range 3 {
		e.legacy(e.store(fmt.Sprintf("Message-ID: <c%d@x>\r\nSubject: %d\r\n\r\nx\r\n", i, i)))
	}
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	if _, err := archive.Reindex(ctx, e.st, e.blobs, e.log); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Reindex: %v", err)
	}
	if pending, err := e.st.HasUnindexed(e.ctx); err != nil || !pending {
		t.Fatalf("HasUnindexed after cancel = %v, %v", pending, err)
	}
	if n := e.reindex(); n != 3 {
		t.Fatalf("Reindex after cancel updated %d", n)
	}
}

// TestReindexCost measures reindex on 10,000 messages of about 8 KB body
// text that the previous version (2) extracted: every row is rewritten,
// and the generated full-text column with it. It writes about 100 MB, so it
// runs only with MAIL_ARCHIVE_BENCH=1:
//
//	MAIL_ARCHIVE_BENCH=1 go test -run TestReindexCost -v ./internal/archive/
func TestReindexCost(t *testing.T) {
	if os.Getenv("MAIL_ARCHIVE_BENCH") != "1" {
		t.Skip("set MAIL_ARCHIVE_BENCH=1 to run")
	}
	e := newReindexEnv(t)
	body := strings.Repeat("Sehr geehrte Damen und Herren, anbei das Angebot für die Küche. ", 128)
	const n = 10000
	metas := make([]store.MessageMeta, 0, 500)
	locs := make([]store.Location, 0, 500)
	for i := range n {
		raw := fmt.Sprintf("Message-ID: <m%d@x>\r\nIn-Reply-To: <m%d@x>\r\nSubject: Angebot %d\r\n\r\n%s\r\n", i, i-i%5, i, body)
		blob, _, err := e.blobs.Put(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		meta, err := archive.BuildMeta(e.ctx, e.st, e.blobs, blob, true)
		if err != nil {
			t.Fatal(err)
		}
		e.uid++
		metas = append(metas, meta)
		locs = append(locs, store.Location{FolderID: e.folder, UIDValidity: 1, UID: e.uid})
		if len(metas) == cap(metas) || i == n-1 {
			if _, err := e.st.SaveBatch(e.ctx, e.folder, e.uid, metas, locs); err != nil {
				t.Fatal(err)
			}
			metas, locs = metas[:0], locs[:0]
		}
	}
	for _, sql := range []string{`UPDATE messages SET index_version = 2`, `VACUUM ANALYZE messages`} {
		if _, err := e.conn.Exec(e.ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	if got := e.reindex(); got != n {
		t.Fatalf("Reindex updated %d", got)
	}
	t.Logf("reindex of %d messages: %v (%.1f ms per message)", n, time.Since(start), float64(time.Since(start).Milliseconds())/n)
}
