package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

func TestSMTPAccountsInTheAPI(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	ctx := context.Background()
	tester, err := f.st.GetUserByName(ctx, "tester")
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "journal", Enabled: true, OwnerID: &tester.ID}
	if err := f.st.CreateSMTPAccount(ctx, acc, []string{"archive@archive.test"}); err != nil {
		t.Fatal(err)
	}
	a := f.account("journal")
	if a.Kind != "smtp" || a.Host != "" || a.TLS != "" || !a.Enabled || a.LastReceivedAt != nil ||
		!slices.Equal(a.Addresses, []string{"archive@archive.test"}) || len(a.Folders) != 1 {
		t.Fatalf("smtp account: %+v", a)
	}

	blob, created, err := f.blobs.Put(strings.NewReader("From: a@example.com\r\nTo: b@example.com\r\nSubject: hi\r\n\r\nHello.\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := archive.BuildMeta(ctx, f.st, f.blobs, blob, created)
	if err != nil {
		t.Fatal(err)
	}
	env := store.Envelope{MailFrom: "a@example.com", RcptTo: []string{"bcc@example.com"}, ReceivedAt: time.Now(),
		ClientIP: netip.MustParseAddr("192.0.2.1"), HELO: "mx"}
	if _, err := f.st.ReceiveMessage(ctx, acc.ID, meta, env, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if a := f.account("journal"); a.LastReceivedAt == nil {
		t.Errorf("last received not set: %+v", a)
	}
	// The message detail shows the envelope, BCC recipient included.
	var msg struct {
		Locations []locationJSON `json:"locations"`
	}
	if err := json.Unmarshal(f.do("GET", "/api/messages/"+meta.SHA256, nil, 200), &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.Locations) != 1 || msg.Locations[0].Envelope == nil {
		t.Fatalf("locations %+v", msg.Locations)
	}
	if e := msg.Locations[0].Envelope; e.MailFrom != "a@example.com" || !slices.Equal(e.RcptTo, []string{"bcc@example.com"}) ||
		e.ClientIP != "192.0.2.1" || e.HELO != "mx" {
		t.Errorf("envelope %+v", e)
	}
	// to: finds the BCC recipient.
	var list struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(f.do("GET", "/api/messages?to=bcc%40example.com", nil, 200), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Messages) != 1 {
		t.Errorf("to: on the envelope found %+v", list)
	}

	// Never synced, no server to change; enabling works.
	for _, req := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/accounts/journal/sync", nil},
		{"GET", "/api/accounts/journal/server-folders", nil},
		{"PATCH", "/api/accounts/journal", map[string]any{"excludedFolders": []string{}}},
		{"PATCH", "/api/accounts/journal", map[string]any{"host": "h"}},
	} {
		body := f.do(req.method, req.path, req.body, http.StatusConflict)
		if !strings.Contains(string(body), "have no server") {
			t.Errorf("%s %s: %s", req.method, req.path, body)
		}
	}
	f.do("PATCH", "/api/accounts/journal", map[string]any{"enabled": false}, http.StatusNoContent)
	if a := f.account("journal"); a.Enabled {
		t.Error("still enabled")
	}
	f.do("PATCH", "/api/accounts/journal", map[string]any{"enabled": true}, http.StatusNoContent)

	var queued map[string]int
	if err := json.Unmarshal(f.do("POST", "/api/sync", nil, http.StatusAccepted), &queued); err != nil || queued["queued"] != 0 {
		t.Errorf("sync all: %v %v", queued, err)
	}
	health, err := f.st.SyncHealth(ctx, &tester.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := health[acc.ID]; ok {
		t.Error("smtp account has a sync health row")
	}

	// Removing keeps the mail and frees the address.
	var res map[string]string
	if err := json.Unmarshal(f.do("DELETE", "/api/accounts/journal", nil, 200), &res); err != nil || res["result"] != "removed" {
		t.Fatalf("delete: %v %v", res, err)
	}
	if a := f.account("journal"); !a.Removed || len(a.Addresses) != 0 {
		t.Errorf("after delete: %+v", a)
	}
}
