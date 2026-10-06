package db

import (
	"context"
	"time"
)

type TwoFactorUser struct {
	ID                 int64
	Name               string
	IsAdmin            bool
	TwoFactorEnabled   bool
	TwoFactorSecret    []byte
	TwoFactorPending   []byte
	TwoFactorLastCount *int64
	TwoFactorVersion   int64
	MustChangePassword bool
}

const getTwoFactorUser = `SELECT id, name, is_admin, two_factor_enabled, two_factor_secret,
	two_factor_pending_secret, two_factor_last_counter, two_factor_version,
	must_change_password
FROM users WHERE id = $1`

func (q *Queries) GetTwoFactorUser(ctx context.Context, id int64) (TwoFactorUser, error) {
	row := q.db.QueryRow(ctx, getTwoFactorUser, id)
	var u TwoFactorUser
	err := row.Scan(&u.ID, &u.Name, &u.IsAdmin, &u.TwoFactorEnabled, &u.TwoFactorSecret,
		&u.TwoFactorPending, &u.TwoFactorLastCount, &u.TwoFactorVersion, &u.MustChangePassword)
	return u, err
}

const lockTwoFactorUser = `SELECT id, name, is_admin, two_factor_enabled, two_factor_secret,
	two_factor_pending_secret, two_factor_last_counter, two_factor_version,
	must_change_password
FROM users WHERE id = $1 FOR UPDATE`

func (q *Queries) LockTwoFactorUser(ctx context.Context, id int64) (TwoFactorUser, error) {
	row := q.db.QueryRow(ctx, lockTwoFactorUser, id)
	var u TwoFactorUser
	err := row.Scan(&u.ID, &u.Name, &u.IsAdmin, &u.TwoFactorEnabled, &u.TwoFactorSecret,
		&u.TwoFactorPending, &u.TwoFactorLastCount, &u.TwoFactorVersion, &u.MustChangePassword)
	return u, err
}

const beginTwoFactorSetup = `UPDATE users
SET two_factor_pending_secret = $2, two_factor_version = two_factor_version + 1
WHERE id = $1`

func (q *Queries) BeginTwoFactorSetup(ctx context.Context, id int64, secret []byte) error {
	_, err := q.db.Exec(ctx, beginTwoFactorSetup, id, secret)
	return err
}

const enableTwoFactor = `UPDATE users
SET two_factor_secret = $2, two_factor_pending_secret = NULL,
	two_factor_enabled = TRUE, two_factor_last_counter = $3,
	two_factor_version = two_factor_version + 1
WHERE id = $1`

func (q *Queries) EnableTwoFactor(ctx context.Context, id int64, secret []byte, counter int64) error {
	_, err := q.db.Exec(ctx, enableTwoFactor, id, secret, counter)
	return err
}

const disableTwoFactor = `UPDATE users
SET two_factor_secret = NULL, two_factor_pending_secret = NULL,
	two_factor_enabled = FALSE, two_factor_last_counter = NULL,
	two_factor_version = two_factor_version + 1
WHERE id = $1`

func (q *Queries) DisableTwoFactor(ctx context.Context, id int64) error {
	_, err := q.db.Exec(ctx, disableTwoFactor, id)
	return err
}

const acceptTwoFactorCounter = `UPDATE users
SET two_factor_last_counter = $2
WHERE id = $1 AND two_factor_enabled = TRUE
  AND (two_factor_last_counter IS NULL OR two_factor_last_counter < $2)`

func (q *Queries) AcceptTwoFactorCounter(ctx context.Context, id, counter int64) (int64, error) {
	r, err := q.db.Exec(ctx, acceptTwoFactorCounter, id, counter)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected(), nil
}

const bumpTwoFactorVersion = `UPDATE users SET two_factor_version = two_factor_version + 1 WHERE id = $1`

func (q *Queries) BumpTwoFactorVersion(ctx context.Context, id int64) error {
	_, err := q.db.Exec(ctx, bumpTwoFactorVersion, id)
	return err
}

const insertRecoveryCode = `INSERT INTO two_factor_recovery_codes (user_id, code_hash) VALUES ($1, $2)`

func (q *Queries) InsertRecoveryCode(ctx context.Context, userID int64, hash []byte) error {
	_, err := q.db.Exec(ctx, insertRecoveryCode, userID, hash)
	return err
}

const deleteRecoveryCodes = `DELETE FROM two_factor_recovery_codes WHERE user_id = $1`

func (q *Queries) DeleteRecoveryCodes(ctx context.Context, userID int64) error {
	_, err := q.db.Exec(ctx, deleteRecoveryCodes, userID)
	return err
}

const consumeRecoveryCode = `UPDATE two_factor_recovery_codes
SET used_at = now()
WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`

func (q *Queries) ConsumeRecoveryCode(ctx context.Context, userID int64, hash []byte) (int64, error) {
	r, err := q.db.Exec(ctx, consumeRecoveryCode, userID, hash)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected(), nil
}

const createTwoFactorChallenge = `INSERT INTO two_factor_challenges
(id, user_id, two_factor_version, expires_at) VALUES ($1, $2, $3, $4)`

func (q *Queries) CreateTwoFactorChallenge(ctx context.Context, id []byte, userID, version int64, expires time.Time) error {
	_, err := q.db.Exec(ctx, createTwoFactorChallenge, id, userID, version, expires)
	return err
}

type TwoFactorChallenge struct {
	ID               []byte
	UserID           int64
	TwoFactorVersion int64
	CreatedAt        time.Time
	ExpiresAt        time.Time
}

const getTwoFactorChallenge = `SELECT id, user_id, two_factor_version, created_at, expires_at
FROM two_factor_challenges WHERE id = $1 AND expires_at > now()`

func (q *Queries) GetTwoFactorChallenge(ctx context.Context, id []byte) (TwoFactorChallenge, error) {
	row := q.db.QueryRow(ctx, getTwoFactorChallenge, id)
	var c TwoFactorChallenge
	err := row.Scan(&c.ID, &c.UserID, &c.TwoFactorVersion, &c.CreatedAt, &c.ExpiresAt)
	return c, err
}

const deleteTwoFactorChallenge = `DELETE FROM two_factor_challenges WHERE id = $1`

func (q *Queries) DeleteTwoFactorChallenge(ctx context.Context, id []byte) error {
	_, err := q.db.Exec(ctx, deleteTwoFactorChallenge, id)
	return err
}

const deleteUserTwoFactorChallenges = `DELETE FROM two_factor_challenges WHERE user_id = $1`

func (q *Queries) DeleteUserTwoFactorChallenges(ctx context.Context, userID int64) error {
	_, err := q.db.Exec(ctx, deleteUserTwoFactorChallenges, userID)
	return err
}
