package web

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
	"rsc.io/qr"
)

func (s *Server) checkTwoFactorLimiter(w http.ResponseWriter, r *http.Request, key string) bool {
	if wait := s.limiter.Blocked(key, clientAddr(r)); wait > 0 {
		secs := int(wait.Seconds())
		if secs < 1 { secs = 1 }
		w.Header().Set("Retry-After", fmt.Sprintf("%d", secs))
		s.writeJSON(w, http.StatusTooManyRequests, retryJSON{Error: "too many failed attempts; try again later", RetryAfter: secs})
		return false
	}
	return true
}

type twoFactorSetupJSON struct {
	Enabled       bool   `json:"enabled"`
	Required      bool   `json:"required"`
	Admin         bool   `json:"admin"`
	SetupPending  bool   `json:"setupPending"`
	Secret        string `json:"secret,omitempty"`
	OtpauthURI    string `json:"otpauthUri,omitempty"`
	QRDataURL     string `json:"qrDataUrl,omitempty"`
}

func (s *Server) twoFactorReady() bool {
	return s.sealer != nil && len(s.secretKey) == 32
}

func (s *Server) handleGetTwoFactor(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady() {
		s.fail(w, r, http.StatusServiceUnavailable, "two-factor authentication is unavailable", nil)
		return
	}
	st, err := s.store.GetTwoFactorState(r.Context(), userID(r), s.sealer)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, twoFactorSetupJSON{
		Enabled: st.Enabled, Required: s.twoFactorRequired(currentSession(r)),
		Admin: st.IsAdmin, SetupPending: st.PendingSecret != "",
	})
}

func (s *Server) handleBeginTwoFactor(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady() {
		s.fail(w, r, http.StatusServiceUnavailable, "two-factor authentication is unavailable", nil)
		return
	}
	st, err := s.store.GetTwoFactorState(r.Context(), userID(r), s.sealer)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	if st.Enabled {
		s.fail(w, r, http.StatusConflict, "two-factor authentication is already enabled", nil)
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if err := s.store.BeginTwoFactorSetup(r.Context(), userID(r), secret, s.sealer); err != nil {
		s.failStore(w, r, err)
		return
	}
	uri := "otpauth://totp/" + url.PathEscape("Mail Archive:"+currentSession(r).UserName) +
		"?secret=" + url.QueryEscape(secret) + "&issuer=" + url.QueryEscape("Mail Archive")
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	s.writeJSON(w, http.StatusOK, twoFactorSetupJSON{
		Enabled: false, Required: s.twoFactorRequired(currentSession(r)), Admin: st.IsAdmin,
		SetupPending: true, Secret: secret, OtpauthURI: uri,
		QRDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()),
	})
}

type twoFactorCodeInput struct {
	Code string `json:"code"`
}

func (s *Server) handleConfirmTwoFactor(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady() {
		s.fail(w, r, http.StatusServiceUnavailable, "two-factor authentication is unavailable", nil)
		return
	}
	var in twoFactorCodeInput
	if !s.decode(w, r, &in) {
		return
	}
	if !s.checkTwoFactorLimiter(w, r, currentSession(r).UserName) { return }
	if len(strings.TrimSpace(in.Code)) != auth.TOTPDigits {
		s.fail(w, r, http.StatusUnauthorized, "invalid two-factor code", nil)
		return
	}
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if err := s.store.ConfirmTwoFactorSetup(r.Context(), userID(r), strings.TrimSpace(in.Code), s.now(), s.sealer, codes, s.secretKey); err != nil {
		if errors.Is(err, store.ErrTwoFactorInvalid) || errors.Is(err, store.ErrTwoFactorReplay) {
			s.limiter.Fail(currentSession(r).UserName, clientAddr(r))
			s.fail(w, r, http.StatusUnauthorized, "invalid two-factor code", nil)
			return
		}
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "recoveryCodes": codes})
}

