package store

import (
	"context"
	"crypto/hmac"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/keepia/archive/internal/crypto"
	"github.com/keepia/archive/internal/store/db"
)

// The check value of the secret key (#396): instance_state.secret_key_check
// holds HMAC-SHA256(key, "mail-archive secret key check v1")
// (crypto.KeyCheck), never the key. serve compares it at start and refuses
// another key; `secret-key accept` records a new one.

// SecretKeyCheck returns the recorded check value, nil when none is
// recorded yet or instance_state does not exist.
func (s *Store) SecretKeyCheck(ctx context.Context) ([]byte, error) {
	if ok, err := s.instanceStateExists(ctx); err != nil || !ok {
		return nil, err
	}
	check, err := s.q.GetSecretKeyCheck(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return check, err
}

// RecordSecretKeyCheck records check unless a check value is recorded
// already; recorded tells whether it wrote it.
func (s *Store) RecordSecretKeyCheck(ctx context.Context, check []byte) (recorded bool, err error) {
	n, err := s.q.RecordSecretKeyCheck(ctx, check)
	return n > 0, err
}

// AcceptSecretKey records check as the instance's secret key check value,
// replacing the one before, which it returns (nil if there was none).
// With an entry from WithAudit in ctx, the change and the entry are
// committed together; when check is recorded already, nothing changes
// and no entry is written.
func (s *Store) AcceptSecretKey(ctx context.Context, check []byte) (previous []byte, err error) {
	err = s.inTx(ctx, func(q *db.Queries) error {
		var err error
		if previous, err = q.LockSecretKeyCheck(ctx); err != nil {
			return err
		}
		if previous != nil && hmac.Equal(previous, check) {
			skipAudit(ctx)
			return nil
		}
		if e := auditFrom(ctx); e != nil {
			if previous != nil {
				e.Detail("previous", crypto.KeyCheckFingerprint(previous))
			}
			e.Detail("fingerprint", crypto.KeyCheckFingerprint(check))
		}
		_, err = q.SetSecretKeyCheck(ctx, check)
		return err
	})
	return previous, err
}

// TwoFactorSecretsOpen tries to open up to limit of the stored TOTP
// secrets with sealer and reports how many it tried and how many opened.
func (s *Store) TwoFactorSecretsOpen(ctx context.Context, sealer *crypto.Sealer, limit int) (tried, opened int, err error) {
	rows, err := s.q.SampleTwoFactorSecrets(ctx, clampInt32(limit))
	if err != nil {
		return 0, 0, err
	}
	for _, r := range rows {
		tried++
		if _, err := sealer.Open(r.TwoFactorSecret, secretContext(r.ID)); err == nil {
			opened++
		}
	}
	return tried, opened, nil
}
