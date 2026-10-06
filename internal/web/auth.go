package web

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
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

type passwordChangeJSON struct {
	Error                  string `json:"error"`
	PasswordChangeRequired bool   `json:"passwordChangeRequired"`
}

// currentSession returns the session of a request that passed requireLogin.
func currentSession(r *http.Request) *store.Session {
	sess, _ := r.Context().Value(sessionKey{}).(*store.Session)
	return sess
}

// userID returns the logged-in user of a request that passed requireLogin.
// Without a session it returns 0, which matches no owner.
func userID(r *http.Request) int64 {
	if sess := currentSession(r); sess != nil {
		return sess.UserID
	}
	return 0
}

// secureRequest reports whether the browser reached the server over HTTPS,
// directly or through a reverse proxy or `tailscale serve`.
func secureRequest(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// sessionToken returns the token from the request's session cookie.
func sessionToken(r *http.Request) string {
	for _, name := range []string{secureCookieName, cookieName} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge time.Duration) {
	c := &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
	if secureRequest(r) {
		c.Name, c.Secure = secureCookieName, true
	}
	http.SetCookie(w, c)
}

func clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	setSessionCookie(w, r, "", -1)
	if secureRequest(r) {
		// Also drop a cookie set before HTTPS was in front of the server.
		http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	}
}

// session looks up the request's session; nil means none or not valid.
func (s *Server) session(r *http.Request) (*store.Session, error) {
	token := sessionToken(r)
	if token == "" {
		return nil, nil
	}
	hash := auth.HashSessionToken(token)
	sess, err := s.store.GetSession(r.Context(), hash, s.now().Add(-auth.IdleTimeout))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if time.Since(sess.LastSeenAt) > touchInterval {
		if err := s.store.TouchSession(r.Context(), hash); err != nil {
			s.log.Warn("update session", "err", err)
		}
	}
	return sess, nil
}

// requireLogin lets requests under /api/ through only with a valid session,
// except the session endpoints themselves. New endpoints are therefore
// protected by default. /healthz and the UI code (/, /assets/) are public;
// they contain no data.
func twoFactorSetupPathAllowed(method, path string) bool {
	if method == http.MethodPut && path == profilePasswordPath { return true }
	if method == http.MethodGet && path == "/api/profile/2fa" { return true }
	if method == http.MethodPost && (path == "/api/profile/2fa/setup" || path == "/api/profile/2fa/confirm") { return true }
	return false
}

func (s *Server) twoFactorRequired(sess *store.Session) bool {
	return sess.IsAdmin || s.require2FA
}

func (s *Server) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/session" || r.URL.Path == "/api/session/2fa" {
			next.ServeHTTP(w, r)
			return
		}
		sess, err := s.session(r)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
		if sess == nil {
			s.fail(w, r, http.StatusUnauthorized, "login required", nil)
			return
		}
		// A generated password and mandatory TOTP both use the same restricted
		// state: the user can only finish authentication setup, not access mail.
		if sess.MustChangePassword && !(r.Method == http.MethodPut && r.URL.Path == profilePasswordPath) {
			s.writeJSON(w, http.StatusForbidden, passwordChangeJSON{Error: "choose your own password first", PasswordChangeRequired: true})
			return
		}
		if s.twoFactorRequired(sess) && !sess.TwoFactorEnabled && !twoFactorSetupPathAllowed(r.Method, r.URL.Path) {
			s.writeJSON(w, http.StatusForbidden, apiError{Error: "two-factor authentication setup required"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess)))
	})
}

type userJSON struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
	// MustChangePassword: the user logged in with a generated password and
	// must choose their own before anything else works.
	MustChangePassword bool `json:"mustChangePassword"`
	TwoFactorEnabled   bool `json:"twoFactorEnabled"`
	TwoFactorRequired   bool `json:"twoFactorRequired"`
}

type sessionJSON struct {
	User userJSON `json:"user"`
}

