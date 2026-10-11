package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// API keys let scripts use the API. The store keeps the SHA-256 of a key
// and the last characters as a hint; the key itself is shown once.

// MaxAPIKeys is the number of keys a user can have that are neither
// revoked nor expired.
const MaxAPIKeys = 20

// API key scopes.
const (
	ScopeRead = "read"
	ScopeSync = "sync"
)

// APIKeyScopes are all scopes, in the order they are stored.
var APIKeyScopes = []string{ScopeRead, ScopeSync}

// API key errors.
var (
	ErrAPIKeyLimit = errors.New("this user has the maximum number of API keys")
	ErrAPIKeyName  = errors.New("an API key with this name exists")
	ErrAPIKeyScope = errors.New("unknown API key scope")
)

// API key states.
const (
	APIKeyActive  = "active"
	APIKeyExpired = "expired"
	APIKeyRevoked = "revoked"
)

// APIKey is a stored key, without the key.
type APIKey struct {
	ID         int64
	UserID     int64
	UserName   string
	Name       string
	Hint       string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// State tells whether the key works (apart from its owner being locked).
func (k *APIKey) State(now time.Time) string {
	switch {
	case k.RevokedAt != nil:
		return APIKeyRevoked
	case k.ExpiresAt != nil && !k.ExpiresAt.After(now):
		return APIKeyExpired
	}
	return APIKeyActive
}

// HasScope reports whether the key has scope.
func (k *APIKey) HasScope(scope string) bool { return slices.Contains(k.Scopes, scope) }

// NormalizeScopes removes duplicates and sorts scopes in their stored order.
// It fails with ErrAPIKeyScope for an unknown one or none.
func NormalizeScopes(scopes []string) ([]string, error) {
	out := make([]string, 0, len(APIKeyScopes))
	for _, s := range scopes {
		if !slices.Contains(APIKeyScopes, s) {
			return nil, ErrAPIKeyScope
		}
	}
	for _, s := range APIKeyScopes {
		if slices.Contains(scopes, s) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, ErrAPIKeyScope
	}
	return out, nil
}

// NewAPIKey is what CreateAPIKey stores.
type NewAPIKey struct {
	Hash      []byte
	Hint      string
	Name      string
	Scopes    []string // normalized by NormalizeScopes
	ExpiresAt *time.Time
}

// CreateAPIKey stores a key for a user, with the audit entry of ctx. It
// fails with ErrAPIKeyLimit when the user has MaxAPIKeys active keys and
// with ErrAPIKeyName when an active or expired key has the name.
func (s *Store) CreateAPIKey(ctx context.Context, userID int64, k NewAPIKey) (*APIKey, error) {
	var out *APIKey
	err := s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUser(ctx, q, userID)
		if err != nil {
			return err
		}
		n, err := q.CountActiveAPIKeys(ctx, userID)
		if err != nil {
			return err
		}
		if n >= MaxAPIKeys {
			return ErrAPIKeyLimit
		}
		row, err := q.InsertAPIKey(ctx, db.InsertAPIKeyParams{
			UserID: userID, KeyHash: k.Hash, Hint: k.Hint, Name: k.Name, Scopes: k.Scopes, ExpiresAt: k.ExpiresAt,
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "api_keys_user_name_idx" {
			return ErrAPIKeyName
		}
		if err != nil {
			return err
		}
		out = &APIKey{
			ID: row.ID, UserID: userID, UserName: u.Name, Name: k.Name, Hint: k.Hint, Scopes: k.Scopes,
			CreatedAt: row.CreatedAt, ExpiresAt: k.ExpiresAt,
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("key_id", row.ID)
		}
		return nil
	})
	return out, err
}

// APIKeyLogin finds the key with hash and returns its owner's state as a
// Session, like GetSession, and the key. It fails with ErrNotFound when
// the key does not exist, is revoked or expired, or its owner is locked.
func (s *Store) APIKeyLogin(ctx context.Context, hash []byte) (*Session, *APIKey, error) {
	r, err := s.q.GetAPIKeyLogin(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	sess := &Session{
		UserID: r.UserID, UserName: r.UserName, IsAdmin: r.IsAdmin,
		MustChangePassword: r.MustChangePassword, TwoFactorEnabled: r.TwoFactorEnabled,
	}
	key := &APIKey{ID: r.ID, UserID: r.UserID, UserName: r.UserName, Name: r.Name, Scopes: r.Scopes, LastUsedAt: r.LastUsedAt}
	return sess, key, nil
}

// RefusedAPIKey describes a stored key that APIKeyLogin refused, for the
// audit entry of the attempt.
type RefusedAPIKey struct {
	ID       int64
	UserID   int64
	UserName string
	Name     string
	Reason   string // revoked, expired or locked
}

// FindRefusedAPIKey looks up a key that APIKeyLogin did not accept. It
// fails with ErrNotFound for a key that does not exist.
func (s *Store) FindRefusedAPIKey(ctx context.Context, hash []byte) (*RefusedAPIKey, error) {
	r, err := s.q.GetAPIKeyByHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &RefusedAPIKey{ID: r.ID, UserID: r.UserID, UserName: r.UserName, Name: r.Name, Reason: "locked"}
	switch {
	case r.RevokedAt != nil:
		out.Reason = APIKeyRevoked
	case r.ExpiresAt != nil && !r.ExpiresAt.After(time.Now()):
		out.Reason = APIKeyExpired
	}
	return out, nil
}

// TouchAPIKey records that a key was used now, at most once a minute.
func (s *Store) TouchAPIKey(ctx context.Context, id int64) error {
	return s.q.TouchAPIKey(ctx, id)
}

// ListAPIKeys returns a user's keys, also revoked and expired ones, newest
// first.
func (s *Store) ListAPIKeys(ctx context.Context, userID int64) ([]APIKey, error) {
	rows, err := s.q.ListUserAPIKeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]APIKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, APIKey{
			ID: r.ID, UserID: userID, Name: r.Name, Hint: r.Hint, Scopes: r.Scopes, CreatedAt: r.CreatedAt,
			ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt, RevokedAt: r.RevokedAt,
		})
	}
	return out, nil
}

