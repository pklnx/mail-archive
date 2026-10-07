package web

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// Passkeys (WebAuthn). A passkey login replaces password and TOTP; it needs
// user verification (Face ID, Touch ID, a PIN). Passkeys are bound to the
// host of MAIL_ARCHIVE_PUBLIC_URL and are off without it. Registering one
// needs the same as a login: the password, and a second factor if the user
// has 2FA on.

// passkeyCeremonyTTL is how long a registration or login may take.
const passkeyCeremonyTTL = 5 * time.Minute

// Starting a passkey login needs no session; this bounds it per address.
const (
	passkeyBeginLimit  = 30
	passkeyBeginWindow = time.Minute
)

const maxPasskeyName = 64

// newWebAuthn configures the relying party for publicURL; nil turns
// passkeys off.
func newWebAuthn(publicURL string) (*webauthn.WebAuthn, string, error) {
	if publicURL == "" {
		return nil, "", nil
	}
	origin, rpID, err := config.ParsePublicURL(publicURL)
	if err != nil {
		return nil, "", err
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:                  rpID,
		RPDisplayName:         "Mail Archive",
		RPOrigins:             []string{origin},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, "", err
	}
	return wa, origin, nil
}

// passkeyUser is a user as the WebAuthn library sees it.
type passkeyUser struct {
	handle []byte
	name   string
	creds  []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte                         { return u.handle }
func (u *passkeyUser) WebAuthnName() string                       { return u.name }
func (u *passkeyUser) WebAuthnDisplayName() string                { return u.name }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (s *Server) passkeysReady(w http.ResponseWriter, r *http.Request) bool {
	if s.webauthn == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "passkeys need MAIL_ARCHIVE_PUBLIC_URL on the server", nil)
		return false
	}
	return true
}

func (s *Server) tooMany(w http.ResponseWriter, wait time.Duration) {
	secs := max(1, int(math.Ceil(wait.Seconds())))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	s.writeJSON(w, http.StatusTooManyRequests, retryJSON{Error: "too many attempts; try again later", RetryAfter: secs})
}

// ceremonyJSON starts a ceremony in the browser: publicKey goes to
// navigator.credentials, token comes back with the result.
type ceremonyJSON struct {
	Token     string `json:"token"`
	PublicKey any    `json:"publicKey"`
}

type ceremonyResult struct {
	Token      string          `json:"token"`
	Credential json.RawMessage `json:"credential"`
}

func (s *Server) handleBeginPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	if !s.passkeysReady(w, r) {
		return
	}
	addr := clientAddr(r)
	if wait := s.limiter.BlockedAddr(addr); wait > 0 {
		s.tooMany(w, wait)
		return
	}
	if ok, wait := s.passkeyBegins.Allow(addr); !ok {
		s.tooMany(w, wait)
		return
	}
	assertion, session, err := s.webauthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	data, err := json.Marshal(session)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	token, err := s.store.BeginPasskeyLogin(r.Context(), data, s.currentTime().Add(passkeyCeremonyTTL))
	if errors.Is(err, store.ErrTooManyCeremonies) {
		s.log.Warn("too many passkey logins in progress", "addr", addr)
		s.tooMany(w, time.Minute)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, ceremonyJSON{Token: token, PublicKey: assertion.Response})
}

// unknownPasskeyJSON lets the UI tell the browser that a passkey no longer
// exists (PublicKeyCredential.signalUnknownCredential).
type unknownPasskeyJSON struct {
	Error             string `json:"error"`
	UnknownCredential bool   `json:"unknownCredential"`
	CredentialID      string `json:"credentialId"`
	RPID              string `json:"rpId"`
}

