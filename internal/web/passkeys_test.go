package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/pklnx/mail-archive/internal/store"
)

const (
	testPublicURL = "https://archive.example.test"
	testRPID      = "archive.example.test"
)

// softKey is a software authenticator: a discoverable ES256 credential
// with user verification, as a phone or security key would create.
type softKey struct {
	t      *testing.T
	priv   *ecdsa.PrivateKey
	id     []byte
	handle []byte // the user handle, learned at registration
	count  uint32

	// Knobs for broken or hostile clients.
	origin  string
	rpID    string
	noUV    bool
	counter func(uint32) uint32 // the sign count to report; nil counts up
}

func newSoftKey(t *testing.T) *softKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softKey{t: t, priv: priv, id: id, origin: testPublicURL, rpID: testRPID}
}

var b64 = base64.RawURLEncoding

func (k *softKey) clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": k.origin, "crossOrigin": false})
	return b
}

func (k *softKey) authData(attested []byte) []byte {
	rp := sha256.Sum256([]byte(k.rpID))
	flags := byte(0x01) // user present
	if !k.noUV {
		flags |= 0x04
	}
	if attested != nil {
		flags |= 0x40
	}
	next := k.count + 1
	if k.counter != nil {
		next = k.counter(k.count)
	}
	k.count = next
	out := append(rp[:], flags)
	out = binary.BigEndian.AppendUint32(out, next)
	return append(out, attested...)
}

// create answers navigator.credentials.create() for the options of a
// registration.
func (k *softKey) create(publicKey map[string]any) map[string]any {
	k.t.Helper()
	user, _ := publicKey["user"].(map[string]any)
	handle, err := b64.DecodeString(user["id"].(string))
	if err != nil {
		k.t.Fatal(err)
	}
	k.handle = handle
	pub := k.priv.PublicKey
	x, y := make([]byte, 32), make([]byte, 32)
	pub.X.FillBytes(x) //nolint:staticcheck // the raw coordinates are what COSE needs
	pub.Y.FillBytes(y) //nolint:staticcheck
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		k.t.Fatal(err)
	}
	attested := make([]byte, 16) // AAGUID: none
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(k.id)))
	attested = append(attested, k.id...)
	attested = append(attested, cose...)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": k.authData(attested)})
	if err != nil {
		k.t.Fatal(err)
	}
	return map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(k.clientData("webauthn.create", publicKey["challenge"].(string))),
			"attestationObject": b64.EncodeToString(att),
		},
		"clientExtensionResults": map[string]any{},
	}
}

// get answers navigator.credentials.get() for the options of a login.
func (k *softKey) get(publicKey map[string]any) map[string]any {
	k.t.Helper()
	cd := k.clientData("webauthn.get", publicKey["challenge"].(string))
	ad := k.authData(nil)
	// The signature covers authenticator data and the client data hash.
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		k.t.Fatal(err)
	}
	return map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(ad),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(k.handle),
		},
		"clientExtensionResults": map[string]any{},
	}
}

func newPasskeyFixture(t *testing.T) *authFixture {
	t.Helper()
	return newAuthFixtureOpts(t, Options{PublicURL: testPublicURL})
}

// beginRegistration starts adding a passkey and returns token and options.
func (f *authFixture) beginRegistration(name, password, code string) (string, map[string]any) {
	f.t.Helper()
	out := f.expect("POST", "/api/profile/passkeys/begin", map[string]string{"name": name, "currentPassword": password, "code": code}, 200)
	pk, _ := out["publicKey"].(map[string]any)
	return out["token"].(string), pk
}

// addPasskey registers k for the logged-in user.
func (f *authFixture) addPasskey(k *softKey, name, password, code string) {
	f.t.Helper()
	token, pk := f.beginRegistration(name, password, code)
	f.expect("POST", "/api/profile/passkeys/finish", map[string]any{"token": token, "credential": k.create(pk)}, 201)
}

// beginPasskeyLogin starts a passkey login and returns token and options.
func (f *authFixture) beginPasskeyLogin() (string, map[string]any) {
	f.t.Helper()
	out := f.expect("POST", "/api/session/passkey/begin", nil, 200)
	pk, _ := out["publicKey"].(map[string]any)
	return out["token"].(string), pk
}

func (f *authFixture) passkeyLogin(k *softKey, want int) map[string]any {
	f.t.Helper()
	token, pk := f.beginPasskeyLogin()
	return f.expect("POST", "/api/session/passkey/finish", map[string]any{"token": token, "credential": k.get(pk)}, want)
}

