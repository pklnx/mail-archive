package web

import (
	"unicode/utf8"

	"golang.org/x/net/html"
)

// This file is the CSS tokenizer that neutralizeCSS and cssFetchSurvivors
// share (neutralizecss.go). It follows CSS Syntax Level 3, section 3.3
// (preprocessing the input stream) and section 4 (tokenization), and reports
// the two places where CSS can make the browser request a URL:
//
//   - a URL start: the first code point of the value of a url token, or of
//     the string after a url( function token, unless the value starts with
//     "data:" (ASCII case-insensitive) or "#". A data: URL loads nothing
//     remote, and "#..." is a reference into the document itself.
//   - a function break: the "(" of a function token whose name is not on
//     cssSafeFunctions, the functions known to load nothing.
//
// There is no other way to name a URL in CSS: url() and functions that take
// a string (image-set(), image(), src() and whatever browsers add later).
// Chromium sends the document's origin with every image CSS loads, whatever
// the referrer policy, so a served mail may not have either (see
// handleMessageHTML).
//
// The tokenizer reads a stream of code points from a cssDecoder, which
// decodes HTML character references (attribute values) or not (raw text,
// already decoded text) and preprocesses the stream as section 3.3 says.
// Every code point carries the byte offset of its source, so a report says
// where to insert. The tokenizer looks ahead at most four code points, so it
// keeps only a small buffer: memory stays constant over a long value, such
// as an inlined image of several MiB.

// cssPoint is one code point of the preprocessed input stream.
type cssPoint struct {
	r rune
	// off is the byte offset of the code point's source: the code point
	// itself, a character reference, or a CR of a CRLF pair.
	off int
	// bare: the source right before off is a character reference without
	// ";". An ASCII letter or digit inserted at off could make that
	// reference decode differently (&quot, &#x22), so an insertion there
	// starts with ";".
	bare bool
}

// cssEOF is the code point the tokenizer sees past the end of the input.
const cssEOF rune = -1

// cssDecoder yields the code points of s[i:], after section 3.3's
// preprocessing: CR, FF and CRLF become LF, NUL and surrogates become
// U+FFFD. With refs, HTML character references are decoded as in an
// attribute value (decodeAttrRef).
type cssDecoder struct {
	s    string
	i    int
	refs bool
	// second is the second code point of a reference that decodes to two.
	second  rune
	hasSec  bool
	secOff  int
	lastRaw bool // the last source read was a reference without ";"
}

// raw returns the next code point before preprocessing, its offset and
// whether its source is a reference without ";".
func (d *cssDecoder) raw() (r rune, off int, bare bool, ok bool) {
	if d.hasSec {
		d.hasSec = false
		return d.second, d.secOff, d.lastRaw, true
	}
	if d.i >= len(d.s) {
		return cssEOF, len(d.s), false, false
	}
	off = d.i
	if d.refs && d.s[d.i] == '&' {
		if r1, r2, n, semi := decodeAttrRef(d.s[d.i:]); n > 0 {
			d.i += n
			if r2 != 0 {
				// The second code point gets the reference's offset too. No
				// insertion goes before it: the second code point of every
				// two-code-point reference is a combining mark or "j"
				// (&fjlig;), never one the tokenizer reports.
				d.second, d.hasSec, d.secOff = r2, true, off
			}
			d.lastRaw = !semi
			return r1, off, !semi, true
		}
	}
	r, w := utf8.DecodeRuneInString(d.s[d.i:])
	d.i += w
	d.lastRaw = false
	if r == '\r' {
		// The HTML input stream's own newline normalization, which comes
		// before character references are decoded: a CR in the bytes (and
		// a CRLF) is an LF, while &#13; stays a CR for section 3.3.
		if d.i < len(d.s) && d.s[d.i] == '\n' {
			d.i++
		}
		r = '\n'
	}
	return r, off, false, true
}

// skip moves past the bytes of s[i:] that are not in stop, and reports
// whether it moved. It reads no reference: stop must hold "&" with refs.
func (d *cssDecoder) skip(stop *[256]bool) bool {
	if d.hasSec {
		return false
	}
	i := d.i
	for i < len(d.s) && !stop[d.s[i]] {
		i++
	}
	moved := i > d.i
	d.i = i
	if moved {
		d.lastRaw = false
	}
	return moved
}

