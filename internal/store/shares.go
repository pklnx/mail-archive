package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Shared accounts (#389). The owner of an account can let other users read
// its mail: search, read and download. Managing the account, syncing it,
// retention, holds and deletion stay with the owner. Every read goes
// through a Scope, the accounts its viewer may read; owner-only paths use
// OwnerScope or the owner's ID as before.

// ErrShareSelf is returned when an owner shares an account with themselves.
var ErrShareSelf = errors.New("the owner already sees the account")

// ErrShareSMTP is returned when an smtp account is shared: its envelopes
// (BCC recipients) stay with the owner.
var ErrShareSMTP = errors.New("smtp accounts cannot be shared")

// ScopeAccount is one account a viewer may read.
type ScopeAccount struct {
	ID      int64
	Name    string
	OwnerID int64
	Owner   string // the owner's user name
	Kind    AccountKind
	Removed bool
	// Own: the viewer owns the account; otherwise it is shared with them.
	Own bool
	// Folders restricts the account to these folder IDs (#393): nil means
	// all its folders, an empty slice none. Code never turns an empty
	// slice into nil.
	Folders []int64
}

// Scope is what a read may see: the accounts of Accounts and nothing
// else. The zero Scope, and one without accounts, matches nothing.
type Scope struct {
	Viewer   int64
	Accounts []ScopeAccount
	// envelopes are the viewer's own smtp accounts, kept when the scope is
	// narrowed: a to: filter matches the envelopes of any of them, as it
	// did before an account filter narrowed the listing.
	envelopes []int64
	// after and before restrict the scope to messages with sort_at in
	// [after, before) (#393); nil is no bound. Only Restrict and
	// AuditorScope set them, and every narrowing keeps them.
	after, before *time.Time
}

// ViewerScope returns the accounts a user may read: their own and those
// shared with them, removed ones included.
func (s *Store) ViewerScope(ctx context.Context, userID int64) (Scope, error) {
	return s.scope(ctx, userID, true)
}

// OwnerScope returns only the user's own accounts. Owner-only work uses
// it: legal holds, retention and export never reach shared accounts.
func (s *Store) OwnerScope(ctx context.Context, userID int64) (Scope, error) {
	return s.scope(ctx, userID, false)
}

func (s *Store) scope(ctx context.Context, userID int64, shared bool) (Scope, error) {
	rows, err := s.qFor(ctx).ListScopeAccounts(ctx, db.ListScopeAccountsParams{UserID: userID, IncludeShared: shared})
	if err != nil {
		return Scope{}, err
	}
	sc := Scope{Viewer: userID, Accounts: make([]ScopeAccount, 0, len(rows)), envelopes: []int64{}}
	for _, r := range rows {
		a := ScopeAccount{
			ID: r.ID, Name: r.Name, OwnerID: r.OwnerID, Owner: r.Owner, Kind: AccountKind(r.Kind), Removed: r.Removed, Own: r.Own,
		}
		sc.Accounts = append(sc.Accounts, a)
		// Shares of smtp accounts are refused; even so, only the viewer's
		// own envelopes (BCC recipients included) may be shown or searched.
		if a.Own && a.Kind == KindSMTP {
			sc.envelopes = append(sc.envelopes, a.ID)
		}
	}
	return sc, nil
}

// Only narrows the scope to the account with this ID; empty if the scope
// does not hold it.
func (sc Scope) Only(id int64) Scope {
	return sc.filter(func(a ScopeAccount) bool { return a.ID == id })
}

// WithoutEnvelopes returns the scope without the viewer's smtp envelopes:
// a to: filter then matches the To and Cc headers only. Legal hold
// filters use it, so a hold never searches envelope (BCC)
// recipients, which differ per account while the message is the same.
func (sc Scope) WithoutEnvelopes() Scope {
	out := sc
	out.envelopes = []int64{}
	return out
}

// OwnAccount narrows the scope to the viewer's own account of this name;
// empty if there is none. A shared account of the same name is left out.
func (sc Scope) OwnAccount(name string) Scope {
	return sc.filter(func(a ScopeAccount) bool { return a.Own && a.Name == name })
}

// Key is how the API names the account: owner/name. User and account
// names cannot hold "/", so the key is unambiguous.
func (a ScopeAccount) Key() string {
	return a.Owner + "/" + a.Name
}

// Named narrows the scope to the account an account= parameter names:
// "owner/name" is the account of that owner and name in the scope (own or
// shared), a plain name the viewer's own account (OwnAccount). Empty if
// there is none.
func (sc Scope) Named(key string) Scope {
	owner, name, ok := strings.Cut(key, "/")
	if !ok {
		return sc.OwnAccount(key)
	}
	return sc.filter(func(a ScopeAccount) bool { return a.Owner == owner && a.Name == name })
}

// Own narrows the scope to the viewer's own accounts.
func (sc Scope) Own() Scope {
	return sc.filter(func(a ScopeAccount) bool { return a.Own })
}

// Shared narrows the scope to the accounts shared with the viewer.
func (sc Scope) Shared() Scope {
	return sc.filter(func(a ScopeAccount) bool { return !a.Own })
}

// HasShared reports whether the scope holds an account shared with the
// viewer.
func (sc Scope) HasShared() bool {
	return slices.ContainsFunc(sc.Accounts, func(a ScopeAccount) bool { return !a.Own })
}

// Account returns the scope's account with this ID.
func (sc Scope) Account(id int64) (ScopeAccount, bool) {
	i := slices.IndexFunc(sc.Accounts, func(a ScopeAccount) bool { return a.ID == id })
	if i < 0 {
		return ScopeAccount{}, false
	}
	return sc.Accounts[i], true
}

// filter starts from a copy of the scope, so that every restriction (the
// accounts' folders, the period) carries over to Only, OwnAccount, Named,
// Own and Shared (#393, B3).
func (sc Scope) filter(keep func(ScopeAccount) bool) Scope {
	out := sc
	out.Accounts = []ScopeAccount{}
	for _, a := range sc.Accounts {
		if keep(a) {
			out.Accounts = append(out.Accounts, a)
		}
	}
	return out
}

// Restrict narrows the scope and never widens it (#393). folders maps
// account IDs to the folder IDs to keep: a nil map changes no account, a
// non-nil one drops every account it does not name, and a nil value keeps
// that account's folders as they are. Folder sets intersect. The later
// after and the earlier before win; nil bounds change nothing. The result
// has folder lists of its own: the scope's are never changed in place.
//
// Folder IDs are checked per account by every query (scope_folder_granted):
// an ID that is not a folder of the account it is listed under matches
// nothing, so it cannot widen the scope. Restrict cannot see which folders
// an account has; Store.RestrictScope also drops such IDs.
func (sc Scope) Restrict(folders map[int64][]int64, after, before *time.Time) Scope {
	out := sc
	out.Accounts = make([]ScopeAccount, 0, len(sc.Accounts))
	for _, a := range sc.Accounts {
		want, named := folders[a.ID]
		if folders != nil && !named {
			continue
		}
		switch {
		case want == nil && a.Folders == nil:
		case want == nil:
			a.Folders = slices.Clone(a.Folders)
		case a.Folders == nil:
			a.Folders = append([]int64{}, want...)
		default:
			keep := []int64{}
			for _, id := range a.Folders {
				if slices.Contains(want, id) {
					keep = append(keep, id)
				}
			}
			a.Folders = keep
		}
		out.Accounts = append(out.Accounts, a)
	}
	if after != nil && (out.after == nil || after.After(*out.after)) {
		t := *after
		out.after = &t
	}
	if before != nil && (out.before == nil || before.Before(*out.before)) {
		t := *before
		out.before = &t
	}
	return out
}

// RestrictScope is Scope.Restrict with the folder IDs of each named
// account first reduced to that account's own folders: an ID of another
// account's folder is dropped, not carried along.
func (s *Store) RestrictScope(ctx context.Context, sc Scope, folders map[int64][]int64, after, before *time.Time) (Scope, error) {
	var own map[int64][]int64
	if folders != nil {
		own = make(map[int64][]int64, len(folders))
		for id, want := range folders {
			if want == nil {
				own[id] = nil
				continue
			}
			found, err := s.qFor(ctx).AccountFolderIDs(ctx, db.AccountFolderIDsParams{AccountID: id, Ids: want})
			if err != nil {
				return Scope{}, err
			}
			own[id] = append([]int64{}, found...)
		}
	}
	return sc.Restrict(own, after, before), nil
}

// Period returns the scope's message period: sort_at in [after, before),
// nil for no bound.
func (sc Scope) Period() (after, before *time.Time) {
	return sc.after, sc.before
}

// folderParams returns the folder predicate's parameters, never nil: the
// accounts read with all their folders, and the restricted accounts'
// folders as pairs in two parallel arrays (account, folder). A location
// matches if its folder's account is in the first, or (account, folder) is
// one of the pairs (scope_folder_granted, 00036), on top of the account
// predicate. Pairs, not a pooled list of folder IDs: a folder ID listed
// under one account never opens a folder of another (#393). A NULL would
// match nothing.
//
// Each folder ID is listed once (the SQL looks up its first pair). A
// folder belongs to one account, so an ID listed under two accounts is
// wrong for at least one of them; it is dropped for both, which only
// narrows. AuditorScope and Store.RestrictScope never list one so.
func (sc Scope) folderParams() (allFolderAccounts, folderAccounts, folders []int64) {
	allFolderAccounts, folderAccounts, folders = []int64{}, []int64{}, []int64{}
	owner := map[int64]int64{}
	twice := map[int64]bool{}
	for _, a := range sc.Accounts {
		for _, f := range a.Folders {
			if o, seen := owner[f]; seen && o != a.ID {
				twice[f] = true
			}
			owner[f] = a.ID
		}
	}
	listed := map[int64]bool{}
	for _, a := range sc.Accounts {
		if a.Folders == nil {
			allFolderAccounts = append(allFolderAccounts, a.ID)
			continue
		}
		for _, f := range a.Folders {
			if twice[f] || listed[f] {
				continue
			}
			listed[f] = true
			folderAccounts = append(folderAccounts, a.ID)
			folders = append(folders, f)
		}
	}
	return allFolderAccounts, folderAccounts, folders
}

// clampPeriod narrows a requested date range to the scope's period.
// Disjoint ranges stay disjoint, and so match nothing.
func (sc Scope) clampPeriod(after, before *time.Time) (*time.Time, *time.Time) {
	if sc.after != nil && (after == nil || sc.after.After(*after)) {
		after = sc.after
	}
	if sc.before != nil && (before == nil || sc.before.Before(*before)) {
		before = sc.before
	}
	return after, before
}

// ids returns the scope's account IDs, never nil: the queries match an
// empty array with nothing, and so would NULL, but an explicit empty array
// keeps that obvious.
func (sc Scope) ids() []int64 {
	ids := make([]int64, 0, len(sc.Accounts))
	for _, a := range sc.Accounts {
		ids = append(ids, a.ID)
	}
	return ids
}

// envelopeIDs returns the viewer's own smtp accounts, also those a
// narrowing left out (see Scope.envelopes); never nil.
func (sc Scope) envelopeIDs() []int64 {
	if sc.envelopes == nil {
		return []int64{}
	}
	return sc.envelopes
}

// ShareOnlyLocation says that the viewer sees a message through a shared
// account and through none of their own.
type ShareOnlyLocation struct {
	SHA256    string
	AccountID int64
	OwnerID   int64
}

// ListShareOnlyLocations returns, for the given messages, the shared
// accounts of sc through which the viewer sees those messages that are in
// none of the viewer's own accounts: one entry per message and account.
// sc is the request's scope, maybe narrowed to one account; full is the
// viewer's whole ViewerScope, whose own accounts all count. Without shared
// accounts in sc it runs no query.
func (s *Store) ListShareOnlyLocations(ctx context.Context, sc, full Scope, shas []string) ([]ShareOnlyLocation, error) {
	shared := sc.Shared()
	if len(shared.Accounts) == 0 || len(shas) == 0 {
		return []ShareOnlyLocation{}, nil
	}
	all, folderAccounts, folders := shared.folderParams()
	rows, err := s.q.ListShareOnlyLocations(ctx, db.ListShareOnlyLocationsParams{
		Shas: shas, SharedAccounts: shared.ids(), OwnAccounts: full.Own().ids(),
		ScopeAllFolderAccounts: all, ScopeFolderAccounts: folderAccounts, ScopeFolders: folders, ScopeAfter: shared.after, ScopeBefore: shared.before,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ShareOnlyLocation, 0, len(rows))
	for _, r := range rows {
		out = append(out, ShareOnlyLocation{SHA256: r.Sha256, AccountID: r.AccountID, OwnerID: r.OwnerID})
	}
	return out, nil
}

// AccountShare is one user an account is shared with.
type AccountShare struct {
	UserID int64
	User   string
	Since  time.Time
}

// ShareAccount lets user read the account's mail. It is checked against
// ref like UpdateAccount (ErrNotFound for a gone, removed or foreign
// account, ErrStale), and fails with ErrNotFound for an unknown user, with
// ErrShareSelf for the owner, with ErrShareSMTP for an smtp account and
// with ErrShareOtherGroup for a user outside the owner's group (#470) and
// with ErrShareAuditor for an auditor (#393). The
// owner's and the user's rows stay locked FOR SHARE until the commit, so
// neither can change group meanwhile (SetUserGroup). It reports false, and
// changes nothing, if the share exists already.
func (s *Store) ShareAccount(ctx context.Context, ref AccountRef, userID int64) (bool, error) {
	var created bool
	err := s.inTx(ctx, func(q *db.Queries) error {
		r, err := lockAccount(ctx, q, ref)
		if err != nil {
			return err
		}
		switch {
		case r.OwnerID != nil && *r.OwnerID == userID:
			return ErrShareSelf
		case AccountKind(r.Kind) == KindSMTP:
			return ErrShareSMTP
		case r.OwnerID == nil:
			return ErrNotFound
		}
		users, err := q.LockUserGroups(ctx, []int64{*r.OwnerID, userID})
		if err != nil {
			return err
		}
		groups := map[int64]int64{}
		auditor := false
		for _, u := range users {
			groups[u.ID] = u.GroupID
			auditor = auditor || u.ID == userID && u.IsAuditor
		}
		reader, ok := groups[userID]
		if !ok {
			return ErrNotFound
		}
		if reader != groups[*r.OwnerID] {
			return ErrShareOtherGroup
		}
		if auditor {
			return ErrShareAuditor
		}
		n, err := q.InsertAccountShare(ctx, db.InsertAccountShareParams{AccountID: r.ID, UserID: userID})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" { // the user is gone
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		created = n == 1
		if !created {
			// The share exists already: nothing changed, no entry.
			skipAudit(ctx)
		}
		return nil
	})
	return created, err
}

// UnshareAccount ends a share at once: the next read of user no longer
// sees the account. It is checked against ref like ShareAccount, but also
// works on removed accounts. ErrNotFound if there was no such share.
func (s *Store) UnshareAccount(ctx context.Context, ref AccountRef, userID int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		if _, err := lockAccountKeepRemoved(ctx, q, ref); err != nil {
			return err
		}
		return one(q.DeleteAccountShare(ctx, db.DeleteAccountShareParams{AccountID: ref.ID, UserID: userID}))
	})
}

