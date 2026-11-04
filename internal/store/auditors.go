package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Auditor access (#393). An auditor is a user with a fixed role (a tax
// advisor or tax auditor) who reads only through a grant: one owner's
// accounts, all or some of their folders, an optional message period and a
// validity window. An auditor never owns accounts, never receives shares,
// holds no API keys and is never an admin. Owner and auditor are in the
// same customer group while a grant is unrevoked (maintainer decision M3);
// the grant's group is the owner's. The database holds the same rules
// (00036), and a grant is active only through the view
// active_auditor_grants, its accounts only through
// active_auditor_grant_accounts.
//
// Lock order on every path: accounts, then users, then grant rows. So a
// grant change and a group move or a user removal wait for each other and
// never deadlock.

// ErrFirstUserAuditor is returned when the first user would be an auditor.
var ErrFirstUserAuditor = errors.New("the first user cannot be an auditor")

// ErrAuditorAdmin is returned when a user would be both admin and auditor.
var ErrAuditorAdmin = errors.New("an auditor cannot be an admin")

// ErrShareAuditor is returned when an account would be shared with an
// auditor: auditors read only through grants.
var ErrShareAuditor = errors.New("accounts cannot be shared with an auditor")

// ErrAuditorKey is returned when an API key would be created for an
// auditor.
var ErrAuditorKey = errors.New("auditors cannot hold API keys")

// ErrAuditorAccount is returned when an auditor would own an account.
var ErrAuditorAccount = errors.New("an auditor cannot own accounts")

// ErrGrantSMTP is returned when an smtp (journal) account would be granted
// (maintainer decision M1).
var ErrGrantSMTP = errors.New("smtp accounts cannot be granted")

// ErrGrantOtherGroup is returned when the auditor and the owner of a grant
// are not in the same customer group. The web layer answers it like an
// unknown auditor.
var ErrGrantOtherGroup = errors.New("the auditor is not in the owner's group")

// ErrGrantNotAuditor is returned when the grant's auditor is not an
// auditor, or its owner is one. The web layer answers it like an unknown
// auditor.
var ErrGrantNotAuditor = errors.New("not an auditor")

// ErrGrantRevoked is returned for a change of a revoked grant: a
// revocation is final.
var ErrGrantRevoked = errors.New("the grant was revoked")

// ErrGrantInvalid wraps every refused grant field: label, accounts,
// folders, period or window.
var ErrGrantInvalid = errors.New("invalid grant")

// MaxGrantDays is the longest validity window of a grant, from its start,
// and of an extension, from the time of the extension (decision 6).
const MaxGrantDays = 365

// The reasons a grant was revoked.
const (
	RevokeOwner        = "owner"
	RevokeOperator     = "operator"
	RevokeGroupChange  = "group_change"
	RevokeAccountMove  = "account_move"
	RevokeAccountPurge = "account_purge"
	RevokeUserRemoved  = "user_removed"
)

// The audit actions of auditor access (classes in auditActionClasses).
// Grant changes are about the auditor and recorded in the grant's group;
// the auditor's own steps (picking a grant, a denied route, listing the
// granted accounts) in the auditor's group.
const (
	ActionAuditorGrant       = "auditor.grant"
	ActionAuditorExtend      = "auditor.extend"
	ActionAuditorRevoke      = "auditor.revoke"
	ActionAuditorGrantChange = "auditor.grant_change"
	ActionAuditorSelect      = "auditor.select"
	ActionAuditorDenied      = "auditor.denied"
	ActionAccountList        = "account.list"
)

// DetailAuditorGrant is the detail that names a grant in an audit entry.
const DetailAuditorGrant = "auditor_grant"

// GrantAccount is one account of a new grant: all its folders (Folders
// nil) or the folders with these IDs (an empty list grants none).
type GrantAccount struct {
	AccountID int64
	Folders   []int64
}

// NewGrant is a grant to create. StartsAt nil starts it now.
type NewGrant struct {
	OwnerID, AuditorID      int64
	Label                   string
	Accounts                []GrantAccount
	PeriodFrom, PeriodUntil *time.Time
	StartsAt                *time.Time
	EndsAt                  time.Time
}

// AuditorGrant is a grant as its owner or its auditor sees it.
type AuditorGrant struct {
	ID      int64
	OwnerID int64
	Owner   string
	// AuditorID and Auditor are nil and empty in the owner's list when the
	// auditor left the owner's group or was removed.
	AuditorID               *int64
	Auditor                 string
	Label                   string
	PeriodFrom, PeriodUntil *time.Time
	StartsAt, EndsAt        time.Time
	RevokedAt               *time.Time
	RevokeReason            string
	CreatedAt               time.Time
	// Active: the grant opens its scope right now (active_auditor_grants).
	Active bool
	// Accounts are those the grant still names; in an auditor's list only
	// those of an active grant (coordinator decision 31).
	Accounts []AuditorGrantAccount
}

// AuditorGrantAccount is a granted account with its granted folder names;
// Folders is nil for all folders.
type AuditorGrantAccount struct {
	ID      int64
	Name    string
	Folders []string
}

// actorID is the user of the pending audit entry: who acts, recorded as
// the revoker. Nil for the CLI and without an entry.
func actorID(ctx context.Context) *int64 {
	if e := auditFrom(ctx); e != nil && e.Actor != ActorCLI {
		return e.UserID
	}
	return nil
}

// grantEvent writes one auditor.* entry about a grant in the transaction of
// q, besides the pending entry: in the grant's group, with the grant and
// its owner, so that it reaches the owner's Log (via_owners). The actor is
// that of the pending entry, the CLI without one.
func grantEvent(ctx context.Context, q *db.Queries, action string, grantID, ownerID int64, auditorID *int64, auditorName string, groupID int64, details map[string]any) error {
	e := TargetedEntry(ActorCLI, action)
	if p := auditFrom(ctx); p != nil {
		e.Actor, e.UserID, e.UserName, e.ClientIP = p.Actor, p.UserID, p.UserName, p.ClientIP
		e.APIKeyID, e.APIKeyName = p.APIKeyID, p.APIKeyName
	}
	if auditorID != nil {
		e.SetTarget(TargetUser, strconv.FormatInt(*auditorID, 10), auditorName)
	}
	g := groupID
	e.GroupID = &g
	e.Detail(DetailAuditorGrant, grantID)
	e.Detail(DetailViaOwners, []int64{ownerID})
	for k, v := range details {
		e.Detail(k, v)
	}
	if err := insertAudit(ctx, q, e, nil); err != nil {
		return fmt.Errorf("%w: %w", ErrAuditUnavailable, err)
	}
	return nil
}

// grantTarget completes the pending entry of a grant action: the auditor
// as target, the grant's group, the grant and its owner.
func grantTarget(ctx context.Context, grantID, ownerID int64, auditorID *int64, auditorName string, groupID int64) {
	e := auditFrom(ctx)
	if e == nil {
		return
	}
	if auditorID != nil {
		e.SetTarget(TargetUser, strconv.FormatInt(*auditorID, 10), auditorName)
	}
	g := groupID
	e.GroupID = &g
	e.Detail(DetailAuditorGrant, grantID)
	e.Detail(DetailViaOwners, []int64{ownerID})
}

func validGrantLabel(label string) bool {
	n := utf8.RuneCountInString(label)
	return n >= 1 && n <= 200 && label == strings.TrimSpace(label) && utf8.ValidString(label)
}

// CreateGrant stores a grant of the owner's accounts to an auditor of the
// owner's group and returns its ID. It locks the accounts (FOR SHARE, in id
// order), then the owner's and the auditor's rows (FOR SHARE), so that
// neither changes group until the commit. It fails with ErrNotFound for an
// unknown auditor or an account the owner does not own, with ErrGrantSMTP
// for an smtp account, with ErrGrantOtherGroup when the auditor is in
// another group, with ErrGrantNotAuditor when the auditor is no auditor
// (or the owner one), and with ErrGrantInvalid for a bad label, folder,
// period or window. The pending entry (auditor.grant) gets the full scope.
func (s *Store) CreateGrant(ctx context.Context, g NewGrant) (int64, error) {
	start := time.Now()
	if g.StartsAt != nil {
		start = *g.StartsAt
	}
	switch {
	case !validGrantLabel(g.Label):
		return 0, fmt.Errorf("%w: a label has 1 to 200 characters, without spaces at either end", ErrGrantInvalid)
	case len(g.Accounts) == 0:
		return 0, fmt.Errorf("%w: no account", ErrGrantInvalid)
	case !g.EndsAt.After(start) || !g.EndsAt.After(time.Now()):
		return 0, fmt.Errorf("%w: the grant must end after its start and in the future", ErrGrantInvalid)
	case g.EndsAt.After(start.AddDate(0, 0, MaxGrantDays)):
		return 0, fmt.Errorf("%w: a grant lasts at most %d days", ErrGrantInvalid, MaxGrantDays)
	case g.PeriodFrom != nil && g.PeriodUntil != nil && !g.PeriodUntil.After(*g.PeriodFrom):
		return 0, fmt.Errorf("%w: the period ends before it starts", ErrGrantInvalid)
	case g.AuditorID == g.OwnerID:
		return 0, ErrGrantNotAuditor
	}
	ids := make([]int64, 0, len(g.Accounts))
	for _, a := range g.Accounts {
		if slices.Contains(ids, a.AccountID) {
			return 0, fmt.Errorf("%w: account %d named twice", ErrGrantInvalid, a.AccountID)
		}
		ids = append(ids, a.AccountID)
	}
	var id int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		accounts, err := q.LockAccountsForGrant(ctx, ids)
		if err != nil {
			return err
		}
		names := map[int64]string{}
		for _, a := range accounts {
			if a.OwnerID == nil || *a.OwnerID != g.OwnerID {
				continue
			}
			if AccountKind(a.Kind) == KindSMTP {
				return ErrGrantSMTP
			}
			names[a.ID] = a.Name
		}
		if len(names) != len(ids) {
			return fmt.Errorf("account: %w", ErrNotFound)
		}
		for _, a := range g.Accounts {
			if a.Folders == nil {
				continue
			}
			found, err := q.AccountFolderIDs(ctx, db.AccountFolderIDsParams{AccountID: a.AccountID, Ids: a.Folders})
			if err != nil {
				return err
			}
			if len(found) != len(slices.Compact(slices.Sorted(slices.Values(a.Folders)))) {
				return fmt.Errorf("%w: a folder is not one of account %d", ErrGrantInvalid, a.AccountID)
			}
		}
		owner, auditor, err := lockGrantUsers(ctx, q, g.OwnerID, g.AuditorID)
		if err != nil {
			return err
		}
		auditorID := g.AuditorID
		id, err = q.InsertGrant(ctx, db.InsertGrantParams{
			AuditorID: &auditorID, OwnerID: g.OwnerID, Label: g.Label,
			PeriodFrom: g.PeriodFrom, PeriodUntil: g.PeriodUntil, StartsAt: start, EndsAt: g.EndsAt,
		})
		if err != nil {
			return err
		}
		scope := make([]map[string]any, 0, len(g.Accounts))
		for _, a := range g.Accounts {
			if err := q.InsertGrantAccount(ctx, db.InsertGrantAccountParams{GrantID: id, AccountID: a.AccountID, AllFolders: a.Folders == nil}); err != nil {
				return err
			}
			for _, f := range slices.Compact(slices.Sorted(slices.Values(a.Folders))) {
				if err := q.InsertGrantFolder(ctx, db.InsertGrantFolderParams{GrantID: id, AccountID: a.AccountID, FolderID: f}); err != nil {
					return err
				}
			}
			entry := map[string]any{"id": a.AccountID, "name": names[a.AccountID], "all_folders": a.Folders == nil}
			if a.Folders != nil {
				entry["folders"] = slices.Compact(slices.Sorted(slices.Values(a.Folders)))
			}
			scope = append(scope, entry)
		}
		grantTarget(ctx, id, g.OwnerID, &auditorID, auditor.name, owner.group)
		if e := auditFrom(ctx); e != nil {
			e.Detail("label", g.Label)
			e.Detail("accounts", scope)
			e.Detail("period_from", g.PeriodFrom)
			e.Detail("period_until", g.PeriodUntil)
			e.Detail("starts_at", start)
			e.Detail("ends_at", g.EndsAt)
		}
		return nil
	})
	return id, err
}

