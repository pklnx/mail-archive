package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store/db"
)

var (
	ErrTwoFactorInvalid = errors.New("invalid two-factor code")
	ErrTwoFactorReplay = errors.New("two-factor code already used")
	ErrTwoFactorExpired = errors.New("two-factor challenge expired")
)

type TwoFactorState struct {
	Enabled       bool
	Secret        string
	PendingSecret string
	LastCounter   *int64
	Version       int64
	IsAdmin       bool
	MustChange    bool
	Locked         bool
}

func twoFactorState(r db.TwoFactorUser, sealer *crypto.Sealer) (*TwoFactorState, error) {
	out := &TwoFactorState{
		Enabled: r.TwoFactorEnabled, Version: r.TwoFactorVersion,
		IsAdmin: r.IsAdmin, MustChange: r.MustChangePassword, Locked: r.LockedAt != nil,
	}
	if len(r.TwoFactorSecret) > 0 {
		b, err := sealer.Open(r.TwoFactorSecret, []byte("totp-secret:user:"+itoa(r.ID)))
		if err != nil {
			return nil, err
		}
		out.Secret = string(b)
	}
	if len(r.TwoFactorPending) > 0 {
		b, err := sealer.Open(r.TwoFactorPending, []byte("totp-secret:user:"+itoa(r.ID)+":pending"))
		if err != nil {
			return nil, err
		}
		out.PendingSecret = string(b)
	}
	out.LastCounter = r.TwoFactorLastCount
	return out, nil
}

func (s *Store) GetTwoFactorState(ctx context.Context, id int64, sealer *crypto.Sealer) (*TwoFactorState, error) {
	r, err := s.q.GetTwoFactorUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return twoFactorState(r, sealer)
}

func (s *Store) BeginTwoFactorSetup(ctx context.Context, id int64, secret string, sealer *crypto.Sealer) error {
	enc, err := sealer.Seal([]byte(secret), []byte("totp-secret:user:"+itoa(id)+":pending"))
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		return q.BeginTwoFactorSetup(ctx, id, enc)
	})
}

func (s *Store) EnableTwoFactor(ctx context.Context, id int64, secret string, counter int64, sealer *crypto.Sealer, recovery []string, key []byte) error {
	enc, err := sealer.Seal([]byte(secret), []byte("totp-secret:user:"+itoa(id)))
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := q.LockTwoFactorUser(ctx, id)
		if err != nil {
			return err
		}
		if len(u.TwoFactorPending) == 0 {
			return errors.New("no pending TOTP setup")
		}
		if err := q.EnableTwoFactor(ctx, id, enc, counter); err != nil {
			return err
		}
		if err := q.DeleteRecoveryCodes(ctx, id); err != nil {
			return err
		}
		for _, code := range recovery {
			if err := q.InsertRecoveryCode(ctx, id, auth.RecoveryCodeHash(key, code)); err != nil {
				return err
			}
		}
		return q.DeleteUserTwoFactorChallenges(ctx, id)
	})
}

func (s *Store) ConsumeRecoveryCode(ctx context.Context, id int64, hash []byte) (bool, error) {
	n, err := s.q.ConsumeRecoveryCode(ctx, id, hash)
	return n == 1, err
}

func (s *Store) ResetTwoFactor(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := q.LockTwoFactorUser(ctx, id)
		if err != nil {
			return err
		}
		if u.IsAdmin && u.TwoFactorEnabled {
			admins, err := q.LockUsableAdmins(ctx)
			if err != nil {
				return err
			}
			if len(admins) == 1 && admins[0] == id {
				return ErrLastAdmin
			}
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
		return q.DeleteUserSessions(ctx, id)
	})
}

func (s *Store) CreateTwoFactorChallenge(ctx context.Context, id int64, version int64, expires time.Time) (string, error) {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}
	if err := s.q.CreateTwoFactorChallenge(ctx, hash, id, version, expires); err != nil {
		return "", err
	}
	return string(token), nil
}

func (s *Store) GetTwoFactorChallenge(ctx context.Context, token string) (*db.TwoFactorChallenge, error) {
	hash := auth.HashSessionToken(token)
	c, err := s.q.GetTwoFactorChallenge(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) ConsumeTwoFactorChallenge(ctx context.Context, token string) error {
	return s.q.DeleteTwoFactorChallenge(ctx, auth.HashSessionToken(token))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}


func (s *Store) ConfirmTwoFactorSetup(ctx context.Context, id int64, code string, now time.Time, sealer *crypto.Sealer, recovery []string, key []byte) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := q.LockTwoFactorUser(ctx, id)
		if err != nil { return err }
		if u.LockedAt != nil || len(u.TwoFactorPending) == 0 { return ErrTwoFactorInvalid }
		secret, err := sealer.Open(u.TwoFactorPending, []byte("totp-secret:user:"+itoa(id)+":pending"))
		if err != nil { return err }
		counter, ok, err := auth.ValidateTOTP(string(secret), code, now)
		if err != nil || !ok { return ErrTwoFactorInvalid }
		enc, err := sealer.Seal(secret, []byte("totp-secret:user:"+itoa(id)))
		if err != nil { return err }
		if err := q.EnableTwoFactor(ctx, id, enc, int64(counter)); err != nil { return err }
		if err := q.DeleteRecoveryCodes(ctx, id); err != nil { return err }
		for _, rc := range recovery {
			if err := q.InsertRecoveryCode(ctx, id, auth.RecoveryCodeHash(key, rc)); err != nil { return err }
		}
		return q.DeleteUserTwoFactorChallenges(ctx, id)
	})
}

