// Package imapsync is a read-only IMAP client used to copy messages.
//
// It never modifies the server: folders are opened with EXAMINE and message
// bodies are fetched with BODY.PEEK[], so the \Seen flag is left untouched.
package imapsync

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
)

// TLS modes, matching store.TLSMode values.
const (
	TLSModeTLS      = "tls"
	TLSModeSTARTTLS = "starttls"
	TLSModeNone     = "none"
)

// Defaults and fixed caps. The caps protect against a hostile server;
// MaxFolders and the listing limits are the ones an honest mailbox can
// reach, so they are settings.
const (
	// DefaultDialTimeout bounds connection setup: TCP, TLS, the greeting
	// and STARTTLS.
	DefaultDialTimeout = 30 * time.Second
	// DefaultIdleTimeout is how long the server may send nothing while a
	// command is in flight.
	DefaultIdleTimeout = 2 * time.Minute
	// DefaultMaxFolders caps the folders one LIST may answer.
	DefaultMaxFolders = 10_000
	// DefaultMaxListFlags caps the messages ListFlags accepts.
	DefaultMaxListFlags = 2_000_000
	// DefaultMaxListHeaders caps the messages ListHeaders accepts.
	DefaultMaxListHeaders = 500_000
	// MaxFolderName caps a folder name, in bytes.
	MaxFolderName = 1024
	// MaxFlags caps the flags of one message.
	MaxFlags = 1000
	// MaxMessageSize caps a message body, as the import does.
	MaxMessageSize = 256 << 20
	// MaxHeaderSize caps a header section in ListHeaders.
	MaxHeaderSize = 128 << 10
	// fetchSlack is how many more FETCH responses than the folder held
	// (EXISTS) a listing accepts: messages that arrive meanwhile.
	fetchSlack = 1000
	// logoutTimeout bounds LOGOUT in Close.
	logoutTimeout = 5 * time.Second
)

// Config describes how to reach and log in to an IMAP server.
type Config struct {
	Host     string
	Port     int
	TLSMode  string
	Username string
	Password string
	// DialTimeout limits connection setup (TCP, TLS, greeting, STARTTLS).
	// Defaults to DefaultDialTimeout.
	DialTimeout time.Duration
	// IdleTimeout is how long the server may send nothing while a command
	// is in flight. Defaults to DefaultIdleTimeout. Between commands no
	// deadline applies.
	IdleTimeout time.Duration
	// MaxFolders caps the folders one LIST may answer. Defaults to
	// DefaultMaxFolders.
	MaxFolders int
	// MaxRead caps the bytes read from the connection in total; 0 means
	// no cap. Login checks set it.
	MaxRead int64
	// TLSConfig overrides the default TLS configuration (used in tests).
	TLSConfig *tls.Config

	// WrapConn, if set, wraps the raw TCP connection below TLS and the
	// meter. It is a test seam (imaptest.SpyWrap); production code leaves
	// it nil.
	WrapConn func(net.Conn) net.Conn

	// noticesMax replaces the constant noticesMax if not 0; a test seam.
	noticesMax int64
}

// Conn is an authenticated, read-only IMAP session. A Conn is used by
// one goroutine at a time.
type Conn struct {
	c          *imapclient.Client
	m          *meteredConn
	ctx        context.Context
	stopCtx    func() bool
	maxFolders int
	loginSent  bool
	// examined is the folder last opened by Examine and its message count
	// from EXAMINE: go-imap signals the end of the command before it
	// records the selected mailbox, so Mailbox can lag behind.
	examined         string
	examinedMessages uint32
}

const opLogin = "login"