// next returns the next preprocessed code point; bare tells whether the
// source before it ended in a reference without ";".
func (d *cssDecoder) next(prevBare *bool) (cssPoint, bool) {
	r, off, bare, ok := d.raw()
	if !ok {
		return cssPoint{r: cssEOF, off: len(d.s)}, false
	}
	p := cssPoint{r: r, off: off, bare: *prevBare}
	*prevBare = bare
	switch {
	case r == '\r':
		p.r = '\n'
		// CRLF is one LF: peek at the next code point.
		save := *d
		if r2, _, bare2, ok2 := d.raw(); ok2 && r2 == '\n' {
			*prevBare = bare2
		} else {
			*d = save
		}
	case r == '\f':
		p.r = '\n'
	case r == 0 || (0xD800 <= r && r <= 0xDFFF):
		p.r = utf8.RuneError
	}
	return p, true
}

// decodeAttrRef decodes the character reference at the start of s ("&...")
// as the HTML tokenizer does in an attribute value: r1 (and r2 for a
// reference to two code points), the length n of its source and whether it
// ends in ";". n is 0 if s does not start with a reference that decodes;
// the "&" is then a literal character. It matches x/net/html's attribute
// unescaping, which the parse check relies on.
func decodeAttrRef(s string) (r1, r2 rune, n int, semi bool) {
	if len(s) < 2 {
		return 0, 0, 0, false
	}
	if s[1] == '#' {
		i := 2
		hex := false
		if i < len(s) && (s[i] == 'x' || s[i] == 'X') {
			hex = true
			i++
		}
		i0 := i
		var x rune
		for i < len(s) {
			c := s[i]
			var d rune
			switch {
			case '0' <= c && c <= '9':
				d = rune(c - '0')
			case hex && 'a' <= c && c <= 'f':
				d = rune(c-'a') + 10
			case hex && 'A' <= c && c <= 'F':
				d = rune(c-'A') + 10
			default:
				d = -1
			}
			if d < 0 {
				break
			}
			if x <= 0x10FFFF {
				if hex {
					x = 16*x + d
				} else {
					x = 10*x + d
				}
			}
			i++
		}
		if i == i0 {
			return 0, 0, 0, false
		}
		if i < len(s) && s[i] == ';' {
			i++
			semi = true
		}
		switch {
		case 0x80 <= x && x <= 0x9F:
			x = windows1252[x-0x80]
		case x == 0 || (0xD800 <= x && x <= 0xDFFF) || x > 0x10FFFF:
			x = utf8.RuneError
		}
		return x, 0, i, semi
	}
	// A named reference: the longest run of ASCII letters and digits, with
	// the ";" after it. In an attribute value a reference without ";" is
	// decoded only if the whole run names one and no "=" follows; a
	// shorter match is followed by a letter or digit and stays as it is.
	i := 1
	for i < len(s) && isASCIIAlnum(s[i]) {
		i++
	}
	if i == 1 {
		return 0, 0, 0, false
	}
	name := s[1:i]
	if i < len(s) && s[i] == ';' {
		if v, ok := namedRef(name + ";"); ok {
			r1, w := utf8.DecodeRuneInString(v)
			if w < len(v) {
				r2, _ = utf8.DecodeRuneInString(v[w:])
			}
			return r1, r2, i + 1, true
		}
		return 0, 0, 0, false
	}
	if i < len(s) && s[i] == '=' {
		return 0, 0, 0, false
	}
	if v, ok := namedRef(name); ok {
		r1, _ := utf8.DecodeRuneInString(v)
		return r1, 0, i, false
	}
	return 0, 0, 0, false
}

