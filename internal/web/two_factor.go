package web

import (
	"encoding/base64"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"rsc.io/qr"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// Two-factor authentication with TOTP. Admins always need it; with
// KEEPIA_ARCHIVE_REQUIRE_2FA everybody does. Users who need it but have not
// set it up yet can only use the setup endpoints (see authenticate). Users
// who turned it on voluntarily are asked for a code at every login too.

// twoFactorChallengeTTL is how long the second login step may take.
const twoFactorChallengeTTL = 5 * time.Minute

const errInvalidSecondFactor = "invalid two-factor code"

// errTwoFactorExhausted answers a login challenge without attempts left
// (store.MaxTwoFactorAttempts). It contains "expired", as the answer to an
// expired challenge does, so that clients that only look for that word
// also return to the password step.
const errTwoFactorExhausted = "too many wrong codes; the login has expired, log in again"

// twoFactorRequired reports whether the user must use 2FA: admins and
// auditors (#393) always.
func (s *Server) twoFactorRequired(admin, auditor bool) bool {
	return admin || auditor || s.require2FA
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
		s.fail(w, r, http.StatusServiceUnavailable, "two-factor authentication needs KEEPIA_ARCHIVE_SECRET_KEY on the server", nil)
		return false
	}
	return true
}

// beginAttempt reserves a login attempt for name and the client address
// (auth.Limiter.Begin), after the request body was read. If the attempt is
// refused it answers the request, or leaves it unanswered when the client
// went away, and returns nil. The caller ends the attempt before it answers
// and defers Release as a safety net.
func (s *Server) beginAttempt(w http.ResponseWriter, r *http.Request, name, blockedMsg string) *auth.Attempt {
	a, wait, err := s.limiter.Begin(r.Context(), name, s.clientAddr(r))
	if s.attemptRefused(w, r, wait, err, blockedMsg) {
		return nil
	}
	return a
}

// attemptRefused answers a refused Begin of the limiter or a rate: 429 for
// a block or a wait that took too long, nothing when the client is gone.
func (s *Server) attemptRefused(w http.ResponseWriter, r *http.Request, wait time.Duration, err error, blockedMsg string) bool {
	switch {
	case errors.Is(err, auth.ErrBusy):
		s.log.Warn("too many attempts in flight", "addr", s.clientIP(r), "waited", auth.MaxWait)
		wait = auth.MaxWait
	case err != nil:
		// The request's context ended while it waited: the client is gone.
		return true
	case wait <= 0:
		return false
	}
	secs := max(1, int(math.Ceil(wait.Seconds())))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	s.writeJSON(w, http.StatusTooManyRequests, retryJSON{Error: blockedMsg, RetryAfter: secs})
	return true
}

const errTooManyAttempts = "too many failed attempts; try again later"

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
		Enabled: st.Enabled, Required: s.twoFactorRequired(st.IsAdmin, currentSession(r).IsAuditor), Admin: st.IsAdmin, SetupPending: st.SetupPending,
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
		Required: s.twoFactorRequired(sess.IsAdmin, sess.IsAuditor), Admin: sess.IsAdmin, SetupPending: true,
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
	a := s.beginAttempt(w, r, sess.UserName, errTooManyAttempts)
	if a == nil {
		return
	}
	defer a.Release()
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		a.Release()
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	err = s.store.ConfirmTwoFactorSetup(withAudit(r, s.selfEntry(r, "profile.2fa_enable")), sess.UserID, auth.NormalizeTOTPCode(in.Code), s.now(), s.sealer, codes, s.secretKey)
	if errors.Is(err, store.ErrTwoFactorInvalid) {
		a.Fail()
		s.fail(w, r, http.StatusUnauthorized, errInvalidSecondFactor, nil)
		return
	}
	// A correct code does not reset the name's failures, as before.
	a.Release()
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
	s.deleteOldSessions(r)
	// Wall-clock time: the database compares the expiry with now().
	challenge, err := s.store.CreateTwoFactorChallenge(r.Context(), u.ID, u.TwoFactorVersion, s.currentTime().Add(twoFactorChallengeTTL))
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
	a := s.beginAttempt(w, r, u.Name, errTooManyAttempts)
	if a == nil {
		return
	}
	defer a.Release()
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		a.Release()
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	e := s.selfEntry(r, "auth.login")
	e.Detail("method", "2fa")
	user, err := s.store.CompleteTwoFactorLogin(withAudit(r, e), in.Challenge, auth.NormalizeTOTPCode(in.Code), s.now(),
		s.sealer, s.secretKey, hash, s.now().Add(auth.MaxSessionAge), truncateUserAgent(r.UserAgent()))
	if errors.Is(err, store.ErrTwoFactorInvalid) {
		a.Fail()
	} else if err != nil {
		a.Release()
	}
	switch {
	case errors.Is(err, store.ErrTwoFactorExhausted):
		// Only the wrong code that used the last attempt was checked and
		// counts (a.Fail above); a request that found no attempt left
		// checked nothing and released its attempt.
		if errors.Is(err, store.ErrTwoFactorInvalid) {
			s.log.Info("login failed: second factor", "user", u.Name, "addr", s.clientIP(r))
			s.auditLoginFailed(r, "2fa", reasonSecondFactor, u)
		}
		s.fail(w, r, http.StatusUnauthorized, errTwoFactorExhausted, nil)
		return
	case errors.Is(err, store.ErrTwoFactorInvalid):
		s.log.Info("login failed: second factor", "user", u.Name, "addr", s.clientIP(r))
		s.auditLoginFailed(r, "2fa", reasonSecondFactor, u)
		s.fail(w, r, http.StatusUnauthorized, errInvalidSecondFactor, nil)
		return
	case errors.Is(err, store.ErrTwoFactorExpired):
		s.fail(w, r, http.StatusUnauthorized, "the login has expired; log in again", nil)
		return
	case s.refuseNoGrant(w, r, "2fa", err, u):
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	body, err := s.loginBody(r.Context(), user, hash, true)
	if err != nil {
		a.Release()
		s.failStore(w, r, err)
		return
	}
	// Only now is the login complete.
	a.Succeed()
	s.log.Info("login", "user", user.Name, "addr", s.clientIP(r), "2fa", true)
	s.setSessionCookie(w, r, token, auth.MaxSessionAge)
	s.writeJSON(w, http.StatusOK, body)
}