type grantUser struct {
	group int64
	name  string
}

// lockGrantUsers locks the owner's and the auditor's rows FOR SHARE (in id
// order) and checks them: ErrNotFound for a missing user,
// ErrGrantOtherGroup for different groups, ErrGrantNotAuditor for the
// wrong roles.
func lockGrantUsers(ctx context.Context, q *db.Queries, ownerID, auditorID int64) (owner, auditor grantUser, err error) {
	rows, err := q.LockUserGroups(ctx, []int64{ownerID, auditorID})
	if err != nil {
		return owner, auditor, err
	}
	var o, a *db.LockUserGroupsRow
	for i := range rows {
		switch rows[i].ID {
		case ownerID:
			o = &rows[i]
		case auditorID:
			a = &rows[i]
		}
	}
	if o == nil || a == nil {
		return owner, auditor, fmt.Errorf("auditor: %w", ErrNotFound)
	}
	if o.GroupID != a.GroupID {
		return owner, auditor, ErrGrantOtherGroup
	}
	if !a.IsAuditor || o.IsAuditor {
		return owner, auditor, ErrGrantNotAuditor
	}
	u, err := q.GetUserByID(ctx, auditorID)
	if err != nil {
		return owner, auditor, err
	}
	return grantUser{group: o.GroupID}, grantUser{group: a.GroupID, name: u.Name}, nil
}