// namedRef returns the value of the named character reference name (with
// its ";", or one of the legacy names without), if it is one. x/net/html
// does not export its table, so its UnescapeString answers: a whole name
// decodes to the value alone, while a legacy prefix decodes with the rest of
// the name after it, which ends in ";" (or, without ";", is at least one
// more code point).
func namedRef(name string) (string, bool) {
	switch name {
	case "amp;", "amp":
		return "&", true
	case "quot;", "quot":
		return `"`, true
	case "lt;", "lt":
		return "<", true
	case "gt;", "gt":
		return ">", true
	case "nbsp;", "nbsp":
		return " ", true
	case "apos;":
		return "'", true
	case "semi;":
		return ";", true
	}
	in := "&" + name
	v := html.UnescapeString(in)
	if v == in {
		return "", false
	}
	if name[len(name)-1] == ';' {
		if v[len(v)-1] == ';' {
			return "", false
		}
		return v, true
	}
	if utf8.RuneCountInString(v) != 1 {
		return "", false
	}
	return v, true
}

// windows1252 maps the numeric references &#x80; to &#x9F; as the HTML
// tokenizer does.
var windows1252 = [32]rune{
	'€', '\u0081', '‚', 'ƒ', '„', '…', '†', '‡',
	'ˆ', '‰', 'Š', '‹', 'Œ', '\u008D', 'Ž', '\u008F',
	'\u0090', '‘', '’', '“', '”', '•', '–', '—',
	'˜', '™', 'š', '›', 'œ', '\u009D', 'ž', 'Ÿ',
}

func isASCIIAlnum(c byte) bool {
	return isASCIIAlpha(c) || '0' <= c && c <= '9'
}

// cssReportKind tells what a report is.
type cssReportKind uint8

const (
	cssURLStart  cssReportKind = iota // insert "data:," at the point
	cssFuncBreak                      // insert "_" at the point (before "(")
)

// cssReport is one place the tokenizer reports.
type cssReport struct {
	kind cssReportKind
	at   cssPoint
	// name is the function's name (lowercase, cut to cssNameMax code
	// points) for a function break, the value's start for a URL start;
	// for messages and tests.
	name string
}

// cssScanner is the tokenizer: section 4.3, "consume a token", repeated
// until the end of the input. It builds no tokens; it only consumes them
// and reports.
type cssScanner struct {
	d        cssDecoder
	prevBare bool
	// look holds the code points read ahead: look[:n].
	look [6]cssPoint
	n    int
	// report is called for every report, in input order. It returns false
	// to stop the scan.
	report func(cssReport) bool
	stop   bool
	// names: reports carry the name or the value's start.
	names bool
}

// scanCSS tokenizes s (with refs: an attribute value with character
// references; otherwise decoded text or raw text) and calls report for each
// report; offsets are relative to s. It stops when report returns false.
// With names, a report carries the function's name or the value's start.
func scanCSS(s string, refs, names bool, report func(cssReport) bool) {
	sc := cssScanner{d: cssDecoder{s: s, refs: refs}, report: report, names: names}
	sc.run()
}

// peek returns the code point k places ahead (0 is the next one), cssEOF
// past the end.
func (s *cssScanner) peek(k int) cssPoint {
	for s.n <= k {
		p, _ := s.d.next(&s.prevBare)
		s.look[s.n] = p
		s.n++
	}
	return s.look[k]
}

// consume returns the next code point and moves past it.
func (s *cssScanner) consume() cssPoint {
	p := s.peek(0)
	if p.r == cssEOF {
		return p
	}
	copy(s.look[:], s.look[1:s.n])
	s.n--
	return p
}

// skipPlain moves past code points that cannot end or change the token
// being consumed (stop holds the bytes that can), without decoding them one
// by one: a URL value or a string can be an inlined image of several MiB.
// It does nothing while code points are read ahead.
func (s *cssScanner) skipPlain(stop *[256]bool) {
	if s.n == 0 && s.d.skip(stop) {
		s.prevBare = false
	}
}

// cssStopSet returns the bytes in chars, the ASCII controls, DEL and "&".
func cssStopSet(chars string) *[256]bool {
	var t [256]bool
	for c := 0; c < 0x20; c++ {
		t[c] = true
	}
	t[0x7F], t['&'] = true, true
	for i := 0; i < len(chars); i++ {
		t[chars[i]] = true
	}
	return &t
}

// The bytes that end or change a url token's value (white space, quotes,
// parentheses, escapes; controls are white space or non-printable) and a
// string's.
var (
	cssURLStop    = cssStopSet(" \"'()\\")
	cssStringStop = cssStopSet("\"'\\")
)

