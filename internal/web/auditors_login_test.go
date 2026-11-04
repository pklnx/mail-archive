package web

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
)

// addAuditor creates an auditor whose password must be changed at the
// first login, as an admin creates one (after the owner "owner": the
// first user cannot be an auditor).
func (f *authFixture) addAuditor(name, password string) *store.User {
	f.t.Helper()
	if _, err := f.st.GetUserByName(context.Background(), "owner"); err != nil {
		f.addUser("owner", "an owner's passphrase", false)
	}
	h, err := cheapHasher.Hash(context.Background(), password)
	if err != nil {
		f.t.Fatal(err)
	}
	u, err := f.st.CreateUserInGroup(context.Background(), store.NewUser{Name: name, PasswordHash: h, Auditor: true, Generated: true})
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

// ownerGrant creates an owner with an account and grants it to the
// auditor until ends.
func (f *authFixture) ownerGrant(owner string, auditor *store.User, ends time.Time) int64 {
	f.t.Helper()
	ctx := context.Background()
	o, err := f.st.GetUserByName(ctx, owner)
	if err != nil {
		o = f.addUser(owner, "an owner's passphrase", false)
	}
	acc := &store.Account{Name: "mail-" + time.Now().Format("150405.000000"), OwnerID: &o.ID, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, Enabled: true}
	if err := f.st.CreateAccount(ctx, acc); err != nil {
		f.t.Fatal(err)
	}
	id, err := f.st.CreateGrant(ctx, store.NewGrant{OwnerID: o.ID, AuditorID: auditor.ID, Label: "Prüfung", Accounts: []store.GrantAccount{{AccountID: acc.ID}}, EndsAt: ends})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

// lastEntry returns the newest entry of action.
func (f *authFixture) lastEntry(action string) store.AuditRecord {
	f.t.Helper()
	all := auditLog(f.t, f.st)
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Action == action {
			return all[i]
		}
	}
	f.t.Fatalf("no %s entry", action)
	return store.AuditRecord{}
}

// sessionExpiry returns the expiry of the auditor's newest session.
func (f *authFixture) sessionExpiry(u *store.User) time.Time {
	f.t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var at time.Time
	if err := conn.QueryRow(ctx, `SELECT expires_at FROM sessions WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`, u.ID).Scan(&at); err != nil {
		f.t.Fatal(err)
	}
	return at
}

// TestAuditorFirstLogin (#393 part 2, login flow on the password path):
// without an active grant the correct password is refused (no_access);
// with one, the session is bound to it and ends with it; the generated
// password must be changed, then 2FA set up, and only then is the grant
// readable.
func TestAuditorFirstLogin(t *testing.T) {
	f := newAuthFixture(t)
	aud := f.addAuditor("stb-meier", "generated passphrase 1")
	if resp, out := f.do("POST", "/api/session", map[string]string{"username": "stb-meier", "password": "generated passphrase 1"}); resp.StatusCode != http.StatusForbidden || out["error"] != errNoAccess {
		t.Fatalf("no grant: %d %v", resp.StatusCode, out)
	}
	e := f.lastEntry("auth.login_failed")
	if d := details(t, e); d["reason"] != reasonNoAccess || d["method"] != "password" || e.UserID == nil || *e.UserID != aud.ID {
		t.Errorf("refusal entry %+v %v", e, d)
	}
	if resp, _ := f.do("GET", "/api/session", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a refused login left a session: %d", resp.StatusCode)
	}
	ends := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	grant := f.ownerGrant("owner", aud, ends)
	out := f.expect("POST", "/api/session", map[string]string{"username": "stb-meier", "password": "generated passphrase 1"}, 200)
	if g, _ := out["grant"].(map[string]any); g == nil || g["id"] != float64(grant) || out["grantSelectionRequired"] != nil {
		t.Errorf("login answer %v", out)
	}
	if got := f.sessionExpiry(aud); !got.Equal(ends) {
		t.Errorf("session expires %v, want the grant's end %v", got, ends)
	}
	e = f.lastEntry("auth.login")
	if d := details(t, e); d[store.DetailAuditorGrant] != float64(grant) {
		t.Errorf("login entry %v", d)
	}
	f.expect("GET", "/api/accounts", nil, http.StatusForbidden) // the password first
	f.expect("PUT", "/api/profile/password", map[string]string{"current": "generated passphrase 1", "new": "the auditor's own passphrase"}, 204)
	if out := f.expect("GET", "/api/accounts", nil, http.StatusForbidden); out["twoFactorSetupRequired"] != true {
		t.Errorf("2FA gate: %v", out)
	}
	setup := f.expect("POST", "/api/profile/2fa/setup", nil, 200)
	if setup["required"] != true {
		t.Errorf("2FA is not required for the auditor: %v", setup)
	}
	f.expect("POST", "/api/profile/2fa/confirm", map[string]string{"code": f.code(setup["secret"].(string))}, 200)
	f.adminSecrets["stb-meier"] = setup["secret"].(string)
	accounts := f.expect("GET", "/api/accounts", nil, 200)
	if shared, _ := accounts["shared"].([]any); len(shared) != 1 {
		t.Errorf("accounts %v", accounts)
	}
	// An auditor cannot switch 2FA off.
	if resp, out := f.do("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "the auditor's own passphrase"}); resp.StatusCode != http.StatusForbidden || out["error"] != errNotForAuditors {
		t.Errorf("2FA off: %d %v", resp.StatusCode, out)
	}
}

// TestAuditorTwoFactorLogin: the TOTP path. Without an active grant the
// refusal comes only after a correct second factor; with two grants of two
// owners the session is unbound and the login entry goes to both owners'
// Log; a wrong code says nothing about grants.
func TestAuditorTwoFactorLogin(t *testing.T) {
	f := newAuthFixture(t)
	aud := f.addAuditor("aud", "auditor passphrase 1")
	if err := f.st.SetUserPassword(context.Background(), aud.ID, mustHash(t, "auditor passphrase 1"), store.PasswordReset{}); err != nil {
		t.Fatal(err)
	}
	secret, _ := f.enableTOTP(aud)
	challenge := f.passwordStep("aud", "auditor passphrase 1")
	f.secondStep(challenge, "000000", http.StatusUnauthorized)
	challenge = f.passwordStep("aud", "auditor passphrase 1")
	if out := f.secondStep(challenge, f.code(secret), http.StatusForbidden); out["error"] != errNoAccess {
		t.Errorf("no grant: %v", out)
	}
	if d := details(t, f.lastEntry("auth.login_failed")); d["reason"] != reasonNoAccess || d["method"] != "2fa" {
		t.Errorf("refusal %v", d)
	}
	f.ownerGrant("anna", aud, time.Now().AddDate(0, 0, 3))
	g2 := f.ownerGrant("bert", aud, time.Now().AddDate(0, 0, 5))
	challenge = f.passwordStep("aud", "auditor passphrase 1")
	out := f.secondStep(challenge, f.code(secret), 200)
	if out["grantSelectionRequired"] != true || out["grant"] != nil {
		t.Errorf("two grants: %v", out)
	}
	if grants, _ := out["grants"].([]any); len(grants) != 2 {
		t.Errorf("grants %v", out["grants"])
	}
	anna, _ := f.st.GetUserByName(context.Background(), "anna")
	bert, _ := f.st.GetUserByName(context.Background(), "bert")
	e := f.lastEntry("auth.login")
	if via := viaOwners(t, e); !slices.Equal(via, []int64{anna.ID, bert.ID}) {
		t.Errorf("login via_owners %v", via)
	}
	if _, ok := details(t, e)[store.DetailAuditorGrant]; ok {
		t.Errorf("an unbound login names a grant: %s", e.Details)
	}
	f.expect("PUT", "/api/session/grant", map[string]any{"id": g2}, 200)
	if got, want := f.sessionExpiry(aud), f.grantEnd(g2); !got.Equal(want) {
		t.Errorf("session expires %v, want %v", got, want)
	}
}

// TestAuditorPasskeyLogin: the passkey path, with the same rules: no grant
// refused after a valid passkey, one grant bound at once.
func TestAuditorPasskeyLogin(t *testing.T) {
	f := newPasskeyFixture(t)
	aud := f.addAuditor("aud", "auditor passphrase 1")
	if err := f.st.SetUserPassword(context.Background(), aud.ID, mustHash(t, "auditor passphrase 1"), store.PasswordReset{}); err != nil {
		t.Fatal(err)
	}
	secret, _ := f.enableTOTP(aud)
	grant := f.ownerGrant("anna", aud, time.Now().AddDate(0, 0, 3))
	f.loginOK("aud", "auditor passphrase 1")
	k := newSoftKey(t)
	f.addPasskey(k, "laptop", "auditor passphrase 1", f.code(secret))
	f.expect("DELETE", "/api/session", nil, 204)
	out := f.passkeyLogin(k, 200)
	if g, _ := out["grant"].(map[string]any); g == nil || g["id"] != float64(grant) {
		t.Errorf("passkey login %v", out)
	}
	if d := details(t, f.lastEntry("auth.login")); d["method"] != "passkey" || d[store.DetailAuditorGrant] != float64(grant) {
		t.Errorf("login entry %v", d)
	}
	f.expect("DELETE", "/api/session", nil, 204)
	if err := f.st.RevokeGrant(context.Background(), grant, nil, nil); err != nil {
		t.Fatal(err)
	}
	if out := f.passkeyLogin(k, http.StatusForbidden); out["error"] != errNoAccess {
		t.Errorf("revoked: %v", out)
	}
	e := f.lastEntry("auth.login_failed")
	if d := details(t, e); d["reason"] != reasonNoAccess || d["method"] != "passkey" || e.UserName != "aud" {
		t.Errorf("refusal %+v %v", e, d)
	}
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := cheapHasher.Hash(context.Background(), pw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// grantEnd returns a grant's ends_at.
func (f *authFixture) grantEnd(id int64) time.Time {
	f.t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var at time.Time
	if err := conn.QueryRow(ctx, `SELECT ends_at FROM auditor_grants WHERE id = $1`, id).Scan(&at); err != nil {
		f.t.Fatal(err)
	}
	return at
}
