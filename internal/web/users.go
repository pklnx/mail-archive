package web

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// User management for admins. Admins manage logins only: they never see
// other users' accounts or mail, just how many accounts a user has.

type adminUserJSON struct {
	Name               string     `json:"name"`
	Admin              bool       `json:"admin"`
	Locked             bool       `json:"locked"`
	MustChangePassword bool       `json:"mustChangePassword"`
	TwoFactorEnabled   bool       `json:"twoFactorEnabled"`
	Accounts           int64      `json:"accounts"`
	CreatedAt          time.Time  `json:"createdAt"`
	LastLoginAt        *time.Time `json:"lastLoginAt"`
	// Self marks the logged-in admin, whose own row cannot be changed here.
	Self bool `json:"self"`
}

// requireAdmin rejects requests from users who are not admins.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if sess := currentSession(r); sess == nil || !sess.IsAdmin {
		s.fail(w, r, http.StatusForbidden, "only admins can manage users", nil)
		return false
	}
	return true
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	owned, err := s.store.CountOwnedAccounts(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := make([]adminUserJSON, 0, len(users))
	for _, u := range users {
		out = append(out, adminUserJSON{
			Name: u.Name, Admin: u.IsAdmin, Locked: u.LockedAt != nil, MustChangePassword: u.MustChangePassword,
			Accounts: owned[u.ID], CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt, Self: u.ID == userID(r),
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

type newUserInput struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

// generatedPasswordJSON carries a generated password. It is shown to the
// admin once and never stored or logged in plain text.
type generatedPasswordJSON struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var in newUserInput
	if !s.decode(w, r, &in) {
		return
	}
	name := auth.NormalizeUserName(in.Name)
	if err := auth.ValidateUserName(name); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	pw, hash, ok := s.generatePassword(w, r)
	if !ok {
		return
	}
	if _, err := s.store.CreateUserWithGeneratedPassword(r.Context(), name, hash, in.Admin); err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.fail(w, r, http.StatusConflict, fmt.Sprintf("a user named %q already exists", name), nil)
			return
		}
		s.failStore(w, r, err)
		return
	}
	s.log.Info("user added", "user", name, "admin", in.Admin, "by", currentSession(r).UserName)
	s.writeJSON(w, http.StatusCreated, generatedPasswordJSON{Name: name, Password: pw})
}

func (s *Server) generatePassword(w http.ResponseWriter, r *http.Request) (pw, hash string, ok bool) {
	pw, err := auth.GeneratePassword()
	if err == nil {
		hash, err = s.hasher.Hash(r.Context(), pw)
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return "", "", false
	}
	return pw, hash, true
}

// pathUser loads the user named in the URL for an admin action on someone
// else: admins change their own login through the profile page.
func (s *Server) pathUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	if !s.requireAdmin(w, r) {
		return nil, false
	}
	u, err := s.store.GetUserByName(r.Context(), auth.NormalizeUserName(r.PathValue("name")))
	if err != nil {
		s.failStore(w, r, err)
		return nil, false
	}
	if u.ID == userID(r) {
		s.fail(w, r, http.StatusForbidden, "you cannot change your own login here; use the profile page, or ask another admin", nil)
		return nil, false
	}
	return u, true
}

func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	pw, hash, ok := s.generatePassword(w, r)
	if !ok {
		return
	}
	if err := s.store.SetUserPassword(r.Context(), u.ID, hash, true); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("password reset", "user", u.Name, "by", currentSession(r).UserName)
	s.writeJSON(w, http.StatusOK, generatedPasswordJSON{Name: u.Name, Password: pw})
}

// userChange is the body of PATCH /api/users/{name}: exactly one field.
type userChange struct {
	Admin  *bool `json:"admin"`
	Locked *bool `json:"locked"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	var in userChange
	if !s.decode(w, r, &in) {
		return
	}
	var err error
	switch {
	case in.Admin != nil && in.Locked == nil:
		err = s.store.SetUserAdmin(r.Context(), u.ID, *in.Admin)
	case in.Locked != nil && in.Admin == nil:
		err = s.store.SetUserLocked(r.Context(), u.ID, *in.Locked)
	default:
		s.fail(w, r, http.StatusBadRequest, "send exactly one of admin and locked", nil)
		return
	}
	if errors.Is(err, store.ErrLastAdmin) {
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("%q is the last admin who can log in", u.Name), nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("user changed", "user", u.Name, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteUser(r.Context(), u.ID)
	switch {
	case errors.Is(err, store.ErrLastAdmin):
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("%q is the last admin who can log in", u.Name), nil)
		return
	case errors.Is(err, store.ErrOwnsAccounts):
		n := int64(0)
		if owned, cerr := s.store.CountOwnedAccounts(r.Context()); cerr == nil {
			n = owned[u.ID]
		}
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("%q still owns %d account(s); move them to another user on the command line first (account move)", u.Name, n), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("user removed", "user", u.Name, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}

type passwordChangeInput struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// handleChangeOwnPassword lets every user change their password, also a
// generated one they must replace. The current password is checked with the
// same limits as a login.
func (s *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	var in passwordChangeInput
	if !s.decode(w, r, &in) {
		return
	}
	sess := currentSession(r)
	addr := clientAddr(r)
	if wait := s.limiter.Blocked(sess.UserName, addr); wait > 0 {
		secs := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		s.writeJSON(w, http.StatusTooManyRequests, retryJSON{Error: "too many failed attempts; try again later", RetryAfter: secs})
		return
	}
	u, err := s.store.GetUserByName(r.Context(), sess.UserName)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	ok := false
	if len(in.Current) <= auth.MaxPasswordLength {
		if ok, _, err = s.hasher.Verify(r.Context(), u.PasswordHash, in.Current); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
	}
	if !ok {
		s.limiter.Fail(sess.UserName, addr)
		s.fail(w, r, http.StatusUnauthorized, "the current password is wrong", nil)
		return
	}
	s.limiter.Succeed(sess.UserName)
	if err := auth.ValidatePassword(in.New); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if in.New == in.Current {
		s.fail(w, r, http.StatusBadRequest, "choose a password different from the current one", nil)
		return
	}
	hash, err := s.hasher.Hash(r.Context(), in.New)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if err := s.store.ChangeOwnPassword(r.Context(), u.ID, hash, auth.HashSessionToken(sessionToken(r))); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("password changed", "user", u.Name)
	w.WriteHeader(http.StatusNoContent)
}


func (s *Server) handleResetUserTwoFactor(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	if err := s.store.ResetTwoFactor(r.Context(), u.ID); err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			s.fail(w, r, http.StatusConflict, "cannot reset the last usable administrator's two-factor authentication", nil)
			return
		}
		s.failStore(w, r, err)
		return
	}
	s.log.Info("2fa reset", "user", u.Name, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}
