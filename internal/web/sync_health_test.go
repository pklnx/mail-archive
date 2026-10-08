package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// newHealthFixture runs a server with a 6h schedule (not started) and the
// default threshold of 3.
func newHealthFixture(t *testing.T) *authFixture {
	t.Helper()
	return newAuthFixtureOpts(t, Options{Runner: &archive.Runner{Interval: 6 * time.Hour}})
}

func (f *authFixture) imapAccount(name string, owner *store.User) *store.Account {
	f.t.Helper()
	a := &store.Account{Name: name, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, Enabled: true}
	if owner != nil {
		a.OwnerID = &owner.ID
	}
	if err := f.st.CreateAccount(context.Background(), a); err != nil {
		f.t.Fatal(err)
	}
	return a
}

func (f *authFixture) runs(a *store.Account, status string, n int) {
	f.t.Helper()
	ctx := context.Background()
	for range n {
		run, err := f.st.StartSyncRun(ctx, a.ID)
		if err != nil {
			f.t.Fatal(err)
		}
		run.Status, run.Health = status, store.HealthSuccess
		if status == "failed" {
			run.Health, run.Error = store.HealthFailure, "LOGIN failed"
		}
		if err := f.st.FinishSyncRun(ctx, run); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *authFixture) syncHealth(host string) (int, string) {
	f.t.Helper()
	req, _ := http.NewRequest("GET", f.srv.URL+"/healthz/sync", nil)
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

func TestSyncHealthEndpoint(t *testing.T) {
	f := newHealthFixture(t)
	owner := f.addUser("kim", "correct horse battery", false)

	// No accounts: healthy, without a login.
	if status, body := f.syncHealth(""); status != 200 || body != `{"status":"ok"}` {
		t.Fatalf("empty: %d %s", status, body)
	}
	good := f.imapAccount("good-secret-name", owner)
	bad := f.imapAccount("bad-secret-name", owner)
	f.runs(good, "ok", 1)
	f.runs(bad, "failed", 2)
	// The answer is cached for 15 seconds.
	f.clock.Advance(syncHealthTTL)
	if status, body := f.syncHealth(""); status != 200 {
		t.Fatalf("two failures: %d %s", status, body)
	}
	f.runs(bad, "failed", 1)
	if status, _ := f.syncHealth(""); status != 200 {
		t.Fatalf("cache not used: %d", status)
	}
	f.clock.Advance(syncHealthTTL)
	status, body := f.syncHealth("")
	if status != 503 {
		t.Fatalf("three failures: %d %s", status, body)
	}
	var out syncHealthJSON
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "degraded" || !slices.Equal(out.Failing, []int64{bad.ID}) || len(out.Stale) != 0 {
		t.Fatalf("degraded: %s", body)
	}
	for _, leak := range []string{"secret-name", "kim", "LOGIN"} {
		if strings.Contains(body, leak) {
			t.Fatalf("body names %q: %s", leak, body)
		}
	}
	// Disabled, removed and import accounts are ignored.
	ctx := context.Background()
	disabled := false
	fresh, _ := f.st.GetAccount(ctx, bad.ID)
	if err := f.st.UpdateAccount(ctx, fresh.Ref(), store.AccountChange{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	imp := &store.Account{Name: "old", Kind: store.KindImport, OwnerID: &owner.ID}
	if err := f.st.CreateAccount(ctx, imp); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(syncHealthTTL)
	if status, body := f.syncHealth(""); status != 200 {
		t.Fatalf("disabled account counted: %d %s", status, body)
	}

	// A host that is not allowed is rejected.
	if status, _ := f.syncHealth("evil.example"); status != http.StatusForbidden {
		t.Fatalf("foreign host: %d", status)
	}
}

func TestSyncHealthStale(t *testing.T) {
	f := newHealthFixture(t)
	owner := f.addUser("kim", "correct horse battery", false)
	a := f.imapAccount("work", owner)
	never := f.imapAccount("never", owner)
	f.runs(a, "ok", 1)
	// Just synced or just created: fine.
	if status, body := f.syncHealth(""); status != 200 {
		t.Fatalf("fresh: %d %s", status, body)
	}
	// 13 hours later, both missed two 6h intervals.
	f.clock.Advance(13 * time.Hour)
	status, body := f.syncHealth("")
	var out syncHealthJSON
	_ = json.Unmarshal([]byte(body), &out)
	want := []int64{a.ID, never.ID}
	slices.Sort(want)
	if status != 503 || !slices.Equal(out.Stale, want) || len(out.Failing) != 0 {
		t.Fatalf("stale: %d %s", status, body)
	}
}

func TestSyncHealthWithoutSchedule(t *testing.T) {
	f := newAuthFixture(t)
	owner := f.addUser("kim", "correct horse battery", false)
	f.imapAccount("never", owner)
	f.clock.Advance(100 * time.Hour)
	if status, body := f.syncHealth(""); status != 200 {
		t.Fatalf("stale without a schedule: %d %s", status, body)
	}
}

func TestHealthInAccountsAndStatus(t *testing.T) {
	f := newHealthFixture(t)
	kim := f.addUser("kim", "correct horse battery", false)
	a := f.imapAccount("work", kim)
	f.runs(a, "failed", 4)
	f.loginOK("kim", "correct horse battery")

	var accounts accountsResponse
	_, raw := f.do("GET", "/api/accounts", nil)
	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, &accounts)
	if accounts.AlertAfter != 3 || accounts.OtherFailing != nil || raw["otherFailing"] != nil {
		t.Fatalf("accounts: %s", b)
	}
	sync := accounts.Accounts[0].Sync
	if sync.FailureStreak != 4 || sync.FailingSince == nil || sync.Health != "failing" {
		t.Fatalf("sync: %+v", sync)
	}
	status := f.expect("GET", "/api/status", nil, 200)
	acc := status["accounts"].([]any)[0].(map[string]any)
	if acc["failureStreak"] != float64(4) || acc["health"] != "failing" {
		t.Fatalf("status: %v", acc)
	}

	// A success resets it.
	f.runs(a, "partial", 1)
	_, raw = f.do("GET", "/api/accounts", nil)
	s := raw["accounts"].([]any)[0].(map[string]any)["sync"].(map[string]any)
	if s["failureStreak"] != float64(0) || s["failingSince"] != nil || s["health"] != "ok" {
		t.Fatalf("after success: %v", s)
	}
}

func TestAdminsSeeOnlyCountsOfOthersFailing(t *testing.T) {
	f := newHealthFixture(t)
	admin := f.addUser("admin", "correct horse battery", true)
	kim := f.addUser("kim", "correct horse battery", false)
	mine := f.imapAccount("admin-mail", admin)
	theirs := f.imapAccount("kims-secret-mail", kim)
	other := f.imapAccount("kims-other-mail", kim)
	f.runs(mine, "failed", 3)
	f.runs(theirs, "failed", 3)
	f.runs(other, "failed", 1)

	f.loginOK("admin", "correct horse battery")
	_, raw := f.do("GET", "/api/accounts", nil)
	b, _ := json.Marshal(raw)
	if raw["otherFailing"] != float64(1) || strings.Contains(string(b), "kims-") {
		t.Fatalf("admin accounts: %s", b)
	}
	users := f.expect("GET", "/api/users", nil, 200)["users"].([]any)
	failing := map[string]any{}
	for _, u := range users {
		m := u.(map[string]any)
		failing[m["name"].(string)] = m["failingAccounts"]
	}
	if failing["admin"] != float64(1) || failing["kim"] != float64(1) {
		t.Fatalf("users: %v", failing)
	}

	// kim sees her own failing account by name, and no count of others.
	g := f.as()
	g.loginOK("kim", "correct horse battery")
	_, raw = g.do("GET", "/api/accounts", nil)
	b, _ = json.Marshal(raw)
	if _, ok := raw["otherFailing"]; ok || strings.Contains(string(b), "admin-mail") || !strings.Contains(string(b), "kims-secret-mail") {
		t.Fatalf("user accounts: %s", b)
	}
}
