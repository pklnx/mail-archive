package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// waitFillID waits until the fill has the status and its account is idle.
func (f *manageFixture) waitFillID(name string, id int64, status string) *fillJSON {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a := f.account(name)
		for _, fj := range a.Fills {
			if fj.ID == id && fj.Status == status && a.Sync.State == "idle" {
				return fj
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("fill %d of %q did not reach %s: %+v", id, name, status, f.account(name).Fills)
	return nil
}

func TestRecurringFillAPI(t *testing.T) {
	f, acc := fillFixtureOpts(t, archive.Schedule{Sync: 6 * time.Hour, SyncMin: time.Hour})
	ctx := context.Background()

	// A recurring fill has no cut-off; its interval is checked.
	for _, c := range []struct {
		set  map[string]any
		want string
	}{
		{map[string]any{"keepRunning": true, "before": "2026-06-01T00:00:00Z"}, "exclude each other"},
		{map[string]any{"interval": "12h"}, "interval needs keepRunning"},
		{map[string]any{"keepRunning": true, "interval": "manual"}, "invalid interval"},
		{map[string]any{"keepRunning": true, "interval": "30m"}, "below the server's minimum"},
	} {
		body := f.fillBody("secret")
		for k, v := range c.set {
			body[k] = v
		}
		if raw := f.do("POST", "/api/accounts/journal/fills", body, http.StatusBadRequest); !strings.Contains(string(raw), c.want) {
			t.Errorf("%v: %s", c.set, raw)
		}
	}

	n := len(auditLog(t, f.st))
	body := f.fillBody("secret")
	body["keepRunning"], body["interval"], body["confirmFolders"] = true, "12h", true
	var started struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(f.do("POST", "/api/accounts/journal/fills", body, http.StatusAccepted), &started); err != nil {
		t.Fatal(err)
	}
	fill := f.waitFillID("journal", started.ID, store.FillOK)
	if !fill.KeepRunning || fill.Before != nil || fill.RecurringSince == nil || fill.RunStartedAt == nil ||
		fill.Interval == nil || *fill.Interval != "12h0m0s" || fill.Every == nil || *fill.Every != "12h0m0s" ||
		fill.NextAt == nil || !fill.NextAt.Equal(fill.RunStartedAt.Add(12*time.Hour)) ||
		fill.Health == nil || fill.Health.State != store.HealthOK || fill.Health.LastSuccessAt == nil {
		t.Fatalf("recurring fill %+v", fill)
	}
	// The user confirmed Spam: copied now and in later runs.
	if len(fill.Folders) != 2 || fill.Folders[1].Source != "Spam" || fill.Folders[1].Imported != 1 {
		t.Errorf("folders %+v", fill.Folders)
	}
	if got, _ := f.st.GetFill(ctx, started.ID); got.PasswordEnc == nil {
		t.Error("a recurring fill lost its password")
	}
	a := f.account("journal")
	if len(a.Fills) != 1 || a.Fill == nil || a.Fill.ID != started.ID {
		t.Errorf("account fills %+v", a.Fills)
	}

	id := fmt.Sprint(started.ID)
	path := "/api/accounts/journal/fills/" + id
	for _, c := range []struct {
		body   map[string]any
		status int
	}{
		{map[string]any{}, http.StatusBadRequest},
		{map[string]any{"password": "secret"}, http.StatusBadRequest},
		{map[string]any{"interval": "manual"}, http.StatusBadRequest},
		{map[string]any{"interval": "30m"}, http.StatusBadRequest},
		{map[string]any{"keepRunning": false, "interval": "24h"}, http.StatusBadRequest},
		{map[string]any{"keepRunning": true, "password": "secret"}, http.StatusConflict},
	} {
		f.do("PATCH", path, c.body, c.status)
	}
	f.do("POST", path+"/resume", map[string]any{"password": "secret"}, http.StatusConflict)
	f.do("PATCH", path, map[string]any{"interval": "24h"}, http.StatusNoContent)
	f.do("PATCH", path, map[string]any{"interval": "24h"}, http.StatusNoContent)
	if got := f.account("journal").Fill; got.Interval == nil || *got.Interval != "24h0m0s" || *got.Every != "24h0m0s" {
		t.Errorf("interval %+v", got)
	}

	// Stop, and resume with the password.
	f.do("PATCH", path, map[string]any{"keepRunning": false}, http.StatusNoContent)
	f.do("PATCH", path, map[string]any{"keepRunning": false}, http.StatusConflict)
	f.do("PATCH", path, map[string]any{"interval": "12h"}, http.StatusConflict)
	if got := f.account("journal").Fill; got.KeepRunning || got.Status != store.FillCancelled || got.Health != nil || got.Every != nil {
		t.Errorf("stopped %+v", got)
	}
	if got, _ := f.st.GetFill(ctx, started.ID); got.PasswordEnc != nil {
		t.Error("a stopped fill kept its password")
	}
	f.do("POST", path+"/resume", map[string]any{"password": "secret"}, http.StatusAccepted)
	if fill := f.waitFillID("journal", started.ID, store.FillOK); !fill.KeepRunning {
		t.Errorf("resumed %+v", fill)
	}

	// An ended one-time fill is switched on with its password; trash and
	// spam folders it did not copy need a yes.
	cutoff := time.Now()
	once := &store.Fill{AccountID: acc.ID, Host: f.host, Port: f.port, TLSMode: store.TLSModeNone, Username: "alice@example.com", Cutoff: &cutoff}
	if err := f.st.CreateFill(ctx, once, func(int64) ([]byte, error) { return []byte("x"), nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CancelFill(ctx, once.ID); err != nil {
		t.Fatal(err)
	}
	if a := f.account("journal"); len(a.Fills) != 2 || a.Fill.ID != once.ID || a.Fills[1].ID != started.ID || a.Fill.Interval != nil {
		t.Fatalf("fills %+v", a.Fills)
	}
	oncePath := "/api/accounts/journal/fills/" + fmt.Sprint(once.ID)
	f.do("PATCH", oncePath, map[string]any{"keepRunning": true}, http.StatusBadRequest)
	f.do("PATCH", oncePath, map[string]any{"keepRunning": true, "password": "wrong"}, http.StatusUnprocessableEntity)
	f.do("PATCH", oncePath, map[string]any{"interval": "12h"}, http.StatusConflict)
	var c folderConfirmation
	if err := json.Unmarshal(f.do("PATCH", oncePath, map[string]any{"keepRunning": true, "password": "secret"}, http.StatusConflict), &c); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.SuggestedExclusions, []string{"Spam"}) {
		t.Errorf("confirmation %+v", c)
	}
	f.do("PATCH", oncePath, map[string]any{"keepRunning": true, "password": "secret", "confirmFolders": true}, http.StatusAccepted)
	if fill := f.waitFillID("journal", once.ID, store.FillOK); !fill.KeepRunning || fill.Before == nil || *fill.Interval != "default" ||
		*fill.Every != "6h0m0s" {
		t.Errorf("switched on %+v", fill)
	}

	var actions []string
	for _, e := range newEntries(t, f.st, n) {
		actions = append(actions, e.Action)
		if strings.Contains(string(e.Details), "secret") {
			t.Errorf("%s names the password: %s", e.Action, e.Details)
		}
		d := auditDetails(t, e)
		switch {
		case e.Action == "data.fill":
			if d["keep_running"] != true || d["interval"] != "12h0m0s" || d["before"] != nil {
				t.Errorf("data.fill %s", e.Details)
			}
		case e.Action == "data.fill_change" && d["fill"] == float64(started.ID):
			if fmt.Sprint(d["interval"]) != "map[from:12h0m0s to:24h0m0s]" || d["keep_running"] != nil {
				t.Errorf("interval change %s", e.Details)
			}
		case e.Action == "data.fill_change":
			if fmt.Sprint(d["keep_running"]) != "map[from:false to:true]" || fmt.Sprint(d["interval"]) != "map[from:<nil> to:default]" {
				t.Errorf("switch on %s", e.Details)
			}
		}
	}
	// Runs of a recurring fill are not audited.
	want := []string{"data.fill", "data.fill_change", "data.fill_cancel", "data.fill_resume", "data.fill_change"}
	if !slices.Equal(actions, want) {
		t.Errorf("audit %v, want %v", actions, want)
	}
}

func TestSyncHealthRecurringFills(t *testing.T) {
	f := newHealthFixture(t)
	owner := f.addUser("kim", "correct horse battery", false)
	ctx := context.Background()
	journal := &store.Account{Name: "journal", Enabled: true, OwnerID: &owner.ID}
	if err := f.st.CreateSMTPAccount(ctx, journal, []string{"archive@archive.test"}); err != nil {
		t.Fatal(err)
	}
	fill := &store.Fill{AccountID: journal.ID, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "secret-mailbox", KeepRunning: true}
	if err := f.st.CreateFill(ctx, fill, func(int64) ([]byte, error) { return []byte("x"), nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.st.FinishFill(ctx, fill.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	if status, body := f.syncHealth(""); status != 200 {
		t.Fatalf("healthy fill: %d %s", status, body)
	}
	for range 3 {
		if err := f.st.StartFillRun(ctx, fill.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := f.st.FinishFill(ctx, fill.ID, store.FillFailed, "LOGIN failed"); err != nil {
			t.Fatal(err)
		}
	}
	f.clock.Advance(syncHealthTTL)
	status, body := f.syncHealth("")
	var out syncHealthJSON
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	// The smtp account's ID, in the list for accounts.
	if status != 503 || !slices.Equal(out.Failing, []int64{journal.ID}) || len(out.Stale) != 0 {
		t.Fatalf("failing fill: %d %s", status, body)
	}
	for _, leak := range []string{"secret-mailbox", "kim", "LOGIN", "fill"} {
		if strings.Contains(body, leak) {
			t.Fatalf("body names %q: %s", leak, body)
		}
	}
	// A successful run ends the streak; 13 hours without one make it stale.
	if err := f.st.StartFillRun(ctx, fill.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.st.FinishFill(ctx, fill.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(syncHealthTTL)
	if status, body := f.syncHealth(""); status != 200 {
		t.Fatalf("after a success: %d %s", status, body)
	}
	f.clock.Advance(13 * time.Hour)
	status, body = f.syncHealth("")
	out = syncHealthJSON{}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if status != 503 || !slices.Equal(out.Stale, []int64{journal.ID}) || len(out.Failing) != 0 {
		t.Fatalf("stale fill: %d %s", status, body)
	}
	// A stopped fill counts no more.
	if err := f.st.CancelFill(ctx, fill.ID); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(syncHealthTTL)
	if status, body := f.syncHealth(""); status != 200 {
		t.Fatalf("stopped fill: %d %s", status, body)
	}
}

// TestSyncHealthRecurringFillsBusy: a recurring fill is not stale while it
// waits for the hold after the server's start, nor while another run holds
// its account's lock.
func TestSyncHealthRecurringFillsBusy(t *testing.T) {
	// The hold ends 70 minutes after the server's start, which on the
	// server's clock comes an hour from now.
	fills := &archive.FillRunner{HoldUntil: time.Now().Add(time.Hour + archive.RecurringStartHold)}
	f := newAuthFixtureOpts(t, Options{
		Runner: &archive.Runner{Schedule: archive.Schedule{Sync: 15 * time.Minute, SyncMin: 5 * time.Minute}}, Fills: fills,
	})
	ctx := context.Background()
	journal := &store.Account{Name: "journal", Enabled: true}
	if err := f.st.CreateSMTPAccount(ctx, journal, []string{"archive@archive.test"}); err != nil {
		t.Fatal(err)
	}
	fill := &store.Fill{AccountID: journal.ID, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", KeepRunning: true}
	if err := f.st.CreateFill(ctx, fill, func(int64) ([]byte, error) { return []byte("x"), nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.st.FinishFill(ctx, fill.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	stale := func() bool {
		t.Helper()
		f.clock.Advance(syncHealthTTL)
		status, body := f.syncHealth("")
		var out syncHealthJSON
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return status == 503 && slices.Equal(out.Stale, []int64{journal.ID})
	}
	// 15 minutes: stale after 75 minutes without a success, but not
	// before 75 minutes after the server's start. Two hours on, the hold
	// is not over yet.
	f.clock.Advance(2 * time.Hour)
	if stale() {
		t.Error("stale during the hold after the start")
	}
	// Long after the start, it counts.
	f.clock.Advance(3 * time.Hour)
	if !stale() {
		t.Error("not stale long after the start")
	}
	// Another run holds the account's lock: the fill waits behind it.
	unlock, ok, err := f.st.TryLockSyncForWrite(ctx, journal.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer unlock()
	if stale() {
		t.Error("stale while waiting behind the account's lock")
	}
}