// ExtendGrant moves the end of an owner's unrevoked grant to endsAt, which
// must be later than the current end and at most MaxGrantDays from now. It
// takes its locks in the order of a group move: it reads the grant's
// owner and auditor (which never change) without a lock, locks both users
// FOR SHARE, then the grant FOR UPDATE, and checks again. ErrNotFound for
// an unknown grant or one of another owner, ErrGrantRevoked for a revoked
// one, ErrGrantOtherGroup or ErrGrantNotAuditor when its users no longer
// fit, ErrGrantInvalid for the date.
func (s *Store) ExtendGrant(ctx context.Context, id, ownerID int64, endsAt time.Time) error {
	if endsAt.After(time.Now().AddDate(0, 0, MaxGrantDays)) {
		return fmt.Errorf("%w: a grant ends at most %d days from now", ErrGrantInvalid, MaxGrantDays)
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		users, err := q.GetGrantUsers(ctx, db.GetGrantUsersParams{ID: id, OwnerID: ownerID})
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if users.AuditorID == nil { // removed with the auditor: revoked
			return ErrGrantRevoked
		}
		// A move or removal that committed meanwhile revoked the grant:
		// report that rather than what it left of the users.
		owner, auditor, uerr := lockGrantUsers(ctx, q, users.OwnerID, *users.AuditorID)
		if uerr != nil && !errors.Is(uerr, ErrNotFound) && !errors.Is(uerr, ErrGrantOtherGroup) && !errors.Is(uerr, ErrGrantNotAuditor) {
			return uerr
		}
		g, err := q.LockGrant(ctx, id)
		if err != nil {
			return err
		}
		if g.RevokedAt != nil {
			return ErrGrantRevoked
		}
		if uerr != nil {
			return uerr
		}
		if !endsAt.After(g.EndsAt) {
			return fmt.Errorf("%w: an extension ends after the current end", ErrGrantInvalid)
		}
		if err := q.SetGrantEnd(ctx, db.SetGrantEndParams{ID: id, EndsAt: endsAt}); err != nil {
			return err
		}
		grantTarget(ctx, id, ownerID, users.AuditorID, auditor.name, owner.group)
		if e := auditFrom(ctx); e != nil {
			e.Detail("label", g.Label)
			e.Detail("from", g.EndsAt)
			e.Detail("to", endsAt)
		}
		return nil
	})
}

