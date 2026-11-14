package notify_test

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/keepia/archive/internal/quota"
)

// TestStorageGateToWebhook: a transition the storage gate persists (part
// 2) reaches the storage webhook (part 3) end to end.
//   - While serve holds the storage state writer lock, a command's gate
//     writes nothing and nothing is sent.
//   - Without serve (cron), the command's gate writes the transition, and
//     the notifier of ./ma sync (Deliver at its end) sends it.
//   - serve's gate writes the next transition and wakes the notifier.
func TestStorageGateToWebhook(t *testing.T) {
	e := newEnv(t)
	rcv := newReceiver(t)
	n := e.notifier(idsWebhook(t, rcv.srv.URL, ""))
	n.StorageQuota = 1000
	e.exec("UPDATE storage_counter_base SET bytes = 1050")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	th := quota.Thresholds{WarnPercent: 80, OverflowPercent: 10}
	command := func() *quota.Gate {
		return quota.New(quota.Config{InstanceQuota: 1000, Thresholds: th, Persist: true, Writer: e.store.StorageWriter(false)},
			e.store, nil, nil, quiet)
	}

	serveWriter := e.store.StorageWriter(true)
	if _, ok, err := serveWriter.TryLock(context.Background()); !ok || err != nil {
		t.Fatalf("serve's writer lock: %v, %v", ok, err)
	}
	c := command()
	c.Refresh(context.Background())
	if c.State(nil) != quota.Full {
		t.Fatalf("command gate: %s", c.State(nil))
	}
	e.deliver(n)
	if got := rcv.got(); len(got) != 0 {
		t.Fatalf("a command's gate sent %d alerts while serve runs", len(got))
	}

	serveWriter.Close() // serve stopped: cron only
	command().Refresh(context.Background())
	e.deliver(n) // the end of ./ma sync
	got := rcv.got()
	if len(got) != 1 {
		t.Fatalf("%d requests after the command's transition, want 1", len(got))
	}
	evs := storageEvents(t, got[0])
	if len(evs) != 1 || evs[0]["id"] != "storage:instance:1" || evs[0]["level"] != "full" || evs[0]["usedBytes"] != 1050.0 ||
		evs[0]["quotaBytes"] != 1000.0 || evs[0]["scope"] != "instance" {
		t.Fatalf("storage event: %v", got[0].json)
	}

	var woken atomic.Int32
	serveWriter = e.store.StorageWriter(true)
	defer serveWriter.Close()
	serve := quota.New(quota.Config{InstanceQuota: 1000, Thresholds: th, Persist: true, Writer: serveWriter, ClaimWriter: true,
		OnTransition: func() { woken.Add(1) }}, e.store, nil, nil, quiet)
	serve.Refresh(context.Background())
	if woken.Load() != 0 {
		t.Fatal("woken without a transition")
	}
	e.exec("UPDATE storage_counter_base SET bytes = 500")
	serve.Refresh(context.Background())
	if woken.Load() != 1 {
		t.Fatalf("notifier woken %d times", woken.Load())
	}
	e.deliver(n)
	got = rcv.got()
	if len(got) != 2 || storageEvents(t, got[1])[0]["level"] != "ok" || storageEvents(t, got[1])[0]["id"] != "storage:instance:2" {
		t.Fatalf("after serve's transition: %d requests", len(got))
	}
	// Nothing changes: no transition, no further alert.
	serve.Refresh(context.Background())
	e.deliver(n)
	if got := rcv.got(); len(got) != 2 || woken.Load() != 1 {
		t.Fatalf("%d requests, woken %d times, after a read without a change", len(got), woken.Load())
	}
}
