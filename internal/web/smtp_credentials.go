package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// SMTP credentials (#395) are the user name and secrets a mail server logs
// in with to deliver an smtp account's journal copies. Like the account's
// addresses, they are managed by its owner, who must be an admin; smtp
// accounts are never shared, so no other user reaches these routes. A new
// secret needs the password and, with 2FA on, a code, like an API key; it
// is in the answer to its creation and nowhere else. Revoking needs
// neither. Any admin may list and revoke every smtp account's secrets
// (#395 Q2), so a locked owner's secrets can be ended; creating stays with
// the owner.

// defaultSMTPGraceDays is how long the current secret stays usable after a
// new one is created, unless the request says otherwise.
const defaultSMTPGraceDays = 7

// adminOnlySMTPCredentials is the 403 answer for users who are not admins.
const adminOnlySMTPCredentials = "only admins can manage the SMTP credentials of smtp accounts"

// smtpCredentialJSON describes a secret, never the secret itself.
type smtpCredentialJSON struct {
	ID         int64      `json:"id"`
	Hint       string     `json:"hint"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	State      string     `json:"state"`
}

func (s *Server) smtpCredentialJSON(c *store.SMTPCredential) smtpCredentialJSON {
	return smtpCredentialJSON{
		ID: c.ID, Hint: c.Hint, CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt,
		LastUsedAt: c.LastUsedAt, RevokedAt: c.RevokedAt, State: c.State(s.currentTime()),
	}
}

type smtpCredentialsJSON struct {
	// Username is the SMTP user name of the account, "smtp-<id>".
	Username    string               `json:"username"`
	Credentials []smtpCredentialJSON `json:"credentials"`
}

// credentialAccount is pathAccount for the credential routes: the caller
// must be an admin, and the account an smtp account that is not removed.
func (s *Server) credentialAccount(w http.ResponseWriter, r *http.Request) (*store.Account, bool) {
	if !s.requireAdminFor(w, r, adminOnlySMTPCredentials) {
		return nil, false
	}
	a, ok := s.pathAccount(w, r)
	if !ok {
		return nil, false
	}
	if a.Kind != store.KindSMTP {
		s.fail(w, r, http.StatusConflict, "only smtp accounts have SMTP credentials", nil)
		return nil, false
	}
	return a, true
}

func (s *Server) handleListSMTPCredentials(w http.ResponseWriter, r *http.Request) {
	a, ok := s.credentialAccount(w, r)
	if !ok {
		return
	}
	list, err := s.store.SMTPCredentials(r.Context(), a.ID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := smtpCredentialsJSON{Username: auth.SMTPUsername(a.ID), Credentials: make([]smtpCredentialJSON, 0, len(list))}
	for i := range list {
		out.Credentials = append(out.Credentials, s.smtpCredentialJSON(&list[i]))
	}
	s.writeJSON(w, http.StatusOK, out)
}

type newSMTPCredentialInput struct {
	CurrentPassword string `json:"currentPassword"`
	// Code is a TOTP or recovery code, needed when the user has 2FA on.
	Code string `json:"code"`
	// GraceDays is how long the current secret stays usable, 0 to 30;
	// missing or null for 7. 0 revokes it at once.
	GraceDays *int `json:"graceDays"`
}

// supersededJSON is an older secret that a new one changed: it expires at
// expiresAt or was revoked at revokedAt.
type supersededJSON struct {
	ID        int64      `json:"id"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

type newSMTPCredentialJSON struct {
	Username string `json:"username"`
	// Secret is shown only here, once.
	Secret     string             `json:"secret"`
	Credential smtpCredentialJSON `json:"credential"`
	Previous   []supersededJSON   `json:"previous"`
}

func (s *Server) handleCreateSMTPCredential(w http.ResponseWriter, r *http.Request) {
	a, ok := s.credentialAccount(w, r)
	if !ok {
		return
	}
	var in newSMTPCredentialInput
	if !s.decode(w, r, &in) {
		return
	}
	grace := defaultSMTPGraceDays
	if in.GraceDays != nil {
		grace = *in.GraceDays
	}
	maxDays := int(store.MaxSMTPGrace / (24 * time.Hour))
	if grace < 0 || grace > maxDays {
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("graceDays must be 0 to %d", maxDays), nil)
		return
	}
	if !s.checkOwnLogin(w, r, in.CurrentPassword, in.Code) {
		return
	}
	secret, hash, hint, err := auth.NewSMTPSecret()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	sess := currentSession(r)
	e := s.targetedEntry(r, "smtp_credential.create")
	auditAccount(e, a)
	e.Detail("hint", hint)
	e.Detail("grace_days", grace)
	by := sess.UserID
	c, previous, err := s.store.CreateSMTPCredential(withAudit(r, e), a.Ref(), store.NewSMTPCredential{
		Hash: hash, Hint: hint, CreatedBy: &by, Grace: time.Duration(grace) * 24 * time.Hour, Now: s.currentTime(),
	})
	switch {
	case errors.Is(err, store.ErrStale):
		s.fail(w, r, http.StatusConflict, "the account was changed meanwhile; try again", nil)
		return
	case errors.Is(err, store.ErrAccountRemoved):
		s.fail(w, r, http.StatusConflict, "account was removed; its archived mail is kept, but it cannot be changed or synced", nil)
		return
	case errors.Is(err, store.ErrNotSMTP):
		s.fail(w, r, http.StatusConflict, "only smtp accounts have SMTP credentials", nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	out := newSMTPCredentialJSON{
		Username: auth.SMTPUsername(a.ID), Secret: secret, Credential: s.smtpCredentialJSON(c),
		Previous: make([]supersededJSON, 0, len(previous)),
	}
	for _, p := range previous {
		out.Previous = append(out.Previous, supersededJSON(p))
	}
	s.log.Info("smtp credential created", "account", a.Name, "credential_id", c.ID, "superseded", len(previous), "by", sess.UserName)
	s.writeJSON(w, http.StatusCreated, out)
}

// handleRevokeSMTPCredential revokes a secret of the owner's smtp account.
// It needs no password: revoking only takes power away.
func (s *Server) handleRevokeSMTPCredential(w http.ResponseWriter, r *http.Request) {
	a, ok := s.credentialAccount(w, r)
	if !ok {
		return
	}
	s.revokeSMTPCredential(w, r, &a.ID)
}

// adminSMTPCredentialJSON is a secret in the admins' list, with its
// account.
type adminSMTPCredentialJSON struct {
	smtpCredentialJSON
	Account  string `json:"account"`
	Username string `json:"username"`
	// Owner is empty for an account without owner.
	Owner          string `json:"owner"`
	AccountRemoved bool   `json:"accountRemoved"`
}

type adminSMTPCredentialsJSON struct {
	Credentials []adminSMTPCredentialJSON `json:"credentials"`
}

// handleListAllSMTPCredentials lists every smtp account's secrets for
// admins, so they can revoke them, for example after locking their owner.
func (s *Server) handleListAllSMTPCredentials(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminFor(w, r, adminOnlySMTPCredentials) {
		return
	}
	list, err := s.store.AllSMTPCredentials(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := adminSMTPCredentialsJSON{Credentials: make([]adminSMTPCredentialJSON, 0, len(list))}
	for i := range list {
		c := &list[i]
		out.Credentials = append(out.Credentials, adminSMTPCredentialJSON{
			smtpCredentialJSON: s.smtpCredentialJSON(c), Account: c.AccountName, Username: auth.SMTPUsername(c.AccountID),
			Owner: c.OwnerName, AccountRemoved: c.AccountRemoved,
		})
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handleRevokeAnySMTPCredential lets an admin revoke any smtp account's
// secret (#395 Q2).
func (s *Server) handleRevokeAnySMTPCredential(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminFor(w, r, adminOnlySMTPCredentials) {
		return
	}
	s.revokeSMTPCredential(w, r, nil)
}

// revokeSMTPCredential revokes the secret in the path: of account, or of
// any account for nil.
func (s *Server) revokeSMTPCredential(w http.ResponseWriter, r *http.Request, account *int64) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not found", nil)
		return
	}
	e := s.targetedEntry(r, "smtp_credential.revoke")
	if account == nil {
		e.Detail("by_admin", true)
	}
	by := userID(r)
	c, err := s.store.RevokeSMTPCredential(withAudit(r, e), account, id, &by, s.currentTime())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("smtp credential revoked", "account", c.AccountName, "credential_id", c.ID, "by", currentSession(r).UserName,
		"by_admin", account == nil)
	w.WriteHeader(http.StatusNoContent)
}

// smtpCredentialsActive counts the usable secrets of the smtp accounts u
// owns: they keep working while u is locked (#395 decision 9).
func (s *Server) smtpCredentialsActive(r *http.Request, u *store.User) (int64, error) {
	counts, err := s.store.UsableSMTPCredentialsByOwner(r.Context(), u.ID, s.currentTime())
	if err != nil {
		return 0, err
	}
	var n int64
	for _, c := range counts {
		n += c.N
	}
	return n, nil
}
