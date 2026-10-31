package store_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

func (f *searchFixture) texts(sha string, texts ...store.AttachmentText) {
	f.t.Helper()
	ok, err := f.st.SaveAttachmentTexts(f.ctx, sha, texts)
	if err != nil || !ok {
		f.t.Fatalf("save texts of %s: %v, %v", sha, ok, err)
	}
}

func pdfText(part int, text string) store.AttachmentText {
	return store.AttachmentText{Part: part, Filename: "Rechnung.pdf", Status: "ok", Text: text}
}

func withAttachment(m searchMsg) searchMsg {
	m.data.HasAttachment = true
	m.data.AttachmentNames = []string{"Rechnung.pdf"}
	return m
}

// Search finds words in the text of attachments, by words (stemmed) and
// per document: all words in the message, or all in one attachment.
func TestSearchAttachmentText(t *testing.T) {
	f := newSearchFixture(t)
	f.add("privat/INBOX", withAttachment(searchMsg{sha: "pdf", subject: "Post", from: "mueller@example.com", sent: "2026-01-03T00:00:00Z",
		body: "Sehr geehrte Damen und Herren, anbei der Entwurf (draft).", data: store.IndexData{ThreadID: "t1"}}))
	f.texts("pdf", pdfText(1, "Rechnung Nummer 4711 über Wartungsvertrag"), pdfText(2, "Lieferschein Paletten"))
	f.add("privat/INBOX", searchMsg{sha: "body", subject: "Re: Post", from: "anna@example.com", sent: "2026-01-04T00:00:00Z",
		body: "Die Rechnung kam an.", data: store.IndexData{ThreadID: "t1"}})
	f.add("arbeit/INBOX", searchMsg{sha: "plain", subject: "Termin", from: "mueller@example.com", sent: "2026-01-02T00:00:00Z", body: "Termin am Montag"})

	// Another user's PDF with the same words is never found.
	other, err := f.st.CreateUser(f.ctx, "bob", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "bob", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &other.ID}
	if err := f.st.CreateAccount(f.ctx, acc); err != nil {
		t.Fatal(err)
	}
	fo, err := f.st.GetOrCreateFolder(f.ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	f.folders["bob/INBOX"] = fo.ID
	f.add("bob/INBOX", withAttachment(searchMsg{sha: "bobs", subject: "x", from: "x@example.com", sent: "2026-01-05T00:00:00Z", body: "x"}))
	f.texts("bobs", pdfText(1, "Wartungsvertrag Geheimvertrag Rechnung"))

	cases := []struct {
		name string
		f    store.SearchFilter
		want []string
	}{
		{"word only in the PDF", store.SearchFilter{Query: "Wartungsvertrag"}, []string{"pdf"}},
		{"stemmed", store.SearchFilter{Query: "Rechnungen"}, []string{"body", "pdf"}},
		{"phrase in the PDF", store.SearchFilter{Query: `"Nummer 4711"`}, []string{"pdf"}},
		{"phrase across attachments", store.SearchFilter{Query: `"Wartungsvertrag Lieferschein"`}, []string{}},
		{"other user's PDF", store.SearchFilter{Query: "Geheimvertrag"}, []string{}},
		{"from and a word in the PDF", store.SearchFilter{From: "mueller", Query: "Lieferschein"}, []string{"pdf"}},
		// Per document: sender and PDF, or two attachments, do not combine.
		{"sender and PDF", store.SearchFilter{Query: "mueller Lieferschein"}, []string{}},
		{"two attachments", store.SearchFilter{Query: "Wartungsvertrag Lieferschein"}, []string{}},
		// Negation per document too: the body says draft, the PDF has the
		// word and not draft.
		{"negation", store.SearchFilter{Query: "Wartungsvertrag -draft"}, []string{"pdf"}},
		{"negation in the message", store.SearchFilter{Query: "Entwurf -draft"}, []string{}},
		// attachment: still matches file names only.
		{"attachment: names only", store.SearchFilter{Attachment: "Wartungsvertrag"}, []string{}},
		{"attachment: name", store.SearchFilter{Attachment: "rechnung", Query: "Paletten"}, []string{"pdf"}},
	}
	for _, walk := range []bool{false, true} {
		if walk {
			// With more matches than the list takes, each message's
			// attachments are checked instead: the same results.
			defer store.SetMaxAttachmentMatches(0)()
		}
		for _, c := range cases {
			if got := f.search(c.f); !slices.Equal(got, c.want) {
				t.Errorf("walk %v, %s: got %v, want %v", walk, c.name, got, c.want)
			}
		}
		// Threads count matches in attachments.
		rows, err := f.st.SearchThreads(f.ctx, store.SearchFilter{Scope: storetest.Viewer(t, f.st, f.owner), Query: "Rechnung", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Count != 2 {
			t.Errorf("walk %v, threads: %+v", walk, rows)
		}
	}
}

// Of two writers of the same message (a retried job, reindex beside the
// web server) only the first stores its texts; a message deleted meanwhile
// is no error; retention and an account purge delete texts, state and
// jobs with the message.
func TestSaveAttachmentTexts(t *testing.T) {
	f := newSearchFixture(t)
	f.add("privat/INBOX", withAttachment(searchMsg{sha: "a", subject: "a", sent: "2026-01-01T00:00:00Z", body: "x"}))
	f.texts("a", pdfText(1, "erster"))
	if ok, err := f.st.SaveAttachmentTexts(f.ctx, "a", []store.AttachmentText{pdfText(1, "zweiter")}); ok || err != nil {
		t.Fatalf("second writer: %v, %v", ok, err)
	}
	if got := f.search(store.SearchFilter{Query: "zweiter"}); len(got) != 0 {
		t.Errorf("the second writer's text was stored")
	}
	if ok, err := f.st.SaveAttachmentTexts(f.ctx, strings.Repeat("f", 64), []store.AttachmentText{pdfText(1, "x")}); ok || err != nil {
		t.Fatalf("gone message: %v, %v", ok, err)
	}
	if done, err := f.st.AttachmentIndexed(f.ctx, "a"); !done || err != nil {
		t.Errorf("indexed: %v, %v", done, err)
	}
}

// A sync batch of 200 messages with attachments queues 200 jobs for new
// mail in its transaction and wakes the listeners once.
func TestSaveBatchQueuesAttachmentText(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "u", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "a", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &u.ID}
	if err := st.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	fo, err := st.GetOrCreateFolder(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	listen, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listen.Close(ctx) }()
	if _, err := listen.Exec(ctx, "LISTEN "+store.JobsChannel); err != nil {
		t.Fatal(err)
	}
	var metas []store.MessageMeta
	var locs []store.Location
	for i := range 201 {
		sha := strings.Repeat("0", 61) + string(rune('0'+i/100)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10))
		meta := store.MessageMeta{SHA256: sha, Size: 1, StoredPath: sha, IndexData: store.IndexData{HasAttachment: i < 200}}
		metas = append(metas, meta)
		locs = append(locs, store.Location{FolderID: fo.ID, UIDValidity: 1, UID: uint32(i + 1)})
	}
	if _, err := st.SaveBatch(ctx, fo.ID, 201, metas, locs); err != nil {
		t.Fatal(err)
	}
	// Storing the same messages again queues nothing.
	if _, err := st.SaveBatch(ctx, fo.ID, 201, metas, locs); err != nil {
		t.Fatal(err)
	}
	// A sentinel from a later transaction: notifications arrive in commit
	// order, so everything before it has arrived when it does.
	if _, err := st.EnqueueJobs(ctx, []store.NewJob{{Kind: "sentinel", Key: "s", MaxAttempts: 1}}, false); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	wctx, cancel := context.WithTimeout(ctx, testwait.Limit(t))
	defer cancel()
	for got["sentinel"] == 0 {
		n, err := listen.WaitForNotification(wctx)
		if err != nil {
			t.Fatalf("notifications %v: %v", got, err)
		}
		got[n.Payload]++
	}
	if got[store.JobAttachmentText] != 1 {
		t.Errorf("notifications %v", got)
	}
	var n, prio int
	if err := listen.QueryRow(ctx, `SELECT count(*), max(priority) FROM jobs WHERE kind = $1 AND state = 'queued'`, store.JobAttachmentText).
		Scan(&n, &prio); err != nil {
		t.Fatal(err)
	}
	if n != 200 || prio != store.PriorityNew {
		t.Errorf("%d jobs, priority %d", n, prio)
	}
}

// attachmentRows gives a message texts, a state and a queued job.
func attachmentRows(t *testing.T, st *store.Store, sha string) {
	t.Helper()
	ctx := context.Background()
	if ok, err := st.SaveAttachmentTexts(ctx, sha, []store.AttachmentText{pdfText(1, "x")}); err != nil || !ok {
		t.Fatalf("texts: %v, %v", ok, err)
	}
	if _, err := st.EnqueueJobs(ctx, []store.NewJob{{Kind: store.JobAttachmentText, Key: sha, MessageSHA256: sha, MaxAttempts: 3}}, false); err != nil {
		t.Fatal(err)
	}
}

func attachmentRowsLeft(t *testing.T, st *store.Store, sha string) {
	t.Helper()
	for _, table := range []string{"attachment_texts", "attachment_index_state", "jobs"} {
		var n int
		if err := st.QueryRowForTest(context.Background(), "SELECT count(*) FROM "+table+" WHERE message_sha256 = $1", sha).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d rows of the deleted message left in %s", n, table)
		}
	}
}

