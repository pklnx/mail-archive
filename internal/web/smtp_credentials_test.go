package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keepia/archive/internal/auth"
	"github.com/keepia/archive/internal/store"
)

// smtpCredFixture: two admins with 2FA, "owner" (with the smtp account
// "journal" and the IMAP account "mail") and "other" (with the smtp account
// "theirs"), and a member, "kim". The server log is captured.
type smtpCredFixture struct {
	*authFixture
	logs            *syncWriter
	owner, other    *store.User
	journal, theirs *store.Account
}

func newSMTPCredFixture(t *testing.T) *smtpCredFixture {
	t.Helper()
	f, logs := newKeyFixture(t)
	ctx := context.Background()
	x := &smtpCredFixture{authFixture: f, logs: logs}
	x.owner = f.addUser("owner", keyPassword, true)
	x.other = f.addUser("other", keyPassword, true)
	f.addUser("kim", keyPassword, false)
	x.journal = &store.Account{Name: "journal", Enabled: true, OwnerID: &x.owner.ID}
	if err := f.st.CreateSMTPAccount(ctx, x.journal, []string{"journal@archive.test"}); err != nil {
		t.Fatal(err)
	}
	x.theirs = &store.Account{Name: "theirs", Enabled: true, OwnerID: &x.other.ID}
	if err := f.st.CreateSMTPAccount(ctx, x.theirs, []string{"theirs@archive.test"}); err != nil {
		t.Fatal(err)
	}
	f.imapAccount("mail", x.owner)
	return x
}

// create makes a secret for account as user, the logged-in admin of f.
func (x *smtpCredFixture) create(f *authFixture, user, account string, body map[string]any) map[string]any {
	x.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	body["currentPassword"] = keyPassword
	body["code"] = f.code(f.adminSecrets[user])
	return f.expect("POST", "/api/accounts/"+account+"/smtp-credentials", body, 201)
}

func credentialIDs(out map[string]any) []int64 {
	list, _ := out["credentials"].([]any)
	ids := make([]int64, 0, len(list))
	for _, c := range list {
		ids = append(ids, int64(c.(map[string]any)["id"].(float64)))
	}
	return ids
}

