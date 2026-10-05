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

// Config describes how to reach and log in to an IMAP server.
type Config struct {
	Host     string
	Port     int
	TLSMode  string
	Username string
	Password string
	// DialTimeout limits connection setup. Defaults to 30s.
	DialTimeout time.Duration
	// TLSConfig overrides the default TLS configuration (used in tests).
	TLSConfig *tls.Config
}

// Conn is an authenticated, read-only IMAP session.
type Conn struct {
	c *imapclient.Client
}

// Dial connects and logs in. The context cancels connection setup and,
// while the connection is open, closes it when done.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	timeout := cfg.DialTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	opts := &imapclient.Options{
		Dialer:    &net.Dialer{Timeout: timeout},
		TLSConfig: cfg.TLSConfig,
	}
	if opts.TLSConfig == nil {
		opts.TLSConfig = &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	var (
		c   *imapclient.Client
		err error
	)
	switch cfg.TLSMode {
	case TLSModeTLS, "":
		c, err = imapclient.DialTLS(addr, opts)
	case TLSModeSTARTTLS:
		c, err = imapclient.DialStartTLS(addr, opts)
	case TLSModeNone:
		c, err = imapclient.DialInsecure(addr, opts)
	default:
		return nil, fmt.Errorf("unknown TLS mode %q", cfg.TLSMode)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	conn := &Conn{c: c}
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-c.Closed():
		}
	}()

	if err := conn.login(cfg.Username, cfg.Password); err != nil {
		_ = c.Close()
		return nil, err
	}
	return conn, nil
}

func (conn *Conn) login(username, password string) error {
	caps := conn.c.Caps()
	if caps.Has(imap.CapLoginDisabled) && caps.Has(imap.AuthCap(sasl.Plain)) {
		if err := conn.c.Authenticate(sasl.NewPlainClient("", username, password)); err != nil {
			return fmt.Errorf("authenticate: %w", err)
		}
		return nil
	}
	if err := conn.c.Login(username, password).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	return nil
}

// Close logs out and closes the connection.
func (conn *Conn) Close() error {
	_ = conn.c.Logout().Wait()
	return conn.c.Close()
}

// Folder is a selectable mailbox on the server.
type Folder struct {
	Name  string
	Attrs []string
}

// ListFolders returns all selectable folders.
func (conn *Conn) ListFolders() ([]Folder, error) {
	list, err := conn.c.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	var out []Folder
	for _, mb := range list {
		selectable := true
		attrs := make([]string, 0, len(mb.Attrs))
		for _, a := range mb.Attrs {
			if a == imap.MailboxAttrNoSelect || a == imap.MailboxAttrNonExistent {
				selectable = false
			}
			attrs = append(attrs, string(a))
		}
		if selectable {
			out = append(out, Folder{Name: mb.Mailbox, Attrs: attrs})
		}
	}
	return out, nil
}

// FolderStatus is the state reported by EXAMINE.
type FolderStatus struct {
	UIDValidity uint32
	UIDNext     uint32
	Messages    uint32
}

// Examine opens a folder read-only.
func (conn *Conn) Examine(name string) (FolderStatus, error) {
	data, err := conn.c.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return FolderStatus{}, fmt.Errorf("examine %q: %w", name, err)
	}
	return FolderStatus{
		UIDValidity: data.UIDValidity,
		UIDNext:     uint32(data.UIDNext),
		Messages:    data.NumMessages,
	}, nil
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
// with the complete message. Messages without a body are skipped.
func FetchAfter[T any](conn *Conn, after uint32, sink BodySink[T], handle func(Message[T]) error) error {
	var set imap.UIDSet
	set.AddRange(imap.UID(after+1), 0) // after+1:*
	section := &imap.FetchItemBodySection{Peek: true}
	cmd := conn.c.Fetch(set, &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		InternalDate: true,
		BodySection:  []*imap.FetchItemBodySection{section},
	})

	var handleErr error
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		if handleErr != nil {
			continue // drain remaining responses; Close does the rest
		}
		m, hasBody, err := readMessage(msg, sink)
		if err != nil {
			handleErr = err
			continue
		}
		// "n:*" always returns the last message even if its UID < n.
		if !hasBody || m.UID <= after {
			continue
		}
		if err := handle(m); err != nil {
			handleErr = err
		}
	}
	closeErr := cmd.Close()
	if handleErr != nil {
		return handleErr
	}
	if closeErr != nil {
		return fmt.Errorf("fetch: %w", closeErr)
	}
	return nil
}

func readMessage[T any](msg *imapclient.FetchMessageData, sink BodySink[T]) (Message[T], bool, error) {
	var m Message[T]
	hasBody := false
	for {
		item := msg.Next()
		if item == nil {
			break
		}
		switch item := item.(type) {
		case imapclient.FetchItemDataUID:
			m.UID = uint32(item.UID)
		case imapclient.FetchItemDataFlags:
			m.Flags = make([]string, len(item.Flags))
			for i, f := range item.Flags {
				m.Flags[i] = string(f)
			}
		case imapclient.FetchItemDataInternalDate:
			m.InternalDate = item.Time
		case imapclient.FetchItemDataBodySection:
			if item.Literal == nil {
				continue
			}
			body, err := sink(item.Literal)
			if err != nil {
				_, _ = io.Copy(io.Discard, item.Literal)
				return m, false, fmt.Errorf("store body: %w", err)
			}
			m.Body = body
			hasBody = true
		}
	}
	if hasBody && m.UID == 0 {
		return m, false, errors.New("server returned message without UID")
	}
	return m, hasBody, nil
}