// reconsume puts p back in front of the stream.
func (s *cssScanner) reconsume(p cssPoint) {
	copy(s.look[1:], s.look[:s.n])
	s.look[0] = p
	s.n++
}

func (s *cssScanner) emitURL(v *urlValue) {
	r := cssReport{kind: cssURLStart, at: v.start}
	if s.names {
		r.name = v.String()
	}
	s.emit(r)
}

func (s *cssScanner) emit(r cssReport) {
	if !s.stop && !s.report(r) {
		s.stop = true
	}
}

// run is section 4.3.1, "consume a token", until EOF.
func (s *cssScanner) run() {
	for !s.stop {
		s.consumeComments()
		p := s.consume()
		switch c := p.r; {
		case c == cssEOF:
			return
		case isCSSWhitespace(c):
			for isCSSWhitespace(s.peek(0).r) {
				s.consume()
			}
		case c == '"' || c == '\'':
			s.consumeString(c, nil)
		case c == '#':
			if isIdentCodePoint(s.peek(0).r) || validEscape(s.peek(0).r, s.peek(1).r) {
				s.consumeIdentSequence()
			}
		case c == '+' || c == '.':
			if startsNumber(c, s.peek(0).r, s.peek(1).r) {
				s.reconsume(p)
				s.consumeNumeric()
			}
		case c == '-':
			switch {
			case startsNumber(c, s.peek(0).r, s.peek(1).r):
				s.reconsume(p)
				s.consumeNumeric()
			case s.peek(0).r == '-' && s.peek(1).r == '>':
				// CDC.
				s.consume()
				s.consume()
			case startsIdent(c, s.peek(0).r, s.peek(1).r):
				s.reconsume(p)
				s.consumeIdentLike()
			}
		case c == '<':
			if s.peek(0).r == '!' && s.peek(1).r == '-' && s.peek(2).r == '-' {
				// CDO.
				s.consume()
				s.consume()
				s.consume()
			}
		case c == '@':
			if startsIdent(s.peek(0).r, s.peek(1).r, s.peek(2).r) {
				s.consumeIdentSequence()
			}
		case c == '\\':
			if validEscape(c, s.peek(0).r) {
				s.reconsume(p)
				s.consumeIdentLike()
			}
		case isDigit(c):
			s.reconsume(p)
			s.consumeNumeric()
		case isIdentStart(c):
			s.reconsume(p)
			s.consumeIdentLike()
		}
		// Everything else is a token of one code point: (, ), [, ], {, },
		// comma, colon, semicolon, and delims.
	}
}

// consumeComments is section 4.3.2.
func (s *cssScanner) consumeComments() {
	for s.peek(0).r == '/' && s.peek(1).r == '*' {
		s.consume()
		s.consume()
		for {
			c := s.consume().r
			if c == cssEOF {
				return
			}
			if c == '*' && s.peek(0).r == '/' {
				s.consume()
				break
			}
		}
	}
}

// consumeNumeric is section 4.3.3 with 4.3.12 (consume a number).
func (s *cssScanner) consumeNumeric() {
	if c := s.peek(0).r; c == '+' || c == '-' {
		s.consume()
	}
	s.consumeDigits()
	if s.peek(0).r == '.' && isDigit(s.peek(1).r) {
		s.consume()
		s.consumeDigits()
	}
	if c := s.peek(0).r; c == 'e' || c == 'E' {
		c1 := s.peek(1).r
		if isDigit(c1) {
			s.consume()
			s.consumeDigits()
		} else if (c1 == '+' || c1 == '-') && isDigit(s.peek(2).r) {
			s.consume()
			s.consume()
			s.consumeDigits()
		}
	}
	if startsIdent(s.peek(0).r, s.peek(1).r, s.peek(2).r) {
		s.consumeIdentSequence() // a dimension's unit
	} else if s.peek(0).r == '%' {
		s.consume()
	}
}

func (s *cssScanner) consumeDigits() {
	for isDigit(s.peek(0).r) {
		s.consume()
	}
}

// cssNameMax is how many code points of a name cssName keeps. Every name
// the tokenizer compares (url and cssSafeFunctions) is shorter.
const cssNameMax = 40