// RevokeGrant ends a grant at once and deletes the sessions bound to it.
// With owner set, only that owner's grant (reason "owner"); nil is for
// admins and the CLI (reason "operator"). by is recorded as the revoker
// (nil for the CLI). ErrNotFound for an unknown grant or one of another
// owner, ErrGrantRevoked if it is revoked already. The pending entry
// (auditor.revoke) is recorded in the grant's group.
func (s *Store) RevokeGrant(ctx context.Context, id int64, owner, by *int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		g, err := q.LockGrant(ctx, id)
		if isNoRows(err) || err == nil && owner != nil && g.OwnerID != *owner {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if g.RevokedAt != nil {
			return ErrGrantRevoked
		}
		reason := RevokeOperator
		if owner != nil {
			reason = RevokeOwner
		}
		if _, err := q.RevokeGrants(ctx, db.RevokeGrantsParams{RevokedBy: by, Reason: reason, Ids: []int64{id}}); err != nil {
			return err
		}
		if err := q.DeleteGrantSessions(ctx, []int64{id}); err != nil {
			return err
		}
		grantTarget(ctx, id, g.OwnerID, g.AuditorID, deref(g.AuditorName), g.OwnerGroup)
		if e := auditFrom(ctx); e != nil {
			e.Detail("label", g.Label)
			e.Detail("reason", reason)
		}
		return nil
	})
}

