package archive

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pklnx/mail-archive/internal/imapsync"
)

// categoryText is the fixed sentence of each IMAP error category. It is
// what users, the web UI and alerts see instead of the server's own text
// (NO/BAD answers) or the network's (dial and TLS errors, which name
// addresses and certificates); that text goes only to the server log.
var categoryText = map[imapsync.Category]string{
	imapsync.CategoryConnect:  "cannot connect to the IMAP server",
	imapsync.CategoryTLS:      "the secure connection to the IMAP server failed",
	imapsync.CategoryLogin:    "the IMAP server refused the login",
	imapsync.CategoryTimeout:  "the IMAP server did not answer in time",
	imapsync.CategoryServer:   "the IMAP server refused a command",
	imapsync.CategoryProtocol: "the IMAP server sent an answer that cannot be read",
	imapsync.CategoryLimit:    "the IMAP server exceeded a limit",
}

// unknownIMAPText stands for a category without a sentence; imapsync has
// none, but a new one must not show its text.
const unknownIMAPText = "the IMAP server failed"

// CategoryText returns the fixed sentence of an IMAP error category.
func CategoryText(c imapsync.Category) string {
	if s, ok := categoryText[c]; ok {
		return s
	}
	return unknownIMAPText
}

// DescribeError returns the text of err to store and show: in sync run
// errors (which alerts send), a fill folder's error and the answer of a
// login check. Every IMAP error in it (an imapsync.Error, from the server
// or the network) is replaced by the fixed sentence of its category; the
// archive's own context around it stays, such as the folder name and the
// setting to raise. Local errors keep their text.
//
// For the limit and timeout categories the sentence names which limit or
// command; that comes from imapsync's own constants and counts, never from
// the server. If the context of an IMAP error cannot be told apart from its
// text, only the sentence of its category remains.
func DescribeError(err error) string {
	if err == nil {
		return ""
	}
	var ie *imapsync.Error
	if !errors.As(err, &ie) {
		return err.Error()
	}
	return describe(err)
}

// describe is DescribeError for an err that holds an imapsync.Error.
func describe(err error) string {
	if e, ok := err.(*imapsync.Error); ok {
		return describeIMAP(e)
	}
	if stuck, ok := err.(*StuckError); ok {
		return stuck.Error() // the archive's own sentence, without the server's
	}
	var inner []error
	switch u := err.(type) {
	case interface{ Unwrap() []error }:
		inner = u.Unwrap()
	case interface{ Unwrap() error }:
		inner = []error{u.Unwrap()}
	}
	// Each wrapped error's text is replaced in the wrapper's text, in
	// order, as errors.Join and fmt.Errorf("...: %w") write them.
	var b strings.Builder
	rest := err.Error()
	replaced := false
	for _, e := range inner {
		if e == nil {
			continue
		}
		s := e.Error()
		var ie *imapsync.Error
		if !errors.As(e, &ie) {
			// Local: its text stays, and the next is looked for after it.
			if i := strings.Index(rest, s); i >= 0 {
				b.WriteString(rest[:i+len(s)])
				rest = rest[i+len(s):]
			}
			continue
		}
		// One wrapped error: the last occurrence, as the wrapper's own
		// text before it (a folder name) may repeat it. Several (a join):
		// each in turn.
		i := strings.LastIndex(rest, s)
		if len(inner) > 1 {
			i = strings.Index(rest, s)
		}
		if i < 0 {
			return fallback(err)
		}
		b.WriteString(rest[:i])
		b.WriteString(describe(e))
		rest = rest[i+len(s):]
		replaced = true
	}
	if !replaced {
		// No wrapped error holds it: a type that finds it with an As method.
		return fallback(err)
	}
	b.WriteString(rest)
	return b.String()
}

// fallback is the sentence of the first IMAP error in err, for a wrapper
// whose text cannot be separated from the IMAP error's.
func fallback(err error) string {
	var ie *imapsync.Error
	errors.As(err, &ie)
	return describeIMAP(ie)
}

// describeIMAP is the sentence of e's category, with the limit or command
// for those categories.
func describeIMAP(e *imapsync.Error) string {
	s := CategoryText(e.Category)
	var lim *imapsync.LimitError
	var timeout *imapsync.TimeoutError
	switch {
	case e.Category == imapsync.CategoryLimit && errors.As(e.Err, &lim):
		s += fmt.Sprintf(" (%s: %d", lim.What, lim.Limit)
		if lim.UID != 0 {
			s += fmt.Sprintf(", message UID %d", lim.UID)
		}
		s += ")"
	case e.Category == imapsync.CategoryTimeout && errors.As(e.Err, &timeout) && timeout.Command != "":
		s += " (during " + timeout.Command + ")"
	}
	return s
}
