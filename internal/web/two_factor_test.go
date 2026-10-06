package web

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/pklnx/mail-archive/internal/auth"
)

// passwordStep logs in with the password and returns the 2FA challenge.
func (f *authFixture) passwordStep(name, password string) string {
	f.t.Helper()
	resp, out := f.do("POST", "/api/session", map[string]string{"username": name, "password": password})
	challenge, _ := out["challenge"].(string)
	if resp.StatusCode != 200 || out["twoFactorRequired"] != true || challenge == "" {
		f.t.Fatalf("password step: %d %v", resp.StatusCode, out)
	}
	return challenge
}

func (f *authFixture) secondStep(challenge, code string, want int) map[string]any {
	f.t.Helper()
	return f.expect("POST", "/api/session/2fa", map[string]string{"challenge": challenge, "code": code}, want)
}

func TestAdminMustSetUpTOTP(t *testing.T) {
	f := newAuthFixture(t)
	h, _ := cheapHasher.Hash(context.Background(), "correct horse battery")
	if _, err := f.st.CreateUser(context.Background(), "admin", h, true); err != nil {
		t.Fatal(err)
	}
	// Without 2FA the admin gets a session that only allows the setup.
	f.loginOK("admin", "correct horse battery")
	if out := f.expect("GET", "/api/session", nil, 200); out["user"].(map[string]any)["twoFactorRequired"] != true {
		t.Fatalf("session: %v", out)
	}
	if out := f.expect("GET", "/api/status", nil, 403); out["twoFactorSetupRequired"] != true {
		t.Fatalf("status: %v", out)
	}
	f.expect("GET", "/api/users", nil, 403)
	setup := f.expect("POST", "/api/profile/2fa/setup", nil, 200)
	secret, _ := setup["secret"].(string)
	if secret == "" || setup["qrDataUrl"] == nil || setup["otpauthUri"] == nil {
		t.Fatalf("setup: %v", setup)
	}
	f.expect("POST", "/api/profile/2fa/confirm", map[string]string{"code": "000000"}, 401)
	code := f.code(secret)
	out := f.expect("POST", "/api/profile/2fa/confirm", map[string]string{"code": code}, 200)
	if codes, _ := out["recoveryCodes"].([]any); len(codes) != auth.RecoveryCodeCount {
		t.Fatalf("recovery codes: %v", out)
	}
	f.expect("GET", "/api/status", nil, 200)
	f.expect("POST", "/api/profile/2fa/setup", nil, 409)
	// Admins cannot turn it off.
	f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "correct horse battery", "code": code}, 403)

	// The confirmation code is used up: it does not work for the next login.
	f.expect("DELETE", "/api/session", nil, 204)
	f.secondStep(f.passwordStep("admin", "correct horse battery"), code, 401)
}

func TestVoluntaryTwoFactorNeedsCodeAtLogin(t *testing.T) {
	f := newAuthFixture(t)
	u := f.addUser("alice", "correct horse battery", false)
	secret, recovery := f.enableTOTP(u)

	// The password alone gives no session.
	challenge := f.passwordStep("alice", "correct horse battery")
	f.expect("GET", "/api/status", nil, 401)
	f.secondStep(challenge, "000000", 401)
	f.secondStep(challenge, f.code(secret), 200)
	f.expect("GET", "/api/status", nil, 200)

	// A recovery code works once, also typed in capitals without dashes.
	f.expect("DELETE", "/api/session", nil, 204)
	typed := strings.ToUpper(strings.ReplaceAll(recovery[0], "-", ""))
	f.secondStep(f.passwordStep("alice", "correct horse battery"), typed, 200)
	f.expect("DELETE", "/api/session", nil, 204)
	f.secondStep(f.passwordStep("alice", "correct horse battery"), recovery[0], 401)
}

func TestSecondFactorRateLimit(t *testing.T) {
	f := newAuthFixture(t)
	u := f.addUser("alice", "correct horse battery", false)
	f.enableTOTP(u)
	// A correct password does not reset the counter of wrong codes.
	for range 5 {
		f.secondStep(f.passwordStep("alice", "correct horse battery"), "000000", 401)
	}
	resp, _ := f.do("POST", "/api/session", map[string]string{"username": "alice", "password": "correct horse battery"})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("password after 5 wrong codes: %d", resp.StatusCode)
	}
}

