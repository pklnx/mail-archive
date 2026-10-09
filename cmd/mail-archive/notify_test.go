package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func runNotifyTest(t *testing.T) (int, string) {
	t.Helper()
	cmd := newNotifyCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := runCmd(t, cmd, "", "test")
	if err != nil {
		out.WriteString(err.Error())
	}
	return exitCodeOf(err), out.String()
}

func TestNotifyTestCommand(t *testing.T) {
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", "postgres://unused")
	status := http.StatusOK
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		auth = r.Header.Get("Authorization")
		w.WriteHeader(status)
	}))
	defer srv.Close()

	t.Setenv("MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL", "")
	if code, out := runNotifyTest(t); code == 0 || !strings.Contains(out, "is not set") {
		t.Fatalf("without a URL: %d %s", code, out)
	}

	t.Setenv("MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL", srv.URL+"/secret-topic")
	t.Setenv("MAIL_ARCHIVE_NOTIFY_WEBHOOK_AUTHORIZATION", "Bearer tk_123")
	code, out := runNotifyTest(t)
	if code != 0 || !strings.Contains(out, "HTTP 200") || got["event"] != "test" || auth != "Bearer tk_123" {
		t.Fatalf("ok: %d %s %v", code, out, got)
	}
	status = http.StatusUnauthorized
	code, out = runNotifyTest(t)
	if code == 0 || !strings.Contains(out, "HTTP 401") {
		t.Fatalf("401: %d %s", code, out)
	}
	if strings.Contains(out, "secret-topic") || strings.Contains(out, "tk_123") {
		t.Fatalf("output shows secrets: %s", out)
	}
}

func TestFailedColumn(t *testing.T) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	for _, c := range []struct {
		h    *store.SyncHealth
		want string
	}{
		{nil, "-"},
		{&store.SyncHealth{}, "-"},
		{&store.SyncHealth{FailureStreak: 4, FailingSince: &since}, "4x since 2026-10-01 12:00"},
	} {
		if got := failedColumn(c.h); got != c.want {
			t.Errorf("failedColumn(%+v) = %q, want %q", c.h, got, c.want)
		}
	}
}

func TestSyncSendsOneAlert(t *testing.T) {
	user := imapmemserver.NewUser("alice", "right")
	imaptest.CreateMailboxes(t, user, "INBOX")
	host, port := imaptest.Start(t, user)
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", t.TempDir())
	t.Setenv("MAIL_ARCHIVE_LOG_LEVEL", "error")
	keyStr, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", keyStr)
	key, _ := crypto.ParseKey(keyStr)
	sealer, _ := crypto.NewSealer(key)
	acc := &store.Account{Name: "work", Host: host, Port: port, TLSMode: store.TLSModeNone, Username: "alice", Enabled: true}
	seal := func(id int64) ([]byte, error) { return archive.SealPassword(sealer, id, "wrong") }
	if err := st.CreateAccountSealed(context.Background(), acc, seal); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		events = append(events, fmt.Sprint(m["event"]))
		mu.Unlock()
	}))
	defer srv.Close()
	t.Setenv("MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL", srv.URL)

	for range 4 {
		if err := runCmd(t, newSyncCmd(), ""); exitCodeOf(err) == 0 {
			t.Fatal("sync with the wrong password succeeded")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0] != "failing" {
		t.Fatalf("events: %v", events)
	}
}

// `sync` reconciles the due folders and sends the loss alert at its end.
func TestSyncSendsLossAlert(t *testing.T) {
	user := imapmemserver.NewUser("alice", "pw")
	imaptest.CreateMailboxes(t, user, "INBOX")
	for i := range 20 {
		imaptest.Append(t, user, "INBOX", fmt.Appendf(nil, "Subject: m%d\r\n\r\nbody %d\r\n", i, i))
	}
	host, port := imaptest.Start(t, user)
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", t.TempDir())
	t.Setenv("MAIL_ARCHIVE_LOG_LEVEL", "error")
	keyStr, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", keyStr)
	key, _ := crypto.ParseKey(keyStr)
	sealer, _ := crypto.NewSealer(key)
	acc := &store.Account{Name: "work", Host: host, Port: port, TLSMode: store.TLSModeNone, Username: "alice", Enabled: true}
	seal := func(id int64) ([]byte, error) { return archive.SealPassword(sealer, id, "pw") }
	if err := st.CreateAccountSealed(context.Background(), acc, seal); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	}))
	defer srv.Close()
	t.Setenv("MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL", srv.URL)

	// The first sync reconciles INBOX for the first time: no alert.
	if err := runCmd(t, newSyncCmd(), ""); err != nil {
		t.Fatal(err)
	}
	uids := make([]uint32, 15)
	for i := range uids {
		uids[i] = uint32(i + 1) //nolint:gosec // test UIDs
	}
	imaptest.Expunge(t, host, port, "alice", "pw", "INBOX", uids...)
	if err := runCmd(t, newSyncCmd(), "", "--reconcile"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0]["event"] != "gone" || !strings.Contains(fmt.Sprint(got[0]["title"]), "15 messages deleted on the server (work)") {
		t.Fatalf("messages: %v", got)
	}
}