func TestSMTPCredentialLifecycle(t *testing.T) {
	x := newSMTPCredFixture(t)
	f := x.authFixture
	f.loginOK("owner", keyPassword)
	base := "/api/accounts/journal/smtp-credentials"
	username := "smtp-" + strconv.FormatInt(x.journal.ID, 10)

	out := f.expect("GET", base, nil, 200)
	if out["username"] != username || len(credentialIDs(out)) != 0 {
		t.Fatalf("empty list: %v", out)
	}

	// The password and, with 2FA, a code are needed; a refusal stores
	// nothing.
	n := len(auditLog(t, f.st))
	f.expect("POST", base, map[string]any{"currentPassword": keyPassword}, 401)
	f.expect("POST", base, map[string]any{"currentPassword": "wrong wrong wrong", "code": f.code(f.adminSecrets["owner"])}, 401)
	f.expect("POST", base, map[string]any{"currentPassword": keyPassword, "code": f.code(f.adminSecrets["owner"]), "graceDays": 31}, 400)
	f.expect("POST", base, map[string]any{"currentPassword": keyPassword, "code": f.code(f.adminSecrets["owner"]), "graceDays": -1}, 400)
	f.expect("POST", base, map[string]any{"currentPassword": keyPassword, "secret": "mine"}, 400)
	for _, e := range newEntries(t, f.st, n) {
		if strings.HasPrefix(e.Action, "smtp_credential.") {
			t.Fatalf("refused create logged %+v", e)
		}
	}
	if list, _ := f.st.SMTPCredentials(context.Background(), x.journal.ID); len(list) != 0 {
		t.Fatalf("refused create stored %+v", list)
	}

	created := x.create(f, "owner", "journal", nil)
	secret, _ := created["secret"].(string)
	first, _ := created["credential"].(map[string]any)
	if !auth.ValidSMTPSecretFormat(secret) || created["username"] != username || first["state"] != "active" ||
		first["hint"] != secret[len(secret)-4:] || len(created["previous"].([]any)) != 0 {
		t.Fatalf("created: %v", created)
	}
	firstID := int64(first["id"].(float64))
	// The secret logs in for its account.
	if l, err := f.st.SMTPLogin(context.Background(), auth.HashSMTPSecret(secret), f.clock.Now()); err != nil || l.AccountID != x.journal.ID {
		t.Fatalf("login with the new secret: %+v %v", l, err)
	}

	// A second secret keeps the first for the default 7 days.
	second := x.create(f, "owner", "journal", nil)
	secret2, _ := second["secret"].(string)
	prev, _ := second["previous"].([]any)
	if len(prev) != 1 || int64(prev[0].(map[string]any)["id"].(float64)) != firstID || prev[0].(map[string]any)["revokedAt"] != nil {
		t.Fatalf("previous: %v", second)
	}
	end, err := time.Parse(time.RFC3339Nano, prev[0].(map[string]any)["expiresAt"].(string))
	if err != nil || end.Sub(f.clock.Now()).Round(time.Minute) != 7*24*time.Hour {
		t.Fatalf("grace end %v (now %v): %v", end, f.clock.Now(), err)
	}
	list := f.expect("GET", base, nil, 200)
	states := map[int64]string{}
	for _, c := range list["credentials"].([]any) {
		m := c.(map[string]any)
		states[int64(m["id"].(float64))] = m["state"].(string)
	}
	secondID := int64(second["credential"].(map[string]any)["id"].(float64))
	if len(states) != 2 || states[firstID] != "expiring" || states[secondID] != "active" {
		t.Fatalf("states: %v", states)
	}
	// A third with a grace of 0 revokes both older ones.
	third := x.create(f, "owner", "journal", map[string]any{"graceDays": 0})
	if prev, _ := third["previous"].([]any); len(prev) != 2 {
		t.Fatalf("grace 0: %v", third)
	}
	thirdID := int64(third["credential"].(map[string]any)["id"].(float64))

	// Revoking needs no password; twice, or through another account, is 404.
	f.expect("DELETE", "/api/accounts/theirs/smtp-credentials/"+itoa(thirdID), nil, 404)
	f.expect("DELETE", "/api/accounts/mail/smtp-credentials/"+itoa(thirdID), nil, 409)
	f.expect("DELETE", base+"/"+itoa(thirdID), nil, 204)
	f.expect("DELETE", base+"/"+itoa(thirdID), nil, 404)
	f.expect("DELETE", base+"/x", nil, 404)
	if err := f.st.SMTPCredentialUsable(context.Background(), thirdID, f.clock.Now()); !errors.Is(err, store.ErrSMTPCredentialRevoked) {
		t.Fatalf("after revoke: %v", err)
	}

	// Audit: create and revoke, with the account as target, never the
	// secret or its hash.
	var creates, revokes []map[string]any
	for _, e := range auditLog(t, f.st) {
		switch e.Action {
		case "smtp_credential.create":
			if e.TargetType != store.TargetAccount || e.TargetID != itoa(x.journal.ID) {
				t.Errorf("create target %+v", e)
			}
			creates = append(creates, details(t, e))
		case "smtp_credential.revoke":
			revokes = append(revokes, details(t, e))
		}
	}
	if len(creates) != 3 || creates[0]["grace_days"] != float64(7) || creates[0]["hint"] != first["hint"] ||
		creates[0]["credential_id"] != float64(firstID) || len(creates[0]["superseded"].([]any)) != 0 ||
		creates[2]["grace_days"] != float64(0) || len(creates[2]["superseded"].([]any)) != 2 {
		t.Errorf("create entries: %v", creates)
	}
	if len(revokes) != 1 || revokes[0]["credential_id"] != float64(thirdID) || revokes[0]["by_admin"] != nil {
		t.Errorf("revoke entries: %v", revokes)
	}

	// The secrets were only in the answers to their creation: not in the
	// lists, the audit log or the server log.
	listed := f.expect("GET", base, nil, 200)
	all := f.expect("GET", "/api/admin/smtp-credentials", nil, 200)
	raw, _ := json.Marshal([]any{listed, all})
	rows, _ := json.Marshal(auditLog(t, f.st))
	for _, s := range []string{secret, secret2, third["secret"].(string)} {
		hash := auth.HashSMTPSecret(s)
		for where, text := range map[string][]byte{"lists": raw, "audit log": rows, "server log": []byte(x.logs.String())} {
			if bytes.Contains(text, []byte(s)) || bytes.Contains(text, []byte(s[4:20])) {
				t.Errorf("a secret in the %s", where)
			}
			if bytes.Contains(text, hash) {
				t.Errorf("a hash in the %s", where)
			}
		}
	}
	if !strings.Contains(x.logs.String(), "smtp credential created") || !strings.Contains(x.logs.String(), "smtp credential revoked") {
		t.Errorf("server log:\n%s", x.logs.String())
	}
}