func (s *Server) handleTwoFactorLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if !s.twoFactorReady() {
		s.fail(w, r, http.StatusServiceUnavailable, "two-factor authentication is unavailable", nil)
		return
	}
	if in.Challenge == "" || strings.TrimSpace(in.Code) == "" {
		s.fail(w, r, http.StatusUnauthorized, "invalid two-factor code", nil)
		return
	}
	// The limiter is keyed by the username only after the challenge is resolved.
	ch, err := s.store.GetTwoFactorChallenge(r.Context(), in.Challenge)
	if err != nil {
		s.fail(w, r, http.StatusUnauthorized, "invalid or expired two-factor challenge", nil)
		return
	}
	u, err := s.store.GetUserByID(r.Context(), ch.UserID)
	if err != nil {
		s.fail(w, r, http.StatusUnauthorized, "invalid or expired two-factor challenge", nil)
		return
	}
	key := u.Name
	if !s.checkTwoFactorLimiter(w, r, key) { return }
	token, user, err := s.store.CompleteTwoFactorLogin(r.Context(), in.Challenge, strings.TrimSpace(in.Code), s.now(), s.sealer, s.secretKey, s.now().Add(auth.MaxSessionAge), truncateUserAgent(r.UserAgent()))
	if err != nil {
		if errors.Is(err, store.ErrTwoFactorInvalid) || errors.Is(err, store.ErrTwoFactorReplay) {
			// Use a stable key derived from the challenge rather than exposing
			// whether the user exists.
			s.limiter.Fail(u.Name, clientAddr(r))
			s.fail(w, r, http.StatusUnauthorized, "invalid two-factor code", nil)
			return
		}
		s.fail(w, r, http.StatusUnauthorized, "invalid or expired two-factor challenge", nil)
		return
	}
	s.limiter.Succeed(u.Name)
	clearSessionCookies(w, r)
	setSessionCookie(w, r, token, auth.MaxSessionAge)
	s.writeJSON(w, http.StatusOK, sessionJSON{User: userJSON{
		Name: user.Name, Admin: user.IsAdmin, MustChangePassword: user.MustChangePassword,
		TwoFactorEnabled: true, TwoFactorRequired: s.twoFactorRequiredUser(user),
	}})
}

func truncateUserAgent(ua string) string {
	if len(ua) > 256 {
		return ua[:256]
	}
	return ua
}

func (s *Server) handleDisableTwoFactor(w http.ResponseWriter, r *http.Request) {
	sess := currentSession(r)
	if sess.IsAdmin {
		s.fail(w, r, http.StatusForbidden, "administrators cannot disable two-factor authentication", nil)
		return
	}
	if s.require2FA {
		s.fail(w, r, http.StatusForbidden, "two-factor authentication is required", nil)
		return
	}
	var in struct {
		CurrentPassword string `json:"currentPassword"`
		Code            string `json:"code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if !s.checkTwoFactorLimiter(w, r, sess.UserName) { return }
	u, err := s.store.GetUserByName(r.Context(), sess.UserName)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	ok, _, err := s.hasher.Verify(r.Context(), u.PasswordHash, in.CurrentPassword)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if !ok {
		s.limiter.Fail(sess.UserName, clientAddr(r))
		s.fail(w, r, http.StatusUnauthorized, "the current password is wrong", nil)
		return
	}
	version, err := s.store.VerifyTwoFactorCode(r.Context(), u.ID, strings.TrimSpace(in.Code), s.now(), s.sealer, s.secretKey)
	if err != nil {
		s.limiter.Fail(sess.UserName, clientAddr(r))
		s.fail(w, r, http.StatusUnauthorized, "invalid two-factor code", nil)
		return
	}
	if err := s.store.DisableTwoFactorIfVersion(r.Context(), u.ID, version, auth.HashSessionToken(sessionToken(r))); err != nil {
		s.failStore(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	sess := currentSession(r)
	var in twoFactorCodeInput
	if !s.decode(w, r, &in) {
		return
	}
	if !s.checkTwoFactorLimiter(w, r, sess.UserName) { return }
	u, err := s.store.GetUserByName(r.Context(), sess.UserName)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	version, err := s.store.VerifyTwoFactorCode(r.Context(), u.ID, strings.TrimSpace(in.Code), s.now(), s.sealer, s.secretKey)
	if err != nil {
		s.limiter.Fail(sess.UserName, clientAddr(r))
		s.fail(w, r, http.StatusUnauthorized, "invalid two-factor code", nil)
		return
	}
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if err := s.store.ReplaceRecoveryCodesIfVersion(r.Context(), u.ID, version, codes, s.secretKey); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

