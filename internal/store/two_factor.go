package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store/db"
)

// Two-factor authentication (TOTP, RFC 6238). The secret is stored encrypted
// with MAIL_ARCHIVE_SECRET_KEY and bound to the user ID; recovery codes are
// stored as keyed hashes. Every change to a user's 2FA state increments
// users.two_factor_version, which ends pending login challenges: a reset
// always wins against a login that is in progress.

// Errors of the second factor. Callers answer all of them alike, so that a
// response does not tell which part was wrong.
var (
	ErrTwoFactorInvalid = errors.New("invalid two-factor code")
	ErrTwoFactorExpired = errors.New("two-factor challenge expired or invalid")
)

// TwoFactorState is a user's 2FA setup.
type TwoFactorState struct {
	Enabled      bool
	SetupPending bool
	IsAdmin      bool
	Version      int64
}

func secretContext(userID int64) []byte {
	return []byte("totp-secret:user:" + strconv.FormatInt(userID, 10))
}

func pendingSecretContext(userID int64) []byte {
	return []byte("totp-secret:user:" + strconv.FormatInt(userID, 10) + ":pending")
}

// GetTwoFactorState returns a user's 2FA state, without secrets.
func (s *Store) GetTwoFactorState(ctx context.Context, id int64) (*TwoFactorState, error) {
	r, err := s.q.GetUserByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &TwoFactorState{
		Enabled: r.TwoFactorEnabled, SetupPending: len(r.TwoFactorPendingSecret) > 0,
		IsAdmin: r.IsAdmin, Version: r.TwoFactorVersion,
	}, nil
}

// BeginTwoFactorSetup stores a new pending secret, replacing an earlier
// pending one. It fails with ErrConflict if 2FA is already enabled.
func (s *Store) BeginTwoFactorSetup(ctx context.Context, id int64, secret string, sealer *crypto.Sealer) error {
	enc, err := sealer.Seal([]byte(secret), pendingSecretContext(id))
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUser(ctx, q, id)
		if err != nil {
			return err
		}
		if u.TwoFactorEnabled {
			return ErrConflict
		}
		return q.BeginTwoFactorSetup(ctx, db.BeginTwoFactorSetupParams{ID: id, TwoFactorPendingSecret: enc})
	})
}

func lockUser(ctx context.Context, q *db.Queries, id int64) (db.User, error) {
	u, err := q.LockUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// ConfirmTwoFactorSetup turns 2FA on if code is valid for the pending
// secret, and stores the recovery codes. The confirmation code counts as
// used: it cannot be used again for a login.
func (s *Store) ConfirmTwoFactorSetup(ctx context.Context, id int64, code string, now time.Time, sealer *crypto.Sealer, recovery []string, key []byte) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUser(ctx, q, id)
		if err != nil {
			return err
		}
		if u.LockedAt != nil || u.TwoFactorEnabled || len(u.TwoFactorPendingSecret) == 0 {
			return ErrTwoFactorInvalid
		}
		secret, err := sealer.Open(u.TwoFactorPendingSecret, pendingSecretContext(id))
		if err != nil {
			return err
		}
		counter, ok, err := auth.ValidateTOTP(string(secret), code, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrTwoFactorInvalid
		}
		enc, err := sealer.Seal(secret, secretContext(id))
		if err != nil {
			return err
		}
		last := int64(counter) //nolint:gosec // time steps fit easily
		if err := q.EnableTwoFactor(ctx, db.EnableTwoFactorParams{ID: id, TwoFactorSecret: enc, TwoFactorLastCounter: &last}); err != nil {
			return err
		}
		if err := replaceRecoveryCodes(ctx, q, id, recovery, key); err != nil {
			return err
		}
		return q.DeleteUserTwoFactorChallenges(ctx, id)
	})
}

func replaceRecoveryCodes(ctx context.Context, q *db.Queries, id int64, codes []string, key []byte) error {
	if err := q.DeleteRecoveryCodes(ctx, id); err != nil {
		return err
	}
	for _, code := range codes {
		if err := q.InsertRecoveryCode(ctx, db.InsertRecoveryCodeParams{UserID: id, CodeHash: auth.RecoveryCodeHash(key, code)}); err != nil {
			return err
		}
	}
	return nil
}

// checkSecondFactor accepts a TOTP code newer than the last accepted one, or
// an unused recovery code, and records its use. u must be locked.
func checkSecondFactor(ctx context.Context, q *db.Queries, u db.User, code string, now time.Time, sealer *crypto.Sealer, key []byte) error {
	if u.LockedAt != nil || !u.TwoFactorEnabled || len(u.TwoFactorSecret) == 0 {
		return ErrTwoFactorInvalid
	}
	secret, err := sealer.Open(u.TwoFactorSecret, secretContext(u.ID))
	if err != nil {
		return err
	}
	counter, ok, err := auth.ValidateTOTP(string(secret), code, now)
	if err != nil {
		return err
	}
	if ok {
		// Replay protection: each time step is accepted once, and never an
		// older one than the last accepted.
		n, err := q.AcceptTwoFactorCounter(ctx, db.AcceptTwoFactorCounterParams{ID: u.ID, TwoFactorLastCounter: ptr(int64(counter))}) //nolint:gosec // time steps fit easily
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		return ErrTwoFactorInvalid
	}
	n, err := q.ConsumeRecoveryCode(ctx, db.ConsumeRecoveryCodeParams{UserID: u.ID, CodeHash: auth.RecoveryCodeHash(key, code)})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrTwoFactorInvalid
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// VerifyTwoFactorCode checks a second factor of a logged-in user (to turn
// 2FA off or to get new recovery codes) and returns the 2FA version it was
// checked against.
func (s *Store) VerifyTwoFactorCode(ctx context.Context, id int64, code string, now time.Time, sealer *crypto.Sealer, key []byte) (int64, error) {
	var version int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUser(ctx, q, id)
		if err != nil {
			return err
		}
		if err := checkSecondFactor(ctx, q, u, code, now, sealer, key); err != nil {
			return err
		}
		version = u.TwoFactorVersion
		return nil
	})
	return version, err
}

