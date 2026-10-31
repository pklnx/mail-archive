package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// Shared accounts (#389). The owner shares an account with another user,
// who may then search, read and download its mail; managing, syncing and
// removing it stay with the owner (every management route resolves the
// account with GetOwnedAccount, so a shared user gets 404). Reads of
// shared mail are audited with via_owners and via_accounts, which make the
// entry part of the owner's Log (store.ListAudit).

// shareTarget resolves the account and the user of a share route. The
// account must be the caller's own (404 otherwise, like a name that does
// not exist); the user must exist (404).
func (s *Server) shareTarget(w http.ResponseWriter, r *http.Request) (*store.Account, *store.User, bool) {
	a, err := s.store.GetOwnedAccount(r.Context(), userID(r), r.PathValue("name"))
	if err != nil {
		s.failStore(w, r, err)
		return nil, nil, false
	}
	u, err := s.store.GetUserByName(r.Context(), auth.NormalizeUserName(r.PathValue("user")))
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such user", nil)
		return nil, nil, false
	}
	if err != nil {
		s.failStore(w, r, err)
		return nil, nil, false
	}
	return a, u, true
}

func shareEntry(e *store.AuditEntry, a *store.Account, u *store.User) {
	auditAccount(e, a)
	e.Detail("user", u.Name)
	e.Detail("user_id", u.ID)
}

