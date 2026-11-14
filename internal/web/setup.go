package web

import (
	"errors"
	"net/http"

	"github.com/keepia/archive/internal/auth"
	"github.com/keepia/archive/internal/store"
)

// setupPath creates the first admin with the setup token (#396, part 3).
const setupPath = "/api/setup"

// Answers of POST /api/setup.
const (
	errSetupUnavailable = "setup not available"
	errSetupToken       = "wrong or expired setup token"
)

type setupInput struct {
	// Token is the setup token from the link's fragment (#setup=<token>):
	// the fragment never reaches the server, the UI sends it here.
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleSetup creates the first admin of a new instance with the setup
// token the operator configured (#396, part 3; maintainer decision Q1). It
// works only while no user exists, setup was never completed, and the
// token matches and is under 7 days old (store.CompleteSetup). The answer
// is a login: 201 with a session cookie; the admin then sets up 2FA.
//
// Wrong or expired tokens count as failed logins of the client's address
// (auth.Limiter.BeginAddr), so a block answers 429. The token is checked
// before the password is hashed, so wrong tokens cost no hashing. The
// token is never logged and never stored, only its SHA-256, compared in
// constant time.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in setupInput
	if !s.decode(w, r, &in) {
		return
	}
	a, wait, err := s.limiter.BeginAddr(r.Context(), s.clientAddr(r))
	if s.attemptRefused(w, r, wait, err, "too many failed attempts; try again later") {
		return
	}
	defer a.Release()
	ctx := r.Context()
	now := s.currentTime()
	hash := store.HashSetupToken(in.Token)
	if !s.setupAllowed(w, r, a, s.store.CheckSetupToken(ctx, hash, now)) {
		return
	}
	name := auth.NormalizeUserName(in.Username)
	if err := auth.ValidateUserName(name); err != nil {
		a.Release()
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if err := auth.ValidatePassword(in.Password); err != nil {
		a.Release()
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	pwHash, err := s.hasher.Hash(ctx, in.Password)
	if err != nil {
		a.Release()
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	e := s.targetedEntry(r, "user.setup")
	e.SetTarget(store.TargetUser, "", name) // the ID is set when it exists
	e.Detail("method", "setup_token")
	e.Detail("admin", true)
	u, err := s.store.CompleteSetup(withAudit(r, e), hash, now, store.NewUser{Name: name, PasswordHash: pwHash})
	if !s.setupAllowed(w, r, a, err) {
		return
	}
	a.Succeed()
	s.log.Info("first admin created with the setup link", "user", u.Name, "addr", s.clientIP(r))

	// Logged in, as after a password login.
	s.deleteOldSessions(r)
	token, sessHash, err := auth.NewSessionToken()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	login := s.selfEntry(r, "auth.login")
	login.SetUser(u.ID, u.Name)
	login.Detail("method", "setup_token")
	if err := s.store.StartSession(withAudit(r, login), sessHash, u.ID, s.currentTime().Add(auth.MaxSessionAge), truncateUserAgent(r.UserAgent())); err != nil {
		s.failStore(w, r, err)
		return
	}
	body, err := s.loginBody(ctx, u, sessHash, false)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.setSessionCookie(w, r, token, auth.MaxSessionAge)
	s.writeJSON(w, http.StatusCreated, body)
}

// setupAllowed answers a refused setup attempt and ends a: 404 when setup
// is not available (complete, or no token set: the two look the same),
// 401 for a wrong or expired token, counted as a failure of the address.
// It reports whether err allows the setup to go on.
func (s *Server) setupAllowed(w http.ResponseWriter, r *http.Request, a *auth.Attempt, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrSetupUnavailable):
		a.Release()
		s.fail(w, r, http.StatusNotFound, errSetupUnavailable, nil)
	case errors.Is(err, store.ErrSetupTokenInvalid):
		a.Fail()
		s.log.Info("setup failed: wrong or expired setup token", "addr", s.clientIP(r))
		s.fail(w, r, http.StatusUnauthorized, errSetupToken, nil)
	case errors.Is(err, store.ErrConflict):
		// A user of that name exists: setup is complete then.
		a.Release()
		s.fail(w, r, http.StatusNotFound, errSetupUnavailable, nil)
	default:
		a.Release()
		s.failStore(w, r, err)
	}
	return false
}