func TestPasskeyRegisterAndLogin(t *testing.T) {
	f := newPasskeyFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")

	out := f.expect("GET", "/api/profile/passkeys", nil, 200)
	if out["available"] != true || out["origin"] != testPublicURL {
		t.Fatalf("passkeys not available: %v", out)
	}
	// Registering needs the password; a wrong one counts as a failure.
	f.expect("POST", "/api/profile/passkeys/begin", map[string]string{"name": "Phone", "currentPassword": "wrong wrong wrong"}, 401)
	f.expect("POST", "/api/profile/passkeys/begin", map[string]string{"name": "", "currentPassword": "correct horse battery"}, 400)

	k := newSoftKey(t)
	f.addPasskey(k, "Phone", "correct horse battery", "")
	// The same name twice is refused before the browser is asked.
	f.expect("POST", "/api/profile/passkeys/begin", map[string]string{"name": "Phone", "currentPassword": "correct horse battery"}, 409)

	list := f.expect("GET", "/api/profile/passkeys", nil, 200)["passkeys"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["name"] != "Phone" || list[0].(map[string]any)["lastUsedAt"] != nil {
		t.Fatalf("list: %v", list)
	}

	// A passkey login needs neither name nor password.
	other := f.as()
	user := other.passkeyLogin(k, 200)["user"].(map[string]any)
	if user["name"] != "alice" {
		t.Fatalf("logged in as %v", user)
	}
	other.expect("GET", "/api/status", nil, 200)
	list = other.expect("GET", "/api/profile/passkeys", nil, 200)["passkeys"].([]any)
	if list[0].(map[string]any)["lastUsedAt"] == nil {
		t.Fatalf("last use not recorded: %v", list)
	}
}

func TestPasskeysOffWithoutPublicURL(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	if out := f.expect("GET", "/api/session", nil, 401); out["passkeyOrigin"] != nil {
		t.Fatalf("passkeys offered: %v", out)
	}
	f.expect("POST", "/api/session/passkey/begin", nil, 503)
	f.loginOK("alice", "correct horse battery")
	if out := f.expect("GET", "/api/profile/passkeys", nil, 200); out["available"] != false {
		t.Fatalf("passkeys available: %v", out)
	}
	f.expect("POST", "/api/profile/passkeys/begin", map[string]string{"name": "Phone", "currentPassword": "correct horse battery"}, 503)
}

func TestPasskeyRegistrationNeedsSecondFactor(t *testing.T) {
	f := newPasskeyFixture(t)
	u := f.addUser("alice", "correct horse battery", false)
	secret, recovery := f.enableTOTP(u)
	f.loginOK("alice", "correct horse battery")

	// With 2FA on, the password alone is not enough: else a taken-over
	// session and a phished password would skip TOTP for good.
	f.expect("POST", "/api/profile/passkeys/begin", map[string]string{"name": "Phone", "currentPassword": "correct horse battery"}, 401)
	f.expect("POST", "/api/profile/passkeys/begin", map[string]string{"name": "Phone", "currentPassword": "correct horse battery", "code": "000000"}, 401)
	k := newSoftKey(t)
	f.addPasskey(k, "Phone", "correct horse battery", f.code(secret))
	f.addPasskey(newSoftKey(t), "Key", "correct horse battery", recovery[0])

	// The passkey replaces password and TOTP.
	f.as().passkeyLogin(k, 200)
}

func TestPasskeyLoginRejects(t *testing.T) {
	f := newPasskeyFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	k := newSoftKey(t)
	f.addPasskey(k, "Phone", "correct horse battery", "")
	anon := f.as()

	for name, broken := range map[string]func(k *softKey){
		"wrong origin":     func(k *softKey) { k.origin = "https://evil.example.test" },
		"wrong RP ID":      func(k *softKey) { k.rpID = "evil.example.test" },
		"no verification":  func(k *softKey) { k.noUV = true },
		"sign count stuck": func(k *softKey) { k.counter = func(c uint32) uint32 { return c } },
	} {
		t.Run(name, func(t *testing.T) {
			bad := *k
			bad.t = t
			broken(&bad)
			anon.passkeyLogin(&bad, 401)
		})
	}

	// A token works once.
	token, pk := anon.beginPasskeyLogin()
	anon.expect("POST", "/api/session/passkey/finish", map[string]any{"token": token, "credential": k.get(pk)}, 200)
	anon.expect("POST", "/api/session/passkey/finish", map[string]any{"token": token, "credential": k.get(pk)}, 401)

	// An expired token does not work.
	f.clock.Advance(-10 * time.Minute)
	token, pk = anon.beginPasskeyLogin()
	f.clock.Advance(10 * time.Minute)
	anon.expect("POST", "/api/session/passkey/finish", map[string]any{"token": token, "credential": k.get(pk)}, 401)

	// An unknown passkey: the answer lets the UI tell the browser.
	stranger := newSoftKey(t)
	stranger.handle = []byte("not a handle")
	out := anon.passkeyLogin(stranger, 401)
	if out["unknownCredential"] != true || out["credentialId"] != b64.EncodeToString(stranger.id) || out["rpId"] != testRPID {
		t.Fatalf("unknown passkey: %v", out)
	}
}

func TestPasskeyLoginFailuresCountPerAddress(t *testing.T) {
	f := newPasskeyFixture(t)
	stranger := newSoftKey(t)
	stranger.handle = []byte("x")
	for range 20 {
		f.passkeyLogin(stranger, 401)
	}
	f.expect("POST", "/api/session/passkey/begin", nil, 429)
}

func TestPasskeyBeginRateLimit(t *testing.T) {
	f := newPasskeyFixture(t)
	for range passkeyBeginLimit {
		f.beginPasskeyLogin()
	}
	resp, out := f.do("POST", "/api/session/passkey/begin", nil)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status %d: %v", resp.StatusCode, out)
	}
}