// ListAllAPIKeys returns every user's keys, by user name and newest first.
func (s *Store) ListAllAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.q.ListAllAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]APIKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, APIKey{
			ID: r.ID, UserID: r.UserID, UserName: r.UserName, Name: r.Name, Hint: r.Hint, Scopes: r.Scopes,
			CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt, RevokedAt: r.RevokedAt,
		})
	}
	return out, nil
}

// APIKeyCounts returns the number of active keys per user ID.
func (s *Store) APIKeyCounts(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.q.CountAPIKeysByUser(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		out[r.UserID] = r.N
	}
	return out, nil
}

// RevokeAPIKey revokes a key that is not revoked yet, with the audit entry
// of ctx. With owner set, only that user's key; nil is for admins. by is
// the user who revokes it (nil for the CLI). It fails with ErrNotFound.
func (s *Store) RevokeAPIKey(ctx context.Context, id int64, owner, by *int64) (*APIKey, error) {
	var out *APIKey
	err := s.inTx(ctx, func(q *db.Queries) error {
		r, err := q.RevokeAPIKey(ctx, db.RevokeAPIKeyParams{ID: id, Owner: owner, RevokedBy: by})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = &APIKey{ID: r.ID, UserID: r.UserID, UserName: r.UserName, Name: r.Name}
		if e := auditFrom(ctx); e != nil {
			e.SetTarget(TargetUser, strconv.FormatInt(r.UserID, 10), r.UserName)
			e.Detail("key_id", r.ID)
			e.Detail("key_name", r.Name)
		}
		return nil
	})
	return out, err
}

// RevokeUserAPIKeys revokes all of a user's keys, with the audit entry of
// ctx, and returns how many were revoked.
func (s *Store) RevokeUserAPIKeys(ctx context.Context, userID int64, by *int64) (int64, error) {
	var n int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		var err error
		n, err = revokeUserAPIKeys(ctx, q, userID, by)
		return err
	})
	return n, err
}

func revokeUserAPIKeys(ctx context.Context, q *db.Queries, userID int64, by *int64) (int64, error) {
	n, err := q.RevokeUserAPIKeys(ctx, db.RevokeUserAPIKeysParams{UserID: userID, RevokedBy: by})
	if err == nil {
		if e := auditFrom(ctx); e != nil {
			e.Detail("api_keys_revoked", n)
		}
	}
	return n, err
}

// DeleteOldAPIKeys deletes keys revoked or expired more than 30 days ago.
func (s *Store) DeleteOldAPIKeys(ctx context.Context) (int64, error) {
	return s.q.DeleteOldAPIKeys(ctx)
}
