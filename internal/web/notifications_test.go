package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keepia/archive/internal/store"
)

// fakeCodes records the confirmation codes the server mails.
type fakeCodes struct {
	mu   sync.Mutex
	sent []sentCode
	fail bool
}

type sentCode struct {
	userID            int64
	email, lang, code string
}

func (f *fakeCodes) SendCode(_ context.Context, userID int64, email, lang, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("relay refused")
	}
	f.sent = append(f.sent, sentCode{userID, email, lang, code})
	return nil
}

func (f *fakeCodes) last() sentCode {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return sentCode{}
	}
	return f.sent[len(f.sent)-1]
}

func (f *fakeCodes) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// notifyWorld is a groupWorld with alert mail on.
func notifyWorld(t *testing.T) (*groupWorld, *fakeCodes) {
	t.Helper()
	w := newGroupWorld(t)
	codes := &fakeCodes{}
	w.server.alertMail = codes
	return w, codes
}

func notifyState(t *testing.T, body []byte) notificationsJSON {
	t.Helper()
	var s notificationsJSON
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return s
}

// TestNotificationsAdminOnly (#397, part 5): members of either group get
// the same 403 on every route of Profile → Notifications, also with a body
// that is not JSON, before anything is looked up: no code is mailed, no
// entry written, no address set.
func TestNotificationsAdminOnly(t *testing.T) {
	w, codes := notifyWorld(t)
	n := len(auditLog(t, w.st))
	var first []byte
	for _, who := range []string{"a", "k"} {
		for _, r := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/api/profile/notifications", nil},
			{"PUT", "/api/profile/notifications", map[string]string{"email": who + "@example.com"}},
			{"PUT", "/api/profile/notifications", json.RawMessage("{")},
			{"POST", "/api/profile/notifications/confirm", map[string]string{"code": "ABCDEF"}},
			{"DELETE", "/api/profile/notifications", nil},
		} {
			status, body := w.sendJSON(w.creds[who], r.method, r.path, r.body)
			if status != http.StatusForbidden {
				t.Fatalf("%s %s %s: %d %s", who, r.method, r.path, status, body)
			}
			if first == nil {
				first = body
			} else if !bytes.Equal(first, body) {
				t.Errorf("%s %s %s: %s, want %s", who, r.method, r.path, body, first)
			}
		}
	}
	if codes.count() != 0 {
		t.Fatalf("a member's request mailed %d codes", codes.count())
	}
	if got := newEntries(t, w.st, n); len(got) != 0 {
		t.Fatalf("entries %+v", got)
	}
	for _, who := range []string{"a", "k"} {
		if s, err := w.st.NotifyEmail(context.Background(), w.users[who].ID); err != nil || s.Email != "" {
			t.Fatalf("%s: %+v %v", who, s, err)
		}
	}
}

// TestNotificationsFlow: an admin sets an address, gets a code, enters a
// wrong one and then the right one (lower case, with a space); the audit
// entries carry no address; removing it works.
func TestNotificationsFlow(t *testing.T) {
	w, codes := notifyWorld(t)
	x := w.creds["x"]
	n := len(auditLog(t, w.st))
	status, body := w.sendJSON(x, "GET", "/api/profile/notifications", nil)
	if s := notifyState(t, body); status != 200 || !s.Available || s.Email != nil || s.Confirmed {
		t.Fatalf("empty: %d %s", status, body)
	}
	for _, bad := range []string{"", "nobody", "a@example.com, b@example.com", "Kim <kim@example.com>", "kim@example.com\r\nBcc: x@example.com"} {
		if status, body := w.sendJSON(x, "PUT", "/api/profile/notifications", map[string]string{"email": bad}); status != 400 {
			t.Errorf("email %q: %d %s", bad, status, body)
		}
	}
	if status, _ := w.sendJSON(x, "PUT", "/api/profile/notifications", map[string]string{"email": "x@example.com", "language": "fr"}); status != 400 {
		t.Errorf("language fr: %d", status)
	}
	status, body = w.sendJSON(x, "PUT", "/api/profile/notifications", map[string]string{"email": "x@example.com", "language": "de"})
	s := notifyState(t, body)
	if status != 200 || s.Email == nil || *s.Email != "x@example.com" || s.Confirmed || s.CodeExpiresAt == nil {
		t.Fatalf("set: %d %s", status, body)
	}
	sent := codes.last()
	if sent.email != "x@example.com" || sent.lang != "de" || sent.userID != w.users["x"].ID || len(sent.code) != 6 {
		t.Fatalf("code mail %+v", sent)
	}
	if status, body := w.sendJSON(x, "POST", "/api/profile/notifications/confirm", map[string]string{"code": "ZZZZZZ"}); status != 400 ||
		!strings.Contains(string(body), errWrongCode) {
		t.Fatalf("wrong code: %d %s", status, body)
	}
	typed := strings.ToLower(sent.code[:3]) + " " + strings.ToLower(sent.code[3:])
	status, body = w.sendJSON(x, "POST", "/api/profile/notifications/confirm", map[string]string{"code": typed})
	if s := notifyState(t, body); status != 200 || !s.Confirmed || s.CodeExpiresAt != nil {
		t.Fatalf("confirm: %d %s", status, body)
	}
	if r, err := w.st.AlertMailRecipient(context.Background(), w.users["x"].ID); err != nil || r.Email != "x@example.com" {
		t.Fatalf("recipient %+v %v", r, err)
	}
	if status, _ := w.sendJSON(x, "DELETE", "/api/profile/notifications", nil); status != http.StatusNoContent {
		t.Fatalf("remove: %d", status)
	}
	var actions []string
	for _, e := range newEntries(t, w.st, n) {
		actions = append(actions, e.Action)
		if strings.Contains(string(e.Details), "example.com") || strings.Contains(e.TargetName, "example.com") {
			t.Errorf("entry %s shows the address: %s", e.Action, e.Details)
		}
	}
	if strings.Join(actions, ",") != "profile.notify_email_set,profile.notify_email_confirm,profile.notify_email_remove" {
		t.Fatalf("entries %v", actions)
	}
}

