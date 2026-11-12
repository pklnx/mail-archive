package notify

import (
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Values in alerts (account and owner names, mailboxes, errors, folder
// names) come from users, IMAP servers and local errors. A message is
// built from pieces, constant text or such a value, and rendered once per
// target field, so a value is always shown as literal text:
//
//   - plain (title, message, the ntfy body and title): as it is;
//   - slack (text, read by Slack and Mattermost): as an inline code span,
//     backticks replaced, &, < and > as entities, which Slack decodes, so
//     no <!channel>, <@U…> or <url|text> reaches it;
//   - discord (content): as an inline code span, backticks replaced.
//
// In every style, control characters, bidi controls and line and
// paragraph separators in a value become a space, so a value cannot start
// a line or reorder the text around it.

// style is how a message is rendered for one target field.
type style int

const (
	stylePlain style = iota
	styleSlack
	styleDiscord
)

// Field limits, in runes.
const (
	// discordLimit is the longest content Discord accepts.
	discordLimit = 2000
	// textLimit caps text below the 16383 characters above which
	// Mattermost splits a webhook post; Slack allows more.
	textLimit = 16000
)

// piece is constant text or an untrusted value.
type piece struct {
	text  string
	value bool
	// quoted: in plain text the value is written as a Go string, as the
	// account's name is in the message body.
	quoted bool
}

func constant(s string) piece { return piece{text: s} }
func value(s string) piece    { return piece{text: s, value: true} }
func quoted(s string) piece   { return piece{text: s, value: true, quoted: true} }

// draft is a message before rendering: its title and one entry per
// account.
type draft struct {
	title   []piece
	entries [][]piece
}

// apply renders d into m: Title and Body as plain text, Slack and Discord
// fitted to their limits.
func (d *draft) apply(m *Message) {
	m.Title = render(d.title, stylePlain)
	bodies := make([]string, len(d.entries))
	for i, e := range d.entries {
		bodies[i] = render(e, stylePlain)
	}
	m.Body = strings.Join(bodies, "\n\n")
	m.Slack = d.fit(styleSlack, textLimit)
	m.Discord = d.fit(styleDiscord, discordLimit)
}

// sanitize replaces characters that break or reorder a line with a space.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || unicode.In(r, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, s)
}

var slackEntities = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// renderPiece writes one piece in st.
func renderPiece(p piece, st style) string {
	if !p.value {
		if st == styleSlack {
			return slackEntities.Replace(p.text)
		}
		return p.text
	}
	v := sanitize(p.text)
	if v == "" {
		v = "-"
	}
	switch st {
	case styleSlack:
		return "`" + slackEntities.Replace(strings.ReplaceAll(v, "`", "'")) + "`"
	case styleDiscord:
		return "`" + strings.ReplaceAll(v, "`", "'") + "`"
	}
	if p.quoted {
		return strconv.Quote(v)
	}
	return v
}

func render(pieces []piece, st style) string {
	var b strings.Builder
	for _, p := range pieces {
		b.WriteString(renderPiece(p, st))
	}
	return b.String()
}

// fit renders the title and the entries in st within limit runes. It
// keeps whole entries and replaces the ones that do not fit with a last
// line "… and N more (see accounts)". If the first entry does not fit
// even alone, it and then the title are shortened piece by piece: values
// are cut before they are escaped and wrapped, so every code span stays
// closed.
func (d *draft) fit(st style, limit int) string {
	fits := func(k int) bool { return utf8.RuneCountInString(render(d.sequence(k), st)) <= limit }
	// Every entry is longer than the closing line, so fewer entries are
	// shorter: search the fewest to drop.
	if drop := sort.Search(len(d.entries), func(i int) bool { return fits(len(d.entries) - i) }); drop < len(d.entries) {
		return render(d.sequence(len(d.entries)-drop), st)
	}
	k, end := 0, len(d.title)
	if len(d.entries) > 0 {
		k, end = 1, len(d.title)+1+len(d.entries[0])
	}
	return shorten(d.sequence(k), end, st, limit)
}

// sequence is the title, a line break, the first k entries and, if any
// are left out, the line counting them.
func (d *draft) sequence(k int) []piece {
	seq := append([]piece(nil), d.title...)
	seq = append(seq, constant("\n"))
	for i, e := range d.entries[:k] {
		if i > 0 {
			seq = append(seq, constant("\n\n"))
		}
		seq = append(seq, e...)
	}
	if more := len(d.entries) - k; more > 0 {
		seq = append(seq, constant("\n\n… and "+strconv.Itoa(more)+" more (see accounts)"))
	}
	return seq
}

// shorten cuts the pieces before end until seq renders in st within
// limit runes; the pieces from end on (the line counting left out
// entries) stay whole. First the longest values are cut to a common
// length, the longest that fits: a value is cut before it is escaped and
// ends in an ellipsis, so its code span is never empty or open, and each
// length is measured after escaping, because one rune of a value can grow
// to five (&amp;). If values of one rune still do not fit, constant text
// is cut at a rune, last first, the line break after the title excepted.
func shorten(seq []piece, end int, st style, limit int) string {
	fits := func(seq []piece) (string, bool) {
		s := render(seq, st)
		return s, utf8.RuneCountInString(s) <= limit
	}
	if s, ok := fits(seq); ok {
		return s
	}
	longest := 0
	for _, p := range seq[:end] {
		if p.value {
			longest = max(longest, utf8.RuneCountInString(p.text))
		}
	}
	// The longest length below longest that fits; values cut to one rune
	// if none does.
	n := sort.Search(longest, func(n int) bool {
		_, ok := fits(cutValues(seq, end, longest-n))
		return ok
	})
	seq = cutValues(seq, end, max(longest-n, 1))
	for i := end - 1; i >= 0; i-- {
		for seq[i].text != "" && !seq[i].value && seq[i].text != "\n" {
			s, ok := fits(seq)
			if ok {
				return s
			}
			r := []rune(seq[i].text)
			seq[i].text = string(r[:len(r)-1])
		}
	}
	s, _ := fits(seq)
	return s
}

// cutValues returns seq with every value before end that is longer than n
// runes cut to n-1 runes and an ellipsis.
func cutValues(seq []piece, end, n int) []piece {
	out := append([]piece(nil), seq...)
	for i, p := range out[:end] {
		if r := []rune(p.text); p.value && len(r) > n {
			out[i].text = string(r[:max(n-1, 0)]) + "…"
		}
	}
	return out
}

// encodeTitle writes s as RFC 2047 B-encoded words of at most 75
// characters, always, also for ASCII: an encoded word inside a value
// then decodes to its literal text. Words are split between runes and
// joined with a space, which decoders drop between encoded words.
func encodeTitle(s string) string {
	const maxBytes = 45 // 60 base64 characters plus the 12 of "=?UTF-8?b??="
	s = strings.ToValidUTF8(s, "�")
	var words []string
	for s != "" {
		n := 0
		for n < len(s) {
			_, size := utf8.DecodeRuneInString(s[n:])
			if n+size > maxBytes {
				break
			}
			n += size
		}
		words = append(words, "=?UTF-8?b?"+base64.StdEncoding.EncodeToString([]byte(s[:n]))+"?=")
		s = s[n:]
	}
	return strings.Join(words, " ")
}
