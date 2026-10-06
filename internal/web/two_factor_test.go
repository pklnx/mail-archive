package web

import (
	"context"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
)

func TestMandatoryAdminTOTP(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.loginOK("admin", "correct horse battery")
	f.expect("GET", "/api/status", nil, 200)

	h, err := cheapHasher.Hash(context.Background(), "correct horse battery")
	if err != nil { t.Fatal(err) }
	if _, err := f.st.CreateUser(context.Background(), "pendingadmin", h, true); err != nil { t.Fatal(err) }
	g := f.as()
	if resp := g.login("pendingadmin", "correct horse battery"); resp.StatusCode != 200 { t.Fatalf("login: %d", resp.StatusCode) }
	g.expect("GET", "/api/status", nil, 403)
	g.expect("GET", "/api/profile/2fa", nil, 200)
}

func TestTwoFactorLoginChallenge(t *testing.T) {
	f := newAuthFixture(t)
	u := f.addUser("alice", "correct horse battery", false)
	secret := "JBSWY3DPEHPK3PXP"
	ctx := context.Background()
	if err := f.st.BeginTwoFactorSetup(ctx, u.ID, secret, f.sealer); err != nil { t.Fatal(err) }
	if err := f.st.ConfirmTwoFactorSetup(ctx, u.ID, testTOTP(t, secret), time.Now(), f.sealer, []string{"recovery-test"}, f.secretKey); err != nil { t.Fatal(err) }

	resp, out := f.do("POST", "/api/session", map[string]string{"username": "alice", "password": "correct horse battery"})
	if resp.StatusCode != 200 || out["twoFactorRequired"] != true { t.Fatalf("login: %d %v", resp.StatusCode, out) }
	challenge := out["challenge"].(string)
	f.expect("GET", "/api/status", nil, 401)
	f.expect("POST", "/api/session/2fa", map[string]string{"challenge": challenge, "code": "000000"}, 401)
	f.expect("POST", "/api/session/2fa", map[string]string{"challenge": challenge, "code": auth.GenerateTOTP(secret, time.Now())}, 200)
	f.expect("GET", "/api/status", nil, 200)
}
