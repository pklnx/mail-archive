package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/keepia/archive/internal/auth"
	"github.com/keepia/archive/internal/store"
)

// Auditor access (#393). An auditor is a user with a fixed role who reads
// only through a grant: one owner's accounts, all or some of their
// folders, an optional message period and a validity window. The store
// holds the access rules (store.AuditorScope, the views of 00036); this
// file has the grant routes, the auditor's grant choice, the allowlist's
// refusal and the tagging that puts every entry of an auditor into the
// grant owner's Log.
//
// No answer tells a non-admin whether a name or an ID of the feature
// exists outside what they may see (#393, 9.5): one 404 per question for
// unknown and foreign alike, from the same query.

// errNoAuditorInGroup answers a grant to a name that is not an auditor of
// the caller's group: unknown, a member or admin, or a user of another
// group.
const errNoAuditorInGroup = "no such auditor in your group"

// errNoSuchGrant answers a grant ID the caller may not use: unknown, of
// another owner, or (for an auditor's choice) not active.
const errNoSuchGrant = "no such grant"

// errNoSuchAccount answers an account name that is not one of the
// caller's accounts.
const errNoSuchAccount = "no such account"

// errNotForAuditors answers an auditor's request to a route outside the
// allowlist.
const errNotForAuditors = "not available to auditors"

// errNoAccess answers a correct login of an auditor without an active
// grant.
const errNoAccess = "you have no active auditor access"

// reasonNoAccess is the reason of auth.login_failed for that login.
const reasonNoAccess = "no_access"

type grantSelectionJSON struct {
	Error                  string `json:"error"`
	GrantSelectionRequired bool   `json:"grantSelectionRequired"`
}

// grantGateAllowed lists what an auditor's session may do before it is
// bound to a grant (besides the login routes and the password and 2FA
// setup): pick one.
func grantGateAllowed(method, path string) bool {
	return method == http.MethodPut && path == "/api/session/grant"
}

// Reasons of auditor.denied: the route is outside the allowlist, or a
// gate refused it (the password and 2FA setup, the grant choice), or the
// request asked for what only admins may see.
const (
	reasonNotAllowed             = "not_allowed"
	reasonPasswordChangeRequired = "password_change_required"
	reasonTwoFactorRequired      = "two_factor_required"
	reasonGrantSelectionRequired = "grant_selection_required"
	reasonAdminOnly              = "admin_only"
)

// auditorDenied answers an auditor's request to a route outside the
// allowlist with 403 and records it.
func (s *Server) auditorDenied(w http.ResponseWriter, r *http.Request, pattern string) {
	s.auditorDeniedEntry(r, currentSession(r), pattern, reasonNotAllowed)
	s.fail(w, r, http.StatusForbidden, errNotForAuditors, nil)
}

// auditorDeniedEntry records a refused request of an auditor's session
// in the auditor's and the grant owners' Log, with the method, the route
// pattern and why. Sessions of other users write nothing. It is best
// effort: the request is refused whether or not the entry is written, and
// no data was read.
func (s *Server) auditorDeniedEntry(r *http.Request, sess *store.Session, pattern, reason string) {
	if sess == nil || !sess.IsAuditor {
		return
	}
	e := s.selfEntry(r, store.ActionAuditorDenied)
	e.Detail("method", r.Method)
	e.Detail("route", pattern)
	e.Detail("reason", reason)
	s.auditBestEffort(r, e)
}

// tagAuditor adds to an entry of an auditor's session the grant and its
// owner (via_owners), which put the entry into that owner's Log; while
// the session has no grant yet, the owners of all the auditor's active
// grants. Entries of other users are left as they are. If those grants
// cannot be read, the entry is marked so that it is never written
// (store.AuditEntry.Unwritable): an entry without its owners would miss
// their Log, so a read that needs it fails closed.
func (s *Server) tagAuditor(ctx context.Context, e *store.AuditEntry, sess *store.Session) {
	if !sess.IsAuditor {
		return
	}
	if sess.GrantID != nil && sess.GrantOwnerID != nil {
		e.Detail(store.DetailAuditorGrant, *sess.GrantID)
		e.Detail(store.DetailViaOwners, []int64{*sess.GrantOwnerID})
		return
	}
	grants, err := s.store.ActiveGrants(ctx, sess.UserID)
	if err != nil {
		s.log.Warn("list the auditor's grants for an entry", "user", sess.UserName, "err", err)
		e.Unwritable = fmt.Errorf("the owners of the auditor's grants: %w", err)
		return
	}
	if len(grants) > 0 {
		e.Detail(store.DetailViaOwners, store.GrantOwners(grants))
	}
}