type noSessionJSON struct {
	Error string `json:"error"`
	// SetupRequired is true while no user exists; the UI then explains how
	// to create the first admin.
	SetupRequired bool `json:"setupRequired"`
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.session(r)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if sess != nil {
		s.writeJSON(w, http.StatusOK, sessionJSON{User: userJSON{Name: sess.UserName, Admin: sess.IsAdmin, MustChangePassword: sess.MustChangePassword, TwoFactorEnabled: sess.TwoFactorEnabled, TwoFactorRequired: s.twoFactorRequired(sess)}})
		return
	}
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	s.writeJSON(w, http.StatusUnauthorized, noSessionJSON{Error: "login required", SetupRequired: n == 0})
}

type loginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

const errWrongLogin = "wrong user name or password"

func (s *Server) twoFactorRequiredUser(u *store.User) bool { return u.IsAdmin || s.require2FA }

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in loginInput
	if !s.decode(w, r, &in) {
		return
	}
	name := auth.NormalizeUserName(in.Username)
	addr := clientAddr(r)
	if wait := s.limiter.Blocked(name, addr); wait > 0 {
		secs := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		s.writeJSON(w, http.StatusTooManyRequests, retryJSON{Error: "too many failed logins; try again later", RetryAfter: secs})
		return
	}
	if len(in.Password) > auth.MaxPasswordLength {
		// No stored password is that long; skip the hashing work.
		s.limiter.Fail(name, addr)
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
		s.log.Info("login failed", "user", name, "addr", addr)
		s.fail(w, r, http.StatusUnauthorized, errWrongLogin, nil)
		return
	}
	if u.LockedAt != nil {
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
	// Password authentication is complete here only when no second factor is required.
	if s.twoFactorRequiredUser(u) && u.TwoFactorEnabled {
		if s.sealer == nil || len(s.secretKey) == 0 {
			s.fail(w, r, http.StatusServiceUnavailable, "two-factor authentication is unavailable", nil)
			return
		}
		if old := sessionToken(r); old != "" { _ = s.store.DeleteSession(ctx, auth.HashSessionToken(old)) }
		challenge, err := s.store.CreateTwoFactorChallenge(ctx, u.ID, u.TwoFactorVersion, s.now().Add(5*time.Minute))
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"twoFactorRequired": true, "challenge": challenge})
		return
	}
	if !s.twoFactorRequiredUser(u) {
		s.limiter.Succeed(name)
	}
	// A fresh token on every login: a token planted before the login is
	// never promoted to a valid session.
	if old := sessionToken(r); old != "" {
		_ = s.store.DeleteSession(ctx, auth.HashSessionToken(old))
	}
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	ua := r.UserAgent()
	if len(ua) > 256 {
		ua = ua[:256]
	}
	if err := s.store.CreateSession(ctx, hash, u.ID, s.now().Add(auth.MaxSessionAge), ua); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if err := s.store.RecordLogin(ctx, u.ID); err != nil {
		s.log.Warn("record login", "user", u.Name, "err", err)
	}
	s.log.Info("login", "user", u.Name, "addr", addr)
	setSessionCookie(w, r, token, auth.MaxSessionAge)
	s.writeJSON(w, http.StatusOK, sessionJSON{User: userJSON{Name: u.Name, Admin: u.IsAdmin, MustChangePassword: u.MustChangePassword, TwoFactorEnabled: u.TwoFactorEnabled, TwoFactorRequired: s.twoFactorRequiredUser(u)}})
}

type retryJSON struct {
	Error      string `json:"error"`
	RetryAfter int    `json:"retryAfter"` // seconds
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := sessionToken(r); token != "" {
		if err := s.store.DeleteSession(r.Context(), auth.HashSessionToken(token)); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
	}
	clearSessionCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// clientAddr is the direct peer address. Proxy headers are not trusted:
// behind a reverse proxy all clients share its address, and the limit per
// user name still applies.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// cleanSessions deletes expired sessions every hour until ctx ends.
func (s *Server) cleanSessions(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
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
