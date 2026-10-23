package web

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// Session cookie names. Over HTTPS the __Host- prefix makes the browser
// insist on Secure, Path=/ and no Domain, so no other host can set it.
const (
	cookieName       = "mail_archive_session"
	secureCookieName = "__Host-" + cookieName
)

// touchInterval limits how often a session's last use is written.
const touchInterval = time.Minute

type sessionKey struct{}

// profilePasswordPath is the one endpoint a user with a generated password
// may use (besides the session endpoints).
const profilePasswordPath = "/api/profile/password" //nolint:gosec // a URL path, not a credential

// The passkey login endpoints, which need no session.
//
//nolint:gosec // URL paths, not credentials
const (
	passkeyLoginBeginPath  = "/api/session/passkey/begin"
	passkeyLoginFinishPath = "/api/session/passkey/finish"
)

type twoFactorSetupJSON struct {
	Error                  string `json:"error"`
	TwoFactorSetupRequired bool   `json:"twoFactorSetupRequired"`
}

type passwordChangeJSON struct {
	Error                  string `json:"error"`
	PasswordChangeRequired bool   `json:"passwordChangeRequired"`
}

// currentSession returns the session of a request that passed authenticate.
func currentSession(r *http.Request) *store.Session {
	sess, _ := r.Context().Value(sessionKey{}).(*store.Session)
	return sess
}

// userID returns the logged-in user of a request that passed authenticate.
// Without a session it returns 0, which matches no owner.
func userID(r *http.Request) int64 {
	if sess := currentSession(r); sess != nil {
		return sess.UserID
	}
	return 0
}

// secureRequest reports whether the browser reached the server over HTTPS,
// directly or through a reverse proxy or `tailscale serve`. X-Forwarded-Proto
// counts only from a trusted proxy (MAIL_ARCHIVE_TRUSTED_PROXIES) or from
// loopback, where `tailscale serve` and a proxy on the same host connect
// from: anybody else could set it.
func (s *Server) secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if r.Header.Get("X-Forwarded-Proto") != "https" {
		return false
	}
	peer := peerAddr(r)
	return peer.IsLoopback() || s.trustedProxy(peer)
}

// warnUntrustedForwardedProto logs once per server (so once per process)
// when a request without TLS carries X-Forwarded-Proto: https from a peer
// that is neither loopback nor a trusted proxy: the header does not count
// (secureRequest), so the session cookie is not __Host-/Secure and no HSTS
// is sent. Behind a proxy that does not connect from loopback, such as one
// on the host of a Docker Compose installation, MAIL_ARCHIVE_TRUSTED_PROXIES
// must name it. Only the peer is logged; headers come from the client.
func (s *Server) warnUntrustedForwardedProto(r *http.Request) {
	if s.forwardedProtoWarned.Load() || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") != "https" {
		return
	}
	peer := peerAddr(r)
	if peer.IsLoopback() || s.trustedProxy(peer) || !s.forwardedProtoWarned.CompareAndSwap(false, true) {
		return
	}
	s.log.Warn("X-Forwarded-Proto from a peer that is neither loopback nor a trusted proxy is ignored: the session cookie is not "+
		"__Host-/Secure and no HSTS is sent; behind a reverse proxy set "+config.EnvTrustedProxies+" to its address", "peer", peer)
}

// sessionTokenKey holds the token of the request's valid session, as found
// by authenticate.
type sessionTokenKey struct{}

// sessionTokens returns the tokens of all of the request's session
// cookies, the __Host- one first. A browser can hold both: a __Host- cookie
// from a time the server saw HTTPS and a plain one from after, for example
// when X-Forwarded-Proto stopped counting because the proxy is not trusted.
//
// Each token costs a database lookup, so at most maxSessionCookies distinct
// tokens per name are returned, however many cookies a request carries.
func sessionTokens(r *http.Request) []string {
	return cookieValues(r, []string{secureCookieName, cookieName}, maxSessionCookies)
}

// maxSessionCookies is how many cookies of one session cookie name count.
// A browser sends one per name and path; more are junk or an attack.
const maxSessionCookies = 2

