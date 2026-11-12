package imapsync

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

// Category is a short, stable class of an IMAP error, safe to show to
// users and in alerts: it never carries the server's own text.
type Category string

// The error categories. Every error that comes from the server or the
// network leaves this package as an *Error with one of them.
const (
	// CategoryConnect: the server could not be reached (DNS, dial).
	CategoryConnect Category = "connect"
	// CategoryTLS: the TLS handshake or certificate check failed.
	CategoryTLS Category = "tls"
	// CategoryLogin: the server answered NO or BAD to LOGIN or
	// AUTHENTICATE.
	CategoryLogin Category = "login"
	// CategoryTimeout: the server stopped sending (idle deadline), or the
	// caller's context or the dial deadline ended.
	CategoryTimeout Category = "timeout"
	// CategoryServer: the server answered NO or BAD to another command.
	CategoryServer Category = "server"
	// CategoryProtocol: the server sent something this client cannot use
	// (parse error, early EOF, a message without UID).
	CategoryProtocol Category = "protocol"
	// CategoryLimit: the server exceeded a size or count cap.
	CategoryLimit Category = "limit"
)

// Error is an error from the IMAP server or the network, with its
// category. Errors returned by a caller's callback are not wrapped.
type Error struct {
	Category Category
	// Op is what was being done, like "login" or `examine "INBOX"`.
	Op string
	// LoginSent reports whether LOGIN or AUTHENTICATE had been sent on
	// the connection when the error happened.
	LoginSent bool
	Err       error
}

func (e *Error) Error() string { return e.Op + ": " + e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

// CategoryOf returns the category of an error from this package, or ""
// if err carries none (a local error, such as one from a callback).
func CategoryOf(err error) Category {
	var e *Error
	if errors.As(err, &e) {
		return e.Category
	}
	return ""
}

// The caps a LimitError can name. The configurable ones (LimitFolders,
// LimitFetchResponses) carry the limit in force, so that a caller can
// name the setting that raises it.
const (
	// LimitUnrequested: the server sent more data than this client asked
	// for (the per-connection credit budget ran out).
	LimitUnrequested = "unrequested data"
	// LimitRead: the connection read more than Config.MaxRead.
	LimitRead = "total read"
	// LimitFolders: a LIST answered more than Config.MaxFolders folders.
	LimitFolders = "folders"
	// LimitFolderName: a folder name is longer than MaxFolderName bytes.
	LimitFolderName = "folder name length"
	// LimitFlags: a message has more than MaxFlags flags.
	LimitFlags = "flags per message"
	// LimitFetchResponses: a FETCH answered more messages than the folder
	// can hold.
	LimitFetchResponses = "FETCH responses"
	// LimitMessageSize: a message is larger than MaxMessageSize.
	LimitMessageSize = "message size"
	// LimitHeaderSize: a header section in ListHeaders is larger than
	// MaxHeaderSize.
	LimitHeaderSize = "header size"
)

// LimitError reports that the server exceeded a cap. The connection is
// closed when it is returned.
type LimitError struct {
	// What is one of the Limit* constants.
	What string
	// Limit is the cap that was exceeded.
	Limit int64
	// UID is the message's UID, 0 if unknown or not about a message.
	UID uint32
	// Configured reports that Limit is the configured or caller's limit
	// (Config.MaxFolders, the limit of ListFlags or ListHeaders), not a
	// fixed cap or the slack over the folder's message count, so that a
	// caller may name the setting that raises it.
	Configured bool
}

func (e *LimitError) Error() string {
	s := fmt.Sprintf("server exceeded the limit on %s (%d)", e.What, e.Limit)
	if e.UID != 0 {
		s += fmt.Sprintf(" at message UID %d", e.UID)
	}
	return s
}

// TimeoutError reports that the server sent nothing before a read
// deadline: the idle deadline while a command was in flight, or one of
// go-imap's own deadlines within a response. The connection is closed.
type TimeoutError struct {
	// Command is the IMAP command in flight, like "LIST"; "" if none.
	Command string
	// Idle is the idle deadline if that was the one that ran out, 0 if it
	// was go-imap's own deadline or LOGOUT's.
	Idle time.Duration
}

func (e *TimeoutError) Error() string {
	what := "within the read deadline"
	if e.Idle > 0 {
		what = "for " + e.Idle.String()
	}
	if e.Command == "" {
		return "server sent nothing " + what
	}
	return fmt.Sprintf("server sent nothing %s during %s", what, e.Command)
}

// Timeout reports true, like a net.Error.
func (e *TimeoutError) Timeout() bool { return true }

// classify wraps err from go-imap or the network into an *Error. The
// order matters: an ended context and the connection's own failure (a
// cap, the budget, MaxRead, the idle deadline) close the connection, and
// the error go-imap then reports is only a symptom of that.
func classify(ctx context.Context, m *meteredConn, op string, loginSent bool, err error) error {
	if err == nil {
		return nil
	}
	var done *Error
	if errors.As(err, &done) {
		return err
	}
	e := &Error{Op: op, LoginSent: loginSent, Err: err}
	var imapErr *imap.Error
	switch {
	case ctx != nil && ctx.Err() != nil:
		e.Category = CategoryTimeout
		e.Err = fmt.Errorf("%w: %w", context.Cause(ctx), err)
	case m != nil && m.failure() != nil:
		cause := m.failure()
		e.Err = cause
		var lim *LimitError
		if errors.As(cause, &lim) {
			e.Category = CategoryLimit
		} else {
			e.Category = CategoryTimeout
		}
	case errors.As(err, &imapErr):
		e.Category = CategoryServer
		if op == opLogin {
			e.Category = CategoryLogin
		}
	case isTLSError(err):
		e.Category = CategoryTLS
	case isTimeout(err):
		e.Category = CategoryTimeout
	default:
		e.Category = CategoryProtocol
	}
	return e
}

// isTLSError reports whether err comes from a TLS handshake or a
// certificate check.
func isTLSError(err error) bool {
	var (
		verify   *tls.CertificateVerificationError
		header   tls.RecordHeaderError
		alert    tls.AlertError
		unknown  x509.UnknownAuthorityError
		hostname x509.HostnameError
		invalid  x509.CertificateInvalidError
		marked   *tlsError
	)
	if errors.As(err, &marked) || errors.As(err, &verify) || errors.As(err, &header) || errors.As(err, &alert) ||
		errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return true
	}
	// crypto/tls reports most handshake failures as plain errors.
	return strings.Contains(err.Error(), "tls: ")
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