func (s *Store) VerifyTwoFactorCode(ctx context.Context, id int64, code string, now time.Time, sealer *crypto.Sealer, key []byte) (int64, error) {
	var version int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		u, err := q.LockTwoFactorUser(ctx, id)
		if err != nil { return err }
		if u.LockedAt != nil || !u.TwoFactorEnabled || len(u.TwoFactorSecret) == 0 { return ErrTwoFactorInvalid }
		secret, err := sealer.Open(u.TwoFactorSecret, []byte("totp-secret:user:"+itoa(id)))
		if err != nil { return err }
		counter, ok, err := auth.ValidateTOTP(string(secret), code, now)
		if err != nil { return err }
		if ok {
			if u.TwoFactorLastCount != nil && int64(counter) <= *u.TwoFactorLastCount { return ErrTwoFactorReplay }
			n, err := q.AcceptTwoFactorCounter(ctx, id, int64(counter))
			if err != nil { return err }
			if n != 1 { return ErrTwoFactorReplay }
			version = u.TwoFactorVersion
			return nil
		}
		used, err := consumeRecoveryCodeTx(ctx, q, id, auth.RecoveryCodeHash(key, code))
		if err != nil { return err }
		if !used { return ErrTwoFactorInvalid }
		version = u.TwoFactorVersion
		return nil
	})
	return version, err
}

func consumeRecoveryCodeTx(ctx context.Context, q *db.Queries, id int64, hash []byte) (bool, error) {
	n, err := q.ConsumeRecoveryCode(ctx, id, hash)
	return n == 1, err
}

func (s *Store) CompleteTwoFactorLogin(ctx context.Context, token, code string, now time.Time, sealer *crypto.Sealer, key []byte, expiresAt time.Time, userAgent string) (string, *User, error) {
	var outUser *User
	rawToken, hash, err := auth.NewSessionToken()
	if err != nil { return "", nil, err }
	err = s.inTx(ctx, func(q *db.Queries) error {
		ch, err := q.GetTwoFactorChallenge(ctx, auth.HashSessionToken(token))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) { return ErrTwoFactorExpired }
			return err
		}
		u, err := q.LockTwoFactorUser(ctx, ch.UserID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) { return ErrTwoFactorExpired }
			return err
		}
		ch, err = q.LockTwoFactorChallenge(ctx, auth.HashSessionToken(token))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) { return ErrTwoFactorExpired }
			return err
		}
		if ch.UserID != u.ID || ch.TwoFactorVersion != u.TwoFactorVersion || !u.TwoFactorEnabled || u.LockedAt != nil { return ErrTwoFactorExpired }
		secret, err := sealer.Open(u.TwoFactorSecret, []byte("totp-secret:user:"+itoa(u.ID)))
		if err != nil { return err }
		valid := false
		counter, ok, err := auth.ValidateTOTP(string(secret), code, now)
		if err != nil { return err }
		if ok && (u.TwoFactorLastCount == nil || int64(counter) > *u.TwoFactorLastCount) {
			n, err := q.AcceptTwoFactorCounter(ctx, u.ID, int64(counter))
			if err != nil { return err }
			valid = n == 1
		}
		if !valid {
			used, err := consumeRecoveryCodeTx(ctx, q, u.ID, auth.RecoveryCodeHash(key, code))
			if err != nil { return err }
			valid = used
		}
		if !valid { return ErrTwoFactorInvalid }
		if err := q.DeleteTwoFactorChallenge(ctx, auth.HashSessionToken(token)); err != nil { return err }
		if err := q.CreateSession(ctx, db.CreateSessionParams{ID: hash, UserID: u.ID, ExpiresAt: expiresAt, UserAgent: userAgent}); err != nil { return err }
		if err := q.RecordLogin(ctx, u.ID); err != nil { return err }
		outUser = &User{ID: u.ID, Name: u.Name, IsAdmin: u.IsAdmin, LockedAt: u.LockedAt, MustChangePassword: u.MustChangePassword, TwoFactorEnabled: true, TwoFactorVersion: u.TwoFactorVersion}
		return nil
	})
	if err != nil { return "", nil, err }
	return string(rawToken), outUser, nil
}


func (s *Store) DisableTwoFactorIfVersion(ctx context.Context, id, version int64, keepSession []byte) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := q.LockTwoFactorUser(ctx, id)
		if err != nil { return err }
		if u.TwoFactorVersion != version || u.IsAdmin { return ErrConflict }
		if err := q.DisableTwoFactor(ctx, id); err != nil { return err }
		if err := q.DeleteRecoveryCodes(ctx, id); err != nil { return err }
		if err := q.DeleteUserTwoFactorChallenges(ctx, id); err != nil { return err }
		return q.DeleteOtherSessions(ctx, db.DeleteOtherSessionsParams{UserID: id, ID: keepSession})
	})
}

func (s *Store) ReplaceRecoveryCodesIfVersion(ctx context.Context, id, version int64, codes []string, key []byte) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := q.LockTwoFactorUser(ctx, id)
		if err != nil { return err }
		if u.TwoFactorVersion != version || !u.TwoFactorEnabled { return ErrConflict }
		if err := q.DeleteRecoveryCodes(ctx, id); err != nil { return err }
		for _, code := range codes {
			if err := q.InsertRecoveryCode(ctx, id, auth.RecoveryCodeHash(key, code)); err != nil { return err }
		}
		if err := q.BumpTwoFactorVersion(ctx, id); err != nil { return err }
		return q.DeleteUserTwoFactorChallenges(ctx, id)
	})
}