// A passkey login ends in the same state as a password login: locks,
// generated passwords and missing mandatory TOTP all still apply.
func TestPasskeySessionStateMachine(t *testing.T) {
	f := newPasskeyFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.loginOK("admin", "correct horse battery")
	adminKey := newSoftKey(t)
	f.addPasskey(adminKey, "Phone", "correct horse battery", f.code(f.adminSecrets["admin"]))
	alice := f.addUser("alice", "correct horse battery", false)
	a := f.as()
	a.loginOK("alice", "correct horse battery")
	aliceKey := newSoftKey(t)
	a.addPasskey(aliceKey, "Phone", "correct horse battery", "")

	// A generated password must be changed first, also after a passkey.
	f.expect("POST", "/api/users/alice/password", map[string]bool{"removePasskeys": false}, 200)
	p := f.as()
	if out := p.passkeyLogin(aliceKey, 200); out["user"].(map[string]any)["mustChangePassword"] != true {
		t.Fatalf("passkey login: %v", out)
	}
	if out := p.expect("GET", "/api/status", nil, 403); out["passwordChangeRequired"] != true {
		t.Fatalf("restricted session: %v", out)
	}

	// A locked user gets in neither way.
	f.expect("PATCH", "/api/users/alice", map[string]bool{"locked": true}, 204)
	f.as().passkeyLogin(aliceKey, 403)
	f.expect("PATCH", "/api/users/alice", map[string]bool{"locked": false}, 204)

	// An admin whose TOTP was reset must set it up again.
	if err := f.st.ResetTwoFactor(t.Context(), mustUser(t, f, "admin").ID); err != nil {
		t.Fatal(err)
	}
	ad := f.as()
	ad.passkeyLogin(adminKey, 200)
	if out := ad.expect("GET", "/api/status", nil, 403); out["twoFactorSetupRequired"] != true {
		t.Fatalf("restricted session: %v", out)
	}
	ad.expect("GET", "/api/profile/passkeys", nil, 403)
	_ = alice
}