// refuseNoGrant answers a login that the store refused because the
// auditor has no active grant, and reports whether err was that. u names
// the user when the caller knows it.
func (s *Server) refuseNoGrant(w http.ResponseWriter, r *http.Request, method string, err error, u *store.User) bool {
	var ng *store.NoGrantError
	if !errors.As(err, &ng) {
		return false
	}
	if u == nil {
		u = &store.User{ID: ng.UserID, Name: ng.UserName}
	}
	s.log.Info("login refused: no active auditor grant", "user", u.Name, "addr", s.clientIP(r))
	s.auditLoginFailed(r, method, reasonNoAccess, u)
	s.fail(w, r, http.StatusForbidden, errNoAccess, nil)
	return true
}

// sessionGrantJSON is an active grant as its auditor's session shows it.
type sessionGrantJSON struct {
	ID          int64      `json:"id"`
	Label       string     `json:"label"`
	Owner       string     `json:"owner"`
	PeriodFrom  *time.Time `json:"periodFrom"`
	PeriodUntil *time.Time `json:"periodUntil"`
	StartsAt    time.Time  `json:"startsAt"`
	EndsAt      time.Time  `json:"endsAt"`
}

func toSessionGrant(g store.AuditorGrant) sessionGrantJSON {
	return sessionGrantJSON{ID: g.ID, Label: g.Label, Owner: g.Owner, PeriodFrom: g.PeriodFrom, PeriodUntil: g.PeriodUntil, StartsAt: g.StartsAt, EndsAt: g.EndsAt}
}

type selectGrantInput struct {
	ID int64 `json:"id"`
}

