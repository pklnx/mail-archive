package archive_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/attachtext"
	"github.com/pklnx/mail-archive/internal/attachtext/pdftest"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/jobs"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// fakeExtractor records the subjects of the messages it was given and
// answers with fn.
type fakeExtractor struct {
	mu       sync.Mutex
	subjects []string
	fn       func(ctx context.Context, subject string) ([]attachtext.Result, error)
}

func (f *fakeExtractor) Message(ctx context.Context, r io.Reader) ([]attachtext.Result, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	subject := ""
	for _, l := range strings.Split(string(raw), "\r\n") {
		if s, ok := strings.CutPrefix(l, "Subject: "); ok {
			subject = s
			break
		}
	}
	f.mu.Lock()
	f.subjects = append(f.subjects, subject)
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(ctx, subject)
	}
	return []attachtext.Result{{Part: 1, Filename: "a.pdf", Status: attachtext.StatusOK, Text: "text of " + subject}}, nil
}

func (f *fakeExtractor) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.subjects...)
}

func (e *reindexEnv) indexer(x archive.AttachmentExtractor) *archive.AttachmentIndexer {
	return &archive.AttachmentIndexer{Store: e.st, Blobs: e.blobs, Extractor: x, Logger: e.log}
}

// work runs the attachment kinds until none is due.
func (e *reindexEnv) work(x *archive.AttachmentIndexer, workers int) {
	e.t.Helper()
	reg := &jobs.Registry{}
	if err := x.Register(reg); err != nil {
		e.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- (&jobs.Pool{Store: e.st, Kinds: reg, Workers: workers, Logger: e.log, UntilIdle: true}).Run(e.ctx)
	}()
	select {
	case err := <-done:
		if err != nil {
			e.t.Fatal(err)
		}
	case <-time.After(testwait.Limit(e.t)):
		e.t.Fatal("the pool did not finish")
	}
}

func (e *reindexEnv) backfill() {
	e.t.Helper()
	if _, err := e.st.EnqueueJobs(e.ctx, []store.NewJob{store.AttachmentBackfillJob()}, false); err != nil {
		e.t.Fatal(err)
	}
}

func (e *reindexEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.conn.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *reindexEnv) job(key string) (state string, priority int, lastErr string) {
	e.t.Helper()
	var le *string
	if err := e.conn.QueryRow(e.ctx, `SELECT state, priority, last_error FROM jobs WHERE kind = 'attachment_text' AND key = $1`, key).
		Scan(&state, &priority, &le); err != nil {
		e.t.Fatal(err)
	}
	if le != nil {
		lastErr = *le
	}
	return state, priority, lastErr
}

func withPDF(subject, text string) string {
	return fmt.Sprintf("Subject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n"+
		"--b\r\nContent-Type: text/plain\r\n\r\nSiehe Anhang.\r\n"+
		"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"Rechnung.pdf\"\r\n"+
		"Content-Transfer-Encoding: base64\r\n\r\n%s\r\n--b--\r\n", subject, base64.StdEncoding.EncodeToString(pdftest.Text(text)))
}