// RevokedGrant is a grant that a change revoked.
type RevokedGrant struct {
	ID      int64  `json:"id"`
	Label   string `json:"label"`
	Owner   string `json:"owner"`
	Auditor string `json:"auditor"`
}

// revokeCrossingGrants revokes the grants of a user who just changed group
// that now cross groups, deletes their sessions (all sessions of a moved
// auditor) and writes one auditor.revoke entry per grant in the group the
// grant was in. The caller holds the user's row FOR UPDATE.
func revokeCrossingGrants(ctx context.Context, q *db.Queries, user db.User) ([]RevokedGrant, error) {
	rows, err := q.RevokeCrossingGrants(ctx, db.RevokeCrossingGrantsParams{RevokedBy: actorID(ctx), UserID: user.ID})
	if err != nil {
		return nil, err
	}
	out := []RevokedGrant{}
	ids := []int64{}
	for _, r := range rows {
		out = append(out, RevokedGrant{ID: r.ID, Label: r.Label, Owner: r.OwnerName, Auditor: r.AuditorName})
		ids = append(ids, r.ID)
		if err := grantEvent(ctx, q, ActionAuditorRevoke, r.ID, r.OwnerID, r.AuditorID, r.AuditorName, r.OldGroup,
			map[string]any{"label": r.Label, "reason": RevokeGroupChange}); err != nil {
			return nil, err
		}
	}
	if user.IsAuditor {
		return out, q.DeleteUserSessions(ctx, user.ID)
	}
	return out, q.DeleteGrantSessions(ctx, ids)
}

// revokeAuditorGrants revokes every unrevoked grant of an auditor who is
// about to be removed (coordinator decision 27): one auditor.revoke entry
// each, in the grant's group, and their sessions end. The caller holds the
// auditor's row FOR UPDATE.
func revokeAuditorGrants(ctx context.Context, q *db.Queries, auditorID int64) ([]RevokedGrant, error) {
	ids, err := q.UnrevokedGrantsOfAuditor(ctx, auditorID)
	if err != nil {
		return nil, err
	}
	out := []RevokedGrant{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.RevokeGrants(ctx, db.RevokeGrantsParams{RevokedBy: actorID(ctx), Reason: RevokeUserRemoved, Ids: ids})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, RevokedGrant{ID: r.ID, Label: r.Label, Owner: r.OwnerName, Auditor: r.AuditorName})
		if err := grantEvent(ctx, q, ActionAuditorRevoke, r.ID, r.OwnerID, r.AuditorID, r.AuditorName, r.OwnerGroup,
			map[string]any{"label": r.Label, "reason": RevokeUserRemoved}); err != nil {
			return nil, err
		}
	}
	return out, q.DeleteUserSessions(ctx, auditorID)
}