// TestNotificationsLimits: three codes per hour, five tries per code, 30
// minutes per code; a code mail that fails answers 502.
func TestNotificationsLimits(t *testing.T) {
	w, codes := notifyWorld(t)
	x := w.creds["x"]
	now := time.Now()
	w.server.now = func() time.Time { return now }
	set := func() (int, []byte) {
		return w.sendJSON(x, "PUT", "/api/profile/notifications", map[string]string{"email": "x@example.com"})
	}
	for i := range 3 {
		if status, body := set(); status != 200 {
			t.Fatalf("code %d: %d %s", i+1, status, body)
		}
	}
	if status, body := set(); status != http.StatusTooManyRequests || !strings.Contains(string(body), errCodeRateLimit) {
		t.Fatalf("fourth code: %d %s", status, body)
	}
	if codes.count() != 3 || codes.last().lang != "en" {
		t.Fatalf("%d codes mailed, last %+v", codes.count(), codes.last())
	}
	code := codes.last().code
	for i := range 5 {
		status, body := w.sendJSON(x, "POST", "/api/profile/notifications/confirm", map[string]string{"code": "ZZZZZZ"})
		want := http.StatusBadRequest
		if i == 4 {
			want = http.StatusConflict
		}
		if status != want {
			t.Fatalf("wrong try %d: %d %s", i+1, status, body)
		}
	}
	if status, body := w.sendJSON(x, "POST", "/api/profile/notifications/confirm", map[string]string{"code": code}); status != http.StatusConflict ||
		!strings.Contains(string(body), errCodeSpent) {
		t.Fatalf("right code after five tries: %d %s", status, body)
	}

	// A new hour: a new code, which expires after 30 minutes.
	now = now.Add(time.Hour + time.Second)
	if status, body := set(); status != 200 {
		t.Fatalf("new hour: %d %s", status, body)
	}
	now = now.Add(store.NotifyCodeTTL)
	if status, body := w.sendJSON(x, "POST", "/api/profile/notifications/confirm", map[string]string{"code": codes.last().code}); status != http.StatusConflict ||
		!strings.Contains(string(body), errNoCode) {
		t.Fatalf("expired: %d %s", status, body)
	}

	codes.mu.Lock()
	codes.fail = true
	codes.mu.Unlock()
	if status, body := set(); status != http.StatusBadGateway || !strings.Contains(string(body), errMailFailed) {
		t.Fatalf("relay failing: %d %s", status, body)
	}
}

// TestNotificationsOff: without alert mail the GET says so, and setting an
// address is refused; removing one still works.
func TestNotificationsOff(t *testing.T) {
	w := newGroupWorld(t)
	x := w.creds["x"]
	status, body := w.sendJSON(x, "GET", "/api/profile/notifications", nil)
	if s := notifyState(t, body); status != 200 || s.Available || s.Email != nil {
		t.Fatalf("off: %d %s", status, body)
	}
	if status, body := w.sendJSON(x, "PUT", "/api/profile/notifications", map[string]string{"email": "x@example.com"}); status != http.StatusConflict ||
		!strings.Contains(string(body), errMailOff) {
		t.Fatalf("set while off: %d %s", status, body)
	}
	if status, _ := w.sendJSON(x, "DELETE", "/api/profile/notifications", nil); status != http.StatusNoContent {
		t.Fatalf("remove while off: %d", status)
	}
}

// TestNotificationsBounceBanner: after a relay refused the admin's address,
// GET /api/accounts tells that admin, and nobody else.
func TestNotificationsBounceBanner(t *testing.T) {
	w, codes := notifyWorld(t)
	x := w.creds["x"]
	w.sendJSON(x, "PUT", "/api/profile/notifications", map[string]string{"email": "x@example.com"})
	w.sendJSON(x, "POST", "/api/profile/notifications/confirm", map[string]string{"code": codes.last().code})
	banner := func(who string) bool {
		status, body := w.sendJSON(w.creds[who], "GET", "/api/accounts", nil)
		if status != 200 {
			t.Fatalf("%s accounts: %d %s", who, status, body)
		}
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		v, ok := out["notifyEmailBounced"]
		return ok && v == true
	}
	if banner("x") {
		t.Fatal("banner before a bounce")
	}
	if ok, err := w.st.BounceNotifyEmail(context.Background(), w.users["x"].ID, "x@example.com"); err != nil || !ok {
		t.Fatalf("bounce %v %v", ok, err)
	}
	if !banner("x") || banner("a") || banner("k") {
		t.Fatal("banner shown to the wrong users")
	}
	status, body := w.sendJSON(x, "GET", "/api/profile/notifications", nil)
	if s := notifyState(t, body); status != 200 || !s.Bounced || s.Confirmed {
		t.Fatalf("state after bounce: %s", body)
	}
}
