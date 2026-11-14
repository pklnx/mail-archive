package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/keepia/archive/internal/store/db"
)

// Auditor logins and the grant views of the web layer (#393, part 2).

// ErrNoActiveGrant is returned for a correct login of an auditor who has
// no active grant: an auditor cannot log in without one.
var ErrNoActiveGrant = errors.New("no active auditor grant")

// NoGrantError is ErrNoActiveGrant with the auditor, for the entry of the
// refused login.
type NoGrantError struct {
	UserID   int64
	UserName string
}

func (e *NoGrantError) Error() string { return ErrNoActiveGrant.Error() }

// Is makes errors.Is(err, ErrNoActiveGrant) hold.
func (e *NoGrantError) Is(target error) bool { return target == ErrNoActiveGrant }

// createLoginSession creates the session of a completed login. An
// auditor's login needs an active grant (active_auditor_grants): without
// one it fails with a NoGrantError and the transaction rolls back; with
// one the session is bound to it and ends with it at the latest
// (SetSessionGrant); with several it stays unbound until the auditor picks
// one. The pending entry (auth.login) of an auditor gets the bound grant
// and its owner, or, while unbound, the owners of all active grants, so
// that each owner's Log shows the login.
func createLoginSession(ctx context.Context, q *db.Queries, userID int64, auditor bool, userName string, hash []byte, expiresAt time.Time, userAgent string) error {
	var grants []db.ListActiveGrantsRow
	if auditor {
		var err error
		if grants, err = q.ListActiveGrants(ctx, userID); err != nil {
			return err
		}
		if len(grants) == 0 {
			return &NoGrantError{UserID: userID, UserName: userName}
		}
	}
	if err := q.CreateSession(ctx, db.CreateSessionParams{ID: hash, UserID: userID, ExpiresAt: expiresAt, UserAgent: userAgent}); err != nil {
		return err
	}
	if !auditor {
		return nil
	}
	e := auditFrom(ctx)
	if len(grants) == 1 {
		err := one(q.SetSessionGrant(ctx, db.SetSessionGrantParams{ID: hash, AuditorID: userID, GrantID: grants[0].ID}))
		if errors.Is(err, ErrNotFound) { // ended since it was listed
			return &NoGrantError{UserID: userID, UserName: userName}
		}
		if err != nil {
			return err
		}
		if e != nil {
			e.Detail(DetailAuditorGrant, grants[0].ID)
			e.Detail(DetailViaOwners, []int64{grants[0].OwnerID})
		}
		return nil
	}
	if e != nil {
		e.Detail(DetailViaOwners, GrantOwners(grantsOf(grants)))
	}
	return nil
}

func grantsOf(rows []db.ListActiveGrantsRow) []AuditorGrant {
	out := make([]AuditorGrant, 0, len(rows))
	for _, r := range rows {
		out = append(out, AuditorGrant{ID: r.ID, OwnerID: r.OwnerID})
	}
	return out
}

// GrantOwners returns the owners of the grants, sorted, each once: the
// via_owners of an auditor's entry that is about no single grant.
func GrantOwners(grants []AuditorGrant) []int64 {
	owners := []int64{}
	for _, g := range grants {
		if !slices.Contains(owners, g.OwnerID) {
			owners = append(owners, g.OwnerID)
		}
	}
	slices.Sort(owners)
	return owners
}

// OwnerGrant returns one grant of an owner, as in OwnerGrants;
// ErrNotFound for an unknown grant and for one of another owner alike.
func (s *Store) OwnerGrant(ctx context.Context, id, ownerID int64) (*AuditorGrant, error) {
	grants, err := s.OwnerGrants(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	for i := range grants {
		if grants[i].ID == id {
			return &grants[i], nil
		}
	}
	return nil, ErrNotFound
}

// GrantOwner returns the owner of a grant, ErrNotFound for an unknown
// one. Only for admins: it tells that a grant exists.
func (s *Store) GrantOwner(ctx context.Context, id int64) (int64, error) {
	owner, err := s.q.GrantOwner(ctx, id)
	if isNoRows(err) {
		return 0, ErrNotFound
	}
	return owner, err
}

// GrantSummary is a grant as the command line lists it.
type GrantSummary struct {
	ID               int64
	Label            string
	Owner, Group     string
	Auditor          string // empty once the auditor was removed
	StartsAt, EndsAt time.Time
	RevokedAt        *time.Time
	RevokeReason     string
	CreatedAt        time.Time
	Active           bool
}

// AllGrants lists every grant with its history, newest first.
func (s *Store) AllGrants(ctx context.Context) ([]GrantSummary, error) {
	rows, err := s.q.ListAllGrants(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]GrantSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, GrantSummary{
			ID: r.ID, Label: r.Label, Owner: r.OwnerName, Group: r.GroupName, Auditor: deref(r.AuditorName),
			StartsAt: r.StartsAt, EndsAt: r.EndsAt, RevokedAt: r.RevokedAt, RevokeReason: deref(r.RevokeReason),
			CreatedAt: r.CreatedAt, Active: r.Active,
		})
	}
	return out, nil
}
