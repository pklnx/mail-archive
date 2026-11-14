// Package gobd writes and verifies the GoBD export, format v1: the
// original .eml files of a selection, CSV tables with an index.xml
// description (the "Beschreibungsstandard" of the GoBD annex), a SHA-256
// manifest and a signed statement (internal/attest, statement.go).
//
// The package has no database. The archive layer selects the messages and
// feeds them to a Writer in the pass order of the format; Verify checks an
// export, a directory or a ZIP, with nothing but the files and the keys
// the customer trusts. The format is specified in
// docs/development/architecture.md, "GoBD export format v1".
package gobd

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/keepia/archive/internal/csvsafe"
)

// Fixed file names of format v1. Paths are ASCII and use "/".
const (
	ReadmeFile    = "README.txt"
	IndexFile     = "index.xml"
	MessagesFile  = "messages.csv"
	LocationsFile = "locations.csv"
	MissingFile   = "missing.csv"
	SumsFile      = "SHA256SUMS"
	StatementFile = "STATEMENT.txt"
	ChainFile     = "attest/attestations.jsonl"
	PublicKeyFile = "attest/signing.pub"
	RetiredFile   = "attest/retired-keys.pem"
	LeavesPrefix  = "attest/leaves/"
	MailPrefix    = "mail/"
)

// MaxValueBytes is the longest CSV value. A longer header value (a
// recipient list of thousands of addresses) is cut at a character
// boundary; the .eml keeps the full value. It bounds a CSV line, which
// Verify reads one at a time.
const MaxValueBytes = 32 << 10

// maxLine bounds a CSV line in Verify: 17 values of MaxValueBytes with
// quotes and separators fit.
const maxLine = 1 << 20

// MaxMissing is the most messages an export lists in missing.csv. The
// writer holds them in memory until pass C; an archive with more broken
// files needs verify, not an export.
const MaxMissing = 100_000

// kind is how a column is written and read.
type kind int

const (
	text    kind = iota // quoted, see textValue
	integer             // digits, or empty where the column allows it
	date                // DD.MM.YYYY, unquoted
)

// column is a CSV column with its index.xml description.
type column struct {
	name string
	kind kind
	// optional: an integer column may be empty.
	optional bool
	// form is what Verify checks beyond the kind.
	form form
	desc string
}

// form is the shape of a value that Verify checks.
type form int

const (
	formAny      form = iota
	formSHA           // 64 lowercase hex characters
	formClock         // HH:MM:SS
	formUTC           // ISO 8601 UTC, whole seconds
	formUTCOpt        // formUTC or empty
	formBool          // 0 or 1
	formIDs           // positive integers separated by single spaces, or empty
	formNonEmpty      // any text but the empty one
)

// The columns of the three tables, in file order.
var (
	messageColumns = []column{
		{name: "sha256", kind: text, form: formSHA, desc: "SHA-256 of the .eml file (key)"},
		{name: "file", kind: text, form: formNonEmpty, desc: "path of the .eml file in this export"},
		{name: "size", kind: integer, desc: "size of the .eml file in bytes"},
		{name: "date", kind: date, desc: "date of the message (Date header, else arrival) in the export time zone"},
		{name: "time", kind: text, form: formClock, desc: "time of the message, HH:MM:SS, in the export time zone"},
		{name: "date_utc", kind: text, form: formUTC, desc: "date and time of the message in UTC, ISO 8601"},
		{name: "from", kind: text, desc: "From header"},
		{name: "to", kind: text, desc: "To header"},
		{name: "cc", kind: text, desc: "Cc header"},
		{name: "subject", kind: text, desc: "Subject header"},
		{name: "message_id", kind: text, desc: "Message-ID header"},
		{name: "in_reply_to", kind: text, desc: "In-Reply-To header"},
		{name: "has_attachment", kind: integer, form: formBool, desc: "1 if the message has attachments, else 0"},
		{name: "attachment_names", kind: text, desc: "file names of the attachments, separated by |"},
		{name: "archived_at", kind: text, form: formUTC, desc: "when the archive stored the message, UTC, ISO 8601"},
		{name: "archive_seq", kind: integer, desc: "sequence number of the message in the archive (instance-wide)"},
		{name: "attestation", kind: integer, optional: true, desc: "seq of the attestation that covers the message; empty if none yet"},
	}
	locationColumns = []column{
		{name: "sha256", kind: text, form: formSHA, desc: "SHA-256 of the message (refers to messages)"},
		{name: "owner", kind: text, desc: "user name of the account's owner; empty for an account without owner"},
		{name: "account", kind: text, form: formNonEmpty, desc: "account name"},
		{name: "account_kind", kind: text, desc: "imap, smtp (journaling) or import"},
		{name: "folder", kind: text, desc: "folder name"},
		{name: "uidvalidity", kind: integer, desc: "IMAP UIDVALIDITY of the folder"},
		{name: "uid", kind: integer, desc: "IMAP UID of the message in the folder"},
		{name: "flags", kind: text, desc: "IMAP flags, separated by spaces"},
		{name: "internal_date", kind: text, form: formUTCOpt, desc: "IMAP INTERNALDATE, UTC, ISO 8601"},
		{name: "first_seen", kind: text, form: formUTC, desc: "first seen in this place, UTC, ISO 8601"},
		{name: "last_seen", kind: text, form: formUTC, desc: "last seen in this place, UTC, ISO 8601"},
		{name: "gone_at", kind: text, form: formUTCOpt, desc: "when the message left this place on the server, UTC, ISO 8601"},
		{name: "envelope_from", kind: text, desc: "SMTP MAIL FROM (journaling)"},
		{name: "envelope_to", kind: text, desc: "SMTP RCPT TO, separated by spaces (journaling; includes Bcc recipients)"},
		{name: "received_at", kind: text, form: formUTCOpt, desc: "SMTP delivery time, UTC, ISO 8601 (journaling)"},
		{name: "retain_until", kind: text, form: formUTCOpt, desc: "end of the retention period, UTC, ISO 8601"},
		{name: "holds", kind: text, form: formIDs, desc: "ids of legal holds on this place, separated by spaces (only in exports by legal hold)"},
	}
	missingColumns = []column{
		{name: "sha256", kind: text, form: formSHA, desc: "SHA-256 of the message whose file is missing or damaged"},
		{name: "file", kind: text, form: formNonEmpty, desc: "path the .eml file would have in this export"},
		{name: "reason", kind: text, form: formNonEmpty, desc: "why the file is not in the export"},
	}
)

