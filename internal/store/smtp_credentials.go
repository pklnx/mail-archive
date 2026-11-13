package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// SMTP credentials (#395) let a mail server log in to the SMTP server for
// one smtp account. The store keeps the SHA-256 of a secret and its last
// characters as a hint; the secret itself is shown once. A new secret keeps
// the current one usable for a grace period, so an account has at most two
// usable secrets: the current one and one that is expiring. Whether a
// secret has expired is decided in Go with the caller's clock (now), never
// with the database's.

// SMTP credential states.
const (
	SMTPCredentialActive   = "active"   // the current secret, without an end
	SMTPCredentialExpiring = "expiring" // superseded, usable until expires_at
	SMTPCredentialExpired  = "expired"
	SMTPCredentialRevoked  = "revoked"
)

// MaxSMTPGrace is the longest grace period of a superseded secret.
const MaxSMTPGrace = 30 * 24 * time.Hour

// SMTP credential errors. A refused login names the reason with one of
// them, or ErrNotFound for a secret that does not exist, ErrNotSMTP for a
// secret of an account of another kind.
var (
	ErrSMTPCredentialExpired = errors.New("the SMTP secret has expired")
	ErrSMTPCredentialRevoked = errors.New("the SMTP secret was revoked")
	ErrAccountRemoved        = errors.New("the account was removed")
	ErrSMTPGrace             = errors.New("the grace period must be 0 to 30 days")
)

// SMTPCredential is a stored secret, without the secret.
type SMTPCredential struct {
	ID        int64
	AccountID int64
	// AccountName, OwnerName and AccountRemoved are set by
	// AllSMTPCredentials; OwnerName is empty for an account without owner.
	AccountName    string
	OwnerName      string
	AccountRemoved bool
	Hint           string
	CreatedAt      time.Time
	ExpiresAt      *time.Time
	LastUsedAt     *time.Time
	RevokedAt      *time.Time
}

// State tells whether the secret works at now (apart from its account).
func (c *SMTPCredential) State(now time.Time) string {
	return smtpCredentialState(c.ExpiresAt, c.RevokedAt, now)
}

func smtpCredentialState(expires, revoked *time.Time, now time.Time) string {
	switch {
	case revoked != nil:
		return SMTPCredentialRevoked
	case expires == nil:
		return SMTPCredentialActive
	case expires.After(now):
		return SMTPCredentialExpiring
	}
	return SMTPCredentialExpired
}

// NewSMTPCredential is what CreateSMTPCredential stores.
type NewSMTPCredential struct {
	Hash      []byte
	Hint      string
	CreatedBy *int64 // nil for the CLI
	// Grace is how long the current secret stays usable, 0 to
	// MaxSMTPGrace; 0 revokes it at once.
	Grace time.Duration
	Now   time.Time
}

// SupersededSMTPCredential is an older secret that a new one changed:
// either it expires at ExpiresAt or it was revoked at RevokedAt.
type SupersededSMTPCredential struct {
	ID        int64
	ExpiresAt *time.Time
	RevokedAt *time.Time
}

