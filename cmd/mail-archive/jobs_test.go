package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/jobs"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// blockingKind registers a test kind whose handler signals that it runs
// and then waits for the end of its context.
func blockingKind(t *testing.T) *testwait.Signal {
	t.Helper()
	var started testwait.Signal
	testJobKinds = []jobs.Kind{{Name: "test", Handler: func(ctx context.Context, _ jobs.Job) error {
		started.Notify()
		<-ctx.Done()
		return ctx.Err()
	}}}
	t.Cleanup(func() { testJobKinds = nil })
	return &started
}

func jobsEnv(t *testing.T) (*store.Store, *pgx.Conn) {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", t.TempDir())
	t.Setenv("MAIL_ARCHIVE_LOG_LEVEL", "error")
	return st, conn
}

func jobState(t *testing.T, conn *pgx.Conn, key string) (state string, attempts int, leased bool) {
	t.Helper()
	if err := conn.QueryRow(context.Background(), `SELECT state, attempts, lease_token IS NOT NULL FROM jobs WHERE kind = 'test' AND key = $1`, key).
		Scan(&state, &attempts, &leased); err != nil {
		t.Fatal(err)
	}
	return state, attempts, leased
}

// TestWorkerCommand: worker refuses 0 workers; it runs a job, and after
// its context ends it returns without an error and the job is queued
// again without the attempt.
func TestWorkerCommand(t *testing.T) {
	st, conn := jobsEnv(t)
	t.Setenv(config.EnvJobWorkers, "0")
	if err := runCmd(t, newWorkerCmd(), ""); err == nil || !strings.Contains(err.Error(), config.EnvJobWorkers+"=0") {
		t.Fatalf("worker with 0: %v", err)
	}
	t.Setenv(config.EnvJobWorkers, "1")
	started := blockingKind(t)
	if _, err := st.EnqueueJobs(context.Background(), []store.NewJob{{Kind: "test", Key: "a", MaxAttempts: 1}}, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := newWorkerCmd()
	cmd.SetArgs(nil)
	done := make(chan error, 1)
	woken := started.C()
	go func() { done <- cmd.ExecuteContext(ctx) }()
	select {
	case <-woken:
	case err := <-done:
		t.Fatalf("worker ended before the job ran: %v", err)
	case <-time.After(testwait.Limit(t)):
		t.Fatal("the job did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker after the cancel: %v", err)
		}
	case <-time.After(testwait.Limit(t)):
		t.Fatal("worker did not stop")
	}
	if state, attempts, leased := jobState(t, conn, "a"); state != store.JobQueued || attempts != 0 || leased {
		t.Errorf("job after the worker stopped: %s, %d attempts, leased %v", state, attempts, leased)
	}
}

// TestStartJobs: serve's pool is off with 0 workers and says so; with 1,
// it runs jobs and its stop releases a running one.
func TestStartJobs(t *testing.T) {
	st, conn := jobsEnv(t)
	started := blockingKind(t)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	cfg := &config.Config{JobWorkers: 0}
	stop, err := startJobs(context.Background(), &app{cfg: cfg, store: st}, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if !strings.Contains(logs.String(), "jobs off in this process") || !strings.Contains(logs.String(), "mail-archive worker") {
		t.Errorf("log with 0 workers: %s", logs.String())
	}
	if _, err := st.EnqueueJobs(context.Background(), []store.NewJob{{Kind: "test", Key: "a", MaxAttempts: 3}}, false); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := jobState(t, conn, "a"); state != store.JobQueued {
		t.Fatalf("ran with 0 workers: %s", state)
	}

	logs.Reset()
	cfg.JobWorkers = 1
	woken := started.C()
	stop, err = startJobs(context.Background(), &app{cfg: cfg, store: st}, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-woken:
	case <-time.After(testwait.Limit(t)):
		stop()
		t.Fatal("the job did not start")
	}
	stop()
	if !strings.Contains(logs.String(), `msg="jobs on" workers=1 kinds=test,attachment_text,attachment_backfill`) {
		t.Errorf("log with 1 worker: %s", logs.String())
	}
	if state, attempts, leased := jobState(t, conn, "a"); state != store.JobQueued || attempts != 0 || leased {
		t.Errorf("job after stop: %s, %d attempts, leased %v", state, attempts, leased)
	}
}

// TestJobsRetryCommand: failed jobs of the kind are queued again, with
// an audit entry; the status line shows the queue.
func TestJobsRetryCommand(t *testing.T) {
	st, conn := jobsEnv(t)
	t.Setenv(config.EnvOperator, "alice")
	ctx := context.Background()
	status := func() string {
		t.Helper()
		out := captureStdout(t, func() {
			if err := runCmd(t, newStatusCmd(), ""); err != nil {
				t.Error(err)
			}
		})
		lines := strings.Split(strings.TrimSpace(out), "\n")
		return lines[len(lines)-1]
	}
	if line := status(); line != "jobs: none" {
		t.Errorf("status without jobs: %q", line)
	}
	if _, err := st.EnqueueJobs(ctx, []store.NewJob{{Kind: "test", Key: "a", MaxAttempts: 1}, {Kind: "test", Key: "b", MaxAttempts: 1}}, false); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `UPDATE jobs SET state = 'failed', attempts = 1, last_error = 'panic', finished_at = now() WHERE key = 'a' RETURNING 1`).Scan(new(int)); err != nil {
		t.Fatal(err)
	}
	if line := status(); !strings.HasPrefix(line, "jobs: test 1 queued (oldest ") || !strings.HasSuffix(line, "), 1 failed") {
		t.Errorf("status: %q", line)
	}

	if out, err := runOut(t, newJobsCmd(), "retry", "Bad-Kind"); err == nil {
		t.Errorf("invalid kind: %s", out)
	}
	out, err := runOut(t, newJobsCmd(), "retry", "test")
	if err != nil || !strings.Contains(out, "queued 1 failed test job(s) again") {
		t.Fatalf("retry: %v\n%s", err, out)
	}
	if state, attempts, _ := jobState(t, conn, "a"); state != store.JobQueued || attempts != 0 {
		t.Errorf("after retry: %s %d", state, attempts)
	}
	var details string
	if err := conn.QueryRow(ctx, `SELECT details::text FROM audit_log WHERE action = 'jobs.retry' AND actor = 'cli'`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind": "test"`, `"jobs": 1`, `"operator": "alice"`} {
		if !strings.Contains(details, want) {
			t.Errorf("audit details %s, want %s", details, want)
		}
	}
}

