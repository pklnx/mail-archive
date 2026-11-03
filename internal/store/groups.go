package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Customer groups (#470). One shared instance serves several customers:
// every user belongs to exactly one group, and an account to its owner's
// group. Shares stay inside a group (ShareAccount, the triggers of 00034
// and the read side in ListScopeAccounts), and every audit entry records
// its group (AuditClass). The operator, the instance admin, manages the
// groups.

// ErrGroupRequired is returned when a user is created without a group
// while several groups exist.
var ErrGroupRequired = errors.New("choose a group")

// ErrUnknownGroup is returned for a group that does not exist.
var ErrUnknownGroup = errors.New("unknown group")

// ErrGroupInUse is returned when a group that still has users would be
// removed.
var ErrGroupInUse = errors.New("the group still has users")

// ErrLastGroup is returned when the only group would be removed: every
// user needs one.
var ErrLastGroup = errors.New("the last group cannot be removed")

// ErrInvalidGroupName is returned for an empty, too long or otherwise
// unusable group name.
var ErrInvalidGroupName = errors.New("a group name has 1 to 100 characters, without control characters or spaces at either end")

// ErrShareOtherGroup is returned when an account would be shared with a
// user of another group. The web layer answers it like an unknown user.
var ErrShareOtherGroup = errors.New("the user is not in the account's group")

// ErrOtherGroup is returned when an account would move to an owner of
// another group without allowOtherGroup.
var ErrOtherGroup = errors.New("the new owner is in another group")

// Group is a customer group.
type Group struct {
	ID        int64
	Name      string
	CreatedAt time.Time
	// Users and Accounts are counted by ListGroups only; Accounts includes
	// removed ones.
	Users, Accounts int64
}

func groupFromDB(r db.CustomerGroup) *Group {
	return &Group{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt}
}

// ValidateGroupName checks a group name, which must already be trimmed.
func ValidateGroupName(name string) error {
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 100 || name != strings.TrimSpace(name) || !utf8.ValidString(name) ||
		strings.ContainsFunc(name, unicode.IsControl) {
		return ErrInvalidGroupName
	}
	return nil
}

// auditGroup records a group action's group in the pending entry.
func auditGroup(ctx context.Context, g db.CustomerGroup) {
	if e := auditFrom(ctx); e != nil {
		id := g.ID
		e.GroupID = &id
		e.Detail("group_id", g.ID)
		e.Detail("group", g.Name)
	}
}

// keepAccountGroup records the group of an account that the transaction
// deletes in the pending entry, which is written after the row is gone.
func keepAccountGroup(ctx context.Context, q *db.Queries, accountID int64) error {
	e := auditFrom(ctx)
	if e == nil || e.GroupID != nil || e.class != AuditTargeted {
		return nil
	}
	g, err := q.AccountGroupID(ctx, accountID)
	if err != nil && !isNoRows(err) {
		return err
	}
	e.GroupID = g
	return nil
}

// CreateGroup adds a group. A name that exists already, regardless of
// case, is ErrConflict.
func (s *Store) CreateGroup(ctx context.Context, name string) (*Group, error) {
	if err := ValidateGroupName(name); err != nil {
		return nil, err
	}
	var g *Group
	err := s.inTx(ctx, func(q *db.Queries) error {
		r, err := q.InsertGroup(ctx, name)
		if isUniqueViolation(err) {
			return fmt.Errorf("group %q: %w", name, ErrConflict)
		}
		if err != nil {
			return err
		}
		auditGroup(ctx, r)
		g = groupFromDB(r)
		return nil
	})
	return g, err
}

// RenameGroup renames a group. ErrConflict if another group has that
// name, ErrUnknownGroup for an unknown id.
func (s *Store) RenameGroup(ctx context.Context, id int64, name string) error {
	if err := ValidateGroupName(name); err != nil {
		return err
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		old, err := q.LockGroup(ctx, id)
		if isNoRows(err) {
			return ErrUnknownGroup
		}
		if err != nil {
			return err
		}
		_, err = q.RenameGroup(ctx, db.RenameGroupParams{ID: id, Name: name})
		if isUniqueViolation(err) {
			return fmt.Errorf("group %q: %w", name, ErrConflict)
		}
		if err != nil {
			return err
		}
		auditGroup(ctx, db.CustomerGroup{ID: id, Name: name})
		if e := auditFrom(ctx); e != nil {
			e.Detail("from", old.Name)
		}
		return nil
	})
}

