package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// API keys let scripts call the read and sync routes with
// "Authorization: Bearer ma_…". A key request never reads cookies, also
// when the key is invalid, and needs no Origin header: the credential is
// the header itself, which a cross-origin page cannot set without a CORS
// preflight that the server never answers. A key does no more than a
// session of its owner could: the owner's role, lock state and gates are
// read on every request.

// Rates of key requests.
const (
	apiKeyLimit    = 600 // requests per key and minute
	badAPIKeyLimit = 30  // invalid keys per client address and minute
	apiKeyWindow   = time.Minute
)

// API key expiry, in days.
const maxAPIKeyDays = 3650

type apiKeyKey struct{}

// currentAPIKey returns the key of a request that passed authenticate with
// a key, or nil for a session.
func currentAPIKey(r *http.Request) *store.APIKey {
	k, _ := r.Context().Value(apiKeyKey{}).(*store.APIKey)
	return k
}

// bearerKey returns the API key of a request whose Authorization header is
// "Bearer ma_…" (the scheme in any case). Other Authorization headers, such
// as Basic from a reverse proxy, are not key requests.
func bearerKey(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, strings.HasPrefix(token, auth.APIKeyPrefix)
}

// requiredScope is the scope a route needs from a key; empty means a key
// cannot use the route.
func requiredScope(a access) string {
	switch a {
	case accessRead:
		return store.ScopeRead
	case accessSync:
		return store.ScopeSync
	}
	return ""
}

// authenticateKey checks the key of a key request for a route with access
// a. It answers the request and returns nil when the key is refused.
func (s *Server) authenticateKey(w http.ResponseWriter, r *http.Request, key string, a access) *http.Request {
	addr := s.clientAddr(r)
	if wait := s.badKeyRate.Blocked(addr); wait > 0 {
		s.tooMany(w, wait)
		return nil
	}
	hash := auth.HashAPIKey(key)
	var sess *store.Session
	var k *store.APIKey
	err := store.ErrNotFound
	if auth.ValidAPIKeyFormat(key) {
		sess, k, err = s.store.APIKeyLogin(r.Context(), hash)
	}
	if errors.Is(err, store.ErrNotFound) {
		s.badKeyRate.Allow(addr)
		s.auditRefusedKey(r, hash)
		w.Header().Set("WWW-Authenticate", `Bearer realm="mail-archive", error="invalid_token"`)
		s.fail(w, r, http.StatusUnauthorized, "invalid API key", nil)
		return nil
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return nil
	}
	if ok, wait := s.apiKeyRate.Allow("k" + strconv.FormatInt(k.ID, 10)); !ok {
		s.tooMany(w, wait)
		return nil
	}
	if sess.MustChangePassword {
		s.writeJSON(w, http.StatusForbidden, passwordChangeJSON{Error: "the owner of this API key must choose their own password first", PasswordChangeRequired: true})
		return nil
	}
	if s.twoFactorRequired(sess.IsAdmin) && !sess.TwoFactorEnabled {
		s.writeJSON(w, http.StatusForbidden, twoFactorSetupJSON{Error: "the owner of this API key must set up two-factor authentication first", TwoFactorSetupRequired: true})
		return nil
	}
	scope := requiredScope(a)
	if scope == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mail-archive", error="insufficient_scope"`)
		s.fail(w, r, http.StatusForbidden, "not available with an API key", nil)
		return nil
	}
	if !k.HasScope(scope) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="mail-archive", error="insufficient_scope", scope=%q`, scope))
		s.writeJSON(w, http.StatusForbidden, scopeJSON{Error: "this API key lacks the " + scope + " scope", Scope: scope})
		return nil
	}
	if k.LastUsedAt == nil || s.currentTime().Sub(*k.LastUsedAt) > touchInterval {
		if err := s.store.TouchAPIKey(r.Context(), k.ID); err != nil {
			s.log.Warn("update API key", "err", err)
		}
	}
	ctx := context.WithValue(r.Context(), sessionKey{}, sess)
	return r.WithContext(context.WithValue(ctx, apiKeyKey{}, k))
}

type scopeJSON struct {
	Error string `json:"error"`
	Scope string `json:"scope"`
}

// auditRefusedKey records a refused key, with its name and owner if it
// exists. The key itself is never written anywhere.
func (s *Server) auditRefusedKey(r *http.Request, hash []byte) {
	e := s.auditEntry(r, "auth.login_failed")
	e.Detail("method", "api_key")
	e.Detail("reason", "invalid")
	k, err := s.store.FindRefusedAPIKey(r.Context(), hash)
	switch {
	case err == nil:
		e.SetUser(k.UserID, k.UserName)
		e.Detail("reason", k.Reason)
		e.Detail("key_id", k.ID)
		e.Detail("key_name", k.Name)
	case !errors.Is(err, store.ErrNotFound):
		s.log.Warn("look up refused API key", "err", err)
	}
	s.auditBestEffort(r, e)
}

// apiKeyJSON describes a key, never the key itself except in the answer
// to its creation.
type apiKeyJSON struct {
	ID         int64      `json:"id"`
	User       string     `json:"user,omitempty"`
	Name       string     `json:"name"`
	Hint       string     `json:"hint"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	State      string     `json:"state"`
}

