package web

import (
	"encoding/base64"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"rsc.io/qr"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// Two-factor authentication with TOTP. Admins always need it; with
// MAIL_ARCHIVE_REQUIRE_2FA everybody does. Users who need it but have not
// set it up yet can only use the setup endpoints (see requireLogin). Users
// who turned it on voluntarily are asked for a code at every login too.

// twoFactorChallengeTTL is how long the second login step may take.
const twoFactorChallengeTTL = 5 * time.Minute

const errInvalidSecondFactor = "invalid two-factor code"

// twoFactorRequired reports whether the user must use 2FA.
func (s *Server) twoFactorRequired(admin bool) bool {
	return admin || s.require2FA
}

// twoFactorSetupPathAllowed lists what a user who must still set up 2FA may
// do (besides the session endpoints and the password change).
func twoFactorSetupPathAllowed(method, path string) bool {
	switch {
	case method == http.MethodGet && path == "/api/profile/2fa":
		return true
	case method == http.MethodPost && (path == "/api/profile/2fa/setup" || path == "/api/profile/2fa/confirm"):
		return true
	}
	return false
}

func (s *Server) twoFactorReady(w http.ResponseWriter, r *http.Request) bool {
	if s.sealer == nil || len(s.secretKey) == 0 {
		s.fail(w, r, http.StatusServiceUnavailable, "two-factor authentication needs MAIL_ARCHIVE_SECRET_KEY on the server", nil)
		return false
	}
	return true
}

// blocked answers 429 if failed attempts for name or the client address are
// over the limit.
func (s *Server) blocked(w http.ResponseWriter, r *http.Request, name string) bool {
	wait := s.limiter.Blocked(name, clientAddr(r))
	if wait <= 0 {
		return false
	}
	secs := int(math.Ceil(wait.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	s.writeJSON(w, http.StatusTooManyRequests, retryJSON{Error: "too many failed attempts; try again later", RetryAfter: secs})
	return true
}

type twoFactorJSON struct {
	Enabled      bool `json:"enabled"`
	Required     bool `json:"required"`
	Admin        bool `json:"admin"`
	SetupPending bool `json:"setupPending"`
	// Only in the answer of POST /api/profile/2fa/setup.
	Secret     string `json:"secret,omitempty"`
	OtpauthURI string `json:"otpauthUri,omitempty"`
	QRDataURL  string `json:"qrDataUrl,omitempty"`
}

func (s *Server) handleGetTwoFactor(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetTwoFactorState(r.Context(), userID(r))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, twoFactorJSON{
		Enabled: st.Enabled, Required: s.twoFactorRequired(st.IsAdmin), Admin: st.IsAdmin, SetupPending: st.SetupPending,
	})
}

// handleBeginTwoFactor creates a new secret and returns it once, with the
// otpauth URI and a QR code (PNG, rendered on the server: no QR code library
// in the UI).
func (s *Server) handleBeginTwoFactor(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady(w, r) {
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if err := s.store.BeginTwoFactorSetup(r.Context(), userID(r), secret, s.sealer); err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.fail(w, r, http.StatusConflict, "two-factor authentication is already on", nil)
			return
		}
		s.failStore(w, r, err)
		return
	}
	sess := currentSession(r)
	uri := (&url.URL{
		Scheme:   "otpauth",
		Host:     "totp",
		Path:     "/Mail Archive:" + sess.UserName,
		RawQuery: url.Values{"secret": {secret}, "issuer": {"Mail Archive"}}.Encode(),
	}).String()
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	s.writeJSON(w, http.StatusOK, twoFactorJSON{
		Required: s.twoFactorRequired(sess.IsAdmin), Admin: sess.IsAdmin, SetupPending: true,
		Secret: secret, OtpauthURI: uri, QRDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()),
	})
}

type twoFactorCodeInput struct {
	Code string `json:"code"`
}

type recoveryCodesJSON struct {
	RecoveryCodes []string `json:"recoveryCodes"`
}

func (s *Server) handleConfirmTwoFactor(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady(w, r) {
		return
	}
	var in twoFactorCodeInput
	if !s.decode(w, r, &in) {
		return
	}
	sess := currentSession(r)
	if s.blocked(w, r, sess.UserName) {
		return
	}
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	err = s.store.ConfirmTwoFactorSetup(r.Context(), sess.UserID, auth.NormalizeTOTPCode(in.Code), s.now(), s.sealer, codes, s.secretKey)
	if errors.Is(err, store.ErrTwoFactorInvalid) {
		s.limiter.Fail(sess.UserName, clientAddr(r))
		s.fail(w, r, http.StatusUnauthorized, errInvalidSecondFactor, nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("2fa enabled", "user", sess.UserName)
	s.writeJSON(w, http.StatusOK, recoveryCodesJSON{RecoveryCodes: codes})
}

// startTwoFactorLogin answers a correct password of a user with 2FA: no
// session yet, but a challenge for the second step.
func (s *Server) startTwoFactorLogin(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !s.twoFactorReady(w, r) {
		return
	}
	if old := sessionToken(r); old != "" {
		_ = s.store.DeleteSession(r.Context(), auth.HashSessionToken(old))
	}
	// Wall-clock time: the database compares the expiry with now().
	challenge, err := s.store.CreateTwoFactorChallenge(r.Context(), u.ID, u.TwoFactorVersion, time.Now().Add(twoFactorChallengeTTL))
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"twoFactorRequired": true, "challenge": challenge})
}