// handleShareAccount lets a user read the caller's account. Sharing again
// changes nothing and writes no entry.
func (s *Server) handleShareAccount(w http.ResponseWriter, r *http.Request) {
	a, u, ok := s.shareTarget(w, r)
	if !ok {
		return
	}
	if a.RemovedAt != nil {
		s.fail(w, r, http.StatusConflict, "account was removed; it can no longer be shared", nil)
		return
	}
	e := s.auditEntry(r, "account.share")
	shareEntry(e, a, u)
	created, err := s.store.ShareAccount(withAudit(r, e), a.Ref(), u.ID)
	switch {
	case errors.Is(err, store.ErrShareSelf):
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	case errors.Is(err, store.ErrShareSMTP), errors.Is(err, store.ErrStale):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	if created {
		s.log.Info("account shared", "account", a.Name, "user", u.Name)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUnshareAccount ends a share: the user's next request no longer
// sees the account. Removed accounts may be unshared too.
func (s *Server) handleUnshareAccount(w http.ResponseWriter, r *http.Request) {
	a, u, ok := s.shareTarget(w, r)
	if !ok {
		return
	}
	e := s.auditEntry(r, "account.unshare")
	shareEntry(e, a, u)
	err := s.store.UnshareAccount(withAudit(r, e), a.Ref(), u.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "the account is not shared with this user", nil)
		return
	case errors.Is(err, store.ErrStale):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("account unshared", "account", a.Name, "user", u.Name)
	w.WriteHeader(http.StatusNoContent)
}

// setVia records which owners' shared accounts an entry is about:
// via_owners (their user IDs, which put the entry into their Log) and
// via_accounts. Without accounts it records nothing.
func setVia(e *store.AuditEntry, via []store.ViaAccount) {
	if len(via) == 0 {
		return
	}
	slices.SortFunc(via, func(a, b store.ViaAccount) int { return cmp.Compare(a.Key, b.Key) })
	owners := []int64{}
	for _, a := range via {
		if !slices.Contains(owners, a.Owner) {
			owners = append(owners, a.Owner)
		}
	}
	slices.Sort(owners)
	e.Detail(store.DetailViaOwners, owners)
	e.Detail(store.DetailViaAccounts, via)
}

// viaLocations records the shared accounts among a message's locations
// (message.open, html, raw, part): the owner sees every read of a message
// that is in their shared account, also when the reader has it too.
func viaLocations(e *store.AuditEntry, viewer int64, locs []store.MessageLocation) {
	var via []store.ViaAccount
	for _, l := range locs {
		if l.OwnerID == viewer || slices.ContainsFunc(via, func(a store.ViaAccount) bool { return a.ID == l.AccountID }) {
			continue
		}
		via = append(via, store.ViaAccount{ID: l.AccountID, Owner: l.OwnerID, Key: l.Owner + "/" + l.Account})
	}
	setVia(e, via)
}

// shareOnly is store.ListShareOnlyLocations, failing when a test says so.
func (s *Server) shareOnly(ctx context.Context, sc, full store.Scope, shas []string) ([]store.ShareOnlyLocation, error) {
	if s.shareOnlyFails != nil {
		if err := s.shareOnlyFails(); err != nil {
			return nil, err
		}
	}
	return s.store.ListShareOnlyLocations(ctx, sc, full, shas)
}

// shareOnlyVia counts, per shared account of sc, the given messages the
// viewer sees only through it. named lists sc's one shared account even
// without such messages; otherwise only accounts with at least one.
func (s *Server) shareOnlyVia(r *http.Request, sc, full store.Scope, shas []string, named bool) ([]store.ViaAccount, error) {
	locs, err := s.shareOnly(r.Context(), sc, full, shas)
	if err != nil {
		return nil, err
	}
	counts := map[int64]int{}
	for _, l := range locs {
		counts[l.AccountID]++
	}
	var via []store.ViaAccount
	for _, a := range sc.Shared().Accounts {
		n := counts[a.ID]
		if n == 0 && !named {
			continue
		}
		via = append(via, store.ViaAccount{ID: a.ID, Owner: a.OwnerID, Key: a.Key(), N: &n})
	}
	return via, nil
}

// listEntry returns the entry of a page of GET /api/messages with the
// messages shas, or nil for none; on an error it answers 503 and returns
// false. sc is the request's scope after account=, full the viewer's.
//
// Without a shared account in sc, it is searchEntry: as before shares.
// With one, every page is logged (#389, revision 2, R1): the first page as
// message.search (with search parameters) or message.list, further pages
// as message.page. A named first page (account= names the shared account)
// always gets that account with n. Every other page, named cursor pages
// included (decision 17), gets every shared account through which it shows
// at least one message the viewer sees only there, with that count n
// (decision 13).
func (s *Server) listEntry(w http.ResponseWriter, r *http.Request, sc, full store.Scope, shas []string) (*store.AuditEntry, bool) {
	if !sc.HasShared() {
		return s.searchEntry(r), true
	}
	q := r.URL.Query()
	page := q.Get("cursor") != ""
	named := q.Get("account") != ""
	action := "message.list"
	switch {
	case page:
		action = "message.page"
	case isSearch(q.Get):
		action = "message.search"
	}
	e := s.auditEntry(r, action)
	for _, k := range append(slices.Clone(searchFields), "thread") {
		if v := q.Get(k); v != "" {
			e.Detail(k, v)
		}
	}
	via, err := s.shareOnlyVia(r, sc, full, shas, named && !page)
	if err != nil {
		s.failAudit(w, r, err)
		return nil, false
	}
	setVia(e, via)
	return e, true
}

// conversationEntry returns the entry of a conversation with members, or
// nil without a shared account in sc (as before shares). The owner gets it
// when members other than the requested message are seen only through
// their shared account; the requested message has its message.open.
func (s *Server) conversationEntry(w http.ResponseWriter, r *http.Request, sc store.Scope, sha string, members []string) (*store.AuditEntry, bool) {
	if !sc.HasShared() {
		return nil, true
	}
	e := s.messageEntry(r, "message.conversation", sha)
	e.Detail("members", len(members))
	others := slices.DeleteFunc(slices.Clone(members), func(m string) bool { return m == sha })
	via, err := s.shareOnlyVia(r, sc, sc, others, false)
	if err != nil {
		s.failAudit(w, r, err)
		return nil, false
	}
	setVia(e, via)
	return e, true
}

// sharedWithJSON is one user an own account is shared with.
type sharedWithJSON struct {
	User  string    `json:"user"`
	Since time.Time `json:"since"`
}

// sharedAccountJSON is an account another user shared with the viewer:
// only what reading its mail needs (decision 10).
type sharedAccountJSON struct {
	// Key names it in account=: owner/name.
	Key     string       `json:"key"`
	Name    string       `json:"name"`
	Owner   string       `json:"owner"`
	Kind    string       `json:"kind"`
	Enabled bool         `json:"enabled"`
	Removed bool         `json:"removed"`
	Folders []folderJSON `json:"folders"`
}
