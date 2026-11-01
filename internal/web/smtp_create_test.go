package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
)

// makeAdmin turns the fixture's user into an admin with 2FA on, which
// admins need before anything else works.
func (f *manageFixture) makeAdmin() {
	f.t.Helper()
	u, err := f.st.GetUserByName(context.Background(), "tester")
	if err != nil {
		f.t.Fatal(err)
	}
	setAdminWithTwoFactor(f.t, f.st, u.ID)
}

// setAdminWithTwoFactor makes the user an admin with 2FA on.
func setAdminWithTwoFactor(t *testing.T, st *store.Store, userID int64) {
	t.Helper()
	ctx := context.Background()
	if err := st.SetUserAdmin(ctx, userID, true); err != nil {
		t.Fatal(err)
	}
	keyStr, _ := crypto.GenerateKey()
	key, _ := crypto.ParseKey(keyStr)
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := auth.GenerateTOTPSecret()
	if err := st.BeginTwoFactorSetup(ctx, userID, secret, sealer); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, _ := auth.GenerateTOTP(secret, now)
	codes, _ := auth.GenerateRecoveryCodes()
	if err := st.ConfirmTwoFactorSetup(ctx, userID, code, now, sealer, codes, []byte("recovery key")); err != nil {
		t.Fatal(err)
	}
}

func TestCreateSMTPAccountAPI(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	body := map[string]any{"kind": "smtp", "name": "journal", "addresses": []string{"Archive@Archive.Test"}}

	// Only admins choose addresses: they decide who gets the journal copies.
	f.do("POST", "/api/accounts", body, 403)
	f.makeAdmin()
	if f.accounts().SMTP != nil {
		t.Error("smtp reported on without SMTPListen")
	}

	for _, bad := range []map[string]any{
		{"kind": "smtp", "name": "journal"},
		{"kind": "smtp", "name": "journal", "addresses": []string{}},
		{"kind": "smtp", "name": "journal", "addresses": []string{"a+b@archive.test"}},
		{"kind": "smtp", "name": "journal", "addresses": []string{"x@archive.test"}, "host": "imap.example.com"},
		{"kind": "smtp", "name": "journal", "addresses": []string{"x@archive.test"}, "confirmFolders": true},
		{"kind": "pop3", "name": "journal"},
		{"name": "mail", "addresses": []string{"x@archive.test"}},
	} {
		f.do("POST", "/api/accounts", bad, 400)
	}

	n := len(auditLog(t, f.st))
	f.do("POST", "/api/accounts", body, 201)
	a := f.account("journal")
	if a.Kind != "smtp" || !a.Enabled || !slices.Equal(a.Addresses, []string{"archive@archive.test"}) {
		t.Fatalf("created %+v", a)
	}
	got := newEntries(t, f.st, n)
	if len(got) != 1 || got[0].Action != "account.add" {
		t.Fatalf("audit %+v", got)
	}
	if d := details(t, got[0]); d["kind"] != "smtp" || fmt.Sprint(d["addresses"]) != "[archive@archive.test]" {
		t.Errorf("audit details %v", d)
	}

	// A taken name or address is a conflict that names it.
	f.do("POST", "/api/accounts", body, 409)
	out := f.do("POST", "/api/accounts", map[string]any{"kind": "smtp", "name": "second", "addresses": []string{"archive@archive.test"}}, 409)
	var e apiError
	_ = json.Unmarshal(out, &e)
	if e.Error != "archive@archive.test: "+store.ErrAddressTaken.Error() {
		t.Errorf("taken address: %q", e.Error)
	}
}

func TestChangeSMTPAddressesAPI(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	ctx := context.Background()
	tester, _ := f.st.GetUserByName(ctx, "tester")
	acc := &store.Account{Name: "journal", Enabled: true, OwnerID: &tester.ID}
	if err := f.st.CreateSMTPAccount(ctx, acc, []string{"archive@archive.test", "old@archive.test"}); err != nil {
		t.Fatal(err)
	}
	other := &store.Account{Name: "other", Enabled: true, OwnerID: &tester.ID}
	if err := f.st.CreateSMTPAccount(ctx, other, []string{"taken@archive.test"}); err != nil {
		t.Fatal(err)
	}
	list := map[string]any{"addresses": []string{"archive@archive.test", "new@archive.test"}}

	// The owner may rename and disable, but not change addresses.
	f.do("PATCH", "/api/accounts/journal", list, 403)
	f.do("PATCH", "/api/accounts/journal", map[string]any{"name": "journal2"}, 204)
	f.makeAdmin()

	n := len(auditLog(t, f.st))
	f.do("PATCH", "/api/accounts/journal2", list, 204)
	if a := f.account("journal2"); !slices.Equal(a.Addresses, []string{"archive@archive.test", "new@archive.test"}) {
		t.Fatalf("addresses %v", a.Addresses)
	}
	got := newEntries(t, f.st, n)
	if len(got) != 1 || got[0].Action != "account.change" {
		t.Fatalf("audit %+v", got)
	}
	if d := details(t, got[0]); fmt.Sprint(d["fields"]) != "[addresses]" || fmt.Sprint(d["added"]) != "[new@archive.test]" ||
		fmt.Sprint(d["removed"]) != "[old@archive.test]" {
		t.Errorf("audit details %v", d)
	}

	f.do("PATCH", "/api/accounts/journal2", map[string]any{"addresses": []string{}}, 400)
	f.do("PATCH", "/api/accounts/journal2", map[string]any{"addresses": []string{"taken@archive.test"}}, 409)
	f.do("PATCH", "/api/accounts/journal2", map[string]any{"kind": "imap"}, 400)

	// An IMAP account has no addresses.
	f.do("POST", "/api/accounts", f.newAccount("mail", "alice", "secret"), 201)
	f.do("PATCH", "/api/accounts/mail", list, 409)
}

func TestSMTPServerState(t *testing.T) {
	f := newManageFixture(t)
	if f.accounts().SMTP != nil {
		t.Fatal("smtp on without SMTPListen")
	}
	s := New(f.st, f.blobs, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{AllowedHosts: []string{"127.0.0.1"}, SMTPListen: ":2525"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	_, token := sessionFor(t, f.st, "kim")
	g := *f
	g.srv, g.cookie = srv, token
	if g.accounts().SMTP == nil {
		t.Error("smtp off with SMTPListen")
	}
}