// Only the owner, who is an admin, manages an account's secrets: another
// admin finds no account, a member is refused, an IMAP or removed account
// has none, and an API key cannot use the routes.
func TestSMTPCredentialAccess(t *testing.T) {
	x := newSMTPCredFixture(t)
	f := x.authFixture
	f.loginOK("owner", keyPassword)
	created := x.create(f, "owner", "journal", nil)
	id := itoa(int64(created["credential"].(map[string]any)["id"].(float64)))
	key := f.createKey("cli", []string{"read", "sync"}, f.code(f.adminSecrets["owner"]))

	base := "/api/accounts/journal/smtp-credentials"
	body := map[string]any{"currentPassword": keyPassword, "code": f.code(f.adminSecrets["owner"])}
	f.expect("GET", "/api/accounts/theirs/smtp-credentials", nil, 404)
	f.expect("POST", "/api/accounts/theirs/smtp-credentials", body, 404)
	f.expect("GET", "/api/accounts/nothing/smtp-credentials", nil, 404)
	f.expect("GET", "/api/accounts/mail/smtp-credentials", nil, 409)
	f.expect("POST", "/api/accounts/mail/smtp-credentials", body, 409)
	for _, c := range []struct{ method, path string }{
		{"GET", base}, {"POST", base}, {"DELETE", base + "/" + id},
		{"GET", "/api/admin/smtp-credentials"}, {"DELETE", "/api/admin/smtp-credentials/" + id},
	} {
		if status := f.keyStatus(key, c.method, c.path); status != 403 {
			t.Errorf("API key %s %s: %d", c.method, c.path, status)
		}
	}

	// The owner's revoke route takes only a secret of the account in the
	// path: a secret of the owner's other smtp account is 404 there and
	// stays usable.
	second := &store.Account{Name: "second", Enabled: true, OwnerID: &x.owner.ID}
	if err := f.st.CreateSMTPAccount(context.Background(), second, []string{"second@archive.test"}); err != nil {
		t.Fatal(err)
	}
	other := x.create(f, "owner", "second", nil)
	otherID := int64(other["credential"].(map[string]any)["id"].(float64))
	f.expect("DELETE", base+"/"+itoa(otherID), nil, 404)
	if err := f.st.SMTPCredentialUsable(context.Background(), otherID, f.clock.Now()); err != nil {
		t.Errorf("revoked through another account's route: %v", err)
	}
	f.expect("DELETE", "/api/accounts/second/smtp-credentials/"+itoa(otherID), nil, 204)

	// A member who owns an smtp account (moved to them on the command
	// line) is refused, also on the admin routes.
	kim, _ := f.st.GetUserByName(context.Background(), "kim")
	moved := &store.Account{Name: "kims", Enabled: true, OwnerID: &kim.ID}
	if err := f.st.CreateSMTPAccount(context.Background(), moved, []string{"kim@archive.test"}); err != nil {
		t.Fatal(err)
	}
	m := f.as()
	m.loginOK("kim", keyPassword)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/accounts/kims/smtp-credentials"}, {"POST", "/api/accounts/kims/smtp-credentials"},
		{"DELETE", "/api/accounts/kims/smtp-credentials/" + id},
		{"GET", "/api/admin/smtp-credentials"}, {"DELETE", "/api/admin/smtp-credentials/" + id},
	} {
		if out := m.expect(c.method, c.path, map[string]any{"currentPassword": keyPassword}, 403); out["error"] != adminOnlySMTPCredentials {
			t.Errorf("member %s %s: %v", c.method, c.path, out)
		}
	}

	// A removed account keeps its name but gets no new secret.
	f.exec("UPDATE accounts SET removed_at = now() WHERE id = $1", x.journal.ID)
	f.expect("GET", base, nil, 409)
	f.expect("POST", base, map[string]any{"currentPassword": keyPassword, "code": f.code(f.adminSecrets["owner"])}, 409)
	f.expect("DELETE", base+"/"+id, nil, 409)
}