func (s *Server) handleTwoFactorLogin(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady(w, r) {
		return
	}
	var in struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	// Challenges are 256-bit random tokens; looking them up before the limit
	// check reveals nothing guessable.
	u, err := s.store.TwoFactorChallengeUser(r.Context(), in.Challenge)
	if errors.Is(err, store.ErrTwoFactorExpired) || errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusUnauthorized, "the login has expired; log in again", nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	addr := clientAddr(r)
	if s.blocked(w, r, u.Name) {
		return
	}
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	user, err := s.store.CompleteTwoFactorLogin(r.Context(), in.Challenge, auth.NormalizeTOTPCode(in.Code), s.now(),
		s.sealer, s.secretKey, hash, s.now().Add(auth.MaxSessionAge), truncateUserAgent(r.UserAgent()))
	switch {
	case errors.Is(err, store.ErrTwoFactorInvalid):
		s.limiter.Fail(u.Name, addr)
		s.log.Info("login failed: second factor", "user", u.Name, "addr", addr)
		s.fail(w, r, http.StatusUnauthorized, errInvalidSecondFactor, nil)
		return
	case errors.Is(err, store.ErrTwoFactorExpired):
		s.fail(w, r, http.StatusUnauthorized, "the login has expired; log in again", nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	// Only now is the login complete.
	s.limiter.Succeed(u.Name)
	s.log.Info("login", "user", user.Name, "addr", addr, "2fa", true)
	setSessionCookie(w, r, token, auth.MaxSessionAge)
	s.writeJSON(w, http.StatusOK, sessionJSON{User: s.userJSON(user.Name, user.IsAdmin, user.MustChangePassword, true)})
}

func truncateUserAgent(ua string) string {
	if len(ua) > 256 {
		return ua[:256]
	}
	return ua
}

// verifyOwnSecondFactor checks a code of the logged-in user with the login
// limits and returns the 2FA version it was checked against.
func (s *Server) verifyOwnSecondFactor(w http.ResponseWriter, r *http.Request, code string) (int64, bool) {
	sess := currentSession(r)
	version, err := s.store.VerifyTwoFactorCode(r.Context(), sess.UserID, auth.NormalizeTOTPCode(code), s.now(), s.sealer, s.secretKey)
	if errors.Is(err, store.ErrTwoFactorInvalid) {
		s.limiter.Fail(sess.UserName, clientAddr(r))
		s.fail(w, r, http.StatusUnauthorized, errInvalidSecondFactor, nil)
		return 0, false
	}
	if err != nil {
		s.failStore(w, r, err)
		return 0, false
	}
	return version, true
}

func (s *Server) handleDisableTwoFactor(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady(w, r) {
		return
	}
	sess := currentSession(r)
	if s.twoFactorRequired(sess.IsAdmin) {
		s.fail(w, r, http.StatusForbidden, "two-factor authentication is required for you", nil)
		return
	}
	var in struct {
		CurrentPassword string `json:"currentPassword"`
		Code            string `json:"code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if s.blocked(w, r, sess.UserName) {
		return
	}
	u, err := s.store.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	ok := false
	if len(in.CurrentPassword) <= auth.MaxPasswordLength {
		if ok, _, err = s.hasher.Verify(r.Context(), u.PasswordHash, in.CurrentPassword); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
	}
	if !ok {
		s.limiter.Fail(sess.UserName, clientAddr(r))
		s.fail(w, r, http.StatusUnauthorized, "the current password is wrong", nil)
		return
	}
	version, ok := s.verifyOwnSecondFactor(w, r, in.Code)
	if !ok {
		return
	}
	if err := s.store.DisableOwnTwoFactor(r.Context(), sess.UserID, version, auth.HashSessionToken(sessionToken(r))); err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.fail(w, r, http.StatusConflict, "two-factor authentication changed meanwhile; reload and try again", nil)
			return
		}
		s.failStore(w, r, err)
		return
	}
	s.limiter.Succeed(sess.UserName)
	s.log.Info("2fa disabled", "user", sess.UserName)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady(w, r) {
		return
	}
	var in twoFactorCodeInput
	if !s.decode(w, r, &in) {
		return
	}
	sess := currentSession(r)
	if s.blocked(w, r, sess.UserName) {
		return
	}
	version, ok := s.verifyOwnSecondFactor(w, r, in.Code)
	if !ok {
		return
	}
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if err := s.store.ReplaceRecoveryCodes(r.Context(), sess.UserID, version, codes, s.secretKey); err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.fail(w, r, http.StatusConflict, "two-factor authentication changed meanwhile; reload and try again", nil)
			return
		}
		s.failStore(w, r, err)
		return
	}
	s.limiter.Succeed(sess.UserName)
	s.writeJSON(w, http.StatusOK, recoveryCodesJSON{RecoveryCodes: codes})
}

func (s *Server) handleResetUserTwoFactor(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	if err := s.store.ResetTwoFactor(r.Context(), u.ID); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("2fa reset", "user", u.Name, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}
