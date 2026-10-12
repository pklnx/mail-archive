package web

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

func TestAccountSyncIntervalAPI(t *testing.T) {
	f := newManageFixtureOpts(t, nil, archive.Schedule{Sync: 6 * time.Hour, SyncMin: 15 * time.Minute}, testUser(t))
	ctx := context.Background()

	// A bad value fails before the login check: with a wrong password too,
	// the answer is 400, not 422.
	bad := f.newAccount("private", "alice", "wrong")
	bad["syncInterval"] = "10m"
	if body := f.do("POST", "/api/accounts", bad, 400); !strings.Contains(string(body), "syncInterval: want default, manual, or a duration of whole minutes from 15m to 720h") {
		t.Errorf("POST below the floor: %s", body)
	}
	good := f.newAccount("private", "alice", "secret")
	good["syncInterval"] = "30m"
	f.do("POST", "/api/accounts", good, 201)
	a := f.waitIdle("private")
	if a.SyncInterval != "30m0s" || a.Sync.SyncEvery == nil || *a.Sync.SyncEvery != "30m0s" ||
		a.Sync.NextAt == nil || !a.Sync.NextAt.Equal(a.Sync.LastRun.StartedAt.Add(30*time.Minute)) {
		t.Fatalf("after POST: %+v %+v", a.SyncInterval, a.Sync)
	}
	if r := f.accounts(); r.SyncInterval != "6h0m0s" || r.SyncIntervalMin != "15m0s" {
		t.Errorf("top level: %q %q", r.SyncInterval, r.SyncIntervalMin)
	}

	patch := func(v string, want int) string {
		t.Helper()
		return string(f.do("PATCH", "/api/accounts/private", map[string]any{"syncInterval": v}, want))
	}
	for _, c := range []struct{ in, stored string }{
		{"default", "default"}, {"manual", "manual"}, {"1h30m", "1h30m0s"}, {"15m", "15m0s"}, {"720h", "720h0m0s"},
	} {
		patch(c.in, 204)
		if got := f.account("private").SyncInterval; got != c.stored {
			t.Errorf("PATCH %q: stored %q, want %q", c.in, got, c.stored)
		}
	}
	for _, in := range []string{"abc", "90s", "5m30s", "4m", "10m", "721h", "0", ""} {
		if body := patch(in, 400); !strings.Contains(body, "from 15m to 720h") {
			t.Errorf("PATCH %q: %s", in, body)
		}
	}
	// What GET says for manual and for the default.
	patch("manual", 204)
	if s := f.account("private").Sync; s.SyncEvery != nil || s.NextAt != nil {
		t.Errorf("manual: %+v", s)
	}
	patch("default", 204)
	if s := f.account("private").Sync; s.SyncEvery == nil || *s.SyncEvery != "6h0m0s" || s.NextAt == nil {
		t.Errorf("default: %+v", s)
	}

	// A value stored before the floor was raised is accepted unchanged.
	acc, _ := f.st.ListAccountsByName(ctx, "private")
	ten := 10 * time.Minute
	if err := f.st.UpdateAccount(ctx, acc[0].Ref(), store.AccountChange{SyncInterval: &store.OwnInterval{D: &ten}}); err != nil {
		t.Fatal(err)
	}
	before := len(auditLog(t, f.st))
	patch("10m", 204)
	a = f.account("private")
	if a.SyncInterval != "10m0s" || a.Sync.SyncEvery == nil || *a.Sync.SyncEvery != "15m0s" {
		t.Errorf("below the raised floor: %q %+v", a.SyncInterval, a.Sync)
	}
	log := auditLog(t, f.st)
	if last := log[len(log)-1]; len(log) != before+1 || strings.Contains(string(last.Details), "sync_interval") {
		t.Errorf("unchanged value in the audit entry: %s", last.Details)
	}

	// A change is audited with its value.
	patch("manual", 204)
	log = auditLog(t, f.st)
	var details map[string]any
	_ = json.Unmarshal(log[len(log)-1].Details, &details)
	if log[len(log)-1].Action != "account.change" || details["sync_interval"] != "manual" ||
		!slices.Equal(toStrings(details["fields"]), []string{"sync_interval"}) {
		t.Errorf("audit: %s %s", log[len(log)-1].Action, log[len(log)-1].Details)
	}

	// Import accounts have no schedule.
	tester, _ := f.st.GetUserByName(ctx, "tester")
	imp := &store.Account{Kind: store.KindImport, Name: "old", OwnerID: &tester.ID}
	if err := f.st.CreateAccount(ctx, imp); err != nil {
		t.Fatal(err)
	}
	f.do("PATCH", "/api/accounts/old", map[string]any{"syncInterval": "30m"}, 409)
	if a := f.account("old"); a.SyncInterval != "default" || a.Sync.SyncEvery != nil || a.Sync.NextAt != nil {
		t.Errorf("import account: %q %+v", a.SyncInterval, a.Sync)
	}
}

