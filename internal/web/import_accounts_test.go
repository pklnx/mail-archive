package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
)

func TestImportAccountsInTheAPI(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	ctx := context.Background()
	f.do("POST", "/api/accounts", f.newAccount("private", "alice", "secret"), http.StatusCreated)
	f.waitIdle("private")

	tester, err := f.st.GetUserByName(ctx, "tester")
	if err != nil {
		t.Fatal(err)
	}
	imp := &store.Account{Kind: store.KindImport, Name: "old", OwnerID: &tester.ID}
	if err := f.st.CreateAccount(ctx, imp); err != nil {
		t.Fatal(err)
	}
	folder, err := f.st.GetOrCreateFolder(ctx, imp.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	meta := store.MessageMeta{SHA256: strings.Repeat("d", 64), Size: 1, StoredPath: "p"}
	if _, err := f.st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{meta}, []store.Location{{FolderID: folder.ID, UIDValidity: 1, UID: 1}}); err != nil {
		t.Fatal(err)
	}

	a := f.account("old")
	if a.Kind != "import" || a.Host != "" || a.Port != 0 || a.TLS != "" || a.Username != "" || a.Enabled || len(a.Folders) != 1 {
		t.Fatalf("import account: %+v", a)
	}
	if p := f.account("private"); p.Kind != "imap" {
		t.Errorf("IMAP account kind %q", p.Kind)
	}

	for _, req := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/accounts/old/sync", nil},
		{"GET", "/api/accounts/old/server-folders", nil},
		{"PATCH", "/api/accounts/old", map[string]any{"enabled": true}},
		{"PATCH", "/api/accounts/old", map[string]any{"excludedFolders": []string{}}},
		{"PATCH", "/api/accounts/old", map[string]any{"name": "x", "host": "h"}},
	} {
		body := f.do(req.method, req.path, req.body, http.StatusConflict)
		if !strings.Contains(string(body), "import accounts cannot be synced or changed") {
			t.Errorf("%s %s: %s", req.method, req.path, body)
		}
	}
	f.do("PATCH", "/api/accounts/old", map[string]any{"name": "older"}, http.StatusNoContent)

	var queued map[string]int
	if err := json.Unmarshal(f.do("POST", "/api/sync", nil, http.StatusAccepted), &queued); err != nil || queued["queued"] != 1 {
		t.Errorf("sync all: %v %v", queued, err)
	}
	var status struct {
		Accounts []statusAccount `json:"accounts"`
	}
	if err := json.Unmarshal(f.do("GET", "/api/status", nil, 200), &status); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, s := range status.Accounts {
		kinds[s.Name] = s.Kind
	}
	if kinds["older"] != "import" || kinds["private"] != "imap" {
		t.Errorf("status kinds %v", kinds)
	}

	// Another user sees neither the account nor its mail.
	_, token := sessionFor(t, f.st, "other")
	g := *f
	g.cookie = token
	if accs := g.accounts().Accounts; len(accs) != 0 {
		t.Errorf("other user sees %+v", accs)
	}
	g.do("POST", "/api/accounts/older/sync", nil, http.StatusNotFound)

	// Delete keeps the mail.
	var res map[string]string
	if err := json.Unmarshal(f.do("DELETE", "/api/accounts/older", nil, 200), &res); err != nil || res["result"] != "removed" {
		t.Fatalf("delete: %v %v", res, err)
	}
	if a := f.account("older"); !a.Removed || len(a.Folders) != 1 {
		t.Errorf("after delete: %+v", a)
	}
}
