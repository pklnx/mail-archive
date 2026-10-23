package archive_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// waitFill waits until the fill has the status. ran is the runner's
// AfterRun: a run's status is stored before it.
func waitFill(t *testing.T, f *fixture, ran *testwait.Signal, id int64, want string) *store.Fill {
	t.Helper()
	var got *store.Fill
	testwait.Until(t, ran, fmt.Sprintf("fill %d to reach %s", id, want), func() bool {
		var err error
		if got, err = f.store.GetFill(f.ctx, id); err != nil {
			t.Fatal(err)
		}
		return got.Status == want
	})
	return got
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
	var runs atomic.Int32
	var ran testwait.Signal
	r := &archive.FillRunner{Filler: f.filler(), CheckEvery: 20 * time.Millisecond,
		AfterRun: func() { runs.Add(1); ran.Notify() }}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	// A run skipped as busy also ends with AfterRun.
	testwait.Until(t, &ran, "a run while locked", func() bool { return runs.Load() >= 1 })
	if st, _ := f.store.FillStatus(f.ctx, fill.ID); st != store.FillRunning {
		t.Fatalf("ran while locked: %s", st)
	}
	unlock()

	got := waitFill(t, f, &ran, fill.ID, store.FillOK)
	if got.PasswordEnc != nil || len(got.Folders) != 2 {
		t.Errorf("fill %+v", got)
	}
	// The end is in the audit log, by the web server; it is written before
	// AfterRun.
	var found []byte
	testwait.Until(t, &ran, "the data.fill_done entry", func() bool {
		_ = f.store.ExportAudit(f.ctx, store.AuditFilter{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour)},
			func(r store.AuditRecord) error {
				if r.Action == "data.fill_done" && r.TargetName == acc.Name {
					found = r.Details
				}
				return nil
			})
		return found != nil
	})
	var d map[string]any
	if err := json.Unmarshal(found, &d); err != nil {
		t.Fatal(err)
	}
	if d["by"] != "web server" || d["status"] != store.FillOK || d["imported"] != float64(archive.FillTotals(got).Imported) {
		t.Errorf("data.fill_done %s", found)
	}
}

func TestFillRunnerQueue(t *testing.T) {
	r := &archive.FillRunner{}
	r.Enqueue(7, 3)
	r.Enqueue(7, 3)
	r.Enqueue(8, 4)
	if q := ids(r.Pending()); len(q) != 2 || !q[3] || !q[4] {
		t.Errorf("queued %v", q)
	}
}
