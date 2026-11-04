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

// Shared accounts (#389). The owner shares an account with another user
// of their customer group (#470), who may then search, read and download
// its mail; managing, syncing and removing it stay with the owner (every
// management route resolves the account with GetOwnedAccount, so a shared
// user gets 404). Reads of shared mail are audited with via_owners and
// via_accounts, which make the entry part of the owner's Log
// (store.ListAudit).
//
// Neither route tells whether a user name exists outside the caller's
// group (#476): a share looks the user up inside the group only, and an
// unknown name and a user of another group get the same answer from the
// same query; an unshare looks up the account's shares, never the user.

// errNoUserInGroup answers a share with a name that is not a user of the
// caller's group, whether it exists elsewhere or not.
const errNoUserInGroup = "no such user in your group"

// errNotShared answers an unshare with a name the account is not shared
// with, whether it is a user or not.
const errNotShared = "the account is not shared with this user"

func shareEntry(e *store.AuditEntry, a *store.Account, userID int64, user string) {
	auditAccount(e, a)
	e.Detail("user", user)
	e.Detail("user_id", userID)
}

// handleShareAccount lets a user of the caller's group read the caller's
// account. Sharing again changes nothing and writes no entry.
func (s *Server) handleShareAccount(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetOwnedAccount(r.Context(), userID(r), r.PathValue("name"))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	u, err := s.store.GetUserByNameInGroup(r.Context(), auth.NormalizeUserName(r.PathValue("user")), currentSession(r).GroupID)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, errNoUserInGroup, nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	if a.RemovedAt != nil {
		s.fail(w, r, http.StatusConflict, "account was removed; it can no longer be shared", nil)
		return
	}
	e := s.targetedEntry(r, "account.share")
	shareEntry(e, a, u.ID, u.Name)
	created, err := s.store.ShareAccount(withAudit(r, e), a.Ref(), u.ID)
	switch {
	case errors.Is(err, store.ErrShareSelf):
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	case errors.Is(err, store.ErrShareSMTP), errors.Is(err, store.ErrStale), errors.Is(err, store.ErrShareAuditor):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case errors.Is(err, store.ErrShareOtherGroup):
		// The user moved to another group since the lookup.
		s.fail(w, r, http.StatusNotFound, errNoUserInGroup, nil)
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
// sees the account. Removed accounts may be unshared too. The user is
// found among the account's shares, so a name the account is not shared
// with gets one answer, whether such a user exists or not.
func (s *Server) handleUnshareAccount(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetOwnedAccount(r.Context(), userID(r), r.PathValue("name"))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	shares, err := s.store.AccountShares(r.Context(), a.ID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	name := auth.NormalizeUserName(r.PathValue("user"))
	i := slices.IndexFunc(shares, func(sh store.AccountShare) bool { return sh.User == name })
	if i < 0 {
		s.fail(w, r, http.StatusNotFound, errNotShared, nil)
		return
	}
	sh := shares[i]
	e := s.targetedEntry(r, "account.unshare")
	shareEntry(e, a, sh.UserID, sh.User)
	err = s.store.UnshareAccount(withAudit(r, e), a.Ref(), sh.UserID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Ended or the user removed since the lookup.
		s.fail(w, r, http.StatusNotFound, errNotShared, nil)
		return
	case errors.Is(err, store.ErrStale):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("account unshared", "account", a.Name, "user", sh.User)
	w.WriteHeader(http.StatusNoContent)
}

// isAuditor reports whether the request comes from an auditor's session
// (#393).
func isAuditor(r *http.Request) bool {
	sess := currentSession(r)
	return sess != nil && sess.IsAuditor
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
//
// An auditor's pages (#393) are all logged like those, also plain folder
// listings and empty pages, even if the grant has no account left.
func (s *Server) listEntry(w http.ResponseWriter, r *http.Request, sc, full store.Scope, shas []string) (*store.AuditEntry, bool) {
	if !sc.HasShared() && !isAuditor(r) {
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
	e := s.selfEntry(r, action)
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
	if !sc.HasShared() && !isAuditor(r) {
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
