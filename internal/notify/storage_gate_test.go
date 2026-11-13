package notify_test

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/pklnx/mail-archive/internal/quota"
)

// TestStorageGateToWebhook: a transition the storage gate persists (part
// 2) reaches the storage webhook (part 3) end to end: the gate's write is
// claimed, sent with the level, figures and event ID, and wakes the
// notifier. A gate that does not persist (a command) sends nothing.
func TestStorageGateToWebhook(t *testing.T) {
	e := newEnv(t)
	rcv := newReceiver(t)
	n := e.notifier(idsWebhook(t, rcv.srv.URL, ""))
	n.StorageQuota = 1000
	e.exec("UPDATE storage_counter_base SET bytes = 1050")
	var woken atomic.Int32
	cfg := quota.Config{InstanceQuota: 1000, Thresholds: quota.Thresholds{WarnPercent: 80, OverflowPercent: 10}}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	command := quota.New(cfg, e.store, nil, nil, quiet)
	command.Refresh(context.Background())
	if command.State(nil) != quota.Full {
		t.Fatalf("command gate: %s", command.State(nil))
	}
	e.deliver(n)
	if got := rcv.got(); len(got) != 0 {
		t.Fatalf("a command's gate sent %d alerts", len(got))
	}

	cfg.Persist, cfg.OnTransition = true, func() { woken.Add(1) }
	serve := quota.New(cfg, e.store, nil, nil, quiet)
	serve.Refresh(context.Background())
	if woken.Load() != 1 {
		t.Fatalf("notifier woken %d times", woken.Load())
	}
	e.deliver(n)
	got := rcv.got()
	if len(got) != 1 {
		t.Fatalf("%d requests after the transition, want 1", len(got))
	}
	evs := storageEvents(t, got[0])
	if len(evs) != 1 || evs[0]["id"] != "storage:instance:1" || evs[0]["level"] != "full" || evs[0]["usedBytes"] != 1050.0 ||
		evs[0]["quotaBytes"] != 1000.0 || evs[0]["scope"] != "instance" {
		t.Fatalf("storage event: %v", got[0].json)
	}
	// Nothing changes: no transition, no second alert.
	serve.Refresh(context.Background())
	e.deliver(n)
	if got := rcv.got(); len(got) != 1 || woken.Load() != 1 {
		t.Fatalf("%d requests, woken %d times, after a read without a change", len(got), woken.Load())
	}
}