// cookieValues returns the values of the request's cookies with the given
// names, name by name in that order and each name in header order. Empty
// values and values already returned are skipped; at most perName values
// of one name are returned, the rest of that name is not read.
func cookieValues(r *http.Request, names []string, perName int) []string {
	var values []string
	seen := make(map[string]bool)
	cookies := r.Cookies()
	for _, name := range names {
		n := 0
		for _, c := range cookies {
			if c.Name != name || c.Value == "" || seen[c.Value] {
				continue
			}
			if n == perName {
				break
			}
			seen[c.Value] = true
			values = append(values, c.Value)
			n++
		}
	}
	return values
}

// sessionToken returns the token of the request's valid session, as found
// by authenticate, and without one the first session cookie's token.
func sessionToken(r *http.Request) string {
	if token, ok := r.Context().Value(sessionTokenKey{}).(string); ok {
		return token
	}
	if tokens := sessionTokens(r); len(tokens) > 0 {
		return tokens[0]
	}
	return ""
}

// deleteOldSessions ends the sessions of all of the request's session
// cookies before a login, so a token planted before the login is never
// promoted to a valid session.
func (s *Server) deleteOldSessions(r *http.Request) {
	for _, token := range sessionTokens(r) {
		_ = s.store.DeleteSession(r.Context(), auth.HashSessionToken(token))
	}
}

// setSessionCookie sets the session cookie, over HTTPS (secureRequest) as
// the __Host- one; a negative maxAge expires it.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge time.Duration) {
	c := &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
	if maxAge < 0 {
		c.MaxAge = -1 // expire now; a sub-second duration would round to no Max-Age
	}
	if s.secureRequest(r) {
		c.Name, c.Secure = secureCookieName, true
	}
	http.SetCookie(w, c)
	if !c.Secure {
		// Drop a __Host- cookie from a time the server saw HTTPS, so it
		// does not outlive its value next to the plain one. A browser that
		// is on HTTPS (behind a proxy that no longer counts) removes it;
		// one on plain HTTP ignores a Secure cookie, which does no harm.
		http.SetCookie(w, &http.Cookie{Name: secureCookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	}
}

// clearSessionCookies expires both session cookies.
func (s *Server) clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	s.setSessionCookie(w, r, "", -1)
	if s.secureRequest(r) {
		// Also drop a cookie set before HTTPS was in front of the server.
		http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	}
}

// session looks up the request's session; nil means none or not valid.
func (s *Server) session(r *http.Request) (*store.Session, error) {
	sess, _, err := s.sessionWithToken(r)
	return sess, err
}

// sessionWithToken looks up the request's session and returns it with its
// token. Of several session cookies the first valid one counts, not the
// first present one: a stale cookie next to a valid one does not log the
// user out.
func (s *Server) sessionWithToken(r *http.Request) (*store.Session, string, error) {
	for _, token := range sessionTokens(r) {
		hash := auth.HashSessionToken(token)
		sess, err := s.store.GetSession(r.Context(), hash, s.now().Add(-auth.IdleTimeout))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if time.Since(sess.LastSeenAt) > touchInterval {
			if err := s.store.TouchSession(r.Context(), hash); err != nil {
				s.log.Warn("update session", "err", err)
			}
		}
		return sess, token, nil
	}
	return nil, "", nil
}

// authenticate lets requests under /api/ through only with a valid session
// or API key, except routes whose access in the route table is public, and
// login routes for requests without a key. A path without a route, or with
// a method no route takes, counts as session-only, so it answers 401
// without a login. New endpoints are protected by default (accessSession
// is the zero value). /healthz and the UI code (/, /assets/) are public;
// they contain no data.
func (s *Server) authenticate(mux *http.ServeMux, accessOf map[string]access) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			mux.ServeHTTP(w, r)
			return
		}
		_, pattern := mux.Handler(r)
		a := accessOf[pattern]
		if a == accessPublic {
			mux.ServeHTTP(w, r)
			return
		}
		// A key request never falls back to the session cookie.
		if key, ok := bearerKey(r); ok {
			if r = s.authenticateKey(w, r, key, a); r != nil {
				mux.ServeHTTP(w, r)
			}
			return
		}
		if a == accessLogin {
			mux.ServeHTTP(w, r)
			return
		}
		sess, token, err := s.sessionWithToken(r)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
		if sess == nil {
			s.fail(w, r, http.StatusUnauthorized, "login required", nil)
			return
		}
		// A generated password, and a required 2FA that is not set up yet,
		// both restrict the session to finishing the setup: first the
		// password, then 2FA.
		if sess.MustChangePassword && (r.Method != http.MethodPut || r.URL.Path != profilePasswordPath) {
			s.writeJSON(w, http.StatusForbidden, passwordChangeJSON{Error: "choose your own password first", PasswordChangeRequired: true})
			return
		}
		if !sess.MustChangePassword && s.twoFactorRequired(sess.IsAdmin) && !sess.TwoFactorEnabled &&
			!twoFactorSetupPathAllowed(r.Method, r.URL.Path) {
			s.writeJSON(w, http.StatusForbidden, twoFactorSetupJSON{Error: "set up two-factor authentication first", TwoFactorSetupRequired: true})
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey{}, sess)
		mux.ServeHTTP(w, r.WithContext(context.WithValue(ctx, sessionTokenKey{}, token)))
	})
}

