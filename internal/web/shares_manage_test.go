package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// A shared user only reads (#389, decision 3, test 4): every route under
// /api/accounts/{name} answers 404 for the owner's account, with a
// session and with a sync key (purges, admins only, 403), and the account stays as it was; POST
// /api/sync queues none of the owner's accounts.
func TestSharedUserCannotManage(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	f.do("POST", "/api/accounts", withConfirm(f.newAccount("private", "alice", "secret")), 201)
	f.waitIdle("private")
	acc, err := accountByName(f.st, "private")
	if err != nil {
		t.Fatal(err)
	}
	reader, token := sessionFor(t, f.st, "reader")
	if _, err := f.st.ShareAccount(context.Background(), acc.Ref(), reader.ID); err != nil {
		t.Fatal(err)
	}
	key, hash, hint, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateAPIKey(context.Background(), reader.ID, store.NewAPIKey{
		Hash: hash, Hint: hint, Name: "both", Scopes: []string{store.ScopeRead, store.ScopeSync},
	}); err != nil {
		t.Fatal(err)
	}
	g := *f
	g.cookie = token
	before := f.account("private")
	var checked int
	for _, rt := range (&Server{}).routes() {
		method, path, ok := strings.Cut(rt.pattern, " ")
		if !ok || !strings.HasPrefix(path, "/api/accounts/{name}") {
			continue
		}
		path = strings.NewReplacer("{name}", "private", "{id}", "1", "{user}", "reader").Replace(path)
		var body any = map[string]any{}
		if method == "PATCH" && !strings.Contains(path, "/fills/") {
			body = map[string]any{"enabled": false, "name": "taken"}
		}
		want := http.StatusNotFound
		if strings.HasSuffix(path, "/purge") {
			want = http.StatusForbidden // admins only, whoever owns the account
		}
		g.do(method, path, body, want)
		if rt.access == accessSync {
			req, _ := http.NewRequest(method, f.srv.URL+path, nil)
			req.Header.Set("Authorization", "Bearer "+key)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s with reader's sync key: %d", rt.pattern, resp.StatusCode)
			}
		}
		checked++
	}
	if checked < 12 {
		t.Fatalf("only %d account routes", checked)
	}
	var queued map[string]int
	if err := json.Unmarshal(g.do("POST", "/api/sync", nil, http.StatusAccepted), &queued); err != nil || queued["queued"] != 0 {
		t.Errorf("reader's sync queued %v %v", queued, err)
	}
	after := f.account("private")
	if after.Name != before.Name || after.Enabled != before.Enabled || after.Removed {
		t.Errorf("the account changed: %+v", after)
	}
	// reader still reads it.
	if got := g.accounts(); len(got.Accounts) != 0 || len(got.Shared) != 1 || got.Shared[0].Key != "tester/private" {
		t.Errorf("reader's accounts %+v", got)
	}
}