func (s *Server) apiKeyJSON(k *store.APIKey, withUser bool) apiKeyJSON {
	out := apiKeyJSON{
		ID: k.ID, Name: k.Name, Hint: k.Hint, Scopes: k.Scopes, CreatedAt: k.CreatedAt,
		ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt, RevokedAt: k.RevokedAt, State: k.State(s.currentTime()),
	}
	if withUser {
		out.User = k.UserName
	}
	return out
}

type apiKeysJSON struct {
	Max  int          `json:"max,omitempty"`
	Keys []apiKeyJSON `json:"keys"`
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListAPIKeys(r.Context(), userID(r))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := apiKeysJSON{Max: store.MaxAPIKeys, Keys: make([]apiKeyJSON, 0, len(keys))}
	for i := range keys {
		out.Keys = append(out.Keys, s.apiKeyJSON(&keys[i], false))
	}
	s.writeJSON(w, http.StatusOK, out)
}

type newAPIKeyInput struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	// ExpiresInDays is 1 to 3650, or null for a key that does not expire.
	ExpiresInDays   *int   `json:"expiresInDays"`
	CurrentPassword string `json:"currentPassword"`
	// Code is a TOTP or recovery code, needed when the user has 2FA on.
	Code string `json:"code"`
}

type newAPIKeyJSON struct {
	apiKeyJSON
	// Key is shown only here, once.
	Key string `json:"key"`
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var in newAPIKeyInput
	if !s.decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !validCredentialName(in.Name) {
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("give the API key a name of 1 to %d characters", maxCredentialName), nil)
		return
	}
	scopes, err := store.NormalizeScopes(in.Scopes)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "choose the scopes read, sync or both", nil)
		return
	}
	var expires *time.Time
	if in.ExpiresInDays != nil {
		if *in.ExpiresInDays < 1 || *in.ExpiresInDays > maxAPIKeyDays {
			s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("expiresInDays must be 1 to %d, or null for no expiry", maxAPIKeyDays), nil)
			return
		}
		t := s.currentTime().Add(time.Duration(*in.ExpiresInDays) * 24 * time.Hour)
		expires = &t
	}
	if !s.checkOwnLogin(w, r, in.CurrentPassword, in.Code) {
		return
	}
	key, hash, hint, err := auth.NewAPIKey()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	sess := currentSession(r)
	e := s.auditEntry(r, "api_key.create")
	e.SetTarget(store.TargetUser, strconv.FormatInt(sess.UserID, 10), sess.UserName)
	e.Detail("key_name", in.Name)
	e.Detail("scopes", scopes)
	if expires != nil {
		e.Detail("expires_at", expires.UTC().Format(time.RFC3339))
	}
	k, err := s.store.CreateAPIKey(withAudit(r, e), sess.UserID, store.NewAPIKey{
		Hash: hash, Hint: hint, Name: in.Name, Scopes: scopes, ExpiresAt: expires,
	})
	switch {
	case errors.Is(err, store.ErrAPIKeyLimit):
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("you already have %d API keys; revoke one first", store.MaxAPIKeys), nil)
		return
	case errors.Is(err, store.ErrAPIKeyName):
		s.fail(w, r, http.StatusConflict, "you already have an API key with this name", nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("API key created", "user", sess.UserName, "key", k.Name, "scopes", strings.Join(scopes, ","))
	s.writeJSON(w, http.StatusCreated, newAPIKeyJSON{apiKeyJSON: s.apiKeyJSON(k, false), Key: key})
}

// handleRevokeOwnAPIKey revokes one of the user's keys. It needs no
// password: revoking only takes power away.
func (s *Server) handleRevokeOwnAPIKey(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	s.revokeAPIKey(w, r, &uid, "user")
}

func (s *Server) handleListAllAPIKeys(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	keys, err := s.store.ListAllAPIKeys(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := apiKeysJSON{Keys: make([]apiKeyJSON, 0, len(keys))}
	for i := range keys {
		out.Keys = append(out.Keys, s.apiKeyJSON(&keys[i], true))
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handleRevokeAnyAPIKey lets an admin revoke any key, also their own.
func (s *Server) handleRevokeAnyAPIKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	s.revokeAPIKey(w, r, nil, "admin")
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request, owner *int64, reason string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not found", nil)
		return
	}
	e := s.auditEntry(r, "api_key.revoke")
	e.Detail("reason", reason)
	by := userID(r)
	k, err := s.store.RevokeAPIKey(withAudit(r, e), id, owner, &by)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("API key revoked", "user", k.UserName, "key", k.Name, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}