// ResetTwoFactor turns 2FA off for a user (by an admin or the CLI), removes
// the recovery codes and ends all their sessions and pending logins. It also
// works for the last admin: a lost authenticator must never lock everyone
// out. An admin without 2FA can only set it up again after the next login.
func (s *Store) ResetTwoFactor(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		if _, err := lockUser(ctx, q, id); err != nil {
			return err
		}
		return disableTwoFactor(ctx, q, id)
	})
}

func disableTwoFactor(ctx context.Context, q *db.Queries, id int64) error {
	if err := q.DisableTwoFactor(ctx, id); err != nil {
		return err
	}
	if err := q.DeleteRecoveryCodes(ctx, id); err != nil {
		return err
	}
	if err := q.DeleteUserTwoFactorChallenges(ctx, id); err != nil {
		return err
	}
	return q.DeleteUserSessions(ctx, id)
}

// DisableOwnTwoFactor turns 2FA off at a user's own request, if the state is
// still the one their second factor was checked against. Admins cannot turn
// it off (ErrConflict). All other sessions end; keepSession stays.
func (s *Store) DisableOwnTwoFactor(ctx context.Context, id, version int64, keepSession []byte) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUser(ctx, q, id)
		if err != nil {
			return err
		}
		if u.TwoFactorVersion != version || u.IsAdmin {
			return ErrConflict
		}
		if err := q.DisableTwoFactor(ctx, id); err != nil {
			return err
		}
		if err := q.DeleteRecoveryCodes(ctx, id); err != nil {
			return err
		}
		if err := q.DeleteUserTwoFactorChallenges(ctx, id); err != nil {
			return err
		}
		return q.DeleteOtherSessions(ctx, db.DeleteOtherSessionsParams{UserID: id, ID: keepSession})
	})
}

// ReplaceRecoveryCodes stores a new set of recovery codes, if the state is
// still the one the user's second factor was checked against.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, id, version int64, codes []string, key []byte) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUser(ctx, q, id)
		if err != nil {
			return err
		}
		if u.TwoFactorVersion != version || !u.TwoFactorEnabled {
			return ErrConflict
		}
		if err := replaceRecoveryCodes(ctx, q, id, codes, key); err != nil {
			return err
		}
		if err := q.BumpTwoFactorVersion(ctx, id); err != nil {
			return err
		}
		return q.DeleteUserTwoFactorChallenges(ctx, id)
	})
}

// CreateTwoFactorChallenge starts the second step of a login after a correct
// password. It returns the token for the client; only its hash is stored.
func (s *Store) CreateTwoFactorChallenge(ctx context.Context, userID, version int64, expires time.Time) (string, error) {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}
	err = s.q.CreateTwoFactorChallenge(ctx, db.CreateTwoFactorChallengeParams{ID: hash, UserID: userID, TwoFactorVersion: version, ExpiresAt: expires})
	return token, err
}

// TwoFactorChallengeUser returns the user of a valid challenge, for rate
// limiting before the code is checked.
func (s *Store) TwoFactorChallengeUser(ctx context.Context, token string) (*User, error) {
	id, err := s.q.GetTwoFactorChallengeUser(ctx, auth.HashSessionToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTwoFactorExpired
	}
	if err != nil {
		return nil, err
	}
	return s.GetUserByID(ctx, id)
}

// CompleteTwoFactorLogin checks the second factor for a challenge and, in
// the same transaction, uses up the challenge and creates the session. The
// challenge fails if the user's 2FA state changed since it was issued.
func (s *Store) CompleteTwoFactorLogin(ctx context.Context, token, code string, now time.Time, sealer *crypto.Sealer, key []byte, sessionHash []byte, expiresAt time.Time, userAgent string) (*User, error) {
	var out *User
	err := s.inTx(ctx, func(q *db.Queries) error {
		challengeID := auth.HashSessionToken(token)
		userID, err := q.GetTwoFactorChallengeUser(ctx, challengeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTwoFactorExpired
		}
		if err != nil {
			return err
		}
		// Lock order: user, then challenge (as every 2FA change locks the user
		// first).
		u, err := lockUser(ctx, q, userID)
		if errors.Is(err, ErrNotFound) {
			return ErrTwoFactorExpired
		}
		if err != nil {
			return err
		}
		ch, err := q.LockTwoFactorChallenge(ctx, challengeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTwoFactorExpired
		}
		if err != nil {
			return err
		}
		if ch.UserID != u.ID || ch.TwoFactorVersion != u.TwoFactorVersion {
			return ErrTwoFactorExpired
		}
		if err := checkSecondFactor(ctx, q, u, code, now, sealer, key); err != nil {
			return err
		}
		if err := q.DeleteTwoFactorChallenge(ctx, challengeID); err != nil {
			return err
		}
		if err := q.CreateSession(ctx, db.CreateSessionParams{ID: sessionHash, UserID: u.ID, ExpiresAt: expiresAt, UserAgent: userAgent}); err != nil {
			return err
		}
		if err := q.RecordLogin(ctx, u.ID); err != nil {
			return err
		}
		out = userFromDB(u)
		return nil
	})
	return out, err
}

// DeleteExpiredTwoFactorChallenges removes challenges nobody completed.
func (s *Store) DeleteExpiredTwoFactorChallenges(ctx context.Context) error {
	return s.q.DeleteExpiredTwoFactorChallenges(ctx)
}