// Dial connects and logs in. The context cancels connection setup and,
// while the connection is open, closes it when done. Connection setup is
// also bounded by Config.DialTimeout.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	timeout := cfg.DialTimeout
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	idle := cfg.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	maxFolders := cfg.MaxFolders
	if maxFolders <= 0 {
		maxFolders = DefaultMaxFolders
	}
	switch cfg.TLSMode {
	case TLSModeTLS, "", TLSModeSTARTTLS, TLSModeNone:
	default:
		return nil, fmt.Errorf("unknown TLS mode %q", cfg.TLSMode)
	}
	tlsConfig := cfg.TLSConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var dialer net.Dialer
	raw, err := dialer.DialContext(dctx, "tcp", addr)
	if err != nil {
		cat := CategoryConnect
		if dctx.Err() != nil {
			cat = CategoryTimeout
		}
		return nil, &Error{Category: cat, Op: "connect to " + addr, Err: err}
	}
	if cfg.WrapConn != nil {
		raw = cfg.WrapConn(raw)
	}
	m := newMeteredConn(raw, idle, cfg.MaxRead)
	if cfg.noticesMax > 0 {
		m.noticesCap = cfg.noticesMax
	}
	// Setup is bounded by dctx; stopSetup runs before cancel.
	stopSetup := context.AfterFunc(dctx, func() { _ = m.Close() })
	defer stopSetup()
	conn := &Conn{m: m, ctx: ctx, maxFolders: maxFolders}
	conn.stopCtx = context.AfterFunc(ctx, func() { _ = m.Close() })
	// fail classifies err against c: the setup's context until the
	// greeting, the caller's for LOGIN.
	fail := func(c context.Context, op string, err error) (*Conn, error) {
		conn.stopCtx()
		_ = m.Close()
		if conn.c != nil {
			_ = conn.c.Close()
		}
		return nil, classify(c, m, op, conn.loginSent, err)
	}

	opts := &imapclient.Options{
		TLSConfig: tlsConfig,
		// Another client expunging or flagging messages in the folder
		// makes an honest server send notices our code does not use;
		// they earn a little credit, bounded per command.
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Expunge: func(seq uint32) { m.notice(expungeWire(seq)) },
			Fetch:   func(msg *imapclient.FetchMessageData) { m.notice(fetchWire(msg)) },
		},
	}
	switch cfg.TLSMode {
	case TLSModeTLS, "":
		tc := tlsConfig.Clone()
		if tc.NextProtos == nil {
			tc.NextProtos = []string{"imap"}
		}
		tlsConn := tls.Client(m, tc)
		if err := tlsConn.HandshakeContext(dctx); err != nil {
			if dctx.Err() == nil && m.failure() == nil {
				return fail(dctx, "tls handshake", &tlsError{err})
			}
			return fail(dctx, "tls handshake", err)
		}
		conn.c = imapclient.New(tlsConn, opts)
	case TLSModeSTARTTLS:
		conn.c, err = imapclient.NewStartTLS(m, opts)
		if err != nil {
			return fail(dctx, "starttls", err)
		}
	case TLSModeNone:
		conn.c = imapclient.New(m, opts)
	}
	if err := conn.c.WaitGreeting(); err != nil {
		return fail(dctx, "greeting", err)
	}
	if !stopSetup() {
		return fail(dctx, "connect to "+addr, context.DeadlineExceeded)
	}

	if err := conn.login(cfg.Username, cfg.Password); err != nil {
		return fail(ctx, opLogin, err)
	}
	return conn, nil
}

// tlsError marks an error from the TLS handshake of implicit TLS, such
// as an EOF from a server that does not speak TLS.
type tlsError struct{ err error }

func (e *tlsError) Error() string { return e.err.Error() }
func (e *tlsError) Unwrap() error { return e.err }

// caps returns the server's capabilities. go-imap may send CAPABILITY
// for it, so it counts as a command in flight.
func (conn *Conn) caps() imap.CapSet {
	conn.m.begin("CAPABILITY", time.Time{}, 0)
	defer conn.m.end()
	return conn.c.Caps()
}

func (conn *Conn) login(username, password string) error {
	caps := conn.caps()
	if conn.m.broken() {
		return errors.New("connection closed before login")
	}
	conn.loginSent = true
	if caps.Has(imap.CapLoginDisabled) && caps.Has(imap.AuthCap(sasl.Plain)) {
		conn.m.begin("AUTHENTICATE", time.Time{}, 0)
		defer conn.m.end()
		return conn.c.Authenticate(sasl.NewPlainClient("", username, password))
	}
	conn.m.begin("LOGIN", time.Time{}, 0)
	defer conn.m.end()
	return conn.c.Login(username, password).Wait()
}

