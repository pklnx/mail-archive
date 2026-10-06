package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
)

// as returns a copy of the fixture with its own cookie jar, i.e. another
// browser.
func (f *authFixture) as() *authFixture {
	jar, _ := cookiejar.New(nil)
	g := *f
	g.client = &http.Client{Jar: jar}
	return &g
}

func (f *authFixture) loginOK(name, password string) {
	f.t.Helper()
	if resp := f.login(name, password); resp.StatusCode != 200 {
		f.t.Fatalf("login %s: %d", name, resp.StatusCode)
	}
}

func TestUserAdminNeedsAdmin(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.addUser("kim", "correct horse battery", false)
	f.loginOK("kim", "correct horse battery")
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/users", nil},
		{"POST", "/api/users", map[string]any{"name": "new"}},
		{"PATCH", "/api/users/admin", map[string]any{"locked": true}},
		{"DELETE", "/api/users/admin", nil},
		{"POST", "/api/users/admin/password", nil},
	} {
		f.expect(c.method, c.path, c.body, 403)
	}
}

func TestCreatedUserMustChangePassword(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.loginOK("admin", "correct horse battery")

	out := f.expect("POST", "/api/users", map[string]any{"name": " Kim ", "admin": false}, 201)
	pw, _ := out["password"].(string)
	if out["name"] != "kim" || len(pw) != 23 {
		t.Fatalf("created: %v", out)
	}
	f.expect("POST", "/api/users", map[string]any{"name": "kim"}, 409)
	list := f.expect("GET", "/api/users", nil, 200)
	if users, _ := list["users"].([]any); len(users) != 2 || !strings.Contains(strings.ToLower(toJSON(users)), `"mustchangepassword":true`) {
		t.Fatalf("list: %v", list)
	}

	kim := f.as()
	kim.loginOK("kim", pw)
	if out := kim.expect("GET", "/api/session", nil, 200); out["user"].(map[string]any)["mustChangePassword"] != true {
		t.Fatalf("session: %v", out)
	}
	// Nothing but the password change works yet.
	if out := kim.expect("GET", "/api/status", nil, 403); out["passwordChangeRequired"] != true {
		t.Fatalf("status before change: %v", out)
	}
	kim.expect("PUT", "/api/profile/password", map[string]string{"current": pw, "new": pw}, 400)
	kim.expect("PUT", "/api/profile/password", map[string]string{"current": pw, "new": "kims own passphrase"}, 204)
	kim.expect("GET", "/api/status", nil, 200)
	if out := kim.expect("GET", "/api/session", nil, 200); out["user"].(map[string]any)["mustChangePassword"] != false {
		t.Fatalf("session after change: %v", out)
	}
	// The generated password no longer works.
	if resp := f.as().login("kim", pw); resp.StatusCode != 401 {
		t.Fatalf("old password: %d", resp.StatusCode)
	}
}

func TestResetPasswordEndsSessions(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.addUser("kim", "kims own passphrase", false)
	f.loginOK("admin", "correct horse battery")
	kim := f.as()
	kim.loginOK("kim", "kims own passphrase")

	out := f.expect("POST", "/api/users/kim/password", nil, 200)
	pw, _ := out["password"].(string)
	kim.expect("GET", "/api/status", nil, 401)
	if resp := f.as().login("kim", "kims own passphrase"); resp.StatusCode != 401 {
		t.Fatalf("old password after reset: %d", resp.StatusCode)
	}
	again := f.as()
	again.loginOK("kim", pw)
	again.expect("GET", "/api/status", nil, 403)
}

func TestAdminCannotChangeOwnLogin(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.addUser("second", "correct horse battery", true)
	f.loginOK("admin", "correct horse battery")
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/api/users/admin", map[string]any{"locked": true}},
		{"PATCH", "/api/users/admin", map[string]any{"admin": false}},
		{"DELETE", "/api/users/admin", nil},
		{"POST", "/api/users/admin/password", nil},
	} {
		f.expect(c.method, c.path, c.body, 403)
	}
	// Other admins can be changed; one field per request.
	f.expect("PATCH", "/api/users/second", map[string]any{"admin": false, "locked": true}, 400)
	f.expect("PATCH", "/api/users/second", map[string]any{"admin": false}, 204)
	f.expect("PATCH", "/api/users/second", map[string]any{"locked": true}, 204)
	f.expect("PATCH", "/api/users/second", map[string]any{"locked": false}, 204)
	f.expect("PATCH", "/api/users/nobody", map[string]any{"locked": true}, 404)
	// The demoted user cannot use the admin endpoints anymore.
	second := f.as()
	second.loginOK("second", "correct horse battery")
	second.expect("GET", "/api/users", nil, 403)
}

func TestDeleteUserWithAccounts(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	kim := f.addUser("kim", "correct horse battery", false)
	f.addUser("empty", "correct horse battery", false)
	acc := &store.Account{Name: "kims", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &kim.ID}
	if err := f.st.CreateAccount(context.Background(), acc); err != nil {
		t.Fatal(err)
	}
	f.loginOK("admin", "correct horse battery")
	if out := f.expect("DELETE", "/api/users/kim", nil, 409); !strings.Contains(out["error"].(string), "1 account") {
		t.Fatalf("delete an owner: %v", out)
	}
	f.expect("DELETE", "/api/users/empty", nil, 204)
	f.expect("DELETE", "/api/users/empty", nil, 404)
}

func TestChangeOwnPassword(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("kim", "kims own passphrase", false)
	f.loginOK("kim", "kims own passphrase")
	other := f.as()
	other.loginOK("kim", "kims own passphrase")

	f.expect("PUT", "/api/profile/password", map[string]string{"current": "kims own passphrase", "new": "short"}, 400)
	f.expect("PUT", "/api/profile/password", map[string]string{"current": "kims own passphrase", "new": "a brand new passphrase"}, 204)
	// This browser stays logged in, the other one is logged out.
	f.expect("GET", "/api/status", nil, 200)
	other.expect("GET", "/api/status", nil, 401)

	// Wrong current passwords count like failed logins.
	for range 5 {
		f.expect("PUT", "/api/profile/password", map[string]string{"current": "wrong wrong wrong", "new": "yet another passphrase"}, 401)
	}
	f.expect("PUT", "/api/profile/password", map[string]string{"current": "a brand new passphrase", "new": "yet another passphrase"}, 429)
}

func TestDemoteEachOtherConcurrently(t *testing.T) {
	f := newAuthFixture(t)
	a := f.addUser("a", "correct horse battery", true)
	b := f.addUser("b", "correct horse battery", true)
	for round := range 10 {
		var errA, errB error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); errA = f.st.SetUserAdmin(context.Background(), b.ID, false) }()
		go func() { defer wg.Done(); errB = f.st.SetUserAdmin(context.Background(), a.ID, false) }()
		wg.Wait()
		if (errA == nil) == (errB == nil) {
			t.Fatalf("round %d: %v / %v, want exactly one success", round, errA, errB)
		}
		// Make both admins again for the next round.
		_ = f.st.SetUserAdmin(context.Background(), a.ID, true)
		_ = f.st.SetUserAdmin(context.Background(), b.ID, true)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
