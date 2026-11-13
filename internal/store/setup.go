package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// SetupTokenValidity is how long a setup token works, from the first start
// with it (#396, coordinator decision 9). A start with another token
// starts a new window.
const SetupTokenValidity = 7 * 24 * time.Hour

// Setup states of the instance (#396, part 3), as GET /api/session and
// InstanceStatus report them.
const (
	// SetupCLI: no user yet and no setup token; the first admin comes
	// from `user add`.
	SetupCLI = "cli"
	// SetupToken: no user yet; the setup link creates the first admin.
	SetupToken = "token"
	// SetupExpired: no user yet; the setup token's window has ended.
	SetupExpired = "expired"
	// SetupComplete: the first user exists or existed. For good: a setup
	// token never works again.
	SetupComplete = "complete"
)

// ErrSetupUnavailable means the setup with a token is not possible: setup
// is complete or no token is set. The two are not told apart.
var ErrSetupUnavailable = errors.New("setup not available")

// ErrSetupTokenInvalid means the setup token does not match the stored
// one, or its window has ended.
var ErrSetupTokenInvalid = errors.New("wrong or expired setup token")

// Setup is the state of the instance's setup.
type Setup struct {
	State string
	// CompletedAt is when the first user was created; nil before, and for
	// an instance that had users before instance_state existed but
	// none of them after.
	CompletedAt *time.Time
	// TokenSince and TokenExpires bound the window of the stored setup
	// token; nil without one.
	TokenSince, TokenExpires *time.Time
	// HasUsers: a user exists.
	HasUsers bool
}

// HashSetupToken is the stored form of a setup token: its SHA-256. The
// token itself is never stored.
func HashSetupToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// setupOf derives the setup state at now.
func setupOf(hash []byte, since, completed *time.Time, hasUsers bool, now time.Time) Setup {
	st := Setup{CompletedAt: completed, HasUsers: hasUsers}
	if hash != nil && since != nil {
		exp := since.Add(SetupTokenValidity)
		st.TokenSince, st.TokenExpires = since, &exp
	}
	switch {
	case completed != nil || hasUsers:
		st.State = SetupComplete
	case st.TokenExpires == nil:
		st.State = SetupCLI
	case !now.Before(*st.TokenExpires):
		st.State = SetupExpired
	default:
		st.State = SetupToken
	}
	return st
}

// SetupState returns the setup state at now.
func (s *Store) SetupState(ctx context.Context, now time.Time) (Setup, error) {
	return setupState(ctx, s.q, now)
}

func setupState(ctx context.Context, q *db.Queries, now time.Time) (Setup, error) {
	r, err := q.GetSetupState(ctx)
	if err != nil {
		return Setup{}, err
	}
	return setupOf(r.SetupTokenHash, r.SetupTokenSince, r.SetupCompletedAt, r.HasUsers, now), nil
}

// RecordSetupToken stores the hash of the configured setup token while
// setup is not complete (#396, start-up step 7). A token of another hash
// replaces the stored one and starts its window at now; the same token
// keeps its window. renewed reports a new window. After setup it changes
// nothing.
func (s *Store) RecordSetupToken(ctx context.Context, hash []byte, now time.Time) (renewed bool, _ Setup, err error) {
	n, err := s.q.SetSetupToken(ctx, db.SetSetupTokenParams{TokenHash: hash, Since: now})
	if err != nil {
		return false, Setup{}, err
	}
	st, err := s.SetupState(ctx, now)
	return n > 0, st, err
}

// ClearSetupToken removes a stored setup token: a start without one
// revokes it (#396, revision 1, point 3). cleared reports that one was
// stored.
func (s *Store) ClearSetupToken(ctx context.Context) (cleared bool, err error) {
	n, err := s.q.ClearSetupToken(ctx)
	return n > 0, err
}

// checkSetup decides a setup attempt with the token of hash at now. The
// hashes are compared in constant time.
func checkSetup(st Setup, stored, hash []byte, now time.Time) error {
	switch {
	case st.State == SetupComplete || stored == nil:
		return ErrSetupUnavailable
	case subtle.ConstantTimeCompare(stored, hash) != 1:
		return ErrSetupTokenInvalid
	case st.State != SetupToken || !now.Before(*st.TokenExpires):
		return ErrSetupTokenInvalid
	}
	return nil
}

// CheckSetupToken checks a setup attempt with the token of hash at now
// without changing anything: ErrSetupUnavailable, ErrSetupTokenInvalid or
// nil. POST /api/setup asks it before it hashes the password, and
// CompleteSetup decides again under the lock.
func (s *Store) CheckSetupToken(ctx context.Context, hash []byte, now time.Time) error {
	r, err := s.q.GetSetupState(ctx)
	if err != nil {
		return err
	}
	st := setupOf(r.SetupTokenHash, r.SetupTokenSince, r.SetupCompletedAt, r.HasUsers, now)
	return checkSetup(st, r.SetupTokenHash, hash, now)
}

// CompleteSetup creates the first admin with the setup token of hash
// (#396, POST /api/setup). In one transaction it locks the row of
// instance_state, requires that setup is not complete, that no user
// exists, and that the token matches and has not expired, then creates
// nu as an admin, records the setup as complete with the user and clears
// the token. It fails with ErrSetupUnavailable or ErrSetupTokenInvalid
// otherwise. The audit entry of ctx gets the new user as its target and
// its user.
func (s *Store) CompleteSetup(ctx context.Context, hash []byte, now time.Time, nu NewUser) (*User, error) {
	nu.Admin, nu.Auditor, nu.Generated, nu.GroupID = true, false, false, 0
	var u *User
	err := s.inTx(ctx, func(q *db.Queries) error {
		r, err := q.LockSetupState(ctx)
		if err != nil {
			return err
		}
		n, err := q.CountUsers(ctx)
		if err != nil {
			return err
		}
		st := setupOf(r.SetupTokenHash, r.SetupTokenSince, r.SetupCompletedAt, n > 0, now)
		if err := checkSetup(st, r.SetupTokenHash, hash, now); err != nil {
			return err
		}
		if u, err = createUser(ctx, q, nu); err != nil {
			return err
		}
		// The person with the link acts as the admin it creates.
		if e := auditFrom(ctx); e != nil {
			e.SetUser(u.ID, u.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// FreshInstance reports whether the instance is new (#396, revision 1):
// setup not complete, no user, no message, and no state of encryption at
// rest recorded. Encryption at setup decides from it.
func (s *Store) FreshInstance(ctx context.Context) (bool, error) {
	return s.q.FreshInstance(ctx)
}
