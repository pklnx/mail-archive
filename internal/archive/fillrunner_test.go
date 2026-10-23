package archive_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

func waitFill(t *testing.T, f *fixture, id int64, want string) *store.Fill {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, err := f.store.GetFill(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == want {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fill %d did not reach %s", id, want)
	return nil
}

func TestFillRunnerContinuesRunningFills(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	// A fill left running, as by a process that stopped: nobody enqueued it.
	fill := f.createFill(acc, "secret", "Spam")
	// While another process holds the account's lock, the runner leaves it.
	unlock, ok, err := f.store.TryLockSyncForWrite(f.ctx, acc.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	r := &archive.FillRunner{Filler: f.filler(), CheckEvery: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(100 * time.Millisecond)
	if st, _ := f.store.FillStatus(f.ctx, fill.ID); st != store.FillRunning {
		t.Fatalf("ran while locked: %s", st)
	}
	unlock()

	got := waitFill(t, f, fill.ID, store.FillOK)
	if got.PasswordEnc != nil || len(got.Folders) != 2 {
		t.Errorf("fill %+v", got)
	}
	// The end is in the audit log, by the web server.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var found []byte
		_ = f.store.ExportAudit(f.ctx, store.AuditFilter{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour)},
			func(r store.AuditRecord) error {
				if r.Action == "data.fill_done" && r.TargetName == acc.Name {
					found = r.Details
				}
				return nil
			})
		if found != nil {
			var d map[string]any
			if err := json.Unmarshal(found, &d); err != nil {
				t.Fatal(err)
			}
			if d["by"] != "web server" || d["status"] != store.FillOK || d["imported"] != float64(archive.FillTotals(got).Imported) {
				t.Errorf("data.fill_done %s", found)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no data.fill_done entry")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestFillRunnerQueue(t *testing.T) {
	r := &archive.FillRunner{}
	r.Enqueue(7, 3)
	r.Enqueue(7, 3)
	r.Enqueue(8, 4)
	if q := r.Pending(); len(q) != 2 || !q[3] || !q[4] {
		t.Errorf("queued %v", q)
	}
}