// Broken reports whether the connection is closed: by a cap, the idle
// deadline, the credit budget, a read error, the context or Close. A
// broken Conn fails every further command.
func (conn *Conn) Broken() bool { return conn.m.broken() }

// wrap classifies an error from go-imap or the network.
func (conn *Conn) wrap(op string, err error) error {
	return classify(conn.ctx, conn.m, op, conn.loginSent, err)
}

// trip closes the connection because the server exceeded a cap and
// returns the classified error.
func (conn *Conn) trip(op string, lim *LimitError) error {
	return conn.wrap(op, conn.m.trip(lim))
}

// connErr ends the connection after it failed while our code read a
// literal (a read error, the idle deadline, the budget, MaxRead, the
// context) and returns the classified error. r is that literal.
//
// The rule: wait for go-imap's reader goroutine (Closed) only when r ended
// in a read error. Then go-imap's buffer is empty, so its reader fails on
// the next read and exits on its own, and waiting keeps go-imap's own
// discard from reading the buffer concurrently with it. In every other
// case (the literal is unread, partly read or at EOF) never wait: go-imap
// may have decoded further messages from its buffer and be waiting for
// someone to read their bodies. Closing the meter is enough; the caller's
// drain loop (cmd.Next) lets go-imap discard the rest, each read failing
// fast on the closed conn, until go-imap ends the command. Our code never
// reads a literal again after an error or after its EOF.
func (conn *Conn) connErr(op string, r *creditReader, err error) error {
	_ = conn.m.Close()
	if r != nil && r.err != nil {
		<-conn.c.Closed()
	}
	return conn.wrap(op, err)
}

// expungeWire is the size of "* seq EXPUNGE\r\n", the form of that notice
// an honest server sends. go-imap also accepts a lone LF, which makes such
// a notice earn 1 B more than it cost; with at most noticesMax notices per
// connection that is at most 1 MB of extra traffic. Counting CRLF as 1 B
// instead would make every honest notice cost 1 B, and a mass expunge of
// a million messages would run the budget dry.
func expungeWire(seq uint32) int {
	return len("* ") + len(strconv.FormatUint(uint64(seq), 10)) + len(" EXPUNGE\r\n")
}

// fetchWire drains an unsolicited FETCH and returns the size of its
// shortest form on the wire, counting only UID, FLAGS and MODSEQ; other
// items, body literals included, count nothing. It is a lower bound of
// what the server sent.
func fetchWire(msg *imapclient.FetchMessageData) int {
	n := len("* ") + len(strconv.FormatUint(uint64(msg.SeqNum), 10)) + len(" FETCH ()\r\n")
	items := 0
	for {
		item := msg.Next()
		if item == nil {
			break
		}
		switch item := item.(type) {
		case imapclient.FetchItemDataUID:
			n += len("UID ") + len(strconv.FormatUint(uint64(item.UID), 10))
		case imapclient.FetchItemDataFlags:
			n += len("FLAGS ()")
			for i, f := range item.Flags {
				n += len(f)
				if i > 0 {
					n++
				}
			}
		case imapclient.FetchItemDataModSeq:
			n += len("MODSEQ ()") + len(strconv.FormatUint(item.ModSeq, 10))
		default:
			continue
		}
		if items > 0 {
			n++ // the space between items
		}
		items++
	}
	return n
}

// noticeLimit is how many unsolicited notices a FETCH may earn credit
// for: the folder's message count plus fetchSlack.
func (conn *Conn) noticeLimit() int64 { return int64(conn.Messages()) + fetchSlack }

// Close logs out, allowing LOGOUT logoutTimeout, and closes the
// connection.
func (conn *Conn) Close() error {
	conn.stopCtx()
	if !conn.m.broken() {
		conn.m.begin("LOGOUT", time.Now().Add(logoutTimeout), 0)
		_ = conn.c.Logout().Wait()
		conn.m.end()
	}
	if err := conn.c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return conn.wrap("close", err)
	}
	return nil
}

