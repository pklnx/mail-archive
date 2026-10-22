package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// sessionStore opens a store whose pool has one connection, which a
// session then holds: whatever its transactions need from the pool waits
// until the deadline.
func sessionStore(t *testing.T, ctx context.Context, url string) *store.Store {
	t.Helper()
	small, err := store.Open(ctx, storetest.WithMaxConns(t, url, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(small.Close)
	return small
}

// agentEntry is an audit entry with a user agent the store has not seen:
// the store caches ids per user agent, so each case needs its own.
func agentEntry(t *testing.T, action string) *store.AuditEntry {
	// Only session and API key entries carry a user agent.
	return &store.AuditEntry{Actor: store.ActorSession, Action: action, UserAgent: "agent of " + t.Name() + " " + action}
}

// TestSessionTxNeedsNoPool (#166): the transactions of a purge or deletion
// session, audit entries included, run on the session's connection only:
// a new user agent and a missing audit partition need no pool connection.
func TestSessionTxNeedsNoPool(t *testing.T) {
	t.Run("purge", func(t *testing.T) {
		f := newPurgeFixture(t)
		p := f.confirm(1)
		ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
		defer cancel()
		small := sessionStore(t, ctx, f.url)
		sess, ok, err := small.StartPurgeSession(ctx)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		defer sess.Close(ctx)
		if ok, err := sess.TryLockBlobs(ctx); err != nil || !ok {
			t.Fatal(ok, err)
		}
		res, err := sess.Batch(store.WithAudit(ctx, agentEntry(t, "account.purge_batch")), p.ID, 10, 0)
		if err != nil || !res.Done || res.Deleted != 1 {
			t.Fatalf("batch %+v %v", res, err)
		}
		if err := sess.UnlockBlobs(ctx); err != nil {
			t.Fatal(err)
		}
		if done, err := sess.Finish(store.WithAudit(ctx, agentEntry(t, "account.purge_finish")), p.ID); err != nil || done.State != store.PurgeDone {
			t.Fatalf("finish %+v %v", done, err)
		}
	})
	t.Run("deletion", func(t *testing.T) {
		f, _ := expiredFixture(t)
		run := f.propose()
		f.confirm(run.ID)
		ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
		defer cancel()
		small := sessionStore(t, ctx, f.url)
		sess, ok, err := small.StartDeletionSession(ctx)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		defer sess.Close(ctx)
		// One session per process and run lock.
		if _, ok, err := f.st.StartDeletionSession(ctx); err != nil || ok {
			t.Fatalf("second session: %v %v", ok, err)
		}
		if ok, err := sess.TryLockBlobs(ctx); err != nil || !ok {
			t.Fatal(ok, err)
		}
		d, k, err := sess.Batch(store.WithAudit(ctx, agentEntry(t, "retention.batch")), run.ID, f.candidates(run.ID), 0)
		if err != nil || d != 2 || k != 0 {
			t.Fatalf("batch %d %d %v", d, k, err)
		}
		if err := sess.UnlockBlobs(ctx); err != nil {
			t.Fatal(err)
		}
		if err := sess.Finish(store.WithAudit(ctx, agentEntry(t, "retention.finish")), run.ID); err != nil {
			t.Fatalf("finish: %v", err)
		}
	})
	t.Run("sweep", func(t *testing.T) {
		f, _ := expiredFixture(t)
		run := f.propose()
		f.confirm(run.ID)
		if _, _, err := f.st.DeleteBatch(f.ctx, run.ID, f.candidates(run.ID), 0); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
		defer cancel()
		small := sessionStore(t, ctx, f.url)
		sess, err := small.StartBlobSession(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close(ctx)
		if ok, err := sess.TryLockBlobs(ctx); err != nil || !ok {
			t.Fatal(ok, err)
		}
		rows, err := sess.DueBlobRemovals(ctx, time.Time{}, "", 10)
		if err != nil || len(rows) != 2 {
			t.Fatalf("due %+v %v", rows, err)
		}
		if n, err := sess.DoneBlobRemovals(ctx, []string{rows[0].SHA256, rows[1].SHA256}); err != nil || n != 2 {
			t.Fatalf("done %d %v", n, err)
		}
		if err := sess.Audit(ctx, *agentEntry(t, "retention.sweep")); err != nil {
			t.Fatalf("audit: %v", err)
		}
	})
	t.Run("missing partition", func(t *testing.T) {
		f := newPurgeFixture(t)
		p := f.confirm(1)
		conn := connect(t, f.url)
		ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
		defer cancel()
		small := sessionStore(t, ctx, f.url)
		sess, ok, err := small.StartPurgeSession(ctx)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		defer sess.Close(ctx)
		if ok, err := sess.TryLockBlobs(ctx); err != nil || !ok {
			t.Fatal(ok, err)
		}
		detachCurrentMonth(t, conn)
		// The insert fails and rolls the batch back; the partition is
		// created on the session's connection for the next attempt.
		entry := &store.AuditEntry{Actor: store.ActorCLI, Action: "account.purge_batch"}
		if _, err := sess.Batch(store.WithAudit(ctx, entry), p.ID, 10, 0); !errors.Is(err, store.ErrAuditUnavailable) || ctx.Err() != nil {
			t.Fatalf("batch without partition: %v (ctx %v)", err, ctx.Err())
		}
		if !slices.Contains(partitions(t, conn), partitionName(0)) {
			t.Fatalf("partition not created: %v", partitions(t, conn))
		}
		entry = &store.AuditEntry{Actor: store.ActorCLI, Action: "account.purge_batch"}
		if res, err := sess.Batch(store.WithAudit(ctx, entry), p.ID, 10, 0); err != nil || !res.Done {
			t.Fatalf("next attempt %+v %v", res, err)
		}
	})
}