// DeleteGroup removes a group without users. ErrGroupInUse while it has
// users, ErrLastGroup for the only group, ErrUnknownGroup for an unknown
// id.
func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	err := s.inTx(ctx, func(q *db.Queries) error {
		g, err := q.LockGroup(ctx, id)
		if isNoRows(err) {
			return ErrUnknownGroup
		}
		if err != nil {
			return err
		}
		if n, err := q.CountGroups(ctx); err != nil {
			return err
		} else if n <= 1 {
			return ErrLastGroup
		}
		if n, err := q.CountGroupUsers(ctx, id); err != nil {
			return err
		} else if n > 0 {
			return ErrGroupInUse
		}
		auditGroup(ctx, g)
		return one(q.DeleteGroup(ctx, id))
	})
	// A user added concurrently: the foreign key refuses the delete
	// (restrict_violation on PostgreSQL 18, foreign_key_violation before).
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23001" || pgErr.Code == "23503") {
		return ErrGroupInUse
	}
	return err
}

// ListGroups returns every group with its user and account counts, by
// name.
func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.q.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Group, 0, len(rows))
	for _, r := range rows {
		out = append(out, Group{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, Users: r.Users, Accounts: r.Accounts})
	}
	return out, nil
}

// GetGroup looks up a group by id; ErrUnknownGroup if there is none.
func (s *Store) GetGroup(ctx context.Context, id int64) (*Group, error) {
	r, err := s.q.GetGroupByID(ctx, id)
	if isNoRows(err) {
		return nil, ErrUnknownGroup
	}
	if err != nil {
		return nil, err
	}
	return groupFromDB(r), nil
}

// GetGroupByName looks up a group by name, regardless of case;
// ErrUnknownGroup if there is none.
func (s *Store) GetGroupByName(ctx context.Context, name string) (*Group, error) {
	r, err := s.q.GetGroupByName(ctx, strings.TrimSpace(name))
	if isNoRows(err) {
		return nil, ErrUnknownGroup
	}
	if err != nil {
		return nil, err
	}
	return groupFromDB(r), nil
}

// EndedShare is a share a group change ended.
type EndedShare struct {
	Account string `json:"account"`
	Owner   string `json:"owner"`
	User    string `json:"user"`
}

// GroupChange is what SetUserGroup did.
type GroupChange struct {
	// Changed is false when the user already was in the group: nothing
	// happened and no entry was written.
	Changed  bool
	From, To string
	Ended    []EndedShare
}

// SetUserGroup moves a user into another group. In the same transaction it
// ends every share that would then cross groups: the user's accounts
// shared with users of the old group and old-group accounts shared with
// the user. The pending entry (user.group_change) gets from_group,
// to_group and shares_ended, and is recorded under the new group.
func (s *Store) SetUserGroup(ctx context.Context, userID, groupID int64) (GroupChange, error) {
	var c GroupChange
	err := s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUser(ctx, q, userID)
		if err != nil {
			return err
		}
		from, err := q.GetGroupByID(ctx, u.GroupID)
		if err != nil {
			return err
		}
		to, err := q.GetGroupByID(ctx, groupID)
		if isNoRows(err) {
			return ErrUnknownGroup
		}
		if err != nil {
			return err
		}
		c = GroupChange{From: from.Name, To: to.Name, Ended: []EndedShare{}}
		if u.GroupID == groupID {
			skipAudit(ctx)
			return nil
		}
		_, err = q.SetUserGroup(ctx, db.SetUserGroupParams{ID: userID, GroupID: groupID})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" { // removed concurrently
			return ErrUnknownGroup
		}
		if err != nil {
			return err
		}
		rows, err := q.DeleteCrossingShares(ctx, userID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			c.Ended = append(c.Ended, EndedShare{Account: r.Account, Owner: r.Owner, User: r.UserName})
		}
		c.Changed = true
		if e := auditFrom(ctx); e != nil {
			e.GroupID = &groupID
			e.Detail("from_group", map[string]any{"id": from.ID, "name": from.Name})
			e.Detail("to_group", map[string]any{"id": to.ID, "name": to.Name})
			e.Detail("shares_ended", c.Ended)
		}
		return nil
	})
	if err != nil {
		return GroupChange{}, err
	}
	return c, nil
}