// Folder is a selectable mailbox on the server.
type Folder struct {
	Name  string
	Attrs []string
	// Delim is the hierarchy delimiter, "" if the server has none (NIL).
	Delim string
}

// Special-use roles (RFC 6154) as returned by Folder.SpecialUse.
const (
	RoleAll     = "all"
	RoleArchive = "archive"
	RoleDrafts  = "drafts"
	RoleFlagged = "flagged"
	RoleJunk    = "junk"
	RoleSent    = "sent"
	RoleTrash   = "trash"
)

// SpecialUse returns the folder's special-use role, such as "trash" or
// "junk", or "" if the server did not mark it.
func (f Folder) SpecialUse() string {
	for _, a := range f.Attrs {
		switch role := strings.ToLower(strings.TrimPrefix(a, `\`)); role {
		case RoleAll, RoleArchive, RoleDrafts, RoleFlagged, RoleJunk, RoleSent, RoleTrash:
			return role
		}
	}
	return ""
}

// ListFolders returns all selectable folders. It streams the LIST answer
// and fails with a LimitError at more than Config.MaxFolders entries or
// a name longer than MaxFolderName.
func (conn *Conn) ListFolders() ([]Folder, error) {
	const op = "list folders"
	// Servers usually include special-use attributes (\Trash, \Junk, ...)
	// unasked; with LIST-EXTENDED we request them explicitly (RFC 6154).
	var opts *imap.ListOptions
	if caps := conn.caps(); caps.Has(imap.CapListExtended) && caps.Has(imap.CapSpecialUse) {
		opts = &imap.ListOptions{ReturnSpecialUse: true}
	}
	conn.m.begin("LIST", time.Time{}, 0)
	defer conn.m.end()
	cmd := conn.c.List("", "*", opts)
	var (
		out     []Folder
		n       int
		stopErr error
	)
	for {
		mb := cmd.Next()
		if mb == nil {
			break
		}
		if stopErr != nil {
			continue // the connection is closed; this drains go-imap
		}
		n++
		if n > conn.maxFolders {
			stopErr = conn.trip(op, &LimitError{What: LimitFolders, Limit: int64(conn.maxFolders), Configured: true})
			continue
		}
		if len(mb.Mailbox) > MaxFolderName {
			stopErr = conn.trip(op, &LimitError{What: LimitFolderName, Limit: MaxFolderName})
			continue
		}
		payload := len(mb.Mailbox)
		selectable := true
		attrs := make([]string, 0, len(mb.Attrs))
		for _, a := range mb.Attrs {
			if a == imap.MailboxAttrNoSelect || a == imap.MailboxAttrNonExistent {
				selectable = false
			}
			attrs = append(attrs, string(a))
			payload += len(a)
		}
		conn.m.credit(int64(creditResponse + payload))
		if selectable {
			f := Folder{Name: mb.Mailbox, Attrs: attrs}
			if mb.Delim != 0 {
				f.Delim = string(mb.Delim)
			}
			out = grow(out, conn.maxFolders)
			out = append(out, f)
		}
	}
	closeErr := cmd.Close()
	if stopErr != nil {
		return nil, stopErr
	}
	if closeErr != nil {
		return nil, conn.wrap(op, closeErr)
	}
	return out, nil
}

// grow makes room for one more element without letting the capacity pass
// limit, so a capped list never allocates beyond its cap.
func grow[T any](s []T, limit int) []T {
	if len(s) < cap(s) {
		return s
	}
	n := min(max(2*cap(s), 64), max(limit, len(s)+1))
	return append(make([]T, 0, n), s...)
}

// FolderStatus is the state reported by EXAMINE.
type FolderStatus struct {
	UIDValidity uint32
	UIDNext     uint32
	Messages    uint32
}

// Examine opens a folder read-only.
func (conn *Conn) Examine(name string) (FolderStatus, error) {
	conn.m.begin("EXAMINE", time.Time{}, 0)
	data, err := conn.c.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait()
	conn.m.end()
	if err != nil {
		return FolderStatus{}, conn.wrap(fmt.Sprintf("examine %q", name), err)
	}
	payload := 0
	for _, f := range data.Flags {
		payload += len(f)
	}
	for _, f := range data.PermanentFlags {
		payload += len(f)
	}
	conn.m.credit(int64(creditResponse + payload))
	conn.examined, conn.examinedMessages = name, data.NumMessages
	return FolderStatus{
		UIDValidity: data.UIDValidity,
		UIDNext:     uint32(data.UIDNext),
		Messages:    data.NumMessages,
	}, nil
}

// Messages returns the number of messages in the examined folder, kept up
// to date by the server's EXISTS and EXPUNGE responses.
func (conn *Conn) Messages() uint32 {
	if mb := conn.c.Mailbox(); mb != nil && mb.Name == conn.examined {
		return mb.NumMessages
	}
	return conn.examinedMessages
}

// Message is a fetched message. Body holds whatever the BodySink returned.
type Message[T any] struct {
	UID          uint32
	Flags        []string
	InternalDate time.Time
	Body         T
}

// BodySink consumes a message body while it is streamed from the server.
// It must read r to EOF (or return an error).
type BodySink[T any] func(r io.Reader) (T, error)

// FetchAfter streams every message in the currently examined folder whose
// UID is greater than after. sink consumes each body; handle is then called
// with the complete message. Messages without a body are skipped. It
// accepts at most the folder's live message count (EXISTS) plus 1,000
// FETCH responses, and no body larger than MaxMessageSize: the size is
// checked before sink is called. Errors from sink and handle are returned
// as they are.
func FetchAfter[T any](conn *Conn, after uint32, sink BodySink[T], handle func(Message[T]) error) error {
	const op = "fetch"
	var set imap.UIDSet
	set.AddRange(imap.UID(after+1), 0) // after+1:*
	section := &imap.FetchItemBodySection{Peek: true}
	conn.m.begin("UID FETCH", time.Time{}, conn.noticeLimit())
	defer conn.m.end()
	cmd := conn.c.Fetch(set, &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		InternalDate: true,
		BodySection:  []*imap.FetchItemBodySection{section},
	})
	return fetchMessages(conn, cmd, op, func() int64 { return int64(conn.Messages()) + fetchSlack }, sink, func(m Message[T]) error {
		// "n:*" always returns the last message even if its UID < n.
		if m.UID <= after {
			return errSkip
		}
		return handle(m)
	})
}

// errSkip tells fetchMessages that a message was not used: it earns no
// credit.
var errSkip = errors.New("skip")

// fetchMessages reads the messages of a body FETCH. limit is evaluated
// on each response. handle gets each message with a body; it returns
// errSkip for a message it does not use. After an error from sink or
// handle, the rest of the answer is read and dropped, so that the
// connection stays usable; that error is returned as it is.
func fetchMessages[T any](conn *Conn, cmd *imapclient.FetchCommand, op string, limit func() int64, sink BodySink[T], handle func(Message[T]) error) error {
	var (
		n         int64
		handleErr error
		stopErr   error
	)
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		if stopErr != nil {
			continue // the connection is closed; this drains go-imap
		}
		n++
		if l := limit(); n > l {
			stopErr = conn.trip(op, &LimitError{What: LimitFetchResponses, Limit: l})
			continue
		}
		if handleErr != nil {
			// Drop the rest, crediting what our code reads, so a local
			// failure does not also break the connection.
			hadBody, err := drainMessage(conn, op, msg)
			if err != nil {
				stopErr = err
			} else if hadBody {
				conn.m.credit(creditResponse)
			}
			continue
		}
		m, hasBody, payload, err := readMessage(conn, op, msg, sink)
		if err != nil {
			if CategoryOf(err) != "" {
				stopErr = err
			} else {
				handleErr = err
			}
			continue
		}
		if !hasBody {
			continue
		}
		switch err := handle(m); {
		case errors.Is(err, errSkip):
		case err != nil:
			handleErr = err
		default:
			conn.m.credit(int64(creditResponse + payload))
		}
	}
	closeErr := cmd.Close()
	switch {
	case stopErr != nil:
		return stopErr
	case handleErr != nil:
		return handleErr
	case closeErr != nil:
		return conn.wrap(op, closeErr)
	}
	return nil
}

// drainMessage reads and drops a message, checking its caps. It reports
// whether the message had a body.
func drainMessage(conn *Conn, op string, msg *imapclient.FetchMessageData) (hadBody bool, err error) {
	var uid uint32
	for {
		item := msg.Next()
		if item == nil {
			return hadBody, nil
		}
		switch item := item.(type) {
		case imapclient.FetchItemDataUID:
			uid = uint32(item.UID)
		case imapclient.FetchItemDataBodySection:
			if item.Literal == nil {
				continue
			}
			if size := item.Literal.Size(); size > MaxMessageSize {
				return hadBody, conn.trip(op, &LimitError{What: LimitMessageSize, Limit: MaxMessageSize, UID: uid})
			}
			hadBody = true
			r := newCreditReader(item.Literal, conn.m)
			if _, err := io.Copy(io.Discard, r); err != nil {
				return hadBody, conn.connErr(op, r, fmt.Errorf("read body: %w", err))
			}
		}
	}
}

// readMessage reads one message of a body FETCH. payload is the size of
// what it kept besides the body. Errors that carry a Category come from
// a cap; others come from sink.
func readMessage[T any](conn *Conn, op string, msg *imapclient.FetchMessageData, sink BodySink[T]) (m Message[T], hasBody bool, payload int, err error) {
	for {
		item := msg.Next()
		if item == nil {
			break
		}
		switch item := item.(type) {
		case imapclient.FetchItemDataUID:
			m.UID = uint32(item.UID)
		case imapclient.FetchItemDataFlags:
			if len(item.Flags) > MaxFlags {
				return m, false, 0, conn.trip(op, &LimitError{What: LimitFlags, Limit: MaxFlags, UID: m.UID})
			}
			m.Flags = make([]string, len(item.Flags))
			for i, f := range item.Flags {
				m.Flags[i] = string(f)
				payload += len(f)
			}
		case imapclient.FetchItemDataInternalDate:
			m.InternalDate = item.Time
		case imapclient.FetchItemDataBodySection:
			if item.Literal == nil {
				continue
			}
			if size := item.Literal.Size(); size > MaxMessageSize {
				return m, false, 0, conn.trip(op, &LimitError{What: LimitMessageSize, Limit: MaxMessageSize, UID: m.UID})
			}
			r := newCreditReader(item.Literal, conn.m)
			body, err := sink(r)
			if err != nil {
				// A failure of the connection, not of the sink: the sink
				// only passed on the read error, or the conn or context
				// ended under it.
				if r.err != nil {
					return m, false, 0, conn.connErr(op, r, fmt.Errorf("read body: %w", err))
				}
				if conn.m.failure() != nil || conn.ctx.Err() != nil {
					return m, false, 0, conn.connErr(op, r, fmt.Errorf("store body: %w", err))
				}
				// The sink's own error; the reader is healthy, so the rest
				// of the literal can be read and dropped.
				if _, err := io.Copy(io.Discard, r); err != nil {
					return m, false, 0, conn.connErr(op, r, fmt.Errorf("read body: %w", err))
				}
				return m, false, 0, fmt.Errorf("store body: %w", err)
			}
			m.Body = body
			hasBody = true
		}
	}
	if hasBody && m.UID == 0 {
		return m, false, 0, conn.wrap(op, errors.New("server returned message without UID"))
	}
	return m, hasBody, payload, nil
}

// ErrIncomplete is returned by ListFlags when the server listed fewer
// messages than the folder holds.
var ErrIncomplete = errors.New("server listed fewer messages than the folder holds")

// listLimit is the FETCH responses a listing accepts: the folder's
// message count at the start plus fetchSlack, at most limit (def if 0).
// configured reports that limit is the bound in force.
func listLimit(conn *Conn, limit, def uint32) (n int64, configured bool) {
	if limit == 0 {
		limit = def
	}
	slack := int64(conn.Messages()) + fetchSlack
	if int64(limit) <= slack {
		return int64(limit), true
	}
	return slack, false
}

// ListFlags sends UID FETCH 1:* (UID FLAGS) in the currently examined
// folder and calls handle for each listed UID with its flags. It reads no
// bodies. It returns ErrIncomplete if fewer messages were listed than the
// folder held after the command (EXISTS), so a caller never mistakes a
// cut-off list for deleted messages. It refuses an empty folder, where
// some servers reject 1:*; check Messages first. It accepts at most the
// folder's message count at the start plus 1,000 responses, and at most
// limit (DefaultMaxListFlags if 0), and at most MaxFlags flags per
// message.
func ListFlags(conn *Conn, limit uint32, handle func(uid uint32, flags []imap.Flag)) error {
	const op = "list flags"
	if conn.Messages() == 0 {
		return errors.New("list flags: empty folder")
	}
	maxN, configured := listLimit(conn, limit, DefaultMaxListFlags)
	var set imap.UIDSet
	set.AddRange(1, 0) // 1:*
	conn.m.begin("UID FETCH", time.Time{}, conn.noticeLimit())
	defer conn.m.end()
	cmd := conn.c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true})
	var (
		n, seen  int64
		protoErr error
		stopErr  error
	)
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		if stopErr != nil {
			continue
		}
		n++
		if n > maxN {
			stopErr = conn.trip(op, &LimitError{What: LimitFetchResponses, Limit: maxN, Configured: configured})
			continue
		}
		var (
			uid   imap.UID
			flags []imap.Flag
		)
		for {
			item := msg.Next()
			if item == nil {
				break
			}
			switch item := item.(type) {
			case imapclient.FetchItemDataUID:
				uid = item.UID
			case imapclient.FetchItemDataFlags:
				if len(item.Flags) > MaxFlags && stopErr == nil {
					stopErr = conn.trip(op, &LimitError{What: LimitFlags, Limit: MaxFlags, UID: uint32(uid)})
				}
				flags = item.Flags
			}
		}
		if stopErr != nil {
			continue
		}
		if uid == 0 {
			if protoErr == nil {
				protoErr = conn.wrap(op, errors.New("server returned message without UID"))
			}
			continue
		}
		payload := 0
		for _, f := range flags {
			payload += len(f)
		}
		conn.m.credit(int64(creditResponse + payload))
		seen++
		handle(uint32(uid), flags)
	}
	closeErr := cmd.Close()
	if stopErr != nil {
		return stopErr
	}
	if closeErr != nil {
		return conn.wrap(op, closeErr)
	}
	if protoErr != nil {
		return protoErr
	}
	// EXISTS and EXPUNGE responses during the command update the count.
	if want := int64(conn.Messages()); seen < want {
		return fmt.Errorf("%w (%d of %d)", ErrIncomplete, seen, want)
	}
	return nil
}

// Header is a message's UID, INTERNALDATE and size, and its Message-ID,
// Date, From and Subject header fields as the server sent them
// ("Message-ID: <...>\r\nDate: ...\r\n...\r\n\r\n"), for the caller to
// parse.
type Header struct {
	UID          uint32
	InternalDate time.Time
	// Size is the message's RFC822.SIZE.
	Size   int64
	Fields []byte
}

// HeaderFields are the header fields ListHeaders fetches: what a fill
// compares with the messages received over SMTP.
var HeaderFields = []string{"Message-ID", "Date", "From", "Subject"}

// maxHeaderField bounds the bytes kept of a header section.
const maxHeaderField = 64 << 10

// ListHeaders sends UID FETCH after+1:* (UID INTERNALDATE RFC822.SIZE
// BODY.PEEK[HEADER.FIELDS (MESSAGE-ID DATE FROM SUBJECT)]) in the examined
// folder and calls handle for each message. It reads no bodies, and it skips the last
// message that "n:*" returns even if its UID is lower. An empty folder
// sends nothing: some servers reject "1:*" there. It accepts at most the
// folder's message count at the start plus 1,000 responses, at most limit
// (DefaultMaxListHeaders if 0), and no header section over MaxHeaderSize.
// Errors from handle are returned as they are.
func ListHeaders(conn *Conn, after, limit uint32, handle func(Header) error) error {
	const op = "fetch headers"
	if conn.Messages() == 0 {
		return nil
	}
	maxN, configured := listLimit(conn, limit, DefaultMaxListHeaders)
	var set imap.UIDSet
	set.AddRange(imap.UID(after+1), 0) // after+1:*
	section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: HeaderFields, Peek: true}
	conn.m.begin("UID FETCH", time.Time{}, conn.noticeLimit())
	defer conn.m.end()
	cmd := conn.c.Fetch(set, &imap.FetchOptions{UID: true, InternalDate: true, RFC822Size: true, BodySection: []*imap.FetchItemBodySection{section}})
	var (
		n         int64
		handleErr error
		stopErr   error
	)
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		if stopErr != nil {
			continue
		}
		n++
		if n > maxN {
			stopErr = conn.trip(op, &LimitError{What: LimitFetchResponses, Limit: maxN, Configured: configured})
			continue
		}
		var h Header
		for {
			item := msg.Next()
			if item == nil {
				break
			}
			switch item := item.(type) {
			case imapclient.FetchItemDataUID:
				h.UID = uint32(item.UID)
			case imapclient.FetchItemDataInternalDate:
				h.InternalDate = item.Time
			case imapclient.FetchItemDataRFC822Size:
				h.Size = item.Size
			case imapclient.FetchItemDataBodySection:
				if item.Literal == nil || stopErr != nil {
					continue
				}
				if item.Literal.Size() > MaxHeaderSize {
					stopErr = conn.trip(op, &LimitError{What: LimitHeaderSize, Limit: MaxHeaderSize, UID: h.UID})
					continue
				}
				r := newCreditReader(item.Literal, conn.m)
				b, err := io.ReadAll(io.LimitReader(r, maxHeaderField))
				if err == nil {
					_, err = io.Copy(io.Discard, r)
				}
				if err != nil {
					// Not read again after an error: go-imap's reader has
					// the connection back.
					stopErr = conn.connErr(op, r, fmt.Errorf("read header: %w", err))
					continue
				}
				h.Fields = b
			}
		}
		if stopErr != nil || handleErr != nil {
			continue // drain; Close does the rest
		}
		if h.UID == 0 {
			stopErr = conn.wrap(op, errors.New("server returned message without UID"))
			continue
		}
		if h.UID <= after {
			continue
		}
		conn.m.credit(int64(creditResponse + len(h.Fields)))
		if err := handle(h); err != nil {
			handleErr = err
		}
	}
	closeErr := cmd.Close()
	switch {
	case stopErr != nil:
		return stopErr
	case handleErr != nil:
		return handleErr
	case closeErr != nil:
		return conn.wrap(op, closeErr)
	}
	return nil
}

// fetchChunk is how many UIDs FetchUIDs asks for in one command.
const fetchChunk = 500

// FetchUIDs is FetchAfter for the given UIDs of the examined folder, sent
// in chunks of 500. A UID the server no longer has is left out. Each
// command accepts at most 2 × 500 FETCH responses.
func FetchUIDs[T any](conn *Conn, uids []uint32, sink BodySink[T], handle func(Message[T]) error) error {
	for start := 0; start < len(uids); start += fetchChunk {
		var set imap.UIDSet
		for _, u := range uids[start:min(start+fetchChunk, len(uids))] {
			set.AddNum(imap.UID(u))
		}
		conn.m.begin("UID FETCH", time.Time{}, conn.noticeLimit())
		cmd := conn.c.Fetch(set, &imap.FetchOptions{
			UID: true, Flags: true, InternalDate: true,
			BodySection: []*imap.FetchItemBodySection{{Peek: true}},
		})
		err := fetchMessages(conn, cmd, "fetch", func() int64 { return 2 * fetchChunk }, sink, handle)
		conn.m.end()
		if err != nil {
			return err
		}
	}
	return nil
}