// cssName is an ident sequence as far as the tokenizer needs it: its first
// cssNameMax code points, ASCII-lowercased, its length and its last code
// point. A name can be as long as the input, so no more is kept. ascii
// holds the kept code points as bytes while they are all ASCII, for
// comparisons without allocation.
type cssName struct {
	buf      [cssNameMax]rune
	ascii    [cssNameMax]byte
	n        int
	nonASCII bool
	last     rune
}

func (n *cssName) add(r rune) {
	if 'A' <= r && r <= 'Z' {
		r += 'a' - 'A'
	}
	if n.n < cssNameMax {
		n.buf[n.n] = r
		if r < utf8.RuneSelf {
			n.ascii[n.n] = byte(r & 0x7F) // r is ASCII: the mask keeps it
		} else {
			n.nonASCII = true
		}
	}
	n.n++
	n.last = r
}

// String returns the kept code points.
func (n *cssName) String() string {
	return string(n.buf[:min(n.n, cssNameMax)])
}

// lower returns the name as ASCII bytes, or false if it is longer than
// cssNameMax or has a non-ASCII code point. Only for comparisons: the
// bytes belong to n.
func (n *cssName) lower() ([]byte, bool) {
	if n.n > cssNameMax || n.nonASCII {
		return nil, false
	}
	return n.ascii[:n.n], true
}

// is reports whether the name is exactly lower (lowercase ASCII).
func (n *cssName) is(lower string) bool {
	b, ok := n.lower()
	return ok && string(b) == lower
}

// consumeIdentSequence is section 4.3.11.
func (s *cssScanner) consumeIdentSequence() cssName {
	var name cssName
	for {
		c := s.peek(0).r
		switch {
		case isIdentCodePoint(c):
			name.add(s.consume().r)
		case validEscape(c, s.peek(1).r):
			s.consume()
			name.add(s.consumeEscaped())
		default:
			return name
		}
	}
}

// consumeEscaped is section 4.3.7; the "\" is consumed.
func (s *cssScanner) consumeEscaped() rune {
	p := s.consume()
	switch {
	case p.r == cssEOF:
		return utf8.RuneError
	case isHexDigit(p.r):
		v := hexValue(p.r)
		for k := 1; k < 6 && isHexDigit(s.peek(0).r); k++ {
			v = 16*v + hexValue(s.consume().r)
		}
		if isCSSWhitespace(s.peek(0).r) {
			s.consume()
		}
		if v == 0 || (0xD800 <= v && v <= 0xDFFF) || v > 0x10FFFF {
			return utf8.RuneError
		}
		return v
	}
	return p.r
}

// consumeIdentLike is section 4.3.4: an ident, a function, or a url.
func (s *cssScanner) consumeIdentLike() {
	name := s.consumeIdentSequence()
	if s.peek(0).r != '(' {
		return
	}
	paren := s.consume()
	if name.is("url") {
		for isCSSWhitespace(s.peek(0).r) && isCSSWhitespace(s.peek(1).r) {
			s.consume()
		}
		c0, c1 := s.peek(0).r, s.peek(1).r
		if c0 == '"' || c0 == '\'' || (isCSSWhitespace(c0) && (c1 == '"' || c1 == '\'')) {
			// A url( function token. Its argument is the string right after
			// it (the white space before it is a token of its own).
			if isCSSWhitespace(c0) {
				s.consume()
			}
			q := s.consume()
			s.consumeString(q.r, &q)
			return
		}
		s.consumeURL()
		return
	}
	if !cssSafeFunction(&name) {
		r := cssReport{kind: cssFuncBreak, at: paren}
		if s.names {
			r.name = name.String()
		}
		s.emit(r)
	}
}

// consumeString is section 4.3.5; the opening quote is consumed. With
// url (the url( function's argument, its quote), the value is reported as
// a URL start, a bad string included: it loads nothing, and "data:," in
// front of it changes nothing.
func (s *cssScanner) consumeString(quote rune, url *cssPoint) {
	var v urlValue
	if url != nil {
		v.start = s.peek(0)
	}
	defer func() {
		if url != nil && v.n > 0 && v.reported() {
			s.emitURL(&v)
		}
	}()
	for {
		if url == nil || v.n >= len(v.buf) {
			s.skipPlain(cssStringStop)
		}
		p := s.consume()
		switch p.r {
		case quote, cssEOF:
			return
		case '\n':
			// A bad string; the newline is the next token.
			s.reconsume(p)
			return
		case '\\':
			switch s.peek(0).r {
			case cssEOF:
			case '\n':
				s.consume()
			default:
				v.add(s.consumeEscaped())
			}
		default:
			v.add(p.r)
		}
	}
}