func TestFormatJobsStatus(t *testing.T) {
	got := formatJobsStatus([]store.JobStat{
		{Kind: "attachment_text", Queued: 1234, OldestDue: 3*time.Minute + 20*time.Second, Running: 1, Failed: 2},
		{Kind: "attachment_backfill", Queued: 1},
		{Kind: "ocr", Retrying: 2, OldestDue: 50 * time.Hour},
	})
	want := "jobs: attachment_text 1234 queued (oldest 3m), 1 running, 2 failed; attachment_backfill 1 queued; ocr 0 queued (oldest 2d), 2 retrying"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got := formatJobsStatus(nil); got != "jobs: none" {
		t.Errorf("empty: %q", got)
	}
}

// TestReindexAttachments: reindex extracts the attachment text of every
// pending message in the foreground, more than one backfill batch, and
// says how many jobs it left to another process.
func TestReindexAttachments(t *testing.T) {
	st, conn := jobsEnv(t)
	ctx := context.Background()
	blobs, err := blobstore.New(os.Getenv("MAIL_ARCHIVE_DATA_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	raw := "Subject: alt\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nbody\r\n" +
		"--b\r\nContent-Type: text/csv\r\nContent-Disposition: attachment; filename=\"liste.csv\"\r\n\r\nArtikel;Preis\r\n--b--\r\n"
	blob, _, err := blobs.Put(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	const total = 2500
	if _, err := conn.Exec(ctx, `INSERT INTO messages (sha256, size, stored_path, sent_at, has_attachment, index_version)
		SELECT lpad(to_hex(i), 64, '0'), 100, $1, timestamptz '2020-01-01' - i * interval '1 minute', true, $2
		FROM generate_series(1, $3::int + 1) i`, blobstore.RelPath(blob.SHA256), store.IndexVersion, total); err != nil {
		t.Fatal(err)
	}
	// The last message is being extracted by another process.
	held := fmt.Sprintf("%064x", total+1)
	if _, err := st.EnqueueJobs(ctx, []store.NewJob{{Kind: store.JobAttachmentText, Key: held, MessageSHA256: held, MaxAttempts: 3}}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE jobs SET state = 'running', lease_token = '\x01', lease_until = now() + interval '1 hour' WHERE key = $1`, held); err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdout(t, func() { runErr = runCmd(t, newReindexCmd(), "") })
	if runErr != nil {
		t.Fatal(runErr)
	}
	want := fmt.Sprintf("updated 0 message(s), extracted text of %d attachment(s)\n1 attachment job(s) are held by another process or wait for a retry; they finish there\n", total)
	if out != want {
		t.Errorf("output %q, want %q", out, want)
	}
	var done int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM attachment_index_state`).Scan(&done); err != nil || done != total {
		t.Errorf("%d done, %v", done, err)
	}
}

func TestExtractorStatus(t *testing.T) {
	if got := extractorStatus(0); got != "" {
		t.Errorf("none: %q", got)
	}
	got := extractorStatus(3)
	if !strings.Contains(got, "PDF extractor unavailable for 3 message(s)") || !strings.Contains(got, "jobs retry attachment_text") || !strings.HasPrefix(got, "\n") {
		t.Errorf("got %q", got)
	}
}
