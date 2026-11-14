package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keepia/archive/internal/auth"
	"github.com/keepia/archive/internal/crypto"
	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/store/storetest"
	"github.com/keepia/archive/internal/testwait"
)

// twoFactorLogin is a user with TOTP on and a pending login challenge.
type twoFactorLogin struct {
	st        *store.Store
	sealer    *crypto.Sealer
	key       []byte
	userID    int64
	secret    string
	challenge string
	// now is one time step after the setup, so that its code is new.
	now time.Time
}

func newTwoFactorLogin(t *testing.T, st *store.Store) *twoFactorLogin {
	t.Helper()
	ctx := context.Background()
	key := bytes.Repeat([]byte{7}, 32)
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(ctx, "alice", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	setup := time.Now()
	code, err := auth.GenerateTOTP(secret, setup)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BeginTwoFactorSetup(ctx, u.ID, secret, sealer); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmTwoFactorSetup(ctx, u.ID, code, setup, sealer, nil, key); err != nil {
		t.Fatal(err)
	}
	l := &twoFactorLogin{st: st, sealer: sealer, key: key, userID: u.ID, secret: secret, now: setup.Add(auth.TOTPPeriod)}
	l.challenge = l.newChallenge(t)
	return l
}

func (l *twoFactorLogin) newChallenge(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	state, err := l.st.GetTwoFactorState(ctx, l.userID)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := l.st.CreateTwoFactorChallenge(ctx, l.userID, state.Version, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

// right is the valid code at l.now.
func (l *twoFactorLogin) right(t *testing.T) string {
	t.Helper()
	code, err := auth.GenerateTOTP(l.secret, l.now)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// wrong is a six-digit code that is not valid at l.now.
func (l *twoFactorLogin) wrong(t *testing.T) string {
	t.Helper()
	for i := range 1000 {
		code := fmt.Sprintf("%06d", i*7919%1000000)
		if _, ok, err := auth.ValidateTOTP(l.secret, code, l.now); err != nil {
			t.Fatal(err)
		} else if !ok {
			return code
		}
	}
	t.Fatal("no wrong code found")
	return ""
}

func (l *twoFactorLogin) complete(ctx context.Context, t *testing.T, challenge, code string) error {
	_, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Error(err)
		return err
	}
	_, err = l.st.CompleteTwoFactorLogin(ctx, challenge, code, l.now, l.sealer, l.key, hash, time.Now().Add(time.Hour), "test")
	return err
}

// lastWrong reports whether err answers the wrong code that used the last
// attempt of a challenge.
func lastWrong(err error) bool {
	return errors.Is(err, store.ErrTwoFactorExhausted) && errors.Is(err, store.ErrTwoFactorInvalid)
}

// noAttemptLeft reports whether err answers a request that found no
// attempt left and checked no code.
func noAttemptLeft(err error) bool {
	return errors.Is(err, store.ErrTwoFactorExhausted) && !errors.Is(err, store.ErrTwoFactorInvalid)
}

// A challenge accepts 3 codes (GHSA-w6jg-ffvw-q4f5, #6). The 3rd wrong one
// ends it, so a 4th code, even the right one, is refused; a new challenge
// works again.
func TestTwoFactorChallengeAcceptsThreeCodes(t *testing.T) {
	ctx := context.Background()
	st, dbURL := storetest.NewWithURL(t)
	l := newTwoFactorLogin(t, st)
	for i := range store.MaxTwoFactorAttempts - 1 {
		if err := l.complete(ctx, t, l.challenge, l.wrong(t)); !errors.Is(err, store.ErrTwoFactorInvalid) || errors.Is(err, store.ErrTwoFactorExhausted) {
			t.Fatalf("wrong code %d: %v", i+1, err)
		}
	}
	if err := l.complete(ctx, t, l.challenge, l.wrong(t)); !lastWrong(err) {
		t.Fatalf("wrong code %d: %v", store.MaxTwoFactorAttempts, err)
	}
	var rows int
	if err := connect(t, dbURL).QueryRow(ctx, `SELECT count(*) FROM two_factor_challenges`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("challenges left: %d %v", rows, err)
	}
	if err := l.complete(ctx, t, l.challenge, l.right(t)); !errors.Is(err, store.ErrTwoFactorExpired) {
		t.Fatalf("right code after %d wrong ones: %v", store.MaxTwoFactorAttempts, err)
	}
	if err := l.complete(ctx, t, l.newChallenge(t), l.right(t)); err != nil {
		t.Fatalf("new challenge: %v", err)
	}
}

// Two wrong codes leave the right one its attempt.
func TestTwoFactorTwoWrongCodesThenRight(t *testing.T) {
	ctx := context.Background()
	l := newTwoFactorLogin(t, storetest.New(t))
	for range 2 {
		if err := l.complete(ctx, t, l.challenge, l.wrong(t)); !errors.Is(err, store.ErrTwoFactorInvalid) || errors.Is(err, store.ErrTwoFactorExhausted) {
			t.Fatalf("wrong code: %v", err)
		}
	}
	if err := l.complete(ctx, t, l.challenge, l.right(t)); err != nil {
		t.Fatalf("right code: %v", err)
	}
}

// A challenge without attempts left checks no code, also not the right
// one, and stays until it expires: requests still in flight on it are not
// cut short by the requests that came too late.
func TestTwoFactorChallengeWithoutAttemptsChecksNothing(t *testing.T) {
	ctx := context.Background()
	st, dbURL := storetest.NewWithURL(t)
	l := newTwoFactorLogin(t, st)
	conn := connect(t, dbURL)
	if _, err := conn.Exec(ctx, `UPDATE two_factor_challenges SET attempts = $1`, store.MaxTwoFactorAttempts); err != nil {
		t.Fatal(err)
	}
	if err := l.complete(ctx, t, l.challenge, l.right(t)); !noAttemptLeft(err) {
		t.Fatalf("right code without attempts: %v", err)
	}
	var attempts int
	if err := conn.QueryRow(ctx, `SELECT attempts FROM two_factor_challenges`).Scan(&attempts); err != nil || attempts != store.MaxTwoFactorAttempts {
		t.Fatalf("attempts: %d %v", attempts, err)
	}
	// An expired challenge stays "expired", attempts or not.
	if _, err := conn.Exec(ctx, `UPDATE two_factor_challenges SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := l.complete(ctx, t, l.challenge, l.right(t)); !errors.Is(err, store.ErrTwoFactorExpired) {
		t.Fatalf("expired challenge: %v", err)
	}
}

// Parallel requests on one challenge cannot check more than 3 codes, with
// no limiter in front of the store. The user's row is held, so every
// request that got an attempt waits for it in LockUser; the others answer
// at once. The test waits until those two add up to all requests.
func TestTwoFactorChallengeParallelCodes(t *testing.T) {
	const n = 12
	ctx := context.Background()
	_, dbURL := storetest.NewWithURL(t)
	// Every held request keeps a connection while it waits.
	st, err := store.Open(ctx, storetest.WithMaxConns(t, dbURL, n+5))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	l := newTwoFactorLogin(t, st)

	holder := connect(t, dbURL)
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, l.userID); err != nil {
		t.Fatal(err)
	}

	var answered atomic.Int32
	results := make(chan error, n)
	wrong := l.wrong(t)
	for range n {
		go func() {
			err := l.complete(ctx, t, l.challenge, wrong)
			answered.Add(1)
			results <- err
		}()
	}

	observer := connect(t, dbURL)
	wait, cancel := context.WithTimeout(ctx, testwait.Limit(t))
	defer cancel()
	var held int
	for {
		if err := observer.QueryRow(wait, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			AND query LIKE '-- name: LockUser :one%'`).Scan(&held); err != nil {
			t.Fatalf("waiting for held requests + answers = %d (answered %d): %v", n, answered.Load(), err)
		}
		if held+int(answered.Load()) >= n {
			break
		}
	}
	if held > store.MaxTwoFactorAttempts {
		t.Errorf("%d requests reached the code check, want at most %d", held, store.MaxTwoFactorAttempts)
	}
	early := int(answered.Load())
	for range early {
		if err := <-results; !noAttemptLeft(err) {
			t.Errorf("request without an attempt: %v", err)
		}
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Of the held requests, the one with the last attempt checks its code
	// and ends the challenge; one that comes after it finds it gone.
	last := 0
	for range n - early {
		select {
		case err := <-results:
			switch {
			case lastWrong(err):
				last++
			case errors.Is(err, store.ErrTwoFactorInvalid), errors.Is(err, store.ErrTwoFactorExpired):
			default:
				t.Errorf("held request: %v", err)
			}
		case <-wait.Done():
			t.Fatal("held requests did not finish")
		}
	}
	if last != 1 {
		t.Errorf("%d answers for the last wrong code, want 1", last)
	}
	if err := l.complete(ctx, t, l.challenge, l.right(t)); !errors.Is(err, store.ErrTwoFactorExpired) {
		t.Fatalf("right code afterwards: %v", err)
	}
}

// 00038 adds the attempts column with 0 for pending challenges; Down drops
// it, also with challenges in the table.
func TestTwoFactorAttemptsMigration(t *testing.T) {
	ctx := context.Background()
	st, dbURL := storetest.NewWithURL(t)
	l := newTwoFactorLogin(t, st)
	if err := l.complete(ctx, t, l.challenge, l.wrong(t)); !errors.Is(err, store.ErrTwoFactorInvalid) {
		t.Fatal(err)
	}
	downTwoFactorAttemptsMigration(t, st)
	conn := connect(t, dbURL)
	var columns int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'two_factor_challenges' AND column_name = 'attempts'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("column after down: %d %v", columns, err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := conn.QueryRow(ctx, `SELECT attempts FROM two_factor_challenges`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("attempts after up: %d %v", attempts, err)
	}
	if err := l.complete(ctx, t, l.challenge, l.right(t)); err != nil {
		t.Fatalf("login after up: %v", err)
	}
}

// downTwoFactorAttemptsMigration rolls back 00045_alert_mail, 00044_attachment_text_per_location, 00043_metrics_key_scope, 00042_deletion_marks, 00041_storage_usage, 00040_smtp_credentials,
// 00039_instance_state and 00038_two_factor_challenge_attempts.
func downTwoFactorAttemptsMigration(t *testing.T, st *store.Store) {
	t.Helper()
	downInstanceStateMigration(t, st)
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00038_two_factor_challenge_attempts") {
		t.Fatalf("down: %q %v", m, err)
	}
}