// Any admin lists every smtp account's secrets and revokes them (#395
// Q2), so a locked owner's secrets can be ended; locking answers how many
// there are.
func TestSMTPCredentialAdmin(t *testing.T) {
	x := newSMTPCredFixture(t)
	f := x.authFixture
	f.loginOK("owner", keyPassword)
	first := x.create(f, "owner", "journal", nil)
	x.create(f, "owner", "journal", nil)
	firstID := int64(first["credential"].(map[string]any)["id"].(float64))

	o := f.as()
	o.loginOK("other", keyPassword)
	// The other admin does not see the owner's account, only its secrets.
	o.expect("GET", "/api/accounts/journal/smtp-credentials", nil, 404)
	out := o.expect("GET", "/api/admin/smtp-credentials", nil, 200)
	list, _ := out["credentials"].([]any)
	if len(list) != 2 {
		t.Fatalf("admin list: %v", out)
	}
	for _, c := range list {
		m := c.(map[string]any)
		if m["account"] != "journal" || m["owner"] != "owner" || m["username"] != "smtp-"+itoa(x.journal.ID) ||
			m["accountRemoved"] != false || m["secret"] != nil || m["hint"] == "" {
			t.Errorf("admin entry %v", m)
		}
	}

	// Locking the owner answers the number of usable secrets.
	if out := o.expect("PATCH", "/api/users/owner", map[string]any{"locked": true}, 200); out["smtpCredentialsActive"] != float64(2) {
		t.Fatalf("lock: %v", out)
	}
	o.expect("DELETE", "/api/admin/smtp-credentials/"+itoa(firstID), nil, 204)
	o.expect("DELETE", "/api/admin/smtp-credentials/"+itoa(firstID), nil, 404)
	o.expect("DELETE", "/api/admin/smtp-credentials/999999", nil, 404)
	if err := f.st.SMTPCredentialUsable(context.Background(), firstID, f.clock.Now()); !errors.Is(err, store.ErrSMTPCredentialRevoked) {
		t.Errorf("revoked by an admin: %v", err)
	}
	e := o.lastEntry("smtp_credential.revoke")
	if d := details(t, e); d["by_admin"] != true || d["credential_id"] != float64(firstID) ||
		e.TargetID != itoa(x.journal.ID) || e.TargetName != "journal" || e.UserName != "other" {
		t.Errorf("admin revoke entry %+v %v", e, d)
	}
	// Unlocking counts nothing; locking again counts the one left.
	if out := o.expect("PATCH", "/api/users/owner", map[string]any{"locked": false}, 200); out["smtpCredentialsActive"] != float64(0) {
		t.Errorf("unlock: %v", out)
	}
	if out := o.expect("PATCH", "/api/users/owner", map[string]any{"locked": true}, 200); out["smtpCredentialsActive"] != float64(1) {
		t.Errorf("lock again: %v", out)
	}
	// admin still answers 204.
	o.expect("PATCH", "/api/users/kim", map[string]any{"admin": true}, 204)
}
