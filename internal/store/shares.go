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
	rows, err := s.q.ListScopeAccounts(ctx, db.ListScopeAccountsParams{UserID: userID, IncludeShared: shared})
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

func (sc Scope) filter(keep func(ScopeAccount) bool) Scope {
	out := Scope{Viewer: sc.Viewer, Accounts: []ScopeAccount{}, envelopes: sc.envelopes}
	for _, a := range sc.Accounts {
		if keep(a) {
			out.Accounts = append(out.Accounts, a)
		}
	}
	return out
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
	rows, err := s.q.ListShareOnlyLocations(ctx, db.ListShareOnlyLocationsParams{
		Shas: shas, SharedAccounts: shared.ids(), OwnAccounts: full.Own().ids(),
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
// ErrShareSelf for the owner and with ErrShareSMTP for an smtp account. It
// reports false, and changes nothing, if the share exists already.
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
