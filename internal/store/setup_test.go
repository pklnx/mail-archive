package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// newSetupToken returns a fresh setup token, made at run time.
func newSetupToken(t *testing.T) string {
	t.Helper()
	tok, err := config.NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestSetupToken (#396, part 3): the states of the setup, the window of a
// token (a new token starts a new one, the same token keeps it, a start
// without one revokes it), and the check of an attempt: no token or a
// completed setup is unavailable, a wrong or expired token invalid.
func TestSetupToken(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	a, b := newSetupToken(t), newSetupToken(t)
	ha, hb := store.HashSetupToken(a), store.HashSetupToken(b)

	if s, err := st.SetupState(ctx, t0); err != nil || s.State != store.SetupCLI || s.HasUsers {
		t.Fatalf("new instance: %+v %v", s, err)
	}
	if err := st.CheckSetupToken(ctx, ha, t0); !errors.Is(err, store.ErrSetupUnavailable) {
		t.Errorf("no token stored: %v", err)
	}
	renewed, s, err := st.RecordSetupToken(ctx, ha, t0)
	if err != nil || !renewed || s.State != store.SetupToken || !s.TokenSince.Equal(t0) || !s.TokenExpires.Equal(t0.Add(7*24*time.Hour)) {
		t.Fatalf("token a: %v %+v %v", renewed, s, err)
	}
	// The same token at a later start keeps its window.
	renewed, s, err = st.RecordSetupToken(ctx, ha, t0.Add(48*time.Hour))
	if err != nil || renewed || !s.TokenSince.Equal(t0) {
		t.Fatalf("token a again: %v %+v %v", renewed, s, err)
	}
	if err := st.CheckSetupToken(ctx, hb, t0); !errors.Is(err, store.ErrSetupTokenInvalid) {
		t.Errorf("wrong token: %v", err)
	}
	if err := st.CheckSetupToken(ctx, ha, t0.Add(7*24*time.Hour-time.Second)); err != nil {
		t.Errorf("right token in its window: %v", err)
	}
	expired := t0.Add(7 * 24 * time.Hour)
	if err := st.CheckSetupToken(ctx, ha, expired); !errors.Is(err, store.ErrSetupTokenInvalid) {
		t.Errorf("expired token: %v", err)
	}
	if s, _ := st.SetupState(ctx, expired); s.State != store.SetupExpired {
		t.Errorf("state after the window: %q", s.State)
	}
	// Another token starts a new window and replaces the old one.
	renewed, s, err = st.RecordSetupToken(ctx, hb, expired)
	if err != nil || !renewed || s.State != store.SetupToken || !s.TokenSince.Equal(expired) {
		t.Fatalf("token b: %v %+v %v", renewed, s, err)
	}
	if err := st.CheckSetupToken(ctx, ha, expired); !errors.Is(err, store.ErrSetupTokenInvalid) {
		t.Errorf("the replaced token: %v", err)
	}
	// A start without a token revokes it.
	if cleared, err := st.ClearSetupToken(ctx); err != nil || !cleared {
		t.Fatalf("clear: %v %v", cleared, err)
	}
	if cleared, err := st.ClearSetupToken(ctx); err != nil || cleared {
		t.Fatalf("clear again: %v %v", cleared, err)
	}
	if s, _ := st.SetupState(ctx, expired); s.State != store.SetupCLI {
		t.Errorf("state after the revocation: %q", s.State)
	}
	if err := st.CheckSetupToken(ctx, hb, expired); !errors.Is(err, store.ErrSetupUnavailable) {
		t.Errorf("revoked token: %v", err)
	}
}

// TestCompleteSetup (#396, part 3): the token creates one admin, completes
// the setup with that user, consumes the token and writes user.setup with
// the admin as target and user; then setup is unavailable, also with a
// token stored again. A wrong token creates nobody.
func TestCompleteSetup(t *testing.T) {
	ctx := context.Background()
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	now := time.Now()
	tok := newSetupToken(t)
	h := store.HashSetupToken(tok)
	if _, _, err := st.RecordSetupToken(ctx, h, now); err != nil {
		t.Fatal(err)
	}
	nu := store.NewUser{Name: "first", PasswordHash: "h"}
	if _, err := st.CompleteSetup(ctx, store.HashSetupToken(newSetupToken(t)), now, nu); !errors.Is(err, store.ErrSetupTokenInvalid) {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := st.CompleteSetup(ctx, h, now.Add(store.SetupTokenValidity), nu); !errors.Is(err, store.ErrSetupTokenInvalid) {
		t.Fatalf("expired token: %v", err)
	}
	if n, _ := st.CountUsers(ctx); n != 0 {
		t.Fatalf("%d users after refused attempts", n)
	}
	e := storetest.Entry(store.ActorSession, "user.setup")
	e.SetTarget(store.TargetUser, "", "first")
	u, err := st.CompleteSetup(store.WithAudit(ctx, e), h, now, nu)
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsAdmin || u.IsAuditor || u.MustChangePassword {
		t.Errorf("the first user: %+v", u)
	}
	s, err := st.SetupState(ctx, now)
	if err != nil || s.State != store.SetupComplete || s.CompletedAt == nil || s.TokenExpires != nil {
		t.Fatalf("after setup: %+v %v", s, err)
	}
	is, err := st.InstanceStatus(ctx, "test")
	if err != nil || is.SetupState != store.SetupComplete || is.SetupCompletedAt == nil || is.EncryptionAtRest != store.BlobOff {
		t.Errorf("InstanceStatus: %+v %v", is, err)
	}
	var setupUser int64
	if err := conn.QueryRow(ctx, `SELECT setup_user_id FROM instance_state`).Scan(&setupUser); err != nil || setupUser != u.ID {
		t.Errorf("setup_user_id %d, want %d: %v", setupUser, u.ID, err)
	}
	var target, user string
	var userID int64
	if err := conn.QueryRow(ctx, `SELECT target_id, user_name, user_id FROM audit_log WHERE action = 'user.setup'`).
		Scan(&target, &user, &userID); err != nil {
		t.Fatal(err)
	}
	if target != fmt.Sprint(u.ID) || user != "first" || userID != u.ID {
		t.Errorf("user.setup entry: target %q, user %q (%d)", target, user, userID)
	}
	if _, err := st.CompleteSetup(ctx, h, now, store.NewUser{Name: "second", PasswordHash: "h"}); !errors.Is(err, store.ErrSetupUnavailable) {
		t.Errorf("second attempt: %v", err)
	}
	// After the setup a start with a token stores nothing.
	if renewed, s, err := st.RecordSetupToken(ctx, h, now); err != nil || renewed || s.State != store.SetupComplete {
		t.Errorf("token after the setup: %v %+v %v", renewed, s, err)
	}
	if err := st.CheckSetupToken(ctx, h, now); !errors.Is(err, store.ErrSetupUnavailable) {
		t.Errorf("token after the setup: %v", err)
	}
}

// TestCreateUserCompletesSetup (#396, part 3): `user add` of the first
// user completes the setup and consumes a stored token, so the setup link
// never works after it.
func TestCreateUserCompletesSetup(t *testing.T) {
	ctx := context.Background()
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	now := time.Now()
	h := store.HashSetupToken(newSetupToken(t))
	if _, _, err := st.RecordSetupToken(ctx, h, now); err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(ctx, "cli-admin", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	s, err := st.SetupState(ctx, now)
	if err != nil || s.State != store.SetupComplete || s.CompletedAt == nil || s.TokenExpires != nil {
		t.Fatalf("after user add: %+v %v", s, err)
	}
	var setupUser int64
	if err := conn.QueryRow(ctx, `SELECT setup_user_id FROM instance_state`).Scan(&setupUser); err != nil || setupUser != u.ID {
		t.Errorf("setup_user_id %d, want %d: %v", setupUser, u.ID, err)
	}
	if _, err := st.CompleteSetup(ctx, h, now, store.NewUser{Name: "link-admin", PasswordHash: "h"}); !errors.Is(err, store.ErrSetupUnavailable) {
		t.Errorf("setup after user add: %v", err)
	}
	// A second user changes nothing.
	if _, err := st.CreateUser(ctx, "other", "h", false); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT setup_user_id FROM instance_state`).Scan(&setupUser); err != nil || setupUser != u.ID {
		t.Errorf("setup_user_id after a second user %d, want %d: %v", setupUser, u.ID, err)
	}
}

// TestSetupRacesUserAdd (#396, revision 1, point 5): the setup with a
// token and `user add` lock the row of instance_state first, so they are
// serialized: exactly one of them creates the first user, which completes
// the setup and gets the accounts added before any user existed. The test
// holds the row and lets them queue for it in a given order (the signal:
// their lock waits), then releases it.
//   - user add first: it creates the first user; the setup, though it
//     passed its check before, decides again under the lock, finds setup
//     complete and creates nobody;
//   - the setup first: it creates the first admin; user add a second user,
//     which neither completes the setup nor takes the accounts.
func TestSetupRacesUserAdd(t *testing.T) {
	for _, setupFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "user add first", true: "setup first"}[setupFirst], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
			defer cancel()
			st, url := storetest.NewWithURL(t)
			now := time.Now()
			h := store.HashSetupToken(newSetupToken(t))
			if _, _, err := st.RecordSetupToken(ctx, h, now); err != nil {
				t.Fatal(err)
			}
			early := &store.Account{Name: "early", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}}
			if err := st.CreateAccount(ctx, early); err != nil {
				t.Fatal(err)
			}
			holder := connect(t, url)
			tx, err := holder.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `SELECT 1 FROM instance_state FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			observer := connect(t, url)
			waiting := func(n int, done ...chan error) {
				t.Helper()
				for _, d := range done {
					waitForRow(t, observer, d, fmt.Sprintf("%d waiting for instance_state", n), `SELECT (count(*) = $1)::int FROM pg_stat_activity
						WHERE datname = current_database() AND wait_event_type = 'Lock'`, n)
				}
			}
			addDone, setupDone := make(chan error, 1), make(chan error, 1)
			add := func() {
				_, err := st.CreateUser(ctx, "cli-admin", "h", true)
				addDone <- err
			}
			setup := func() {
				_, err := st.CompleteSetup(ctx, h, now, store.NewUser{Name: "link-admin", PasswordHash: "h"})
				setupDone <- err
			}
			first, second, firstDone := add, setup, addDone
			if setupFirst {
				first, second, firstDone = setup, add, setupDone
			}
			go first()
			waiting(1, firstDone)
			go second()
			waiting(2, addDone, setupDone)
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			addErr, setupErr := <-addDone, <-setupDone
			if addErr != nil {
				t.Fatalf("user add: %v", addErr)
			}
			want, users := "cli-admin", 1
			if setupFirst {
				if setupErr != nil {
					t.Fatalf("setup: %v", setupErr)
				}
				want, users = "link-admin", 2
			} else if !errors.Is(setupErr, store.ErrSetupUnavailable) {
				t.Errorf("setup after user add: %v, want ErrSetupUnavailable", setupErr)
			}
			var setupUser, owner string
			var n int
			if err := observer.QueryRow(ctx, `SELECT
				(SELECT u.name FROM instance_state i JOIN users u ON u.id = i.setup_user_id),
				(SELECT u.name FROM accounts a JOIN users u ON u.id = a.owner_id WHERE a.id = $1),
				(SELECT count(*) FROM users)`, early.ID).Scan(&setupUser, &owner, &n); err != nil {
				t.Fatal(err)
			}
			if setupUser != want || owner != want || n != users {
				t.Errorf("setup user %q, owner of the early account %q, %d users; want %q and %d", setupUser, owner, n, want, users)
			}
		})
	}
}

// TestFreshInstance (#396, revision 1): new until a user, a message or a
// state of encryption at rest exists.
func TestFreshInstance(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if fresh, err := st.FreshInstance(ctx); err != nil || !fresh {
		t.Fatalf("new instance: %v %v", fresh, err)
	}
	if _, err := st.CreateUser(ctx, "alice", "h", true); err != nil {
		t.Fatal(err)
	}
	if fresh, err := st.FreshInstance(ctx); err != nil || fresh {
		t.Errorf("with a user: %v %v", fresh, err)
	}
	other, url := storetest.NewWithURL(t)
	if _, err := connect(t, url).Exec(ctx, `INSERT INTO blob_encryption (state, generation, keyring, keyring_id, keyring_generation, current_key)
		VALUES ('off', 0, '\x00', repeat('0', 32), 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if fresh, err := other.FreshInstance(ctx); err != nil || fresh {
		t.Errorf("with encryption at rest recorded: %v %v", fresh, err)
	}
}