func (s *Server) handleFinishPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	if !s.passkeysReady(w, r) {
		return
	}
	addr := clientAddr(r)
	if wait := s.limiter.BlockedAddr(addr); wait > 0 {
		s.tooMany(w, wait)
		return
	}
	var in ceremonyResult
	if !s.decode(w, r, &in) {
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(in.Credential)
	if err != nil {
		s.limiter.FailAddr(addr)
		s.fail(w, r, http.StatusBadRequest, "invalid passkey response", nil)
		return
	}
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	verify := func(data, handle, credential []byte) ([]byte, error) {
		var session webauthn.SessionData
		var cred webauthn.Credential
		if err := json.Unmarshal(data, &session); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(credential, &cred); err != nil {
			return nil, err
		}
		user := &passkeyUser{handle: handle, creds: []webauthn.Credential{cred}}
		_, got, err := s.webauthn.ValidatePasskeyLogin(func(_, _ []byte) (webauthn.User, error) { return user, nil }, session, parsed)
		if err != nil {
			return nil, err
		}
		// A sign count that did not go up means a copied authenticator.
		if got.Authenticator.CloneWarning {
			return nil, errors.New("sign count did not increase")
		}
		return json.Marshal(got)
	}
	login, err := s.store.CompletePasskeyLogin(r.Context(), in.Token, parsed.RawID, verify,
		hash, s.currentTime().Add(auth.MaxSessionAge), truncateUserAgent(r.UserAgent()))
	switch {
	case errors.Is(err, store.ErrCeremonyExpired):
		s.fail(w, r, http.StatusUnauthorized, "the passkey login has expired; try again", nil)
		return
	case errors.Is(err, store.ErrPasskeyUnknown):
		s.limiter.FailAddr(addr)
		s.log.Info("login failed: unknown passkey", "addr", addr)
		s.writeJSON(w, http.StatusUnauthorized, unknownPasskeyJSON{
			Error: "this passkey is not registered", UnknownCredential: true,
			CredentialID: base64.RawURLEncoding.EncodeToString(parsed.RawID), RPID: s.webauthn.Config.RPID,
		})
		return
	case errors.Is(err, store.ErrPasskeyInvalid):
		s.limiter.FailAddr(addr)
		s.log.Info("login failed: passkey", "addr", addr, "err", err)
		s.fail(w, r, http.StatusUnauthorized, "the passkey was not accepted", nil)
		return
	case errors.Is(err, store.ErrUserLocked):
		s.fail(w, r, http.StatusForbidden, "this user is locked", nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	u := login.User
	s.limiter.Succeed(u.Name)
	if old := sessionToken(r); old != "" {
		_ = s.store.DeleteSession(r.Context(), auth.HashSessionToken(old))
	}
	s.log.Info("login", "user", u.Name, "addr", addr, "passkey", login.PasskeyName)
	setSessionCookie(w, r, token, auth.MaxSessionAge)
	s.writeJSON(w, http.StatusOK, sessionJSON{User: s.userJSON(u.Name, u.IsAdmin, u.MustChangePassword, u.TwoFactorEnabled)})
}

type passkeyJSON struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

type passkeysJSON struct {
	// Available is false without MAIL_ARCHIVE_PUBLIC_URL; Origin is where
	// passkeys work.
	Available bool          `json:"available"`
	Origin    string        `json:"origin,omitempty"`
	Max       int           `json:"max"`
	Passkeys  []passkeyJSON `json:"passkeys"`
}

func (s *Server) handleListPasskeys(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListPasskeys(r.Context(), userID(r))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := passkeysJSON{Available: s.webauthn != nil, Origin: s.publicOrigin, Max: store.MaxPasskeys, Passkeys: make([]passkeyJSON, 0, len(list))}
	for _, p := range list {
		out.Passkeys = append(out.Passkeys, passkeyJSON{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, LastUsedAt: p.LastUsedAt})
	}
	s.writeJSON(w, http.StatusOK, out)
}

type passkeyRegistrationInput struct {
	Name            string `json:"name"`
	CurrentPassword string `json:"currentPassword"`
	// Code is a TOTP or recovery code, needed when the user has 2FA on.
	Code string `json:"code"`
}

func validPasskeyName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > maxPasskeyName {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// checkOwnLogin checks the password, and the second factor if the user has
// 2FA on, with the login limits. It answers the request when they fail.
func (s *Server) checkOwnLogin(w http.ResponseWriter, r *http.Request, password, code string) bool {
	sess := currentSession(r)
	addr := clientAddr(r)
	if s.blocked(w, r, sess.UserName) {
		return false
	}
	u, err := s.store.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		s.failStore(w, r, err)
		return false
	}
	ok := false
	if len(password) <= auth.MaxPasswordLength {
		if ok, _, err = s.hasher.Verify(r.Context(), u.PasswordHash, password); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return false
		}
	}
	if !ok {
		s.limiter.Fail(sess.UserName, addr)
		s.fail(w, r, http.StatusUnauthorized, "the current password is wrong", nil)
		return false
	}
	if u.TwoFactorEnabled {
		if !s.twoFactorReady(w, r) {
			return false
		}
		_, err := s.store.VerifyTwoFactorCode(r.Context(), u.ID, auth.NormalizeTOTPCode(code), s.now(), s.sealer, s.secretKey)
		if errors.Is(err, store.ErrTwoFactorInvalid) {
			s.limiter.Fail(sess.UserName, addr)
			s.fail(w, r, http.StatusUnauthorized, errInvalidSecondFactor, nil)
			return false
		}
		if err != nil {
			s.failStore(w, r, err)
			return false
		}
	}
	s.limiter.Succeed(sess.UserName)
	return true
}

func (s *Server) handleBeginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.passkeysReady(w, r) {
		return
	}
	var in passkeyRegistrationInput
	if !s.decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !validPasskeyName(in.Name) {
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("give the passkey a name of 1 to %d characters", maxPasskeyName), nil)
		return
	}
	if !s.checkOwnLogin(w, r, in.CurrentPassword, in.Code) {
		return
	}
	sess := currentSession(r)
	var creation *protocol.CredentialCreation
	token, err := s.store.BeginPasskeyRegistration(r.Context(), sess.UserID, in.Name, s.currentTime().Add(passkeyCeremonyTTL),
		func(handle []byte, existing [][]byte) ([]byte, error) {
			user := &passkeyUser{handle: handle, name: sess.UserName}
			for _, raw := range existing {
				var c webauthn.Credential
				if err := json.Unmarshal(raw, &c); err != nil {
					return nil, err
				}
				user.creds = append(user.creds, c)
			}
			var session *webauthn.SessionData
			var err error
			creation, session, err = s.webauthn.BeginRegistration(user,
				webauthn.WithExclusions(webauthn.Credentials(user.creds).CredentialDescriptors()))
			if err != nil {
				return nil, err
			}
			return json.Marshal(session)
		})
	if s.failPasskeyChange(w, r, err) {
		return
	}
	s.writeJSON(w, http.StatusOK, ceremonyJSON{Token: token, PublicKey: creation.Response})
}

