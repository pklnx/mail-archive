package notify_test

import (
	"mime"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/notify"
	"github.com/pklnx/mail-archive/internal/store"
)

// transition records a transition of the instance (group 0) or a group to
// state, as part 2's gate does: state, state_since and the next event_seq.
func (e *env) transition(group int64, state string) {
	e.t.Helper()
	if group == 0 {
		e.exec("UPDATE storage_states SET state = $1, state_since = now(), event_seq = event_seq + 1 WHERE group_id IS NULL", state)
		return
	}
	e.exec(`INSERT INTO storage_states (group_id, state, event_seq) VALUES ($1, $2, 1)
		ON CONFLICT (group_id) DO UPDATE SET state = EXCLUDED.state, state_since = now(), event_seq = storage_states.event_seq + 1`,
		group, state)
}

// storageEvents returns the storage events of a request.
func storageEvents(t *testing.T, r received) []map[string]any {
	t.Helper()
	list, _ := r.json["storage"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, v := range list {
		out = append(out, v.(map[string]any))
	}
	return out
}

func (e *env) group(name string) int64 {
	e.t.Helper()
	g, err := e.store.CreateGroup(e.ctx, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return g.ID
}

// TestStorageAlerts: one message per transition and scope, with the
// level, the figures and an ID per transition, so warn, ok and warn again
// are three messages with three IDs, and a delivered transition is not
// sent again.
func TestStorageAlerts(t *testing.T) {
	e := newEnv(t)
	e.exec("UPDATE storage_counter_base SET bytes = 820")
	rcv := newReceiver(t)
	n := e.notifier(idsWebhook(t, rcv.srv.URL, ""))
	n.StorageQuota = 1000

	e.deliver(n)
	if got := rcv.got(); len(got) != 0 {
		t.Fatalf("no transition, %d requests", len(got))
	}
	e.transition(0, "warn")
	e.deliver(n)
	e.deliver(n)
	got := rcv.got()
	if len(got) != 1 {
		t.Fatalf("%d requests after warn, want 1", len(got))
	}
	j := got[0].json
	evs := storageEvents(t, got[0])
	if j["event"] != "storage" || j["id"] != "storage:instance:1" || got[0].header.Get("X-Mail-Archive-Id") != "storage:instance:1" ||
		j["title"] != "Mail archive: storage at 82% of 1000 B" || len(evs) != 1 || len(j["accounts"].([]any)) != 0 {
		t.Fatalf("warn message: %v", j)
	}
	ev := evs[0]
	if ev["id"] != "storage:instance:1" || ev["level"] != "warn" || ev["scope"] != "instance" || ev["groupId"] != nil ||
		ev["usedBytes"] != 820.0 || ev["quotaBytes"] != 1000.0 || ev["percent"] != 82.0 || ev["since"] == nil {
		t.Fatalf("warn event: %v", ev)
	}
	if msg := j["message"].(string); !strings.HasPrefix(msg, "Storage of the instance: warn since ") || !strings.HasSuffix(msg, "820 B used of 1000 B (82%).") {
		t.Fatalf("warn text: %q", msg)
	}

	e.transition(0, "ok")
	e.deliver(n)
	e.transition(0, "warn")
	e.deliver(n)
	got = rcv.got()
	if len(got) != 3 {
		t.Fatalf("%d requests after ok and warn, want 3", len(got))
	}
	for i, want := range []string{"storage:instance:2", "storage:instance:3"} {
		if ev := storageEvents(t, got[i+1])[0]; ev["id"] != want || got[i+1].json["id"] != want {
			t.Errorf("request %d: %v, want %s", i+2, got[i+1].json, want)
		}
	}
	if storageEvents(t, got[1])[0]["level"] != "ok" || got[1].json["title"] != "Mail archive: storage back to normal, at 82% of 1000 B" {
		t.Errorf("ok message: %v", got[1].json)
	}
	if !strings.Contains(e.logs.String(), "storage alert sent") {
		t.Errorf("log: %s", e.logs)
	}
}

// TestStorageAlertGroup: a group's transition names the group by ID, with
// its own quota, and its usage.
func TestStorageAlertGroup(t *testing.T) {
	e := newEnv(t)
	g := e.group("acme")
	e.exec("UPDATE customer_groups SET quota_bytes = 2048 WHERE id = $1", g)
	e.exec("INSERT INTO storage_group_deltas (group_id, messages, bytes) VALUES ($1, 3, 2100)", g)
	rcv := newReceiver(t)
	n := e.notifier(idsWebhook(t, rcv.srv.URL, "ntfy"))
	e.transition(g, "full")
	e.deliver(n)
	got := rcv.got()
	if len(got) != 1 {
		t.Fatalf("%d requests", len(got))
	}
	title := decodeTitle(t, got[0].header.Get("Title"))
	if want := "Mail archive: storage of group " + itoa(g) + " at 102.5% of 2.0 KiB"; title != want {
		t.Errorf("title %q, want %q", title, want)
	}
	body := string(got[0].body)
	if !strings.Contains(body, "Storage of group "+itoa(g)+": full since ") || !strings.Contains(body, "2.1 KiB used of 2.0 KiB (102.5%)") ||
		!strings.Contains(body, "New mail from IMAP syncs and imports is not archived") || strings.Contains(body, "acme") {
		t.Errorf("body %q", body)
	}
	if got[0].header.Get("Priority") != "high" || got[0].header.Get("Tags") != "warning" {
		t.Errorf("headers %v", got[0].header)
	}
	// Back to ok: default priority.
	e.transition(g, "ok")
	e.deliver(n)
	if got := rcv.got(); len(got) != 2 || got[1].header.Get("Priority") != "default" {
		t.Fatalf("ok: %d %v", len(got), got[len(got)-1].header)
	}
}

// TestStorageAlertRetryAndRemovedGroup: a failed request is retried with
// the same ID; a group removed with its state row leaves nothing to send;
// transitions written faster than they are sent go out once, as the
// latest.
func TestStorageAlertRetryAndRemovedGroup(t *testing.T) {
	e := newEnv(t)
	rcv := newReceiver(t, http.StatusInternalServerError)
	n := e.notifier(idsWebhook(t, rcv.srv.URL, ""))
	e.transition(0, "disk_low")
	e.deliver(n)
	e.deliver(n) // waits for the retry time
	if got := rcv.got(); len(got) != 1 {
		t.Fatalf("%d requests after a failure, want 1", len(got))
	}
	e.exec("UPDATE storage_states SET notify_next_at = now() - interval '1 second'")
	e.deliver(n)
	got := rcv.got()
	if len(got) != 2 || got[1].json["id"] != "storage:instance:1" || got[1].json["title"] != "Mail archive: data disk below its free-space reserve" {
		t.Fatalf("retry: %d %v", len(got), got[len(got)-1].json)
	}
	if ev := storageEvents(t, got[1])[0]; ev["quotaBytes"] != nil || ev["percent"] != nil || ev["level"] != "disk_low" {
		t.Fatalf("disk_low without a quota: %v", ev)
	}

	gone := e.group("gone")
	e.transition(gone, "warn")
	e.exec("DELETE FROM customer_groups WHERE id = $1", gone)
	e.deliver(n)
	if got := rcv.got(); len(got) != 2 {
		t.Fatalf("removed group: %d requests, want 2", len(got))
	}

	e.transition(0, "ok")
	e.transition(0, "warn")
	e.deliver(n)
	got = rcv.got()
	if len(got) != 3 || got[2].json["id"] != "storage:instance:3" || storageEvents(t, got[2])[0]["level"] != "warn" {
		t.Fatalf("two transitions: %d %v", len(got), got[len(got)-1].json)
	}
	var notified, seq int64
	if err := e.conn.QueryRow(e.ctx, "SELECT notified_seq, event_seq FROM storage_states WHERE group_id IS NULL").Scan(&notified, &seq); err != nil || notified != 3 || seq != 3 {
		t.Fatalf("notified %d of %d: %v", notified, seq, err)
	}
}

// TestStorageEventID: the ID names the scope and the transition.
func TestStorageEventID(t *testing.T) {
	g := int64(7)
	if a, b := notify.StorageEventID(nil, 4), notify.StorageEventID(&g, 4); a != "storage:instance:4" || b != "storage:7:4" {
		t.Fatalf("%s %s", a, b)
	}
	m := notify.BuildStorageMessage([]store.PendingStorageNotification{
		{ID: 1, State: "full", EventSeq: 2, UsedBytes: 10},
		{ID: 2, GroupID: &g, State: "overflow", EventSeq: 5, UsedBytes: 11},
	}, 0)
	if !strings.HasPrefix(m.ID, "batch-") || m.Title != "Mail archive: storage changed in 2 scopes" ||
		!strings.Contains(m.Body, "Storage of group 7: overflow since") || !strings.Contains(m.Body, "deferred with 452") {
		t.Fatalf("batch %+v", m)
	}
}

func decodeTitle(t *testing.T, raw string) string {
	t.Helper()
	title, err := new(mime.WordDecoder).DecodeHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	return title
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
