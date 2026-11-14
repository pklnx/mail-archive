package web

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// Profile → Notifications (#397, part 5): each admin may set one address
// for alert mail and confirms it with a code mailed to it. Only admins get
// alert mail, so every handler answers 403 to anyone else before it reads
// the body or looks anything up. Without KEEPIA_ARCHIVE_MAIL_URL the GET
// says so and the UI hides the tab. The audit entries never carry the
// address.

// AlertMailer mails a confirmation code (alertmail.Service.SendCode).
type AlertMailer interface {
	SendCode(ctx context.Context, userID int64, email, lang, code string) error
}

const notificationsAdminOnly = "only admins get alert mail"

// Codes of the notification answers (apiError.Code).
const (
	errMailOff       = "mail_off"
	errMailFailed    = "mail_failed"
	errWrongCode     = "wrong_code"
	errNoCode        = "no_code"
	errCodeSpent     = "code_spent"
	errCodeRateLimit = "code_limit"
)

type notificationsJSON struct {
	// Available is false without KEEPIA_ARCHIVE_MAIL_URL; then nothing else
	// is set.
	Available bool `json:"available"`
	// Email is the address, null without one.
	Email       *string    `json:"email"`
	Confirmed   bool       `json:"confirmed"`
	ConfirmedAt *time.Time `json:"confirmedAt"`
	// Bounced: a relay refused the address; it gets no alert mail until
	// it is confirmed again.
	Bounced bool `json:"bounced"`
	// CodeExpiresAt is set while a code waits to be entered.
	CodeExpiresAt *time.Time `json:"codeExpiresAt"`
}

type notificationsInput struct {
	Email string `json:"email"`
	// Language is the UI's language (de or en) for the code mail, and
	// for alert mail while the user's language setting is auto.
	Language string `json:"language"`
}

type notificationsCodeInput struct {
	Code string `json:"code"`
}

func (s *Server) notificationsState(r *http.Request, userID int64) (notificationsJSON, error) {
	st, err := s.store.NotifyEmail(r.Context(), userID)
	if err != nil {
		return notificationsJSON{}, err
	}
	out := notificationsJSON{Available: true, Confirmed: st.ConfirmedAt != nil, ConfirmedAt: st.ConfirmedAt, Bounced: st.BouncedAt != nil}
	if st.Email != "" {
		out.Email = &st.Email
	}
	if st.CodeExpiresAt != nil && s.currentTime().Before(*st.CodeExpiresAt) && st.CodeTries < store.NotifyCodeMaxTries {
		out.CodeExpiresAt = st.CodeExpiresAt
	}
	return out, nil
}