// ChangedGrant is a grant that an account move or purge narrowed or
// revoked.
type ChangedGrant struct {
	ID      int64  `json:"id"`
	Label   string `json:"label"`
	Revoked bool   `json:"revoked"`
}

// dropAccountFromGrants takes an account out of every unrevoked grant
// before it moves to another owner or is deleted (decision 9). A grant left
// without accounts is revoked with reason and its sessions end; each grant
// gets an auditor.grant_change or auditor.revoke entry in its group. The
// caller holds the account's row.
func dropAccountFromGrants(ctx context.Context, q *db.Queries, accountID int64, reason string) ([]ChangedGrant, error) {
	rows, err := q.DropAccountFromGrants(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := []ChangedGrant{}
	var empty []int64
	for _, r := range rows {
		revoked := r.AccountsLeft == 0
		out = append(out, ChangedGrant{ID: r.ID, Label: r.Label, Revoked: revoked})
		action := ActionAuditorGrantChange
		if revoked {
			action = ActionAuditorRevoke
			empty = append(empty, r.ID)
		}
		if err := grantEvent(ctx, q, action, r.ID, r.OwnerID, r.AuditorID, r.AuditorName, r.OwnerGroup,
			map[string]any{"label": r.Label, "reason": reason, "account": accountID}); err != nil {
			return nil, err
		}
	}
	if len(empty) == 0 {
		return out, nil
	}
	if _, err := q.RevokeGrants(ctx, db.RevokeGrantsParams{RevokedBy: actorID(ctx), Reason: reason, Ids: empty}); err != nil {
		return nil, err
	}
	return out, q.DeleteGrantSessions(ctx, empty)
}

// AuditorScope returns what an auditor's session reads through one of
// their grants: its accounts (all shared, never own) with their granted
// folders, and its period. It is empty, and matches nothing, when the
// grant is not an active grant of this auditor (fail-closed): revoked,
// outside its window, suspended by a lock, or across groups. A grant's
// accounts come only from active_auditor_grant_accounts.
func (s *Store) AuditorScope(ctx context.Context, auditorID, grantID int64) (Scope, error) {
	rows, err := s.q.AuditorScopeAccounts(ctx, db.AuditorScopeAccountsParams{GrantID: grantID, AuditorID: auditorID})
	if err != nil {
		return Scope{}, err
	}
	sc := Scope{Viewer: auditorID, Accounts: make([]ScopeAccount, 0, len(rows)), envelopes: []int64{}}
	for i, r := range rows {
		a := ScopeAccount{ID: r.ID, Name: r.Name, OwnerID: r.OwnerID, Owner: r.Owner, Kind: AccountKind(r.Kind), Removed: r.Removed}
		if !r.AllFolders {
			a.Folders = append([]int64{}, r.Folders...)
		}
		sc.Accounts = append(sc.Accounts, a)
		if i == 0 {
			sc.after, sc.before = r.PeriodFrom, r.PeriodUntil
		}
	}
	return sc, nil
}

// SetSessionGrant binds an auditor's session to one of their active
// grants and caps the session's expiry at the grant's end. ErrNotFound for
// a session of someone else or a grant that is not an active grant of the
// auditor.
func (s *Store) SetSessionGrant(ctx context.Context, idHash []byte, auditorID, grantID int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		return one(q.SetSessionGrant(ctx, db.SetSessionGrantParams{ID: idHash, AuditorID: auditorID, GrantID: grantID}))
	})
}

// ActiveGrants lists an auditor's active grants, soonest end first,
// without accounts.
func (s *Store) ActiveGrants(ctx context.Context, auditorID int64) ([]AuditorGrant, error) {
	rows, err := s.q.ListActiveGrants(ctx, auditorID)
	if err != nil {
		return nil, err
	}
	out := make([]AuditorGrant, 0, len(rows))
	for _, r := range rows {
		id := auditorID
		out = append(out, AuditorGrant{
			ID: r.ID, OwnerID: r.OwnerID, Owner: r.OwnerName, AuditorID: &id, Label: r.Label,
			PeriodFrom: r.PeriodFrom, PeriodUntil: r.PeriodUntil, StartsAt: r.StartsAt, EndsAt: r.EndsAt, Active: true,
		})
	}
	return out, nil
}