func mustUser(t *testing.T, f *authFixture, name string) *store.User {
	t.Helper()
	u, err := f.st.GetUserByName(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Logins with the same passkey and the same sign count at once: the rows
// are locked, so each sees the count of the one before and only the first
// succeeds.
func TestPasskeyConcurrentLogins(t *testing.T) {
	f := newPasskeyFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	k := newSoftKey(t)
	f.addPasskey(k, "Phone", "correct horse battery", "")

	const n = 6
	bodies := make([]map[string]any, n)
	start := k.count
	for i := range bodies {
		token, pk := f.beginPasskeyLogin()
		k.count = start // all report the same next count
		bodies[i] = map[string]any{"token": token, "credential": k.get(pk)}
	}
	if ok := countStatus(f, "/api/session/passkey/finish", bodies, 200); ok != 1 {
		t.Fatalf("%d of %d logins succeeded, want 1", ok, n)
	}
}

// countStatus sends the bodies at the same time from separate clients and
// counts the answers with status want.
func countStatus(f *authFixture, path string, bodies []map[string]any, want int) int {
	f.t.Helper()
	var wg sync.WaitGroup
	var mu sync.Mutex
	ready := make(chan struct{})
	ok := 0
	for _, body := range bodies {
		c := f.as()
		c.client.Jar = f.client.Jar
		wg.Go(func() {
			<-ready
			resp, _ := c.do("POST", path, body)
			if resp.StatusCode == want {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		})
	}
	close(ready)
	wg.Wait()
	return ok
}

// Registrations at 9 passkeys at once: the user row is locked, so only one
// gets the 10th place.
func TestPasskeyLimitConcurrent(t *testing.T) {
	f := newPasskeyFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	for i := range store.MaxPasskeys - 1 {
		f.addPasskey(newSoftKey(t), fmt.Sprintf("Key %d", i), "correct horse battery", "")
	}
	var bodies []map[string]any
	for i := range 6 {
		token, pk := f.beginRegistration(fmt.Sprintf("New %d", i), "correct horse battery", "")
		bodies = append(bodies, map[string]any{"token": token, "credential": newSoftKey(t).create(pk)})
	}
	if ok := countStatus(f, "/api/profile/passkeys/finish", bodies, 201); ok != 1 {
		t.Fatalf("%d registrations succeeded, want 1", ok)
	}
	if n := len(f.expect("GET", "/api/profile/passkeys", nil, 200)["passkeys"].([]any)); n != store.MaxPasskeys {
		t.Fatalf("%d passkeys", n)
	}
	f.expect("POST", "/api/profile/passkeys/begin", map[string]string{"name": "C", "currentPassword": "correct horse battery"}, 409)
}

func TestPasskeyOfAnotherUser(t *testing.T) {
	f := newPasskeyFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.addUser("bob", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	k := newSoftKey(t)
	f.addPasskey(k, "Phone", "correct horse battery", "")

	b := f.as()
	b.loginOK("bob", "correct horse battery")
	token, pk := b.beginRegistration("Phone", "correct horse battery", "")
	b.expect("POST", "/api/profile/passkeys/finish", map[string]any{"token": token, "credential": k.create(pk)}, 409)
	// Someone else's token does not finish this user's registration.
	token, pk = f.beginRegistration("Laptop", "correct horse battery", "")
	b.expect("POST", "/api/profile/passkeys/finish", map[string]any{"token": token, "credential": newSoftKey(t).create(pk)}, 400)
}

func TestRemovePasskeyEndsOtherSessions(t *testing.T) {
	f := newPasskeyFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	k := newSoftKey(t)
	f.addPasskey(k, "Phone", "correct horse battery", "")
	thief := f.as()
	thief.passkeyLogin(k, 200)

	id := f.expect("GET", "/api/profile/passkeys", nil, 200)["passkeys"].([]any)[0].(map[string]any)["id"].(float64)
	out := f.expect("DELETE", fmt.Sprintf("/api/profile/passkeys/%d", int64(id)), nil, 200)
	if out["credentialId"] != b64.EncodeToString(k.id) || out["rpId"] != testRPID {
		t.Fatalf("removal: %v", out)
	}
	f.expect("GET", "/api/status", nil, 200)
	thief.expect("GET", "/api/status", nil, 401)
	f.as().passkeyLogin(k, 401)
	f.expect("DELETE", fmt.Sprintf("/api/profile/passkeys/%d", int64(id)), nil, 404)
}

func TestAdminRemovesPasskeys(t *testing.T) {
	f := newPasskeyFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.addUser("alice", "correct horse battery", false)
	a := f.as()
	a.loginOK("alice", "correct horse battery")
	k := newSoftKey(t)
	a.addPasskey(k, "Phone", "correct horse battery", "")

	f.loginOK("admin", "correct horse battery")
	if n := f.listedPasskeys()["alice"]; n != 1 {
		t.Fatalf("listed passkeys: %v", n)
	}
	f.expect("DELETE", "/api/users/admin/passkeys", nil, 403) // not your own
	f.expect("DELETE", "/api/users/alice/passkeys", nil, 204)
	a.expect("GET", "/api/status", nil, 401)
	f.as().passkeyLogin(k, 401)

	// A password reset removes passkeys unless told otherwise.
	a.loginOK("alice", "correct horse battery")
	a.addPasskey(k, "Phone", "correct horse battery", "")
	f.expect("POST", "/api/users/alice/password", nil, 200)
	if n := f.listedPasskeys()["alice"]; n != 0 {
		t.Fatalf("passkeys after password reset: %v", n)
	}
}

// listedPasskeys returns the passkey count per name from the user list.
func (f *authFixture) listedPasskeys() map[string]float64 {
	f.t.Helper()
	users, _ := f.expect("GET", "/api/users", nil, 200)["users"].([]any)
	out := map[string]float64{}
	for _, u := range users {
		m, _ := u.(map[string]any)
		out[m["name"].(string)], _ = m["passkeys"].(float64)
	}
	return out
}
