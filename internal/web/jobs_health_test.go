package web

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/jobs"
	"github.com/pklnx/mail-archive/internal/store"
)

func (f *authFixture) jobsHealth() (int, string) {
	f.t.Helper()
	resp, err := http.Get(f.srv.URL + "/healthz/jobs")
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

func (f *authFixture) enqueueJob(kind, key string, priority int) {
	f.t.Helper()
	if _, err := f.st.EnqueueJobs(context.Background(), []store.NewJob{{Kind: kind, Key: key, Priority: priority, MaxAttempts: 3}}, false); err != nil {
		f.t.Fatal(err)
	}
}

// TestJobsHealthEndpoint: /healthz/jobs needs no login and gives only a
// status: degraded with the kind once new-mail work has been due for more
// than 30 minutes; backfills and failed jobs never degrade it. No counts.
func TestJobsHealthEndpoint(t *testing.T) {
	f := newHealthFixture(t)
	if status, body := f.jobsHealth(); status != 200 || body != `{"status":"ok"}` {
		t.Fatalf("empty: %d %s", status, body)
	}
	// Due for 29 minutes, a backfill due for days and a failed job: ok.
	f.enqueueJob("attachment_text", "new", store.PriorityNew)
	f.exec(`UPDATE jobs SET run_after = now() - interval '29 minutes' WHERE key = 'new'`)
	f.enqueueJob("attachment_backfill", "v1", store.PriorityBackfill)
	f.exec(`UPDATE jobs SET run_after = now() - interval '3 days' WHERE key = 'v1'`)
	f.enqueueJob("ocr", "failed", store.PriorityNew)
	f.exec(`UPDATE jobs SET state = 'failed', attempts = 3, run_after = now() - interval '2 hours', finished_at = now() WHERE key = 'failed'`)
	f.clock.Advance(syncHealthTTL)
	if status, body := f.jobsHealth(); status != 200 || body != `{"status":"ok"}` {
		t.Fatalf("backfill and failed jobs: %d %s", status, body)
	}

	// Over 30 minutes: degraded, but only after the cache's 15 seconds.
	f.exec(`UPDATE jobs SET run_after = now() - interval '31 minutes' WHERE key = 'new'`)
	if status, _ := f.jobsHealth(); status != 200 {
		t.Fatalf("cache not used: %d", status)
	}
	f.clock.Advance(syncHealthTTL)
	status, body := f.jobsHealth()
	if status != 503 || body != `{"status":"degraded","stale":["attachment_text"]}` {
		t.Fatalf("stale: %d %s", status, body)
	}
	if strings.ContainsAny(body, "0123456789") {
		t.Errorf("counts in %s", body)
	}
	// The job is done: ok again.
	f.exec(`UPDATE jobs SET state = 'done', finished_at = now() WHERE key = 'new'`)
	f.clock.Advance(syncHealthTTL)
	if status, body := f.jobsHealth(); status != 200 {
		t.Fatalf("after the job: %d %s", status, body)
	}
	if jobs.StaleAfter.Minutes() != 30 {
		t.Errorf("StaleAfter %v", jobs.StaleAfter)
	}
}

// TestStatusJobsForAdmins: GET /api/status lists the queue for admins
// only: it covers every user's mail.
func TestStatusJobsForAdmins(t *testing.T) {
	f := newHealthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.addUser("kim", "correct horse battery", false)
	f.enqueueJob("attachment_text", "a", store.PriorityNew)
	f.enqueueJob("attachment_text", "b", store.PriorityNew)
	f.exec(`UPDATE jobs SET run_after = now() - interval '90 seconds' WHERE key = 'a'`)
	f.exec(`UPDATE jobs SET state = 'failed', finished_at = now() WHERE key = 'b'`)

	admin := f.as()
	admin.loginOK("admin", "correct horse battery")
	out := admin.expect("GET", "/api/status", nil, 200)
	list, ok := out["jobs"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("admin: %v", out)
	}
	j := list[0].(map[string]any)
	if j["kind"] != "attachment_text" || j["queued"] != float64(1) || j["failed"] != float64(1) || j["running"] != float64(0) ||
		j["retrying"] != float64(0) || j["oldestDueSeconds"].(float64) < 89 || j["oldestNewSeconds"].(float64) < 89 {
		t.Errorf("admin jobs: %v", j)
	}

	user := f.as()
	user.loginOK("kim", "correct horse battery")
	if out := user.expect("GET", "/api/status", nil, 200); out["jobs"] != nil {
		t.Errorf("user sees jobs: %v", out)
	}
}
