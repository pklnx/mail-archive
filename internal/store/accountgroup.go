package store

import (
	"context"
	"fmt"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// The customer group of a save's account (#539, part 1). lockAccountGroup
// and its queries are those of #397, part 1 (storage usage); whichever of
// the two lands second keeps one copy.

// accountGroup is the account a save writes into and the customer group
// of its owner, locked by lockAccountGroup until the save commits. Group
// is nil for an account without an owner ("Without group").
type accountGroup struct {
	Account int64
	Group   *int64
}

// key is the group's key in deletion marks and group lookups: the group's
// id, or 0 for the accounts without an owner (group ids start at 1).
func (g accountGroup) key() int64 {
	if g.Group == nil {
		return 0
	}
	return *g.Group
}

// lockTarget names the account of a save: by one of its folders or
// directly.
type lockTarget struct {
	folder, account int64
}

func byFolder(id int64) lockTarget  { return lockTarget{folder: id} }
func byAccount(id int64) lockTarget { return lockTarget{account: id} }

// lockAccountGroup is the first step of every save that adds locations,
// before the folder's lock: it locks the account and then its owner FOR
// KEY SHARE and returns the owner's group. KEY SHARE conflicts only with
// FOR UPDATE, DELETE and key updates, which is what the moves take
// (SetUserGroup: LockUser; SetAccountOwner: LockAccount; the adoption:
// LockUnownedAccounts), not with ordinary updates of either row. So a move
// waits until the open saves of the owner or account end, and a save that
// starts during a move waits for it and reads the new group (READ
// COMMITTED reads the new row version after the wait).
//
// The owner is locked in a second statement: PostgreSQL refuses a row lock
// on the nullable side of an outer join.
func lockAccountGroup(ctx context.Context, q *db.Queries, t lockTarget) (accountGroup, error) {
	var (
		g     accountGroup
		owner *int64
	)
	if t.folder != 0 {
		r, err := q.LockFolderAccountGroup(ctx, t.folder)
		if isNoRows(err) {
			return g, fmt.Errorf("folder %d: %w", t.folder, ErrNotFound)
		}
		if err != nil {
			return g, fmt.Errorf("lock account: %w", err)
		}
		g.Account, owner = r.ID, r.OwnerID
	} else {
		r, err := q.LockAccountGroup(ctx, t.account)
		if isNoRows(err) {
			return g, fmt.Errorf("account %d: %w", t.account, ErrNotFound)
		}
		if err != nil {
			return g, fmt.Errorf("lock account: %w", err)
		}
		g.Account, owner = r.ID, r.OwnerID
	}
	if owner == nil {
		return g, nil
	}
	group, err := q.LockOwnerGroup(ctx, *owner)
	if err != nil {
		return g, fmt.Errorf("lock owner: %w", err)
	}
	g.Group = &group
	return g, nil
}

// groupHasLocations returns the hashes of shas that an account of the
// group holds, the saving account included: one probe per folder of the
// group and hash (GroupHasLocations), so its cost never depends on other
// groups.
func groupHasLocations(ctx context.Context, q *db.Queries, g accountGroup, shas []string) (map[string]bool, error) {
	held := make(map[string]bool, len(shas))
	if len(shas) == 0 {
		return held, nil
	}
	rows, err := q.GroupHasLocations(ctx, db.GroupHasLocationsParams{Shas: shas, GroupID: g.key()})
	if err != nil {
		return nil, fmt.Errorf("group has locations: %w", err)
	}
	for _, sha := range rows {
		held[sha] = true
	}
	return held, nil
}