func (s *Server) handleGetNotifications(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminFor(w, r, notificationsAdminOnly) {
		return
	}
	if s.alertMail == nil {
		s.writeJSON(w, http.StatusOK, notificationsJSON{})
		return
	}
	out, err := s.notificationsState(r, currentSession(r).UserID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// requireMail answers 409 mail_off without alert mail.
func (s *Server) requireMail(w http.ResponseWriter) bool {
	if s.alertMail == nil {
		s.writeJSON(w, http.StatusConflict, apiError{Error: "alert mail is off: " + config.EnvMailURL + " is not set", Code: errMailOff})
		return false
	}
	return true
}

// handleSetNotifications sets the admin's address, unconfirmed, and mails
// a new code to it: at most store.NotifyCodesPerHour codes per hour.
func (s *Server) handleSetNotifications(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminFor(w, r, notificationsAdminOnly) || !s.requireMail(w) {
		return
	}
	var in notificationsInput
	if !s.decode(w, r, &in) {
		return
	}
	email := strings.TrimSpace(in.Email)
	a, err := config.ParseMailAddress(email)
	if err != nil || a.Name != "" || a.Address != email {
		s.fail(w, r, http.StatusBadRequest, "email must be one mail address, such as admin@example.com", nil)
		return
	}
	lang := ""
	switch in.Language {
	case "de", "en":
		lang = in.Language
	case "", "auto":
	default:
		s.fail(w, r, http.StatusBadRequest, `language must be "de" or "en"`, nil)
		return
	}
	sess := currentSession(r)
	code := newNotifyCode()
	err = s.store.SetNotifyEmail(withAudit(r, s.selfEntry(r, "profile.notify_email_set")), sess.UserID, store.NotifyCodeRequest{
		Email: email, CodeHash: store.NotifyCodeHash(sess.UserID, code), Language: lang, Now: s.currentTime(),
	})
	var limit *store.NotifyCodeLimitError
	switch {
	case errors.As(err, &limit):
		secs := max(int(math.Ceil(limit.RetryAt.Sub(s.currentTime()).Seconds())), 1)
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		s.writeJSON(w, http.StatusTooManyRequests, retryJSON{Error: "too many codes requested; try again later", Code: errCodeRateLimit, RetryAfter: secs})
		return
	case errors.Is(err, store.ErrNotifyAdminOnly):
		s.fail(w, r, http.StatusForbidden, notificationsAdminOnly, nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	if lang == "" {
		lang = "en"
	}
	if err := s.alertMail.SendCode(r.Context(), sess.UserID, email, lang, code); err != nil {
		// The address stays set and unconfirmed; the code is spent.
		s.writeJSON(w, http.StatusBadGateway, apiError{Error: "the code could not be sent to this address; check it, or ask the operator to check the mail relay", Code: errMailFailed})
		return
	}
	out, err := s.notificationsState(r, sess.UserID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handleConfirmNotifications confirms the address with the mailed code:
// store.NotifyCodeMaxTries tries per code, within store.NotifyCodeTTL.
func (s *Server) handleConfirmNotifications(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminFor(w, r, notificationsAdminOnly) || !s.requireMail(w) {
		return
	}
	var in notificationsCodeInput
	if !s.decode(w, r, &in) {
		return
	}
	code := normalizeNotifyCode(in.Code)
	if len(code) != notifyCodeLen {
		s.writeJSON(w, http.StatusBadRequest, apiError{Error: "the code has 6 characters", Code: errWrongCode})
		return
	}
	sess := currentSession(r)
	err := s.store.ConfirmNotifyEmail(withAudit(r, s.selfEntry(r, "profile.notify_email_confirm")), sess.UserID,
		store.NotifyCodeHash(sess.UserID, code), s.currentTime())
	switch {
	case errors.Is(err, store.ErrNotifyWrongCode):
		s.writeJSON(w, http.StatusBadRequest, apiError{Error: "wrong code", Code: errWrongCode})
		return
	case errors.Is(err, store.ErrNotifyCodeTries):
		s.writeJSON(w, http.StatusConflict, apiError{Error: "too many wrong codes; request a new one", Code: errCodeSpent})
		return
	case errors.Is(err, store.ErrNotifyNoCode):
		s.writeJSON(w, http.StatusConflict, apiError{Error: "no valid code; request a new one", Code: errNoCode})
		return
	case errors.Is(err, store.ErrNotifyAdminOnly):
		s.fail(w, r, http.StatusForbidden, notificationsAdminOnly, nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	out, err := s.notificationsState(r, sess.UserID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handleRemoveNotifications removes the address; also when alert mail is
// off.
func (s *Server) handleRemoveNotifications(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminFor(w, r, notificationsAdminOnly) {
		return
	}
	err := s.store.RemoveNotifyEmail(withAudit(r, s.selfEntry(r, "profile.notify_email_remove")), currentSession(r).UserID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.failStore(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// notifyCodeAlphabet has no 0, O, 1 or I.
const notifyCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const notifyCodeLen = 6

// newNotifyCode is a random code of notifyCodeLen characters.
func newNotifyCode() string {
	b := make([]byte, notifyCodeLen)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = notifyCodeAlphabet[int(b[i])%len(notifyCodeAlphabet)]
	}
	return string(b)
}

// normalizeNotifyCode drops spaces and dashes and capitalizes, so a code
// typed in lower case or copied with a space counts.
func normalizeNotifyCode(s string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(s))
}

// notifyEmailBounced reports whether the admin's own address bounced, for
// the banner of GET /api/accounts.
func (s *Server) notifyEmailBounced(ctx context.Context, userID int64) (bool, error) {
	if s.alertMail == nil {
		return false, nil
	}
	st, err := s.store.NotifyEmail(ctx, userID)
	return err == nil && st.BouncedAt != nil && st.ConfirmedAt == nil, err
}