// OwnerGrants lists an owner's grants with their history, newest first.
// An auditor who left the owner's group, or was removed, is not named.
// Accounts are those the owner still owns.
func (s *Store) OwnerGrants(ctx context.Context, ownerID int64) ([]AuditorGrant, error) {
	rows, err := s.q.ListOwnerGrants(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	accounts, err := s.q.ListOwnerGrantAccounts(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	byGrant := map[int64][]AuditorGrantAccount{}
	for _, a := range accounts {
		byGrant[a.GrantID] = append(byGrant[a.GrantID], grantAccount(a.AccountID, a.Name, a.AllFolders, a.Folders))
	}
	out := make([]AuditorGrant, 0, len(rows))
	for _, r := range rows {
		out = append(out, AuditorGrant{
			ID: r.ID, OwnerID: r.OwnerID, Owner: r.OwnerName, AuditorID: r.AuditorID, Auditor: deref(r.AuditorName), Label: r.Label,
			PeriodFrom: r.PeriodFrom, PeriodUntil: r.PeriodUntil, StartsAt: r.StartsAt, EndsAt: r.EndsAt,
			RevokedAt: r.RevokedAt, RevokeReason: deref(r.RevokeReason), CreatedAt: r.CreatedAt, Active: r.Active,
			Accounts: nonNilAccounts(byGrant[r.ID]),
		})
	}
	return out, nil
}

// AuditorGrants lists an auditor's grants whose owner is in the auditor's
// current group, history included, newest first. Only an active grant
// lists its accounts (coordinator decision 31).
func (s *Store) AuditorGrants(ctx context.Context, auditorID int64) ([]AuditorGrant, error) {
	rows, err := s.q.ListAuditorGrants(ctx, auditorID)
	if err != nil {
		return nil, err
	}
	accounts, err := s.q.ListAuditorGrantAccounts(ctx, auditorID)
	if err != nil {
		return nil, err
	}
	byGrant := map[int64][]AuditorGrantAccount{}
	for _, a := range accounts {
		byGrant[a.GrantID] = append(byGrant[a.GrantID], grantAccount(a.AccountID, a.Name, a.AllFolders, a.Folders))
	}
	out := make([]AuditorGrant, 0, len(rows))
	for _, r := range rows {
		id := r.AuditorID
		out = append(out, AuditorGrant{
			ID: r.ID, OwnerID: r.OwnerID, Owner: r.OwnerName, AuditorID: &id, Auditor: r.AuditorName, Label: r.Label,
			PeriodFrom: r.PeriodFrom, PeriodUntil: r.PeriodUntil, StartsAt: r.StartsAt, EndsAt: r.EndsAt,
			RevokedAt: r.RevokedAt, RevokeReason: deref(r.RevokeReason), CreatedAt: r.CreatedAt, Active: r.Active,
			Accounts: nonNilAccounts(byGrant[r.ID]),
		})
	}
	return out, nil
}

func grantAccount(id int64, name string, all bool, folders []string) AuditorGrantAccount {
	a := AuditorGrantAccount{ID: id, Name: name}
	if !all {
		a.Folders = append([]string{}, folders...)
	}
	return a
}

func nonNilAccounts(a []AuditorGrantAccount) []AuditorGrantAccount {
	if a == nil {
		return []AuditorGrantAccount{}
	}
	return a
}

// GetAuditorByNameInGroup looks up an auditor by login name inside a
// customer group. An unknown name, a member or admin and a user of another
// group are all ErrNotFound, found with the same query.
func (s *Store) GetAuditorByNameInGroup(ctx context.Context, name string, groupID int64) (*User, error) {
	r, err := s.q.GetAuditorByNameInGroup(ctx, db.GetAuditorByNameInGroupParams{Name: name, GroupID: groupID})
	if isNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return userFromDB(r), nil
}
