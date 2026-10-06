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

type TwoFactorState struct {
	Enabled       bool
	Secret        string
	PendingSecret string
	LastCounter   *int64
	Version       int64
	IsAdmin       bool
	MustChange    bool
}

func twoFactorState(r db.TwoFactorUser, sealer *crypto.Sealer) (*TwoFactorState, error) {
	out := &TwoFactorState{
		Enabled: r.TwoFactorEnabled, Version: r.TwoFactorVersion,
		IsAdmin: r.IsAdmin, MustChange: r.MustChangePassword,
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