// consumeURL is section 4.3.6; "url(" is consumed. A bad url is reported as
// well: Chromium requests nothing for it, and the report only adds
// "data:," in front of it.
func (s *cssScanner) consumeURL() {
	for isCSSWhitespace(s.peek(0).r) {
		s.consume()
	}
	var v urlValue
	v.start = s.peek(0)
	if c := v.start.r; c == ')' || c == cssEOF {
		s.consume()
		return // url(): nothing to request
	}
	defer func() {
		if v.reported() {
			s.emitURL(&v)
		}
	}()
	for {
		if v.n >= len(v.buf) {
			s.skipPlain(cssURLStop)
		}
		p := s.consume()
		switch c := p.r; {
		case c == ')' || c == cssEOF:
			return
		case isCSSWhitespace(c):
			for isCSSWhitespace(s.peek(0).r) {
				s.consume()
			}
			if c := s.peek(0).r; c == ')' || c == cssEOF {
				s.consume()
				return
			}
			s.consumeBadURLRemnants()
			return
		case c == '"' || c == '\'' || c == '(' || isNonPrintable(c):
			s.consumeBadURLRemnants()
			return
		case c == '\\':
			if !validEscape(c, s.peek(0).r) {
				s.consumeBadURLRemnants()
				return
			}
			v.add(s.consumeEscaped())
		default:
			v.add(c)
		}
	}
}

// consumeBadURLRemnants is section 4.3.14.
func (s *cssScanner) consumeBadURLRemnants() {
	for {
		p := s.consume()
		if p.r == ')' || p.r == cssEOF {
			return
		}
		if validEscape(p.r, s.peek(0).r) {
			s.consumeEscaped()
		}
	}
}

// urlValue is the start of a URL value: where it starts and its first
// code points, enough to tell data: and # apart.
type urlValue struct {
	start cssPoint
	buf   [5]rune
	n     int
}

func (v *urlValue) add(r rune) {
	if v.n < len(v.buf) {
		v.buf[v.n] = r
	}
	v.n++
}

func (v *urlValue) String() string { return string(v.buf[:min(v.n, len(v.buf))]) }

// reported: a value that does not start with "data:" (ASCII
// case-insensitive) or "#".
func (v *urlValue) reported() bool {
	if v.n > 0 && v.buf[0] == '#' {
		return false
	}
	if v.n < 5 {
		return true
	}
	for k, c := range "data:" {
		r := v.buf[k]
		if 'A' <= r && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r != c {
			return true
		}
	}
	return false
}

// The code point classes of section 4.2. Non-ASCII code points are ident
// code points, as in Chromium's tokenizer (and CSS Syntax 3 before the
// editor's draft narrowed the set): a longer name than the browser sees
// can only fail closed, as no listed name has a non-ASCII code point.