// Regression: a retention deletion run leaves no attachment texts, state
// or jobs of the deleted messages (the cascade).
func TestRetentionDeletesAttachmentRows(t *testing.T) {
	f, _ := expiredFixture(t)
	attachmentRows(t, f.st, hashOf('a'))
	attachmentRows(t, f.st, hashOf('b'))
	run := f.propose()
	f.confirm(run.ID)
	if deleted, _, err := f.st.DeleteBatch(f.ctx, run.ID, f.candidates(run.ID), time.Hour); err != nil || deleted != 2 {
		t.Fatalf("batch: %d, %v", deleted, err)
	}
	attachmentRowsLeft(t, f.st, hashOf('a'))
	attachmentRowsLeft(t, f.st, hashOf('b'))
}

// Regression: an account purge leaves no attachment texts, state or jobs
// of the messages it deleted, and keeps those of shared ones.
func TestPurgeDeletesAttachmentRows(t *testing.T) {
	f := newPurgeFixture(t)
	attachmentRows(t, f.st, f.m1)
	attachmentRows(t, f.st, f.m2)
	p, err := f.st.ConfirmPurge(store.WithAudit(f.ctx, &store.AuditEntry{Actor: store.ActorCLI, Action: "account.purge"}), f.acc.Ref(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(p.ID, 500, time.Hour); err != nil {
		t.Fatal(err)
	}
	attachmentRowsLeft(t, f.st, f.m1)
	var n int
	if err := f.st.QueryRowForTest(f.ctx, "SELECT count(*) FROM attachment_texts WHERE message_sha256 = $1", f.m2).Scan(&n); err != nil || n != 1 {
		t.Errorf("shared message's texts: %d, %v", n, err)
	}
}

// downAttachmentTextMigration rolls back 00033_attachment_text.
func downAttachmentTextMigration(t *testing.T, st *store.Store) {
	t.Helper()
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00033_attachment_text") {
		t.Fatalf("down: %q %v", m, err)
	}
}

// TestAttachmentTextMigration: 00033 down drops both tables, up creates
// them again; and creating them while another connection keeps inserting
// messages makes no insert wait past a lock_timeout of 1 s.
func TestAttachmentTextMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	downAttachmentTextMigration(t, st)
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('attachment_texts') IS NOT NULL OR to_regclass('attachment_index_state') IS NOT NULL").
		Scan(&exists); err != nil || exists {
		t.Fatalf("tables after down: %v %v", exists, err)
	}
	writer := connect(t, url)
	if _, err := writer.Exec(ctx, "SET lock_timeout = '1s'"); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	var inserted testwait.Signal
	var count atomic.Int64
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if _, err := writer.Exec(ctx, `INSERT INTO messages (sha256, size, stored_path, has_attachment) VALUES (lpad($1::text, 64, '0'), 1, 'x', true)`,
				fmt.Sprint(i)); err != nil {
				done <- err
				return
			}
			count.Add(1)
			inserted.Notify()
		}
	}()
	failed := func() bool {
		select {
		case err := <-done:
			t.Fatalf("an insert failed during the migration: %v", err)
		default:
		}
		return false
	}
	testwait.Until(t, &inserted, "an insert before the migration", func() bool { return failed() || count.Load() > 0 })
	start := time.Now()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("migration took %v", time.Since(start))
	after := count.Load()
	testwait.Until(t, &inserted, "an insert after the migration", func() bool { return failed() || count.Load() > after })
	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("an insert failed during the migration: %v", err)
	}
	if ok, err := st.SaveAttachmentTexts(ctx, fmt.Sprintf("%064d", 0), []store.AttachmentText{pdfText(0, "nach der Migration")}); err != nil || !ok {
		t.Fatalf("after up: %v, %v", ok, err)
	}
}