// handleSelectGrant binds the auditor's session to one of their active
// grants (decision 2) and caps the session at the grant's end. Any other
// ID, and any caller who is no auditor, gets the same 404.
func (s *Server) handleSelectGrant(w http.ResponseWriter, r *http.Request) {
	var in selectGrantInput
	if !s.decode(w, r, &in) {
		return
	}
	sess := currentSession(r)
	if !sess.IsAuditor {
		s.fail(w, r, http.StatusNotFound, errNoSuchGrant, nil)
		return
	}
	grants, err := s.store.ActiveGrants(r.Context(), sess.UserID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	var grant *store.AuditorGrant
	for i := range grants {
		if grants[i].ID == in.ID {
			grant = &grants[i]
		}
	}
	if grant == nil {
		s.fail(w, r, http.StatusNotFound, errNoSuchGrant, nil)
		return
	}
	e := s.selfEntry(r, store.ActionAuditorSelect)
	// The chosen grant, not the one the session had: the entry goes to
	// the chosen grant's owner.
	e.Detail(store.DetailAuditorGrant, grant.ID)
	e.Detail(store.DetailViaOwners, []int64{grant.OwnerID})
	e.Detail("label", grant.Label)
	hash := auth.HashSessionToken(s.sessionToken(r))
	err = s.store.SetSessionGrant(withAudit(r, e), hash, sess.UserID, grant.ID)
	if errors.Is(err, store.ErrNotFound) { // ended since it was listed
		s.fail(w, r, http.StatusNotFound, errNoSuchGrant, nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	next, err := s.store.GetSession(r.Context(), hash, s.now().Add(-auth.IdleTimeout))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	body, err := s.sessionBody(r.Context(), next)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, body)
}

// grantJSON is a grant in the owner's or the auditor's list.
type grantJSON struct {
	ID    int64  `json:"id"`
	Owner string `json:"owner"`
	// Auditor is null when the auditor left the owner's group or was
	// removed (the owner's list, revokeReason says which).
	Auditor     *string    `json:"auditor"`
	Label       string     `json:"label"`
	PeriodFrom  *time.Time `json:"periodFrom"`
	PeriodUntil *time.Time `json:"periodUntil"`
	StartsAt    time.Time  `json:"startsAt"`
	EndsAt      time.Time  `json:"endsAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	RevokedAt   *time.Time `json:"revokedAt"`
	// RevokeReason: owner, operator, group_change, account_move,
	// account_purge or user_removed; null while not revoked.
	RevokeReason *string `json:"revokeReason"`
	// State: active, scheduled (not started), expired, revoked, or
	// suspended (inside its window, but the owner or the auditor is
	// locked, M2).
	State    string             `json:"state"`
	Accounts []grantAccountJSON `json:"accounts"`
}

// grantAccountJSON is a granted account: all its folders (folders null)
// or the named ones.
type grantAccountJSON struct {
	Name    string   `json:"name"`
	Folders []string `json:"folders"`
}

func toGrantJSON(g store.AuditorGrant, now time.Time) grantJSON {
	out := grantJSON{
		ID: g.ID, Owner: g.Owner, Label: g.Label, PeriodFrom: g.PeriodFrom, PeriodUntil: g.PeriodUntil,
		StartsAt: g.StartsAt, EndsAt: g.EndsAt, CreatedAt: g.CreatedAt, RevokedAt: g.RevokedAt, Accounts: []grantAccountJSON{},
	}
	if g.AuditorID != nil {
		name := g.Auditor
		out.Auditor = &name
	}
	if g.RevokeReason != "" {
		reason := g.RevokeReason
		out.RevokeReason = &reason
	}
	switch {
	case g.RevokedAt != nil:
		out.State = "revoked"
	case g.Active:
		out.State = "active"
	case now.Before(g.StartsAt):
		out.State = "scheduled"
	case !now.Before(g.EndsAt):
		out.State = "expired"
	default:
		out.State = "suspended"
	}
	for _, a := range g.Accounts {
		out.Accounts = append(out.Accounts, grantAccountJSON{Name: a.Name, Folders: a.Folders})
	}
	return out
}

// handleListGrants lists the caller's grants with their history: an
// auditor's own (only of owners in the auditor's current group, accounts
// only of active grants, decision 31), everyone else's as owner.
func (s *Server) handleListGrants(w http.ResponseWriter, r *http.Request) {
	sess := currentSession(r)
	var grants []store.AuditorGrant
	var err error
	if sess.IsAuditor {
		grants, err = s.store.AuditorGrants(r.Context(), sess.UserID)
	} else {
		grants, err = s.store.OwnerGrants(r.Context(), sess.UserID)
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	now := time.Now()
	out := make([]grantJSON, 0, len(grants))
	for _, g := range grants {
		out = append(out, toGrantJSON(g, now))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"grants": out})
}

// pathGrantID reads {id}; anything that is no ID answers like an unknown
// grant.
func (s *Server) pathGrantID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.fail(w, r, http.StatusNotFound, errNoSuchGrant, nil)
		return 0, false
	}
	return id, true
}

// handleGetGrant returns one of the caller's grants.
func (s *Server) handleGetGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathGrantID(w, r)
	if !ok {
		return
	}
	g, err := s.store.OwnerGrant(r.Context(), id, userID(r))
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, errNoSuchGrant, nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, toGrantJSON(*g, time.Now()))
}

type newGrantInput struct {
	// Auditor is the auditor's exact login name, in the owner's group.
	Auditor  string            `json:"auditor"`
	Label    string            `json:"label"`
	Accounts []grantAccountArg `json:"accounts"`
	// PeriodFrom and PeriodUntil limit the messages by date, [from,
	// until); null is no bound.
	PeriodFrom  *time.Time `json:"periodFrom"`
	PeriodUntil *time.Time `json:"periodUntil"`
	// StartsAt null starts the grant now; EndsAt is required.
	StartsAt        *time.Time `json:"startsAt"`
	EndsAt          *time.Time `json:"endsAt"`
	CurrentPassword string     `json:"currentPassword"`
	// Code is a TOTP or recovery code, needed when the owner has 2FA on.
	Code string `json:"code"`
}

// grantAccountArg names one of the owner's accounts and, unless Folders
// is null (all folders, also later ones), the folders granted.
type grantAccountArg struct {
	Name    string    `json:"name"`
	Folders *[]string `json:"folders"`
}

// handleCreateGrant grants an auditor of the owner's group read access to
// the owner's accounts. It needs the owner's password and, with 2FA, a
// code (step-up, as for API keys). Unknown, member, admin and other-group
// names all get the same 404.
func (s *Server) handleCreateGrant(w http.ResponseWriter, r *http.Request) {
	var in newGrantInput
	if !s.decode(w, r, &in) {
		return
	}
	in.Label = strings.TrimSpace(in.Label)
	switch {
	case in.Label == "":
		s.fail(w, r, http.StatusBadRequest, "give the grant a label", nil)
		return
	case len(in.Accounts) == 0:
		s.fail(w, r, http.StatusBadRequest, "name at least one account", nil)
		return
	case in.EndsAt == nil:
		s.fail(w, r, http.StatusBadRequest, "endsAt is required", nil)
		return
	}
	if !s.checkOwnLogin(w, r, in.CurrentPassword, in.Code) {
		return
	}
	sess := currentSession(r)
	auditor, err := s.store.GetAuditorByNameInGroup(r.Context(), auth.NormalizeUserName(in.Auditor), sess.GroupID)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, errNoAuditorInGroup, nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	g := store.NewGrant{
		OwnerID: sess.UserID, AuditorID: auditor.ID, Label: in.Label,
		PeriodFrom: in.PeriodFrom, PeriodUntil: in.PeriodUntil, StartsAt: in.StartsAt, EndsAt: *in.EndsAt,
	}
	for _, arg := range in.Accounts {
		a, err := s.store.GetOwnedAccount(r.Context(), sess.UserID, arg.Name)
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, errNoSuchAccount, nil)
			return
		}
		if err != nil {
			s.failStore(w, r, err)
			return
		}
		ga := store.GrantAccount{AccountID: a.ID}
		if arg.Folders != nil {
			folders, err := s.store.ListFolderStates(r.Context(), a.ID)
			if err != nil {
				s.failStore(w, r, err)
				return
			}
			ga.Folders = []int64{}
			for _, name := range *arg.Folders {
				id := int64(0)
				for _, f := range folders {
					if f.Name == name {
						id = f.ID
					}
				}
				if id == 0 {
					s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("account %q has no folder %q", a.Name, name), nil)
					return
				}
				ga.Folders = append(ga.Folders, id)
			}
		}
		g.Accounts = append(g.Accounts, ga)
	}
	e := s.targetedEntry(r, store.ActionAuditorGrant)
	id, err := s.store.CreateGrant(withAudit(r, e), g)
	switch {
	case errors.Is(err, store.ErrGrantOtherGroup), errors.Is(err, store.ErrGrantNotAuditor):
		// The auditor moved or changed since the lookup: the same answer.
		s.fail(w, r, http.StatusNotFound, errNoAuditorInGroup, nil)
		return
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, errNoSuchAccount, nil)
		return
	case errors.Is(err, store.ErrGrantSMTP):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case errors.Is(err, store.ErrGrantInvalid):
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("auditor grant created", "id", id, "owner", sess.UserName, "auditor", auditor.Name)
	created, err := s.store.OwnerGrant(r.Context(), id, sess.UserID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, toGrantJSON(*created, time.Now()))
}

type extendGrantInput struct {
	EndsAt          *time.Time `json:"endsAt"`
	CurrentPassword string     `json:"currentPassword"`
	Code            string     `json:"code"`
}

// handleExtendGrant moves the end of one of the owner's unrevoked grants
// later (step-up as for creating one).
func (s *Server) handleExtendGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathGrantID(w, r)
	if !ok {
		return
	}
	var in extendGrantInput
	if !s.decode(w, r, &in) {
		return
	}
	if in.EndsAt == nil {
		s.fail(w, r, http.StatusBadRequest, "endsAt is required", nil)
		return
	}
	if !s.checkOwnLogin(w, r, in.CurrentPassword, in.Code) {
		return
	}
	sess := currentSession(r)
	e := s.targetedEntry(r, store.ActionAuditorExtend)
	err := s.store.ExtendGrant(withAudit(r, e), id, sess.UserID, *in.EndsAt)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, errNoSuchGrant, nil)
		return
	case errors.Is(err, store.ErrGrantRevoked):
		s.fail(w, r, http.StatusConflict, "the grant was revoked; create a new one", nil)
		return
	case errors.Is(err, store.ErrGrantOtherGroup), errors.Is(err, store.ErrGrantNotAuditor):
		s.fail(w, r, http.StatusConflict, "the grant can no longer be extended; create a new one", nil)
		return
	case errors.Is(err, store.ErrGrantInvalid):
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("auditor grant extended", "id", id, "owner", sess.UserName)
	g, err := s.store.OwnerGrant(r.Context(), id, sess.UserID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, toGrantJSON(*g, time.Now()))
}

// handleRevokeGrant ends a grant at once, with its sessions: an owner's
// own grant, or for an admin any grant (decision 11). It needs no
// password: revoking only takes power away.
func (s *Server) handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathGrantID(w, r)
	if !ok {
		return
	}
	sess := currentSession(r)
	me := sess.UserID
	err := s.store.RevokeGrant(withAudit(r, s.targetedEntry(r, store.ActionAuditorRevoke)), id, &me, &me)
	if errors.Is(err, store.ErrNotFound) && sess.IsAdmin {
		// Not the admin's own: as the operator.
		err = s.store.RevokeGrant(withAudit(r, s.targetedEntry(r, store.ActionAuditorRevoke)), id, nil, &me)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, errNoSuchGrant, nil)
		return
	case errors.Is(err, store.ErrGrantRevoked):
		s.fail(w, r, http.StatusConflict, "the grant is revoked already", nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("auditor grant revoked", "id", id, "by", sess.UserName)
	w.WriteHeader(http.StatusNoContent)
}