type userJSON struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
	// MustChangePassword: the user logged in with a generated password and
	// must choose their own before anything else works.
	MustChangePassword bool `json:"mustChangePassword"`
	// TwoFactorEnabled and TwoFactorRequired: a user who must use 2FA but
	// has not set it up can only do that until it is on.
	TwoFactorEnabled  bool `json:"twoFactorEnabled"`
	TwoFactorRequired bool `json:"twoFactorRequired"`
	// Language and Locale are the user's web UI settings, "auto" or a tag.
	Language string `json:"language"`
	Locale   string `json:"locale"`
}

func (s *Server) userJSON(name string, admin, mustChange, twoFactor bool, language, locale string) userJSON {
	return userJSON{
		Name: name, Admin: admin, MustChangePassword: mustChange, TwoFactorEnabled: twoFactor, TwoFactorRequired: s.twoFactorRequired(admin),
		Language: language, Locale: locale,
	}
}

type sessionJSON struct {
	User userJSON `json:"user"`
}

type noSessionJSON struct {
	Error string `json:"error"`
	// SetupRequired is true while no user exists; the UI then explains how
	// to create the first admin.
	SetupRequired bool `json:"setupRequired"`
	// PasskeyOrigin is where passkey logins work; empty when they are off.
	PasskeyOrigin string `json:"passkeyOrigin,omitempty"`
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.session(r)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if sess != nil {
		s.writeJSON(w, http.StatusOK, sessionJSON{User: s.userJSON(sess.UserName, sess.IsAdmin, sess.MustChangePassword, sess.TwoFactorEnabled, sess.Language, sess.Locale)})
		return
	}
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	s.writeJSON(w, http.StatusUnauthorized, noSessionJSON{Error: "login required", SetupRequired: n == 0, PasskeyOrigin: s.publicOrigin})
}

type loginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

const errWrongLogin = "wrong user name or password"

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in loginInput
	if !s.decode(w, r, &in) {
		return
	}
	name := auth.NormalizeUserName(in.Username)
	addr := s.clientAddr(r)
	if wait := s.limiter.Blocked(name, addr); wait > 0 {
		secs := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		s.writeJSON(w, http.StatusTooManyRequests, retryJSON{Error: "too many failed logins; try again later", RetryAfter: secs})
		return
	}
	if len(in.Password) > auth.MaxPasswordLength {
		// No stored password is that long; skip the hashing work.
		s.limiter.Fail(name, addr)
		s.auditUnknownLogin(r, name, reasonWrongPassword)
		s.fail(w, r, http.StatusUnauthorized, errWrongLogin, nil)
		return
	}

	ctx := r.Context()
	u, err := s.store.GetUserByName(ctx, name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	ok, rehash := false, false
	if u == nil {
		s.hasher.VerifyDummy(ctx, in.Password)
	} else {
		ok, rehash, err = s.hasher.Verify(ctx, u.PasswordHash, in.Password)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
	}
	if !ok {
		s.limiter.Fail(name, addr)
		s.log.Info("login failed", "user", name, "addr", s.clientIP(r))
		if u == nil {
			s.auditUnknownLogin(r, name, reasonUnknownUser)
		} else {
			s.auditLoginFailed(r, "password", reasonWrongPassword, u)
		}
		s.fail(w, r, http.StatusUnauthorized, errWrongLogin, nil)
		return
	}
	if u.LockedAt != nil {
		s.auditLoginFailed(r, "password", reasonLocked, u)
		s.fail(w, r, http.StatusForbidden, "this user is locked", nil)
		return
	}

	if rehash {
		if h, err := s.hasher.Hash(ctx, in.Password); err == nil {
			err = s.store.RehashUserPassword(ctx, u.ID, h)
			if err != nil {
				s.log.Warn("renew password hash", "user", u.Name, "err", err)
			}
		}
	}
	// With 2FA on (required or voluntary), the password is only the first
	// step: the limiter is reset after the second one.
	if u.TwoFactorEnabled {
		s.startTwoFactorLogin(w, r, u)
		return
	}
	s.limiter.Succeed(name)
	// A fresh token on every login: a token planted before the login is
	// never promoted to a valid session.
	s.deleteOldSessions(r)
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	e := s.auditEntry(r, "auth.login")
	e.SetUser(u.ID, u.Name)
	e.Detail("method", "password")
	if err := s.store.StartSession(withAudit(r, e), hash, u.ID, s.currentTime().Add(auth.MaxSessionAge), truncateUserAgent(r.UserAgent())); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("login", "user", u.Name, "addr", s.clientIP(r))
	s.setSessionCookie(w, r, token, auth.MaxSessionAge)
	s.writeJSON(w, http.StatusOK, sessionJSON{User: s.userJSON(u.Name, u.IsAdmin, u.MustChangePassword, false, u.Language, u.Locale)})
}