// failPasskeyChange answers the errors of a registration; false means none.
func (s *Server) failPasskeyChange(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrPasskeyLimit):
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("you already have %d passkeys; remove one first", store.MaxPasskeys), nil)
	case errors.Is(err, store.ErrPasskeyName):
		s.fail(w, r, http.StatusConflict, "you already have a passkey with this name", nil)
	case errors.Is(err, store.ErrPasskeyRegistered):
		s.fail(w, r, http.StatusConflict, "this passkey is already registered", nil)
	case errors.Is(err, store.ErrCeremonyExpired):
		s.fail(w, r, http.StatusBadRequest, "the registration has expired; start again", nil)
	case errors.Is(err, store.ErrPasskeyInvalid):
		s.log.Info("passkey not accepted", "user", currentSession(r).UserName, "err", err)
		s.fail(w, r, http.StatusBadRequest, "the passkey was not accepted", nil)
	default:
		s.failStore(w, r, err)
	}
	return true
}

func (s *Server) handleFinishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.passkeysReady(w, r) {
		return
	}
	var in ceremonyResult
	if !s.decode(w, r, &in) {
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(in.Credential)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid passkey response", nil)
		return
	}
	sess := currentSession(r)
	name, err := s.store.FinishPasskeyRegistration(r.Context(), sess.UserID, in.Token, func(handle, data []byte) ([]byte, []byte, error) {
		var session webauthn.SessionData
		if err := json.Unmarshal(data, &session); err != nil {
			return nil, nil, err
		}
		cred, err := s.webauthn.CreateCredential(&passkeyUser{handle: handle, name: sess.UserName}, session, parsed)
		if err != nil {
			return nil, nil, err
		}
		raw, err := json.Marshal(cred)
		return cred.ID, raw, err
	})
	if s.failPasskeyChange(w, r, err) {
		return
	}
	s.log.Info("passkey added", "user", sess.UserName, "passkey", name)
	s.writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

func (s *Server) handleRemovePasskey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not found", nil)
		return
	}
	sess := currentSession(r)
	p, err := s.store.RemoveOwnPasskey(r.Context(), sess.UserID, id, auth.HashSessionToken(sessionToken(r)))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("passkey removed", "user", sess.UserName, "passkey", p.Name)
	out := map[string]string{"credentialId": base64.RawURLEncoding.EncodeToString(p.CredentialID)}
	if s.webauthn != nil {
		out["rpId"] = s.webauthn.Config.RPID
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRemoveUserPasskeys(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	n, err := s.store.RemovePasskeys(r.Context(), u.ID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("passkeys removed", "user", u.Name, "count", n, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}
