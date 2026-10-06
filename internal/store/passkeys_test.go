package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// fakeCred stands in for the WebAuthn library's credential record: only
// the sign count matters here.
type fakeCred struct {
	Count int `json:"count"`
}

// registerFake adds a passkey with credential ID id and sign count 0.
func registerFake(t *testing.T, st *store.Store, userID int64, name, id string) error {
	t.Helper()
	ctx := context.Background()
	token, err := st.BeginPasskeyRegistration(ctx, userID, name, time.Now().Add(time.Minute),
		func(_ []byte, _ [][]byte) ([]byte, error) { return []byte(`{}`), nil })
	if err != nil {
		return err
	}
	_, err = st.FinishPasskeyRegistration(ctx, userID, token, func(_, _ []byte) ([]byte, []byte, error) {
		return []byte(id), []byte(`{"count":0}`), nil
	})
	return err
}

func TestPasskeyCeremonies(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, "bob", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerFake(t, st, alice.ID, "Phone", "cred-1"); err != nil {
		t.Fatal(err)
	}
	if err := registerFake(t, st, alice.ID, "Phone", "cred-2"); !errors.Is(err, store.ErrPasskeyName) {
		t.Fatalf("same name: %v", err)
	}
	if err := registerFake(t, st, bob.ID, "Phone", "cred-1"); !errors.Is(err, store.ErrPasskeyRegistered) {
		t.Fatalf("credential of another user: %v", err)
	}

	// A registration token is single use and bound to its user, also when
	// the check fails.
	token, err := st.BeginPasskeyRegistration(ctx, alice.ID, "Key", time.Now().Add(time.Minute),
		func(_ []byte, _ [][]byte) ([]byte, error) { return []byte(`{}`), nil })
	if err != nil {
		t.Fatal(err)
	}
	finish := func(_, _ []byte) ([]byte, []byte, error) { return []byte("cred-3"), []byte(`{}`), nil }
	if _, err := st.FinishPasskeyRegistration(ctx, bob.ID, token, finish); !errors.Is(err, store.ErrCeremonyExpired) {
		t.Fatalf("other user: %v", err)
	}
	if _, err := st.FinishPasskeyRegistration(ctx, alice.ID, token, finish); !errors.Is(err, store.ErrCeremonyExpired) {
		t.Fatalf("second use: %v", err)
	}

	// Expired ceremonies are refused and cleaned up.
	token, err = st.BeginPasskeyLogin(ctx, []byte(`{}`), time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	verify := func(_, _, c []byte) ([]byte, error) { return c, nil }
	if _, err := st.CompletePasskeyLogin(ctx, token, []byte("cred-1"), verify, sessionHash(t), time.Now().Add(time.Hour), ""); !errors.Is(err, store.ErrCeremonyExpired) {
		t.Fatalf("expired: %v", err)
	}
	if err := st.DeleteExpiredWebAuthnCeremonies(ctx); err != nil {
		t.Fatal(err)
	}

	// A login token works once, even after a failed check.
	token, err = st.BeginPasskeyLogin(ctx, []byte(`{}`), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	fail := func(_, _, _ []byte) ([]byte, error) { return nil, errors.New("bad signature") }
	if _, err := st.CompletePasskeyLogin(ctx, token, []byte("cred-1"), fail, sessionHash(t), time.Now().Add(time.Hour), ""); !errors.Is(err, store.ErrPasskeyInvalid) {
		t.Fatalf("bad signature: %v", err)
	}
	if _, err := st.CompletePasskeyLogin(ctx, token, []byte("cred-1"), verify, sessionHash(t), time.Now().Add(time.Hour), ""); !errors.Is(err, store.ErrCeremonyExpired) {
		t.Fatalf("second use: %v", err)
	}
}

func sessionHash(t *testing.T) []byte {
	t.Helper()
	_, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// Logins with the same passkey and the same sign count at once. The check
// is slow on purpose: without the row locks every login would read the old
// count and succeed.
func TestPasskeyLoginsSerialized(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerFake(t, st, alice.ID, "Phone", "cred-1"); err != nil {
		t.Fatal(err)
	}
	verify := func(_, _, raw []byte) ([]byte, error) {
		var c fakeCred
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
		if c.Count >= 1 {
			return nil, errors.New("sign count did not increase")
		}
		return json.Marshal(fakeCred{Count: 1})
	}
	const n = 5
	tokens := make([]string, n)
	for i := range tokens {
		if tokens[i], err = st.BeginPasskeyLogin(ctx, []byte(`{}`), time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i, token := range tokens {
		wg.Go(func() {
			_, errs[i] = st.CompletePasskeyLogin(ctx, token, []byte("cred-1"), verify, sessionHash(t), time.Now().Add(time.Hour), "")
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, store.ErrPasskeyInvalid):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d of %d logins succeeded, want 1: %v", ok, n, errs)
	}
}

// Registrations at 9 passkeys at once, with a slow check: the user row is
// locked, so only one gets the 10th place.
func TestPasskeyLimitSerialized(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	for i := range store.MaxPasskeys - 1 {
		if err := registerFake(t, st, alice.ID, fmt.Sprintf("Key %d", i), fmt.Sprintf("cred-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	const n = 5
	tokens := make([]string, n)
	for i := range tokens {
		tokens[i], err = st.BeginPasskeyRegistration(ctx, alice.ID, fmt.Sprintf("New %d", i), time.Now().Add(time.Minute),
			func(_ []byte, _ [][]byte) ([]byte, error) { return []byte(`{}`), nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i, token := range tokens {
		wg.Go(func() {
			_, errs[i] = st.FinishPasskeyRegistration(ctx, alice.ID, token, func(_, _ []byte) ([]byte, []byte, error) {
				time.Sleep(50 * time.Millisecond)
				return fmt.Appendf(nil, "new-%d", i), []byte(`{}`), nil
			})
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, store.ErrPasskeyLimit):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d registrations succeeded, want 1: %v", ok, errs)
	}
}

func TestPasskeyRemovalAndPasswordReset(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	keep, other := sessionHash(t), sessionHash(t)
	for _, h := range [][]byte{keep, other} {
		if err := st.CreateSession(ctx, h, alice.ID, time.Now().Add(time.Hour), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := registerFake(t, st, alice.ID, "Phone", "cred-1"); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListPasskeys(ctx, alice.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	p, err := st.RemoveOwnPasskey(ctx, alice.ID, list[0].ID, keep)
	if err != nil || p.Name != "Phone" || string(p.CredentialID) != "cred-1" {
		t.Fatalf("remove: %+v %v", p, err)
	}
	if _, err := st.GetSession(ctx, keep, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("own session ended: %v", err)
	}
	if _, err := st.GetSession(ctx, other, time.Now().Add(-time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other session still valid: %v", err)
	}

	// A password reset keeps or removes passkeys as asked.
	if err := registerFake(t, st, alice.ID, "Phone", "cred-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserPassword(ctx, alice.ID, "h2", true, false); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.PasskeyCounts(ctx); n[alice.ID] != 1 {
		t.Fatalf("passkeys after reset with keep: %v", n)
	}
	if err := st.SetUserPassword(ctx, alice.ID, "h3", true, true); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.PasskeyCounts(ctx); n[alice.ID] != 0 {
		t.Fatalf("passkeys after reset: %v", n)
	}
}

func TestPasskeyLoginCeremonyCap(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	for i := range store.MaxOpenLoginCeremonies {
		if _, err := st.BeginPasskeyLogin(ctx, []byte(`{}`), time.Now().Add(time.Minute)); err != nil {
			t.Fatalf("ceremony %d: %v", i, err)
		}
	}
	if _, err := st.BeginPasskeyLogin(ctx, []byte(`{}`), time.Now().Add(time.Minute)); !errors.Is(err, store.ErrTooManyCeremonies) {
		t.Fatalf("over the cap: %v", err)
	}
}