// table is one CSV file and its description.
type table struct {
	file, name, desc string
	columns          []column
	// keys are the primary key columns; the description standard's DTD
	// wants them first (VariablePrimaryKey+ before VariableColumn*), so
	// they are the leading columns. refs are the foreign key columns.
	keys []string
	refs map[string]string
}

var tables = []table{
	{file: MessagesFile, name: "messages", desc: "one row per exported message", columns: messageColumns,
		keys: []string{"sha256"}},
	{file: LocationsFile, name: "locations", desc: "one row per selected place of a message", columns: locationColumns,
		keys: []string{"sha256", "owner", "account", "account_kind", "folder", "uidvalidity", "uid"},
		refs: map[string]string{"sha256": "messages"}},
	{file: MissingFile, name: "missing", desc: "messages of the selection without a valid file", columns: missingColumns,
		keys: []string{"sha256"}},
}

// header is the first line of a table.
func header(cols []column) string {
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(`"` + c.name + `"`)
	}
	b.WriteString("\r\n")
	return b.String()
}

// textValue is how a text value appears in a CSV file, without its quotes:
// valid UTF-8; " becomes ' (GoBD annex, item 4: the text quote never
// appears in a value); CR, LF, TAB, other control characters and the
// Unicode line and paragraph separators become a space, so every record is
// one line; at most MaxValueBytes; and a leading = + - @ gets a leading '
// (csvsafe).
func textValue(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '"':
			return '\''
		case unicode.IsControl(r), r == ' ', r == ' ':
			return ' '
		}
		return r
	}, s)
	if len(s) > MaxValueBytes {
		cut := MaxValueBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return csvsafe.Cell(s)
}

// localTime is t in loc, clamped to the years 1 to 9999 so that the date
// column always has the form DD.MM.YYYY. date_utc keeps the real value.
func localTime(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	switch {
	case t.Year() < 1:
		return time.Date(1, 1, 1, 0, 0, 0, 0, loc)
	case t.Year() > 9999:
		return time.Date(9999, 12, 31, 23, 59, 59, 0, loc)
	}
	return t
}

// formatDate is DD.MM.YYYY; formatClock HH:MM:SS; both in loc.
func formatDate(t time.Time, loc *time.Location) string {
	return localTime(t, loc).Format("02.01.2006")
}

func formatClock(t time.Time, loc *time.Location) string {
	return localTime(t, loc).Format("15:04:05")
}

// formatUTC is ISO 8601 in UTC with whole seconds; "" for the zero time.
func formatUTC(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return localTime(t, time.UTC).Format("2006-01-02T15:04:05Z")
}

func formatOptUTC(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatUTC(*t)
}

// MessagePath is where the .eml of sha is in an export: mail/YYYY/MM/,
// the month of sortAt in loc (as in the date column).
func MessagePath(sha string, sortAt time.Time, loc *time.Location) string {
	t := localTime(sortAt, loc)
	return fmt.Sprintf("%s%04d/%02d/%s.eml", MailPrefix, t.Year(), int(t.Month()), sha)
}

// LeavesPath is where the leaves file of attestation seq is in an export.
func LeavesPath(seq int64) string {
	return LeavesPrefix + strconv.FormatInt(seq, 10) + ".txt"
}

// pathFromDate is MessagePath for the date column (DD.MM.YYYY) as written,
// so Verify needs no time zone database.
func pathFromDate(sha, ddmmyyyy string) string {
	return MailPrefix + ddmmyyyy[6:10] + "/" + ddmmyyyy[3:5] + "/" + sha + ".eml"
}

func isSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