type retryJSON struct {
	Error      string `json:"error"`
	RetryAfter int    `json:"retryAfter"` // seconds
}

// handleLogout ends the session. A logout never fails because of the audit
// log: without its entry the session is deleted anyway.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Only a valid session writes a logout entry; every other session
	// cookie's session is deleted too.
	sess, valid, err := s.sessionWithToken(r)
	if err != nil {
		sess = nil
	}
	for _, token := range sessionTokens(r) {
		hash := auth.HashSessionToken(token)
		ctx := r.Context()
		if sess != nil && token == valid {
			e := s.auditEntry(r, "auth.logout")
			e.SetUser(sess.UserID, sess.UserName)
			ctx = withAudit(r, e)
		}
		err := s.store.EndSession(ctx, hash)
		if errors.Is(err, store.ErrAuditUnavailable) {
			s.log.Error("audit log entry lost", "action", "auth.logout", "err", err)
			err = s.store.DeleteSession(r.Context(), hash)
		}
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
	}
	s.clearSessionCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// auditMonthsAhead is how many months of audit log partitions serve keeps
// ready beyond the current one.
const auditMonthsAhead = 3

// housekeeping runs at start and then every hour until ctx ends: it creates
// the audit log partitions ahead and deletes expired sessions, challenges
// and ceremonies, and API keys revoked or expired 30 days ago.
func (s *Server) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := s.store.EnsureAuditPartitions(ctx, auditMonthsAhead); err != nil {
			if ctx.Err() == nil {
				s.log.Error("create audit log partitions", "err", err)
			}
		} else if n > 0 {
			s.log.Info("created audit log partitions", "count", n)
		}
		if err := s.store.DeleteExpiredTwoFactorChallenges(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("delete expired two-factor challenges", "err", err)
		}
		if err := s.store.DeleteExpiredWebAuthnCeremonies(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("delete expired passkey ceremonies", "err", err)
		}
		if n, err := s.store.DeleteOldAPIKeys(ctx); err != nil {
			if ctx.Err() == nil {
				s.log.Warn("delete old API keys", "err", err)
			}
		} else if n > 0 {
			s.log.Debug("deleted API keys revoked or expired 30 days ago", "count", n)
		}
		if n, err := s.store.DeleteExpiredSessions(ctx, time.Now().Add(-auth.IdleTimeout)); err != nil {
			if ctx.Err() == nil {
				s.log.Warn("delete expired sessions", "err", err)
			}
		} else if n > 0 {
			s.log.Debug("deleted expired sessions", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// auditUnknownLogin records a failed password login without a known user.
// The name is kept only if it is a valid user name, so that a password
// typed into the name field is not stored.
func (s *Server) auditUnknownLogin(r *http.Request, name, reason string) {
	e := s.auditEntry(r, "auth.login_failed")
	e.Detail("method", "password")
	e.Detail("reason", reason)
	if auth.ValidateUserName(name) == nil {
		e.Detail("name", name)
	}
	s.auditBestEffort(r, e)
}