func toStrings(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func TestSyncHealthPerAccountInterval(t *testing.T) {
	// The runner is not started, so what is enqueued stays queued.
	runner := &archive.Runner{Schedule: archive.Schedule{Sync: 6 * time.Hour, SyncMin: 5 * time.Minute}}
	f := newAuthFixtureOpts(t, Options{Runner: runner})
	owner := f.addUser("kim", "correct horse battery", false)
	ctx := context.Background()
	quarter, zero := 15*time.Minute, time.Duration(0)
	own := func(name string, d *time.Duration) *store.Account {
		a := f.imapAccount(name, owner)
		if d != nil {
			if err := f.st.UpdateAccount(ctx, a.Ref(), store.AccountChange{SyncInterval: &store.OwnInterval{D: d}}); err != nil {
				t.Fatal(err)
			}
		}
		f.runs(a, "ok", 1)
		return a
	}
	fast := own("fast", &quarter)
	queued := own("queued", &quarter)
	running := own("running", &quarter)
	manual := own("manual", &zero)
	def := own("default", nil)
	stale := func() []int64 {
		t.Helper()
		f.clock.Advance(syncHealthTTL)
		_, body := f.syncHealth("")
		var out syncHealthJSON
		_ = json.Unmarshal([]byte(body), &out)
		return out.Stale
	}
	f.clock.Advance(time.Hour)
	if got := stale(); len(got) != 0 {
		t.Fatalf("after an hour: %v", got)
	}
	// 15 minutes is stale after 75; queued and running accounts are not.
	runner.Enqueue(queued.ID)
	unlock, ok, err := f.st.TryLockSync(ctx, running.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer unlock()
	f.clock.Advance(16 * time.Minute)
	if got := stale(); !slices.Equal(got, []int64{fast.ID}) {
		t.Fatalf("after 76 minutes: %v, want only %d", got, fast.ID)
	}
	// The default is stale after 12 hours; manual never.
	f.clock.Advance(11 * time.Hour)
	got := stale()
	if !slices.Contains(got, def.ID) || slices.Contains(got, manual.ID) {
		t.Fatalf("after 12 hours: %v", got)
	}

	// /api/status and /api/accounts agree.
	f.loginOK("kim", "correct horse battery")
	status := f.expect("GET", "/api/status", nil, 200)
	for _, a := range status["accounts"].([]any) {
		m := a.(map[string]any)
		switch m["name"] {
		case "manual":
			if m["health"] != "ok" || m["syncEvery"] != nil || m["nextAt"] != nil {
				t.Errorf("status manual: %v", m)
			}
		case "fast":
			if m["health"] != "stale" || m["syncEvery"] != "15m0s" || m["nextAt"] == nil {
				t.Errorf("status fast: %v", m)
			}
		case "queued":
			if m["health"] != "ok" {
				t.Errorf("status queued: %v", m)
			}
		}
	}
}
