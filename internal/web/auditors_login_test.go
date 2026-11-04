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

// expectDenied checks that the newest entries since n are exactly one
// auditor.denied with reason for route, in the Log of the owners via.
func (f *authFixture) expectDenied(n int, route, reason string, via []int64) {
	f.t.Helper()
	got := newEntries(f.t, f.st, n)
	if len(got) != 1 || got[0].Action != store.ActionAuditorDenied {
		f.t.Errorf("%s (%s): entries %+v, want one auditor.denied", route, reason, got)
		return
	}
	if d := details(f.t, got[0]); d["reason"] != reason || d["route"] != route {
		f.t.Errorf("%s: denial details %v, want reason %s", route, d, reason)
	}
	if v := viaOwners(f.t, got[0]); !slices.Equal(v, via) {
		f.t.Errorf("%s: denial via_owners %v, want %v", route, v, via)
	}
}

// auditorSetupThenChoice walks an unbound auditor session (several
// grants) whose 2FA is not set up, and whose password is generated when
// mustChange, through the setup to the first read (#511, review A1): the
// setup steps work without a grant, every other route stays closed with
// the gate's refusal recorded, and the grant gate applies once the setup
// is done.
func (f *authFixture) auditorSetupThenChoice(password string, mustChange bool, grant int64, via []int64) {
	f.t.Helper()
	n := len(auditLog(f.t, f.st))
	if mustChange {
		if out := f.expect("PUT", "/api/session/grant", map[string]any{"id": grant}, http.StatusForbidden); out["passwordChangeRequired"] != true {
			f.t.Errorf("grant choice before the password: %v", out)
		}
		f.expectDenied(n, "PUT /api/session/grant", reasonPasswordChangeRequired, via)
		n = len(auditLog(f.t, f.st))
		if out := f.expect("GET", "/api/accounts", nil, http.StatusForbidden); out["passwordChangeRequired"] != true {
			f.t.Errorf("read before the password: %v", out)
		}
		f.expectDenied(n, "GET /api/accounts", reasonPasswordChangeRequired, via)
		f.expect("PUT", "/api/profile/password", map[string]string{"current": password, "new": "the auditor's own passphrase"}, 204)
	}
	n = len(auditLog(f.t, f.st))
	if out := f.expect("PUT", "/api/session/grant", map[string]any{"id": grant}, http.StatusForbidden); out["twoFactorSetupRequired"] != true {
		f.t.Errorf("grant choice before 2FA: %v", out)
	}
	f.expectDenied(n, "PUT /api/session/grant", reasonTwoFactorRequired, via)
	n = len(auditLog(f.t, f.st))
	if out := f.expect("GET", "/api/messages", nil, http.StatusForbidden); out["twoFactorSetupRequired"] != true {
		f.t.Errorf("read before 2FA: %v", out)
	}
	f.expectDenied(n, "GET /api/messages", reasonTwoFactorRequired, via)
	f.expect("GET", "/api/profile/2fa", nil, 200)
	setup := f.expect("POST", "/api/profile/2fa/setup", nil, 200)
	f.expect("POST", "/api/profile/2fa/confirm", map[string]string{"code": f.code(setup["secret"].(string))}, 200)
	n = len(auditLog(f.t, f.st))
	if out := f.expect("GET", "/api/accounts", nil, http.StatusForbidden); out["grantSelectionRequired"] != true {
		f.t.Errorf("read before the grant choice: %v", out)
	}
	f.expectDenied(n, "GET /api/accounts", reasonGrantSelectionRequired, via)
	if out := f.expect("PUT", "/api/session/grant", map[string]any{"id": grant}, 200); out["grantSelectionRequired"] != nil {
		f.t.Errorf("grant choice: %v", out)
	}
	accounts := f.expect("GET", "/api/accounts", nil, 200)
	if shared, _ := accounts["shared"].([]any); len(shared) != 1 {
		f.t.Errorf("accounts after the choice %v", accounts)
	}
}