func isCSSWhitespace(c rune) bool { return c == '\n' || c == '\t' || c == ' ' }
func isDigit(c rune) bool         { return '0' <= c && c <= '9' }
func isHexDigit(c rune) bool {
	return isDigit(c) || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func hexValue(c rune) rune {
	switch {
	case isDigit(c):
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

func isIdentStart(c rune) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_' || c >= 0x80
}

func isIdentCodePoint(c rune) bool { return isIdentStart(c) || isDigit(c) || c == '-' }

func isNonPrintable(c rune) bool {
	return 0 <= c && c <= 8 || c == 0x0B || 0x0E <= c && c <= 0x1F || c == 0x7F
}

// validEscape is section 4.3.8.
func validEscape(c0, c1 rune) bool { return c0 == '\\' && c1 != '\n' }

// startsIdent is section 4.3.9.
func startsIdent(c0, c1, c2 rune) bool {
	switch {
	case c0 == '-':
		return isIdentStart(c1) || c1 == '-' || validEscape(c1, c2)
	case isIdentStart(c0):
		return true
	case c0 == '\\':
		return validEscape(c0, c1)
	}
	return false
}

// startsNumber is section 4.3.10.
func startsNumber(c0, c1, c2 rune) bool {
	switch c0 {
	case '+', '-':
		return isDigit(c1) || c1 == '.' && isDigit(c2)
	case '.':
		return isDigit(c1)
	}
	return isDigit(c0)
}

// cssSafeFunctions are the CSS functions known to load nothing: a function
// token with any other name gets a function break. Names are lowercase
// ASCII and compared after escapes are decoded and a vendor prefix
// (-webkit-, -moz-, -ms-, -o-) is removed, so -webkit-image-set is
// image-set, which is not here. Adding a name is a change to the HTML mail
// guarantee (docs/reference/security.md): it must be a function that takes
// no URL and no string that names one. A URL inside a listed function (as
// in if() or symbols()) is still a url token of its own.
var cssSafeFunctions = func() map[string]bool {
	m := map[string]bool{}
	for _, group := range []string{
		// Colours.
		"rgb rgba hsl hsla hwb lab lch oklab oklch color color-mix light-dark contrast-color device-cmyk",
		// Gradients; gradient is the old -webkit-gradient().
		"linear-gradient radial-gradient conic-gradient repeating-linear-gradient repeating-radial-gradient repeating-conic-gradient gradient",
		// Maths.
		"calc calc-size min max clamp round mod rem sin cos tan asin acos atan atan2 pow sqrt hypot log exp abs sign progress sibling-index sibling-count",
		// Substitution: values of custom properties, the environment, the
		// element's attributes (Chromium does not let attr() make a URL),
		// conditions, and the types of typed attr().
		"var env attr if type",
		// Transforms.
		"matrix matrix3d translate translatex translatey translatez translate3d scale scalex scaley scalez scale3d " +
			"rotate rotatex rotatey rotatez rotate3d skew skewx skewy perspective",
		// Filters.
		"blur brightness contrast drop-shadow grayscale hue-rotate invert opacity saturate sepia",
		// Easing and timelines.
		"cubic-bezier steps linear scroll view",
		// Shapes and layout.
		"inset circle ellipse polygon path rect xywh shape ray superellipse repeat minmax fit-content anchor anchor-size",
		// Counters and fonts.
		"counter counters symbols format local tech",
		// Selectors; any is :-webkit-any().
		"not is where has any nth-child nth-last-child nth-of-type nth-last-of-type nth-col nth-last-col lang dir " +
			"host host-context slotted part state cue highlight picker active-view-transition-type " +
			"view-transition-group view-transition-image-pair view-transition-old view-transition-new",
		// At-rule preludes and conditions.
		"selector font-tech font-format style supports layer media scroll-state",
	} {
		start := -1
		for i := 0; i <= len(group); i++ {
			if i == len(group) || group[i] == ' ' {
				if start >= 0 {
					m[group[start:i]] = true
				}
				start = -1
			} else if start < 0 {
				start = i
			}
		}
	}
	return m
}()

// cssVendorPrefixes are removed from a function name before it is compared.
var cssVendorPrefixes = [...]string{"-webkit-", "-moz-", "-ms-", "-o-"}

// cssSafeFunction reports whether a function token named name loads
// nothing: a name on cssSafeFunctions, or one that ends in "_". The rewrite
// produces that form (neutralizeCSS), and no function of CSS or of Chromium
// ends in "_": their names are hyphenated words of letters and digits. A
// custom function (@function --name_) may end in "_", but its body is the
// mail's own CSS, which the tokenizer reads too, so a URL or a function
// that loads in that body is reported there.
func cssSafeFunction(name *cssName) bool {
	if name.n > 0 && name.last == '_' {
		return true
	}
	if name.n > cssNameMax {
		return false
	}
	b, ok := name.lower()
	if !ok {
		return false
	}
	for _, p := range cssVendorPrefixes {
		if len(b) > len(p) && string(b[:len(p)]) == p {
			b = b[len(p):]
			break
		}
	}
	return cssSafeFunctions[string(b)]
}
