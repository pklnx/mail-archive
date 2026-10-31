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
	// A message whose body says draft and whose PDF has no text layer: an
	// attachment row without text matches no query, not even a negation.
	f.add("arbeit/INBOX", withAttachment(searchMsg{sha: "scan", subject: "Scan", from: "x@example.com", sent: "2026-01-01T00:00:00Z", body: "draft"}))
	f.texts("scan", store.AttachmentText{Part: 1, Filename: "scan.pdf", Status: "no_text"})
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
		if got := f.search(store.SearchFilter{Query: "-draft"}); slices.Contains(got, "scan") || !slices.Contains(got, "plain") {
			t.Errorf("walk %v, only a negation: got %v", walk, got)
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

// A row that matched by an attachment only names the matching attachment
// with the lowest part index and shows a snippet of its text; a row whose
// subject, sender or body matched keeps its body snippet (#390, part 4).
func TestSearchMatchedAttachment(t *testing.T) {
	f := newSearchFixture(t)
	f.add("privat/INBOX", withAttachment(searchMsg{sha: "pdf", subject: "Post", from: "x@example.com", sent: "2026-01-03T00:00:00Z",
		body: "Anbei die Unterlagen.", data: store.IndexData{ThreadID: "t1"}}))
	f.texts("pdf",
		store.AttachmentText{Part: 2, Filename: "scan.pdf", Status: "no_text"},
		store.AttachmentText{Part: 3, Filename: "Rechnung-2026-10.pdf", Status: "ok", Text: "Wartungsvertrag Rechnungsbetrag 1.234,56 Euro"},
		store.AttachmentText{Part: 5, Filename: "Anlage.pdf", Status: "ok", Text: "Wartungsvertrag Lieferschein"})
	// The body matches too: the body snippet, no attachment.
	f.add("privat/INBOX", withAttachment(searchMsg{sha: "body", subject: "Re: Post", from: "y@example.com", sent: "2026-01-04T00:00:00Z",
		body: "Der Wartungsvertrag liegt bei.", data: store.IndexData{ThreadID: "t2"}}))
	f.texts("body", store.AttachmentText{Part: 2, Filename: "Vertrag.pdf", Status: "ok", Text: "Wartungsvertrag"})
	// The subject matches as a substring.
	f.add("arbeit/INBOX", withAttachment(searchMsg{sha: "subj", subject: "Wartungsvertragsentwurf", from: "z@example.com", sent: "2026-01-02T00:00:00Z",
		body: "Hallo", data: store.IndexData{ThreadID: "t3"}}))
	f.texts("subj", store.AttachmentText{Part: 2, Filename: "Entwurf.pdf", Status: "ok", Text: "Wartungsvertrag"})

	mark := func(word string) string { return store.HighlightStart + word + store.HighlightEnd }
	check := func(name string, rows []store.MessageSummary, want map[string]*store.MatchedAttachment, snippet map[string]string) {
		t.Helper()
		for _, r := range rows {
			sha := strings.TrimSpace(r.SHA256)
			w, ok := want[sha]
			if !ok {
				t.Errorf("%s: unexpected row %s", name, sha)
				continue
			}
			if (w == nil) != (r.MatchedAttachment == nil) || w != nil && *w != *r.MatchedAttachment {
				t.Errorf("%s, %s: matched attachment %+v, want %+v", name, sha, r.MatchedAttachment, w)
			}
			if !strings.Contains(r.Snippet, snippet[sha]) {
				t.Errorf("%s, %s: snippet %q does not contain %q", name, sha, r.Snippet, snippet[sha])
			}
		}
		if len(rows) != len(want) {
			t.Errorf("%s: %d rows, want %d", name, len(rows), len(want))
		}
	}
	viewer := storetest.Viewer(t, f.st, f.owner)
	for _, walk := range []bool{false, true} {
		if walk {
			defer store.SetMaxAttachmentMatches(0)()
		}
		for _, c := range []struct {
			query   string
			want    map[string]*store.MatchedAttachment
			snippet map[string]string
		}{
			// Two attachments match: the lower part index.
			{"Wartungsvertrag",
				map[string]*store.MatchedAttachment{"pdf": {Part: 3, Filename: "Rechnung-2026-10.pdf"}, "body": nil, "subj": nil},
				map[string]string{"pdf": mark("Wartungsvertrag") + " Rechnungsbetrag", "body": mark("Wartungsvertrag") + " liegt", "subj": "Hallo"}},
			// Only the second one matches.
			{"Lieferschein",
				map[string]*store.MatchedAttachment{"pdf": {Part: 5, Filename: "Anlage.pdf"}},
				map[string]string{"pdf": mark("Lieferschein")}},
		} {
			for _, grouped := range []bool{false, true} {
				search := f.st.SearchMessages
				if grouped {
					search = f.st.SearchThreads
				}
				rows, err := search(f.ctx, store.SearchFilter{Scope: viewer, Query: c.query, Limit: 10})
				if err != nil {
					t.Fatal(err)
				}
				check(fmt.Sprintf("walk %v, grouped %v, %s", walk, grouped, c.query), rows, c.want, c.snippet)
			}
		}
	}
	// Without a query: no attachment, the start of the body.
	rows, err := f.st.SearchMessages(f.ctx, store.SearchFilter{Scope: viewer, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	check("no query", rows, map[string]*store.MatchedAttachment{"pdf": nil, "body": nil, "subj": nil},
		map[string]string{"pdf": "Anbei die Unterlagen.", "body": "Der Wartungsvertrag", "subj": "Hallo"})

	// The snippet step itself sees only the scope's messages: another
	// user gets nothing for this message's hash.
	other, err := f.st.CreateUser(f.ctx, "bob", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		scope store.Scope
		want  bool
	}{{"owner", viewer, true}, {"other user", storetest.Viewer(t, f.st, other.ID), false}} {
		got, err := f.st.AttachmentSnippetsOf(f.ctx, c.scope, "Wartungsvertrag", []string{"pdf"})
		if err != nil {
			t.Fatal(err)
		}
		if (got[0].MatchedAttachment != nil) != c.want || (got[0].Snippet != "") != c.want {
			t.Errorf("%s: %+v", c.name, got[0])
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

type attachmentJob struct {
	State, Message string
	Priority       int
}

// attachmentJobs returns the attachment_text jobs by key.
func attachmentJobs(t *testing.T, conn *pgx.Conn) map[string]attachmentJob {
	t.Helper()
	rows, err := conn.Query(context.Background(), `SELECT key, state, priority, coalesce(message_sha256, '') FROM jobs WHERE kind = $1`, store.JobAttachmentText)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]attachmentJob{}
	for rows.Next() {
		var key string
		var j attachmentJob
		if err := rows.Scan(&key, &j.State, &j.Priority, &j.Message); err != nil {
			t.Fatal(err)
		}
		out[key] = j
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func withAttachmentMeta(m store.MessageMeta) store.MessageMeta {
	m.HasAttachment = true
	return m
}

// wantOneNewJob checks that exactly the message sha has an attachment_text
// job, queued for new mail with its message set, and marks it done, so
// that a later enqueue would show (it queues a done job again).
func wantOneNewJob(t *testing.T, conn *pgx.Conn, sha string) {
	t.Helper()
	jobs := attachmentJobs(t, conn)
	if j := jobs[sha]; len(jobs) != 1 || j.State != store.JobQueued || j.Priority != store.PriorityNew || j.Message != sha {
		t.Fatalf("jobs %+v, want one queued for new mail for %s", jobs, sha)
	}
	if _, err := conn.Exec(context.Background(), `UPDATE jobs SET state = 'done', finished_at = now() WHERE key = $1`, sha); err != nil {
		t.Fatal(err)
	}
}

// ReceiveMessage queues the attachment text of a new message with
// attachments in its transaction; a duplicate delivery (the same folder, or
// another smtp account) and a message without attachments queue nothing.
func TestReceiveMessageQueuesAttachmentText(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	a := createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")
	b := createSMTPAccount(t, st, &u.ID, "other", "other@example.com")
	with, without := withAttachmentMeta(receivedMeta(1)), receivedMeta(2)
	if _, err := st.ReceiveMessage(ctx, a.ID, with, envelope("x@example.com"), present); err != nil {
		t.Fatal(err)
	}
	wantOneNewJob(t, conn, with.SHA256)
	if _, err := st.ReceiveMessage(ctx, a.ID, without, envelope("x@example.com"), present); err != nil {
		t.Fatal(err)
	}
	for _, dup := range []struct {
		account int64
		rcpt    string
	}{{a.ID, "y@example.com"}, {b.ID, "x@example.com"}} {
		if _, err := st.ReceiveMessage(ctx, dup.account, with, envelope(dup.rcpt), present); err != nil {
			t.Fatal(err)
		}
	}
	jobs := attachmentJobs(t, conn)
	if len(jobs) != 1 || jobs[with.SHA256].State != store.JobDone {
		t.Errorf("after the duplicates and a message without attachments: %+v", jobs)
	}
}

// A fill queues the attachment text of a new message with attachments in
// the transaction of its batch; a message already there and one without
// attachments queue nothing.
func TestFillQueuesAttachmentText(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
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
	with, without := withAttachmentMeta(receivedMeta(1)), receivedMeta(2)
	if _, err := st.SaveFillBatch(ctx, ff, []store.MessageMeta{with, without}, make([]store.Location, 2)); err != nil {
		t.Fatal(err)
	}
	wantOneNewJob(t, conn, with.SHA256)
	// The same message again, from the same and from another folder.
	sent, err := st.FillFolder(ctx, f, "Sent")
	if err != nil {
		t.Fatal(err)
	}
	for _, folder := range []*store.FillFolder{ff, sent} {
		if _, err := st.SaveFillBatch(ctx, folder, []store.MessageMeta{with}, make([]store.Location, 1)); err != nil {
			t.Fatal(err)
		}
	}
	jobs := attachmentJobs(t, conn)
	if len(jobs) != 1 || jobs[with.SHA256].State != store.JobDone {
		t.Errorf("after the duplicates and a message without attachments: %+v", jobs)
	}
}
