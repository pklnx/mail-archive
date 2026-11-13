package archive_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/attachtext"
	"github.com/pklnx/mail-archive/internal/jobs"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// Attachment text per location and customer group (#539, part 2), with
// the queue and a fake extractor that counts its calls.

// member creates a user in group (0: the default group) with an account
// and an INBOX, and returns the user and the folder.
func (e *reindexEnv) member(name string, group int64) (user, folder int64) {
	e.t.Helper()
	nu := store.NewUser{Name: name, PasswordHash: "h", GroupID: group}
	if group == 0 {
		g, err := e.st.GetGroupByName(e.ctx, "Default")
		if err != nil {
			e.t.Fatal(err)
		}
		nu.GroupID = g.ID
	}
	u, err := e.st.CreateUserInGroup(e.ctx, nu)
	if err != nil {
		e.t.Fatal(err)
	}
	acc := &store.Account{Kind: store.KindImport, Name: name, OwnerID: &u.ID}
	if err := e.st.CreateAccount(e.ctx, acc); err != nil {
		e.t.Fatal(err)
	}
	fo, err := e.st.GetOrCreateFolder(e.ctx, acc.ID, "INBOX")
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID, fo.ID
}

func (e *reindexEnv) group(name string) int64 {
	e.t.Helper()
	g, err := e.st.CreateGroup(e.ctx, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return g.ID
}

// storeIn saves raw into folder the way sync does: a message whose file
// existed is known, and only its hash is passed.
func (e *reindexEnv) storeIn(folder int64, raw string) string {
	e.t.Helper()
	blob, created, err := e.blobs.Put(strings.NewReader(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	meta, err := archive.BuildMeta(e.ctx, e.st, e.blobs, blob, created, true)
	if err != nil {
		e.t.Fatal(err)
	}
	e.uid++
	loc := store.Location{FolderID: folder, UIDValidity: 1, UID: e.uid}
	if _, err := e.st.SaveBatch(e.ctx, folder, e.uid, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		e.t.Fatal(err)
	}
	return blob.SHA256
}

// finds reports whether user finds a message by query, and whether its
// row names the matched attachment.
func (e *reindexEnv) finds(user int64, query string) (found, snippet bool) {
	e.t.Helper()
	rows, err := e.st.SearchMessages(e.ctx, store.SearchFilter{Scope: storetest.Viewer(e.t, e.st, user), Query: query, Limit: 10})
	if err != nil {
		e.t.Fatal(err)
	}
	if len(rows) == 0 {
		return false, false
	}
	return true, rows[0].MatchedAttachment != nil && strings.Contains(rows[0].Snippet, query)
}

// blockingExtractor blocks its first call until released, after
// signalling that it started.
func blockingExtractor() (x *fakeExtractor, started *testwait.Signal, release chan struct{}) {
	started = &testwait.Signal{}
	release = make(chan struct{})
	first := true
	x = &fakeExtractor{}
	x.fn = func(ctx context.Context, subject string) ([]attachtext.Result, error) {
		x.mu.Lock()
		block := first
		first = false
		x.mu.Unlock()
		if block {
			started.Notify()
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []attachtext.Result{{Part: 1, Filename: "a.pdf", Status: attachtext.StatusOK, Text: "Zahlungserinnerung " + subject}}, nil
	}
	return x, started, release
}

// workAsync runs the attachment kinds until none is due, in the
// background.
func (e *reindexEnv) workAsync(x *archive.AttachmentIndexer) <-chan error {
	reg := &jobs.Registry{}
	if err := x.Register(reg); err != nil {
		e.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- (&jobs.Pool{Store: e.st, Kinds: reg, Workers: 1, Logger: e.log, UntilIdle: true}).Run(e.ctx)
	}()
	return done
}

func (e *reindexEnv) await(c <-chan error, what string) {
	e.t.Helper()
	select {
	case err := <-c:
		if err != nil {
			e.t.Fatal(err)
		}
	case <-time.After(testwait.Limit(e.t)):
		e.t.Fatalf("%s did not finish", what)
	}
}

func awaitSignal(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(testwait.Limit(t)):
		t.Fatalf("%s did not happen", what)
	}
}

// pdfFake answers every message with one PDF whose text is in no subject
// or body of these tests.
func pdfFake() *fakeExtractor {
	return &fakeExtractor{fn: func(context.Context, string) ([]attachtext.Result, error) {
		return []attachtext.Result{{Part: 1, Filename: "a.pdf", Status: attachtext.StatusOK, Text: "Zahlungserinnerung Kundennummer"}}, nil
	}}
}

// TestAttachmentTextPerGroup: a message done in group A and then saved
// into group B is not found by B's user through its attachment text until
// B's own extraction ran (the extractor runs once more); another user of
// B who saves it afterwards finds it at once, without a job. A message
// without an attachment queues nothing.
func TestAttachmentTextPerGroup(t *testing.T) {
	e := newReindexEnv(t)
	gB := e.group("B")
	alice, fa := e.member("alice", 0)
	bob, fb := e.member("bob", gB)
	carol, fc := e.member("carol", gB)
	fake := pdfFake()
	x := e.indexer(fake)
	m := withPDF("Rechnung", "x")
	sha := e.storeIn(fa, m)
	e.work(x, 1)
	if ok, snip := e.finds(alice, "Kundennummer"); !ok || !snip {
		t.Fatalf("alice: %v %v", ok, snip)
	}
	e.storeIn(fb, m)
	if ok, _ := e.finds(bob, "Kundennummer"); ok {
		t.Error("bob finds it before his extraction")
	}
	if state, prio, _ := e.job(sha); state != store.JobQueued || prio != store.PriorityNew {
		t.Errorf("bob's job: %s %d", state, prio)
	}
	e.work(x, 1)
	if got := fake.calls(); len(got) != 2 {
		t.Errorf("extracted %d times, want 2", len(got))
	}
	if ok, snip := e.finds(bob, "Kundennummer"); !ok || !snip {
		t.Errorf("bob after his extraction: %v %v", ok, snip)
	}
	e.storeIn(fc, m)
	if ok, _ := e.finds(carol, "Kundennummer"); !ok {
		t.Error("carol does not find it at once")
	}
	if state, _, _ := e.job(sha); state != store.JobDone {
		t.Errorf("carol's save queued the job: %s", state)
	}
	plain := e.storeIn(fb, "Subject: ohne\r\n\r\nkein Anhang\r\n")
	if n := e.count(`SELECT count(*) FROM jobs WHERE key = $1`, plain); n != 0 {
		t.Error("a message without an attachment was queued")
	}
	if n := e.count(`SELECT count(*) FROM message_locations WHERE message_sha256 = $1 AND attachment_text_ok`, plain); n != 1 {
		t.Error("a message without an attachment is not flagged")
	}
	e.work(x, 1)
	if got := fake.calls(); len(got) != 2 {
		t.Errorf("extracted %d times after carol's save, want 2", len(got))
	}
}

// TestAttachmentTextSaveDuringRun: a save into another group while the
// job runs (the extractor blocked) asks for a rerun; the job is queued
// again when it ends, runs once more, and then the new location shows the
// text. The first run did not set it.
func TestAttachmentTextSaveDuringRun(t *testing.T) {
	e := newReindexEnv(t)
	gB := e.group("B")
	alice, fa := e.member("alice", 0)
	bob, fb := e.member("bob", gB)
	fake, started, release := blockingExtractor()
	x := e.indexer(fake)
	m := withPDF("Rechnung", "x")
	sha := e.storeIn(fa, m)
	woken := started.C()
	done := e.workAsync(x)
	awaitSignal(t, woken, "the extraction")
	e.storeIn(fb, m)
	var requeue bool
	if err := e.conn.QueryRow(e.ctx, `SELECT requeue FROM jobs WHERE key = $1 AND state = 'running'`, sha).Scan(&requeue); err != nil || !requeue {
		t.Fatalf("running job without requeue: %v %v", requeue, err)
	}
	close(release)
	e.await(done, "the queue")
	if got := fake.calls(); len(got) != 2 {
		t.Errorf("extracted %d times, want 2", len(got))
	}
	for name, u := range map[string]int64{"alice": alice, "bob": bob} {
		if ok, _ := e.finds(u, "Zahlungserinnerung"); !ok {
			t.Errorf("%s does not find it", name)
		}
	}
	if state, _, _ := e.job(sha); state != store.JobDone {
		t.Errorf("job %s", state)
	}
}

// TestAttachmentTextMoveDuringRun: a user whose message, new to the
// archive, is being extracted moves to another group during the run; the
// location is in the run's needed, so she finds it once the run ends,
// without a rerun.
func TestAttachmentTextMoveDuringRun(t *testing.T) {
	e := newReindexEnv(t)
	gB := e.group("B")
	alice, fa := e.member("alice", 0)
	fake, started, release := blockingExtractor()
	x := e.indexer(fake)
	sha := e.storeIn(fa, withPDF("Rechnung", "x"))
	woken := started.C()
	done := e.workAsync(x)
	awaitSignal(t, woken, "the extraction")
	if _, err := e.st.SetUserGroup(e.ctx, alice, gB); err != nil {
		t.Fatal(err)
	}
	close(release)
	e.await(done, "the queue")
	if ok, _ := e.finds(alice, "Zahlungserinnerung"); !ok {
		t.Error("alice does not find it after the run")
	}
	if got := fake.calls(); len(got) != 1 {
		t.Errorf("extracted %d times, want 1", len(got))
	}
	if state, _, _ := e.job(sha); state != store.JobDone {
		t.Errorf("job %s", state)
	}
}

// TestAttachmentTextUnreadableInGroup: a message done with searchable
// text whose file cannot be read when another group receives it fails its
// job at once with its code; that group does not find it, and the
// extractor is not called.
func TestAttachmentTextUnreadableInGroup(t *testing.T) {
	e := newReindexEnv(t)
	gB := e.group("B")
	_, fa := e.member("alice", 0)
	bob, fb := e.member("bob", gB)
	fake := pdfFake()
	x := e.indexer(fake)
	m := withPDF("Rechnung", "x")
	sha := e.storeIn(fa, m)
	e.work(x, 1)
	e.storeIn(fb, m)
	if err := e.blobs.Remove(sha); err != nil {
		t.Fatal(err)
	}
	e.work(x, 1)
	if state, _, code := e.job(sha); state != store.JobFailed || code != archive.CodeMessageUnreadable {
		t.Errorf("job %s (%s)", state, code)
	}
	if ok, _ := e.finds(bob, "Zahlungserinnerung"); ok {
		t.Error("bob finds it without an extraction")
	}
	if got := fake.calls(); len(got) != 1 {
		t.Errorf("extracted %d times, want 1", len(got))
	}
}

// TestAttachmentTextGuardAcrossGroups: the crash-loop guard holds across
// groups. With the message's job failed, another group's save leaves it
// failed and the extractor uncalled; the backfill does not queue it and
// it is not counted as pending; `jobs retry` runs one extraction, after
// which both groups find it.
func TestAttachmentTextGuardAcrossGroups(t *testing.T) {
	e := newReindexEnv(t)
	gB, gC := e.group("B"), e.group("C")
	alice, fa := e.member("alice", 0)
	bob, fb := e.member("bob", gB)
	_, fc := e.member("carol", gC)
	m := withPDF("absturz", "x")
	sha := e.storeIn(fa, m)
	if _, err := e.conn.Exec(e.ctx, `UPDATE jobs SET state = 'failed', attempts = 3, last_error = 'lease expired', finished_at = now() WHERE key = $1`, sha); err != nil {
		t.Fatal(err)
	}
	e.storeIn(fb, m)
	e.storeIn(fc, m)
	fake := pdfFake()
	x := e.indexer(fake)
	e.backfill()
	e.work(x, 1)
	if state, _, _ := e.job(sha); state != store.JobFailed {
		t.Errorf("job %s", state)
	}
	if got := fake.calls(); len(got) != 0 {
		t.Errorf("extracted %d times", len(got))
	}
	if n, err := e.st.CountAttachmentPending(e.ctx); err != nil || n != 0 {
		t.Errorf("pending %d %v", n, err)
	}
	if n, err := e.st.RetryFailedJobs(e.ctx, store.JobAttachmentText); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	e.work(x, 1)
	if got := fake.calls(); len(got) != 1 {
		t.Errorf("extracted %d times after the retry, want 1", len(got))
	}
	for name, u := range map[string]int64{"alice": alice, "bob": bob} {
		if ok, _ := e.finds(u, "Kundennummer"); !ok {
			t.Errorf("%s does not find it after the retry", name)
		}
	}
}