func TestResetDuringLoginWins(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	u := f.addUser("alice", "correct horse battery", false)
	secret, _ := f.enableTOTP(u)

	alice := f.as()
	alice.loginOK("alice", "correct horse battery")
	challenge := f.as().passwordStep("alice", "correct horse battery")

	f.loginOK("admin", "correct horse battery")
	if on := f.listedTwoFactor(); !on["alice"] || !on["admin"] {
		t.Fatalf("2FA in user list before reset: %v", on)
	}
	f.expect("POST", "/api/users/alice/2fa/reset", nil, 204)
	f.expect("POST", "/api/users/admin/2fa/reset", nil, 403) // not your own
	if on := f.listedTwoFactor(); on["alice"] || !on["admin"] {
		t.Fatalf("2FA in user list after reset: %v", on)
	}

	// The pending login and the existing session are both gone.
	f.secondStep(challenge, f.code(secret), 401)
	alice.expect("GET", "/api/status", nil, 401)
	// Alice logs in with the password alone again (2FA is optional for her).
	alice.loginOK("alice", "correct horse battery")
	alice.expect("GET", "/api/status", nil, 200)
}

func TestRecoveryCodeUsedOnceConcurrently(t *testing.T) {
	f := newAuthFixture(t)
	u := f.addUser("alice", "correct horse battery", false)
	_, recovery := f.enableTOTP(u)
	const tries = 6
	challenges := make([]string, tries)
	for i := range challenges {
		challenges[i] = f.as().passwordStep("alice", "correct horse battery")
	}
	codes := make([]int, tries)
	var wg sync.WaitGroup
	for i := range challenges {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := f.as().do("POST", "/api/session/2fa", map[string]string{"challenge": challenges[i], "code": recovery[1]})
			codes[i] = resp.StatusCode
		}()
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == 200 {
			ok++
		} else if c != 401 {
			t.Fatalf("status %d", c)
		}
	}
	if ok != 1 {
		t.Fatalf("%d logins with one recovery code (statuses %v)", ok, codes)
	}
}

func TestDisableAndRegenerate(t *testing.T) {
	f := newAuthFixture(t)
	u := f.addUser("alice", "correct horse battery", false)
	secret, recovery := f.enableTOTP(u)
	f.secondStep(f.passwordStep("alice", "correct horse battery"), f.code(secret), 200)

	// New recovery codes need a current code; the old ones stop working.
	f.expect("POST", "/api/profile/2fa/recovery-codes", map[string]string{"code": "000000"}, 401)
	out := f.expect("POST", "/api/profile/2fa/recovery-codes", map[string]string{"code": f.code(secret)}, 200)
	fresh, _ := out["recoveryCodes"].([]any)
	if len(fresh) != auth.RecoveryCodeCount {
		t.Fatalf("new codes: %v", out)
	}
	other := f.as()
	other.secondStep(other.passwordStep("alice", "correct horse battery"), recovery[2], 401)

	// Turning it off needs the password and a code.
	f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "wrong wrong wrong", "code": f.code(secret)}, 401)
	f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "correct horse battery", "code": fresh[0].(string)}, 204)
	f.expect("GET", "/api/status", nil, 200)
	f.expect("DELETE", "/api/session", nil, 204)
	f.loginOK("alice", "correct horse battery") // password only again
}

func TestRequire2FAForEveryone(t *testing.T) {
	f := newAuthFixtureWith(t, true)
	u := f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	f.expect("GET", "/api/status", nil, 403)
	secret, _ := f.enableTOTP(u)
	f.expect("DELETE", "/api/session", nil, 204)
	code := f.code(secret)
	f.secondStep(f.passwordStep("alice", "correct horse battery"), code, 200)
	f.expect("GET", "/api/status", nil, 200)
	f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "correct horse battery", "code": code}, 403)
}

func TestGeneratedPasswordThenTOTP(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.loginOK("admin", "correct horse battery")
	out := f.expect("POST", "/api/users", map[string]any{"name": "boss", "admin": true}, 201)
	pw := out["password"].(string)

	boss := f.as()
	boss.loginOK("boss", pw)
	// First the password, then 2FA.
	boss.expect("POST", "/api/profile/2fa/setup", nil, 403)
	boss.expect("PUT", "/api/profile/password", map[string]string{"current": pw, "new": "the boss passphrase"}, 204)
	boss.expect("GET", "/api/status", nil, 403)
	boss.expect("POST", "/api/profile/2fa/setup", nil, 200)
}

// listedTwoFactor returns twoFactorEnabled per name from the admin user list.
func (f *authFixture) listedTwoFactor() map[string]bool {
	f.t.Helper()
	users, _ := f.expect("GET", "/api/users", nil, 200)["users"].([]any)
	on := map[string]bool{}
	for _, u := range users {
		m, _ := u.(map[string]any)
		name, _ := m["name"].(string)
		on[name], _ = m["twoFactorEnabled"].(bool)
	}
	return on
}
