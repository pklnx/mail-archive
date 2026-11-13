package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// TestAttachmentRepairSchedule (#539, part 2): the repair runs at start
// and on every tick (a fake clock). A location written with the column's
// default after the message was done (as a process before 1.6.0 does),
// whose message has no queued job, is queued by the next pass; the
// following pass queues nothing; a location of a message without an
// attachment gets its flag.
func TestAttachmentRepairSchedule(t *testing.T) {
	st, conn := jobsEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	acc := &store.Account{Kind: store.KindImport, Name: "alt"}
	if err := st.CreateAccount(context.Background(), acc); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOrCreateFolder(context.Background(), acc.ID, "INBOX"); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO messages (sha256, size, stored_path, has_attachment) VALUES (lpad('a', 64, '0'), 1, 'x', true), (lpad('b', 64, '0'), 1, 'x', false)`)
	exec(`INSERT INTO attachment_index_state (message_sha256, version) VALUES (lpad('a', 64, '0'), $1)`, store.AttachmentIndexVersion)
	tick := make(chan time.Time)
	passes := make(chan store.RepairResult, 4)
	r := &attachmentRepair{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), tick: tick,
		repaired: func(res store.RepairResult, err error) {
			if err != nil {
				t.Errorf("repair: %v", err)
			}
			passes <- res
		}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.run(ctx)
	}()
	next := func(what string) store.RepairResult {
		t.Helper()
		select {
		case res := <-passes:
			return res
		case <-time.After(testwait.Limit(t)):
			t.Fatalf("no pass %s", what)
		}
		return store.RepairResult{}
	}
	if res := next("at start"); res.Queued != 0 || res.Cleared != 0 {
		t.Errorf("at start: %+v", res)
	}
	// As 1.5.x after the upgrade: locations without the column.
	exec(`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid)
		SELECT s, (SELECT id FROM folders), 1, n FROM (VALUES (lpad('a', 64, '0'), 1), (lpad('b', 64, '0'), 2)) v (s, n)`)
	tick <- time.Now()
	if res := next("on the tick"); res.Queued != 1 || res.Cleared != 1 {
		t.Errorf("on the tick: %+v", res)
	}
	var state string
	var prio int
	if err := conn.QueryRow(context.Background(), `SELECT state, priority FROM jobs WHERE kind = $1 AND key = lpad('a', 64, '0')`, store.JobAttachmentText).
		Scan(&state, &prio); err != nil || state != store.JobQueued || prio != store.PriorityBackfill {
		t.Errorf("job %s %d %v", state, prio, err)
	}
	tick <- time.Now()
	if res := next("on the second tick"); res.Queued != 0 || res.Cleared != 0 {
		t.Errorf("second tick: %+v", res)
	}
	cancel()
	<-done
}