// OwnerShares lists the shares of every account of an owner, by account ID,
// each by user name.
func (s *Store) OwnerShares(ctx context.Context, ownerID int64) (map[int64][]AccountShare, error) {
	rows, err := s.q.ListOwnerShares(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]AccountShare)
	for _, r := range rows {
		out[r.AccountID] = append(out[r.AccountID], AccountShare{User: r.UserName, Since: r.CreatedAt})
	}
	return out, nil
}

// AccountShares lists the users an account is shared with, by name.
func (s *Store) AccountShares(ctx context.Context, accountID int64) ([]AccountShare, error) {
	rows, err := s.q.ListAccountShares(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]AccountShare, 0, len(rows))
	for _, r := range rows {
		out = append(out, AccountShare{UserID: r.UserID, User: r.UserName, Since: r.CreatedAt})
	}
	return out, nil
}

// lockAccountKeepRemoved is lockAccount for changes that removed accounts
// allow too.
func lockAccountKeepRemoved(ctx context.Context, q *db.Queries, ref AccountRef) (db.Account, error) {
	r, err := q.LockAccount(ctx, ref.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if !sameOwner(r.OwnerID, ref.OwnerID) {
		return r, ErrNotFound
	}
	if r.Version != ref.Version {
		return r, ErrStale
	}
	return r, nil
}