// TestAuditorSetupWithSeveralGrants (#511, review A1): a new auditor with
// a generated password, no 2FA and two grants can change the password,
// set up TOTP, pick a grant and read; so can one whose 2FA an admin reset.
// Before the fix, the grant gate refused the setup steps and the setup
// gates refused the grant choice.
func TestAuditorSetupWithSeveralGrants(t *testing.T) {
	f := newAuthFixture(t)
	aud := f.addAuditor("stb-meier", "generated passphrase 1")
	g1 := f.ownerGrant("anna", aud, time.Now().AddDate(0, 0, 3))
	f.ownerGrant("bert", aud, time.Now().AddDate(0, 0, 5))
	anna, _ := f.st.GetUserByName(context.Background(), "anna")
	bert, _ := f.st.GetUserByName(context.Background(), "bert")
	via := []int64{anna.ID, bert.ID}
	out := f.expect("POST", "/api/session", map[string]string{"username": "stb-meier", "password": "generated passphrase 1"}, 200)
	if out["grantSelectionRequired"] != true {
		t.Fatalf("login %v", out)
	}
	f.auditorSetupThenChoice("generated passphrase 1", true, g1, via)

	// An admin resets the auditor's 2FA: the next login is unbound again
	// and must set up 2FA before the grant choice.
	if err := f.st.ResetTwoFactor(context.Background(), aud.ID); err != nil {
		t.Fatal(err)
	}
	out = f.expect("POST", "/api/session", map[string]string{"username": "stb-meier", "password": "the auditor's own passphrase"}, 200)
	if out["grantSelectionRequired"] != true {
		t.Fatalf("login after the reset %v", out)
	}
	f.auditorSetupThenChoice("the auditor's own passphrase", false, g1, via)
}

// TestAuditorAdminOnlyFilterDenied: an auditor's GET /api/audit?user=
// is refused like any non-admin's and recorded as auditor.denied.
func TestAuditorAdminOnlyFilterDenied(t *testing.T) {
	f := newAuthFixture(t)
	aud := f.addAuditor("aud", "auditor passphrase 1")
	if err := f.st.SetUserPassword(context.Background(), aud.ID, mustHash(t, "auditor passphrase 1"), store.PasswordReset{}); err != nil {
		t.Fatal(err)
	}
	secret, _ := f.enableTOTP(aud)
	f.ownerGrant("anna", aud, time.Now().AddDate(0, 0, 3))
	anna, _ := f.st.GetUserByName(context.Background(), "anna")
	f.secondStep(f.passwordStep("aud", "auditor passphrase 1"), f.code(secret), 200)
	n := len(auditLog(t, f.st))
	f.expect("GET", "/api/audit?user=anna", nil, http.StatusForbidden)
	f.expectDenied(n, "GET /api/audit", reasonAdminOnly, []int64{anna.ID})
	n = len(auditLog(t, f.st))
	f.expect("GET", "/api/audit", nil, 200)
	if got := newEntries(t, f.st, n); len(got) != 0 {
		t.Errorf("the own Log is logged: %+v", got)
	}
}

// TestAuditorTagFailsClosed (#511, review A note): when the owners of an
// unbound auditor session's grants cannot be read, its entry is not
// written at all rather than written without via_owners.
func TestAuditorTagFailsClosed(t *testing.T) {
	f := newAuthFixture(t)
	aud := f.addAuditor("aud", "auditor passphrase 1")
	f.ownerGrant("anna", aud, time.Now().AddDate(0, 0, 3))
	f.ownerGrant("bert", aud, time.Now().AddDate(0, 0, 5))
	sess := &store.Session{UserID: aud.ID, UserName: aud.Name, IsAuditor: true}
	broken, cancel := context.WithCancel(context.Background())
	cancel()
	e := store.SelfEntry(store.ActorSession, store.ActionAuditorDenied)
	e.SetUser(aud.ID, aud.Name)
	f.server.tagAuditor(broken, e, sess)
	n := len(auditLog(t, f.st))
	if err := f.st.Audit(context.Background(), *e); err == nil {
		t.Error("an entry without its owners was written")
	}
	if got := newEntries(t, f.st, n); len(got) != 0 {
		t.Errorf("entries %+v", got)
	}
	// With the grants readable, the same entry carries both owners.
	e = store.SelfEntry(store.ActorSession, store.ActionAuditorDenied)
	e.SetUser(aud.ID, aud.Name)
	f.server.tagAuditor(context.Background(), e, sess)
	if err := f.st.Audit(context.Background(), *e); err != nil {
		t.Fatal(err)
	}
	if got := newEntries(t, f.st, n); len(got) != 1 || len(viaOwners(t, got[0])) != 2 {
		t.Errorf("entries %+v", got)
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