// seed inserts n messages with attachments that all point to the blob of
// raw, with sort_at from at(i), as an older archive.
func (e *reindexEnv) seed(raw string, n int, at func(i int) time.Time) []string {
	e.t.Helper()
	blob, _, err := e.blobs.Put(strings.NewReader(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	shas := make([]string, n)
	times := make([]time.Time, n)
	for i := range n {
		shas[i] = fmt.Sprintf("%064x", i+1)
		times[i] = at(i)
	}
	_, err = e.conn.Exec(e.ctx, `INSERT INTO messages (sha256, size, stored_path, subject, sent_at, has_attachment, index_version)
		SELECT s, 100, $3, 'seeded', t, true, 3 FROM unnest($1::text[], $2::timestamptz[]) AS x (s, t)`, shas, times, blobstore.RelPath(blob.SHA256))
	if err != nil {
		e.t.Fatal(err)
	}
	return shas
}

// End to end with the real extractor: a message stored with a PDF is
// queued for new mail, its PDF text is stored, and a word only in the PDF
// finds the message; the message rows are not written.
func TestAttachmentTextEndToEnd(t *testing.T) {
	e := newReindexEnv(t)
	sha := e.store(withPDF("Ohne Treffer", "Zahlungserinnerung Kundennummer 4711"))
	if state, prio, _ := e.job(sha); state != store.JobQueued || prio != store.PriorityNew {
		t.Fatalf("job %s, priority %d", state, prio)
	}
	if n := e.search(store.SearchFilter{Query: "Zahlungserinnerung"}); n != 0 {
		t.Fatalf("found before extraction: %d", n)
	}
	before := e.count(`SELECT xmin::text::bigint FROM messages WHERE sha256 = $1`, sha)
	ex := attachtext.New(e.log)
	t.Cleanup(func() { _ = ex.Close() })
	x := e.indexer(ex)
	e.work(x, 1)
	if n := e.search(store.SearchFilter{Query: "Zahlungserinnerung"}); n != 1 {
		t.Errorf("found %d by the PDF's text", n)
	}
	if n := e.search(store.SearchFilter{Query: "Zahlungserinnerung Siehe"}); n != 0 {
		t.Errorf("words from the body and the PDF matched together: %d", n)
	}
	if x.Extracted() != 1 {
		t.Errorf("extracted %d", x.Extracted())
	}
	if after := e.count(`SELECT xmin::text::bigint FROM messages WHERE sha256 = $1`, sha); after != before {
		t.Error("the message row was written")
	}
	if state, _, _ := e.job(sha); state != store.JobDone {
		t.Errorf("job %s", state)
	}
}

// A message whose file cannot be read is done without text, so that the
// backfill does not queue it again; and a message that was deleted is
// done without error.
func TestAttachmentTextUnreadableAndGone(t *testing.T) {
	e := newReindexEnv(t)
	sha := e.store(withPDF("weg", "x"))
	e.store(withPDF("lesbar", "y"))
	if err := e.blobs.Remove(sha); err != nil {
		t.Fatal(err)
	}
	// The job of a message deleted since (its row is gone, so the job has
	// no message reference any more).
	gone := strings.Repeat("e", 64)
	if _, err := e.st.EnqueueJobs(e.ctx, []store.NewJob{{Kind: store.JobAttachmentText, Key: gone, MaxAttempts: 3}}, false); err != nil {
		t.Fatal(err)
	}
	fake := &fakeExtractor{}
	e.work(e.indexer(fake), 1)
	if state, _, _ := e.job(gone); state != store.JobDone {
		t.Errorf("job of a deleted message: %s", state)
	}
	if got := fake.calls(); len(got) != 1 || got[0] != "lesbar" {
		t.Fatalf("extracted %q", got)
	}
	if n := e.count(`SELECT count(*) FROM attachment_index_state WHERE message_sha256 = $1 AND version = $2`, sha, store.AttachmentIndexVersion); n != 1 {
		t.Error("the unreadable message is not done")
	}
	if n := e.count(`SELECT count(*) FROM attachment_texts WHERE message_sha256 = $1`, sha); n != 0 {
		t.Errorf("%d texts of the unreadable message", n)
	}
	n, err := e.st.CountAttachmentPending(e.ctx)
	if err != nil || n != 0 {
		t.Errorf("pending %d, %v", n, err)
	}
}

// The crash-loop guard: a message whose extraction ended the process
// three times (its lease expired each time) fails, and its extractor is
// not called again; jobs retry queues it again.
func TestAttachmentTextCrashLoopGuard(t *testing.T) {
	e := newReindexEnv(t)
	sha := e.store(withPDF("absturz", "x"))
	kinds := []string{store.JobAttachmentText}
	// Three times: the guard of revision 1, now the queue's attempts.
	for i := range 3 {
		claimed, err := e.st.ClaimJobs(e.ctx, kinds, []byte{byte(i + 1)}, time.Minute, 1)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("claim %d: %v, %v", i, claimed, err)
		}
		// The process died: its lease runs out, and a pool reaps it.
		if _, err := e.conn.Exec(e.ctx, `UPDATE jobs SET lease_until = now() - interval '1 second', run_after = now() WHERE key = $1`, sha); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.ReapExpiredJobs(e.ctx, store.JobAttachmentText, store.JobBackoff{First: time.Millisecond, Cap: time.Millisecond}, nil, 10); err != nil {
			t.Fatal(err)
		}
		if _, err := e.conn.Exec(e.ctx, `UPDATE jobs SET run_after = now() WHERE key = $1 AND state = 'queued'`, sha); err != nil {
			t.Fatal(err)
		}
	}
	if state, _, code := e.job(sha); state != store.JobFailed || code != jobs.CodeLeaseExpired {
		t.Fatalf("job %s (%s)", state, code)
	}
	fake := &fakeExtractor{}
	e.backfill()
	e.work(e.indexer(fake), 1)
	if got := fake.calls(); len(got) != 0 {
		t.Errorf("extracted again: %q", got)
	}
	if n, err := e.st.RetryFailedJobs(e.ctx, store.JobAttachmentText); err != nil || n != 1 {
		t.Fatalf("retry: %d, %v", n, err)
	}
	e.work(e.indexer(fake), 1)
	if got := fake.calls(); len(got) != 1 {
		t.Errorf("after retry: %q", got)
	}
}

// A shutdown during extraction leaves the message pending, and the job is
// released without counting the attempt, also three times in a row.
func TestAttachmentTextShutdown(t *testing.T) {
	e := newReindexEnv(t)
	sha := e.store(withPDF("shutdown", "x"))
	var started testwait.Signal
	fake := &fakeExtractor{fn: func(ctx context.Context, _ string) ([]attachtext.Result, error) {
		started.Notify()
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	x := e.indexer(fake)
	reg := &jobs.Registry{}
	if err := x.Register(reg); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		ctx, cancel := context.WithCancel(e.ctx)
		woken := started.C()
		done := make(chan error, 1)
		go func() { done <- (&jobs.Pool{Store: e.st, Kinds: reg, Workers: 1, Logger: e.log}).Run(ctx) }()
		select {
		case <-woken:
		case <-time.After(testwait.Limit(t)):
			t.Fatal("not started")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(testwait.Limit(t)):
			t.Fatal("Run did not return")
		}
		var state string
		var attempts int
		if err := e.conn.QueryRow(e.ctx, `SELECT state, attempts FROM jobs WHERE key = $1`, sha).Scan(&state, &attempts); err != nil {
			t.Fatal(err)
		}
		if state != store.JobQueued || attempts != 0 {
			t.Fatalf("round %d: %s, %d attempts", i, state, attempts)
		}
	}
	if n := e.count(`SELECT count(*) FROM attachment_index_state`); n != 0 {
		t.Error("marked done at shutdown")
	}
}

// Results of the extraction are statuses, not errors: a message whose
// deadline passed is done with its timeout rows. A PDF module that did not
// compile fails the job at once with its code.
func TestAttachmentTextStatusesAndUnavailable(t *testing.T) {
	e := newReindexEnv(t)
	late := e.store(withPDF("spät", "x"))
	broken := e.store(withPDF("kaputt", "y"))
	fake := &fakeExtractor{fn: func(_ context.Context, subject string) ([]attachtext.Result, error) {
		if subject == "kaputt" {
			return nil, fmt.Errorf("pdf: %w", attachtext.ErrUnavailable)
		}
		return []attachtext.Result{{Part: 1, Filename: "a.pdf", Status: attachtext.StatusTimeout}, {Part: 2, Filename: "b.txt", Status: attachtext.StatusTimeout}}, nil
	}}
	e.work(e.indexer(fake), 1)
	if n := e.count(`SELECT count(*) FROM attachment_texts WHERE message_sha256 = $1 AND status = 'timeout' AND body_text IS NULL`, late); n != 2 {
		t.Errorf("%d timeout rows", n)
	}
	if state, _, _ := e.job(late); state != store.JobDone {
		t.Errorf("late: %s", state)
	}
	if state, _, code := e.job(broken); state != store.JobFailed || code != archive.CodeExtractorUnavailable {
		t.Errorf("broken: %s (%s)", state, code)
	}
	if n, err := e.st.CountFailedJobsWithCode(e.ctx, store.JobAttachmentText, archive.CodeExtractorUnavailable); err != nil || n != 1 {
		t.Errorf("failed with the code: %d, %v", n, err)
	}
}

// The backfill runs to the end over more than one batch with one worker:
// every message gets its state, the queue never holds more than one batch
// of backfill children, and they are extracted newest first. New mail
// stored meanwhile goes before the queued children.
func TestAttachmentBackfillRunsToTheEnd(t *testing.T) {
	e := newReindexEnv(t)
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	const total = 2500
	shas := e.seed(withPDF("alt", "x"), total, func(i int) time.Time { return base.Add(-time.Duration(i) * time.Minute) })
	fake := &fakeExtractor{}
	x := e.indexer(fake)
	most, batches := 0, 0
	newStored := false
	archive.SetAttachmentHooks(x, 1000, func(int, bool) {
		batches++
		most = max(most, e.count(`SELECT count(*) FROM jobs WHERE kind = 'attachment_text' AND state = 'queued' AND priority = $1`, store.PriorityBackfill))
		if !newStored {
			newStored = true
			e.store(withPDF("neu", "x"))
		}
	}, nil)
	e.backfill()
	e.work(x, 1)
	if n := e.count(`SELECT count(*) FROM attachment_index_state WHERE version = $1`, store.AttachmentIndexVersion); n != total+1 {
		t.Errorf("%d messages done, want %d", n, total+1)
	}
	if most > 1000 || most == 0 {
		t.Errorf("at most %d backfill children queued", most)
	}
	if batches != 3 {
		t.Errorf("%d batches", batches)
	}
	got := fake.calls()
	// The new message was stored after the first batch was queued, and is
	// extracted first.
	if len(got) != total+1 || got[0] != "neu" {
		t.Fatalf("%d calls, first %q", len(got), got[0])
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'attachment_backfill' AND state = 'done'`); n != 1 {
		t.Error("the backfill is not done")
	}
	// Newest first: the order of the done children follows sort_at.
	var ordered bool
	if err := e.conn.QueryRow(e.ctx, `SELECT bool_and(ok) FROM (
		SELECT finished_at >= lag(finished_at) OVER (ORDER BY m.sort_at DESC, m.sha256 DESC) IS NOT FALSE AS ok
		FROM jobs j JOIN messages m ON m.sha256 = j.message_sha256 WHERE j.priority = $1 AND m.sha256 = ANY ($2)) x`,
		store.PriorityBackfill, shas).Scan(&ordered); err != nil {
		t.Fatal(err)
	}
	if !ordered {
		t.Error("backfill children did not run newest first")
	}
	// A second run finds nothing.
	e.backfill()
	e.work(x, 1)
	if len(fake.calls()) != total+1 {
		t.Errorf("a second run extracted %d", len(fake.calls())-total-1)
	}
}

// Messages that share one sort_at across a batch boundary are all found:
// the walk orders by (sort_at, sha256) descending like its cursor.
func TestAttachmentBackfillTiesAcrossBatches(t *testing.T) {
	e := newReindexEnv(t)
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	// Rows 996 to 1,005 (index 995 to 1,004) share one sort_at.
	e.seed(withPDF("alt", "x"), 1005, func(i int) time.Time {
		return base.Add(-time.Duration(min(i, 995)) * time.Minute)
	})
	x := e.indexer(&fakeExtractor{})
	e.backfill()
	e.work(x, 1)
	if n := e.count(`SELECT count(*) FROM attachment_index_state`); n != 1005 {
		t.Errorf("%d of 1005 messages done", n)
	}
	if n, err := e.st.CountAttachmentPending(e.ctx); err != nil || n != 0 {
		t.Errorf("pending %d, %v", n, err)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'attachment_backfill' AND state = 'done'`); n != 1 {
		t.Error("the backfill is not done")
	}
}

// Extraction and the backfill write only the two new tables: the xmin of
// every message row is unchanged, those with attachments included.
func TestAttachmentTextLeavesMessagesAlone(t *testing.T) {
	e := newReindexEnv(t)
	e.store(withPDF("eins", "x"))
	e.store("Subject: ohne\r\n\r\nkein Anhang\r\n")
	e.seed(withPDF("alt", "x"), 20, func(i int) time.Time {
		return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
	})
	before := e.count(`SELECT sum(xmin::text::bigint) FROM messages`)
	e.backfill()
	e.work(e.indexer(&fakeExtractor{}), 2)
	if after := e.count(`SELECT sum(xmin::text::bigint) FROM messages`); after != before {
		t.Error("message rows were written")
	}
	if n := e.count(`SELECT count(*) FROM attachment_index_state`); n != 21 {
		t.Errorf("%d done", n)
	}
}
