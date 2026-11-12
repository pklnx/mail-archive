package smtpd

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// SMTP AUTH (#395): a mail server logs in with the user name of an smtp
// account (auth.SMTPUsername) and one of its secrets. go-smtp offers AUTH
// only over TLS and answers it with 523 before STARTTLS; STARTTLS starts a
// new session, so a login never outlives the plain-text part.
//
// The password is client input: a customer may type a real mailbox password
// into the wrong field. It is hashed only once it has the format of an SMTP
// secret, and neither it nor its hash nor any SASL bytes are ever logged.

// maxSASLField caps a user name, password or authorization identity.
const maxSASLField = 1024

// SASL mechanisms the server offers with Auth.
const (
	mechPlain = sasl.Plain
	mechLogin = "LOGIN"
)

var (
	// errAuthBlocked answers a login from an address with too many
	// failures, before its credentials are looked at.
	errAuthBlocked = &smtp.SMTPError{Code: 454, EnhancedCode: smtp.EnhancedCode{4, 7, 0},
		Message: "too many failed logins, try again later"}
	// errAuthTemp answers a login that could not be checked.
	errAuthTemp = &smtp.SMTPError{Code: 454, EnhancedCode: smtp.EnhancedCode{4, 7, 0},
		Message: "temporary authentication failure"}
	// errAuthFailed answers every refused login alike, whatever the reason.
	errAuthFailed = &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8},
		Message: "authentication failed"}
)

// AuthMechanisms lists what EHLO announces after STARTTLS: nothing without
// Auth.
func (ss *session) AuthMechanisms() []string {
	if !ss.s.Auth {
		return nil
	}
	return []string{mechPlain, mechLogin}
}

// Auth starts a SASL exchange. Without Auth every mechanism is unknown
// (504 after STARTTLS; go-smtp answers 523 before it). A blocked address
// gets 454 at once, before it sends a user name or password.
func (ss *session) Auth(mech string) (sasl.Server, error) {
	if !ss.s.Auth {
		return nil, smtp.ErrAuthUnknownMechanism
	}
	if mech != mechPlain && mech != mechLogin {
		return nil, smtp.ErrAuthUnknownMechanism
	}
	if wait := ss.s.Limiter.AddrBlocked(auth.LimitKey(ss.ip)); wait > 0 {
		ss.s.Logger.Warn("smtp auth blocked", "client_ip", ss.ip.String(), "mechanism", mech, "until", ss.s.Now().Add(wait))
		return nil, errAuthBlocked
	}
	switch mech {
	case mechPlain:
		return &plainServer{ss: ss}, nil
	case mechLogin:
		return &loginServer{ss: ss}, nil
	}
	return nil, smtp.ErrAuthUnknownMechanism
}

// plainServer is RFC 4616 PLAIN. go-sasl has one, but it hands a malformed
// answer to the client as its own error text; this one answers every
// failure with an SMTPError and counts it.
type plainServer struct {
	ss   *session
	done bool
}

func (p *plainServer) Next(response []byte) ([]byte, bool, error) {
	if p.done {
		return nil, true, errAuthFailed
	}
	if response == nil {
		return []byte{}, false, nil // no initial response: ask for it
	}
	p.done = true
	parts := bytes.Split(response, []byte{0})
	if len(parts) != 3 || len(parts[0]) > maxSASLField || len(parts[1]) > maxSASLField || len(parts[2]) > maxSASLField {
		return nil, true, p.ss.authenticate(mechPlain, "", "", false)
	}
	authzid, user, pass := string(parts[0]), string(parts[1]), string(parts[2])
	// The authorization identity must be empty or the user name: a login
	// acts only as itself.
	return nil, true, p.ss.authenticate(mechPlain, user, pass, authzid == "" || authzid == user)
}

// loginServer is the LOGIN mechanism (draft-murchison-sasl-login), which
// go-sasl has no server for. Some clients send the user name with the
// AUTH command.
type loginServer struct {
	ss   *session
	step int
	user string
}

func (l *loginServer) Next(response []byte) ([]byte, bool, error) {
	if len(response) > maxSASLField {
		l.step = 3
		return nil, true, l.ss.authenticate(mechLogin, "", "", false)
	}
	switch l.step {
	case 0:
		if response == nil {
			l.step = 1
			return []byte("Username:"), false, nil
		}
		l.user, l.step = string(response), 2
		return []byte("Password:"), false, nil
	case 1:
		l.user, l.step = string(response), 2
		return []byte("Password:"), false, nil
	case 2:
		l.step = 3
		return nil, true, l.ss.authenticate(mechLogin, l.user, string(response), true)
	}
	return nil, true, errAuthFailed
}

// authenticate checks a login, in this order: the address's failures in the
// limiter (454 while blocked, without looking at the credentials), the
// secret's format, the secret (unknown, revoked, expired, its account
// removed or not an smtp account), and that the user name is its
// account's. Every refusal answers the same 535 and counts as a failure of
// the client address; a database error answers 454 and counts nothing.
func (ss *session) authenticate(mech, user, pass string, wellFormed bool) error {
	log := ss.s.Logger.With("client_ip", ss.ip.String(), "mechanism", mech)
	// The user name only in its own strict format: a customer may type
	// the secret, or a password, into the user name field.
	if _, ok := auth.ParseSMTPUsername(user); ok {
		log = log.With("username", user)
	} else {
		log = log.With("username_valid", false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	a, wait, err := ss.s.Limiter.BeginAddr(ctx, auth.LimitKey(ss.ip))
	if err != nil {
		log.Error("smtp auth not checked", "err", err)
		return errAuthTemp
	}
	if a == nil {
		log.Warn("smtp auth blocked", "until", ss.s.Now().Add(wait))
		return errAuthBlocked
	}
	defer a.Release()
	fail := func(reason string) error {
		a.Fail()
		log.Warn("smtp auth failed", "reason", reason)
		return errAuthFailed
	}
	if !wellFormed || !auth.ValidSMTPSecretFormat(pass) {
		return fail("malformed")
	}
	login, err := ss.s.logins.SMTPLogin(ctx, auth.HashSMTPSecret(pass), ss.s.Now())
	if reason := loginRefusal(err); reason != "" {
		return fail(reason)
	}
	if err != nil {
		a.Release()
		log.Error("smtp auth not checked", "err", err)
		return errAuthTemp
	}
	if id, ok := auth.ParseSMTPUsername(user); !ok || id != login.AccountID {
		return fail("wrong_user")
	}
	a.Succeed()
	ss.cred, ss.account = login.CredentialID, login.AccountID
	if g := gated(ss.conn.Conn()); g != nil {
		g.authenticated()
	}
	log.Info("smtp auth", "credential_id", login.CredentialID)
	return nil
}

// loginRefusal names why the store refused a secret, for the log; "" for
// no refusal (nil or another error).
func loginRefusal(err error) string {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "unknown"
	case errors.Is(err, store.ErrSMTPCredentialRevoked):
		return "revoked"
	case errors.Is(err, store.ErrSMTPCredentialExpired):
		return "expired"
	case errors.Is(err, store.ErrAccountRemoved):
		return "account_removed"
	case errors.Is(err, store.ErrNotSMTP):
		return "not_smtp"
	}
	return ""
}

// gated finds the gate's connection under c, also below TLS.
func gated(c net.Conn) *gatedConn {
	if t, ok := c.(*tls.Conn); ok {
		c = t.NetConn()
	}
	g, _ := c.(*gatedConn)
	return g
}