// CreateSMTPCredential stores a new secret for the smtp account of ref,
// with the audit entry of ctx, and supersedes the older ones: the current
// secret expires after n.Grace (with a grace of 0 it is revoked), and a
// secret that was already expiring is revoked, so at most two stay usable.
// It fails with ErrNotFound if the account is gone or has another owner
// than ref, ErrStale if it changed, ErrAccountRemoved if it was removed,
// ErrNotSMTP for an account of another kind and ErrSMTPGrace for a grace
// out of range.
func (s *Store) CreateSMTPCredential(ctx context.Context, ref AccountRef, n NewSMTPCredential) (*SMTPCredential, []SupersededSMTPCredential, error) {
	if n.Grace < 0 || n.Grace > MaxSMTPGrace {
		return nil, nil, ErrSMTPGrace
	}
	var out *SMTPCredential
	var superseded []SupersededSMTPCredential
	err := s.inTx(ctx, func(q *db.Queries) error {
		superseded = nil
		a, err := lockAccountKeepRemoved(ctx, q, ref)
		if err != nil {
			return err
		}
		if a.RemovedAt != nil {
			return ErrAccountRemoved
		}
		if AccountKind(a.Kind) != KindSMTP {
			return ErrNotSMTP
		}
		old, err := q.LockUnrevokedSMTPCredentials(ctx, a.ID)
		if err != nil {
			return err
		}
		for _, c := range old {
			switch smtpCredentialState(c.ExpiresAt, nil, n.Now) {
			case SMTPCredentialExpired:
				continue
			case SMTPCredentialActive:
				if n.Grace > 0 {
					end := n.Now.Add(n.Grace)
					if err := q.SupersedeSMTPCredential(ctx, db.SupersedeSMTPCredentialParams{ID: c.ID, ExpiresAt: end}); err != nil {
						return err
					}
					superseded = append(superseded, SupersededSMTPCredential{ID: c.ID, ExpiresAt: &end})
					continue
				}
			}
			// Expiring already, or the current one without a grace.
			if err := q.RevokeSupersededSMTPCredential(ctx, db.RevokeSupersededSMTPCredentialParams{ID: c.ID, Now: n.Now, RevokedBy: n.CreatedBy}); err != nil {
				return err
			}
			at := n.Now
			superseded = append(superseded, SupersededSMTPCredential{ID: c.ID, RevokedAt: &at})
		}
		row, err := q.InsertSMTPCredential(ctx, db.InsertSMTPCredentialParams{
			AccountID: a.ID, SecretHash: n.Hash, Hint: n.Hint, Now: n.Now, CreatedBy: n.CreatedBy,
		})
		if err != nil {
			return err
		}
		out = &SMTPCredential{ID: row.ID, AccountID: a.ID, AccountName: a.Name, Hint: n.Hint, CreatedAt: row.CreatedAt}
		if e := auditFrom(ctx); e != nil {
			ids := make([]int64, 0, len(superseded))
			for _, c := range superseded {
				ids = append(ids, c.ID)
			}
			e.Detail("credential_id", row.ID)
			e.Detail("superseded", ids)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, superseded, nil
}

// SMTPCredentials returns an account's secrets, also revoked and expired
// ones, newest first.
func (s *Store) SMTPCredentials(ctx context.Context, accountID int64) ([]SMTPCredential, error) {
	rows, err := s.q.ListSMTPCredentials(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]SMTPCredential, 0, len(rows))
	for _, r := range rows {
		out = append(out, SMTPCredential{
			ID: r.ID, AccountID: r.AccountID, Hint: r.Hint, CreatedAt: r.CreatedAt,
			ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt, RevokedAt: r.RevokedAt,
		})
	}
	return out, nil
}

// AllSMTPCredentials returns the secrets of every smtp account, also of
// removed ones, by account name and newest first, for admins.
func (s *Store) AllSMTPCredentials(ctx context.Context) ([]SMTPCredential, error) {
	rows, err := s.q.ListAllSMTPCredentials(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SMTPCredential, 0, len(rows))
	for _, r := range rows {
		c := SMTPCredential{
			ID: r.ID, AccountID: r.AccountID, AccountName: r.AccountName, AccountRemoved: r.AccountRemovedAt != nil,
			Hint: r.Hint, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt, RevokedAt: r.RevokedAt,
		}
		if r.OwnerName != nil {
			c.OwnerName = *r.OwnerName
		}
		out = append(out, c)
	}
	return out, nil
}

// RevokeSMTPCredential revokes a secret that is not revoked yet, with the
// audit entry of ctx, which it targets at the secret's account. With
// accountID set, only a secret of that account; nil is for admins. by is
// the user who revokes it (nil for the CLI). It fails with ErrNotFound.
func (s *Store) RevokeSMTPCredential(ctx context.Context, accountID *int64, id int64, by *int64, now time.Time) (*SMTPCredential, error) {
	var out *SMTPCredential
	err := s.inTx(ctx, func(q *db.Queries) error {
		r, err := q.RevokeSMTPCredential(ctx, db.RevokeSMTPCredentialParams{ID: id, Account: accountID, RevokedBy: by, Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = &SMTPCredential{ID: r.ID, AccountID: r.AccountID, AccountName: r.AccountName, Hint: r.Hint, RevokedAt: &now}
		if e := auditFrom(ctx); e != nil {
			e.SetTarget(TargetAccount, strconv.FormatInt(r.AccountID, 10), r.AccountName)
			e.Detail("credential_id", r.ID)
			e.Detail("hint", r.Hint)
		}
		return nil
	})
	return out, err
}

// SMTPLogin is a stored secret as a login sees it.
type SMTPLogin struct {
	CredentialID int64
	AccountID    int64
	ExpiresAt    *time.Time
	RevokedAt    *time.Time
}

// smtpLoginRefusal is why a secret of an account does not log in at now,
// or nil.
func smtpLoginRefusal(kind string, removed, expires, revoked *time.Time, now time.Time) error {
	switch {
	case AccountKind(kind) != KindSMTP:
		return ErrNotSMTP
	case removed != nil:
		return ErrAccountRemoved
	}
	switch smtpCredentialState(expires, revoked, now) {
	case SMTPCredentialRevoked:
		return ErrSMTPCredentialRevoked
	case SMTPCredentialExpired:
		return ErrSMTPCredentialExpired
	}
	return nil
}

// SMTPLogin finds the secret with hash and says whether it logs in at now.
// It fails with ErrNotFound for a secret that does not exist; for one that
// exists but does not log in, it returns the login with ErrNotSMTP,
// ErrAccountRemoved, ErrSMTPCredentialRevoked or ErrSMTPCredentialExpired.
// A disabled account logs in: its mail is refused later, and stays queued.
// A successful login records the time of use, at most once a minute.
func (s *Store) SMTPLogin(ctx context.Context, hash []byte, now time.Time) (*SMTPLogin, error) {
	r, err := s.q.GetSMTPCredentialLogin(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	login := &SMTPLogin{CredentialID: r.ID, AccountID: r.AccountID, ExpiresAt: r.ExpiresAt, RevokedAt: r.RevokedAt}
	if err := smtpLoginRefusal(r.Kind, r.RemovedAt, r.ExpiresAt, r.RevokedAt, now); err != nil {
		return login, err
	}
	if r.LastUsedAt == nil || now.Sub(*r.LastUsedAt) > time.Minute {
		if err := s.q.TouchSMTPCredential(ctx, db.TouchSMTPCredentialParams{ID: r.ID, Now: now}); err != nil {
			return nil, fmt.Errorf("record the use of SMTP secret %d: %w", r.ID, err)
		}
	}
	return login, nil
}

// SMTPCredentialUsable checks with one indexed query whether the secret
// with ID id still logs in at now, as SMTPLogin would: nil, ErrNotFound for
// a secret deleted meanwhile, or the reason it no longer does.
func (s *Store) SMTPCredentialUsable(ctx context.Context, id int64, now time.Time) error {
	r, err := s.q.GetSMTPCredentialState(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return smtpLoginRefusal(r.Kind, r.RemovedAt, r.ExpiresAt, r.RevokedAt, now)
}

// SMTPCredentialCount is how many usable secrets an smtp account has.
type SMTPCredentialCount struct {
	AccountID   int64
	AccountName string
	N           int64
}

// UsableSMTPCredentialsByOwner returns, by account name, the smtp accounts
// of a user that are not removed and have secrets usable at now. A locked
// owner does not stop them (#395 decision 9): the caller hints at rotating
// them.
func (s *Store) UsableSMTPCredentialsByOwner(ctx context.Context, ownerID int64, now time.Time) ([]SMTPCredentialCount, error) {
	rows, err := s.q.CountUsableSMTPCredentialsByOwner(ctx, db.CountUsableSMTPCredentialsByOwnerParams{Owner: ownerID, Now: now})
	if err != nil {
		return nil, err
	}
	out := make([]SMTPCredentialCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, SMTPCredentialCount{AccountID: r.AccountID, AccountName: r.AccountName, N: r.N})
	}
	return out, nil
}

// UsableSMTPCredentials returns how many secrets of an account are usable
// at now.
func (s *Store) UsableSMTPCredentials(ctx context.Context, accountID int64, now time.Time) (int64, error) {
	return s.q.CountUsableSMTPCredentials(ctx, db.CountUsableSMTPCredentialsParams{AccountID: accountID, Now: now})
}

// DeleteOldSMTPCredentials deletes secrets revoked or expired more than 30
// days before now.
func (s *Store) DeleteOldSMTPCredentials(ctx context.Context, now time.Time) (int64, error) {
	return s.q.DeleteOldSMTPCredentials(ctx, now)
}