// truncateUserAgent makes a user agent storable for a session: valid UTF-8
// without NUL (header values may hold other bytes) of at most 256 bytes,
// cut at a character boundary.
func truncateUserAgent(ua string) string {
	ua = strings.ReplaceAll(strings.ToValidUTF8(ua, "\uFFFD"), "\x00", "\uFFFD")
	if len(ua) <= 256 {
		return ua
	}
	cut := 256
	for cut > 0 && !utf8.RuneStart(ua[cut]) {
		cut--
	}
	return ua[:cut]
}

// verifyOwnSecondFactor checks a code of the logged-in user within the
// caller's login attempt a and returns the 2FA version it was checked
// against. A wrong code fails a; on every refusal a has ended and the
// request is answered.
func (s *Server) verifyOwnSecondFactor(w http.ResponseWriter, r *http.Request, a *auth.Attempt, code string) (int64, bool) {
	sess := currentSession(r)
	version, err := s.store.VerifyTwoFactorCode(r.Context(), sess.UserID, auth.NormalizeTOTPCode(code), s.now(), s.sealer, s.secretKey)
	if errors.Is(err, store.ErrTwoFactorInvalid) {
		a.Fail()
		s.fail(w, r, http.StatusUnauthorized, errInvalidSecondFactor, nil)
		return 0, false
	}
	if err != nil {
		a.Release()
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
	if s.twoFactorRequired(sess.IsAdmin, sess.IsAuditor) {
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
	a := s.beginAttempt(w, r, sess.UserName, errTooManyAttempts)
	if a == nil {
		return
	}
	defer a.Release()
	u, err := s.store.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		a.Release()
		s.failStore(w, r, err)
		return
	}
	ok := false
	if len(in.CurrentPassword) <= auth.MaxPasswordLength {
		if ok, _, err = s.verifyPassword(r.Context(), u.PasswordHash, in.CurrentPassword); err != nil {
			a.Release()
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
	}
	if !ok {
		a.Fail()
		s.fail(w, r, http.StatusUnauthorized, "the current password is wrong", nil)
		return
	}
	version, ok := s.verifyOwnSecondFactor(w, r, a, in.Code)
	if !ok {
		return
	}
	if err := s.store.DisableOwnTwoFactor(withAudit(r, s.selfEntry(r, "profile.2fa_disable")), sess.UserID, version, auth.HashSessionToken(s.sessionToken(r))); err != nil {
		a.Release()
		if errors.Is(err, store.ErrConflict) {
			s.fail(w, r, http.StatusConflict, "two-factor authentication changed meanwhile; reload and try again", nil)
			return
		}
		s.failStore(w, r, err)
		return
	}
	a.Succeed()
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
	a := s.beginAttempt(w, r, sess.UserName, errTooManyAttempts)
	if a == nil {
		return
	}
	defer a.Release()
	version, ok := s.verifyOwnSecondFactor(w, r, a, in.Code)
	if !ok {
		return
	}
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		a.Release()
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if err := s.store.ReplaceRecoveryCodes(withAudit(r, s.selfEntry(r, "profile.recovery_codes")), sess.UserID, version, codes, s.secretKey); err != nil {
		a.Release()
		if errors.Is(err, store.ErrConflict) {
			s.fail(w, r, http.StatusConflict, "two-factor authentication changed meanwhile; reload and try again", nil)
			return
		}
		s.failStore(w, r, err)
		return
	}
	a.Succeed()
	s.writeJSON(w, http.StatusOK, recoveryCodesJSON{RecoveryCodes: codes})
}

func (s *Server) handleResetUserTwoFactor(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	e := s.targetedEntry(r, "user.2fa_reset")
	auditUser(e, u)
	if err := s.store.ResetTwoFactor(withAudit(r, e), u.ID); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("2fa reset", "user", u.Name, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}
