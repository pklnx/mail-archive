package web

import "strings"

// neutralizeRules lists the start tags whose attributes neutralizeHTML
// renames, and the attribute-name prefix it renames on each (#209):
//
//   - template, shadowroot...: declarative shadow roots (shadowrootmode, the
//     legacy shadowroot and any future shadowroot* name). The HTML parser
//     attaches such a root without script, and a click inside a closed root
//     cannot be seen by the app's link blocker.
//   - iframe, srcdoc...: a nested document built from an attribute value.
//     The app's link blocker is installed only on the mail's own document,
//     and the nested document's content is escaped, so a scan for the
//     template rule never sees a shadow root inside it.
//
// Tags and prefixes are lowercase ASCII.
var neutralizeRules = [...]struct{ tag, prefix string }{
	{"template", "shadowroot"},
	{"iframe", "srcdoc"},
}

// linkNames are the attribute names neutralizeHTML renames on every start
// tag, whatever the element (#459), compared as exact names (ASCII
// case-insensitive). conditional: the name keeps its value if linkKept
// says so; the others are always renamed.
//
//   - href, xlink:href: links on <a>, <area>, SVG <a>, <use>, <image>,
//     MathML elements, <link> and <base> alike. A rule per element would
//     not be context-free: which namespace an element is in depends on tree
//     construction.
//   - ping: a request on every click of a link.
//   - attributename: SVG animation (<set>, <animate>) sets any attribute,
//     an href among them, without one in the markup.
//   - http-equiv: <meta http-equiv=refresh> navigates the frame; on other
//     elements it does nothing.
var linkNames = [...]struct {
	name        string
	conditional bool
}{
	{"href", true},
	{"xlink:href", true},
	{"ping", false},
	{"attributename", false},
	{"http-equiv", false},
}

// neutralizePrefix goes in front of every renamed attribute name. It holds no
// "<", ">", "=", "/", quote, white space or "&", so it never ends a token or
// changes a token's type.
const neutralizePrefix = "data-"

// neutralizeHTML returns doc with neutralizePrefix inserted before every
// attribute name that starts with a rule's prefix (ASCII case-insensitive)
// in a start tag of the rule's element, and before every attribute name of
// linkNames on any start tag, an href or xlink:href only if linkKept does not
// keep its value. A template then stays an inert <template> and a frame an
// empty about:blank; their content is not shown. No link in the served HTML
// points into the app (hosts) or to anything but an http(s), mailto: or tel:
// URL, in every engine, without any script in the browser (#459). All other
// bytes are kept, so the token boundaries and token types are the same as in
// doc.
//
// The scan is context-free: which state the browser's tokenizer is in at a
// "<" depends on tree construction (raw text, foreign content, noscript with
// scripting off), which x/net/html's Tokenizer does not model. So every "<"
// followed by a rule's tag name (ASCII case-insensitive) and white space, "/"
// or ">" is a candidate of that rule, and every "<" followed by an ASCII
// letter a candidate of any tag, wherever it is: in markup, comments, raw
// text and attribute values. A real start tag can only begin with such
// bytes. From there the spec's tag name state and attribute states (before
// attribute name, attribute name, after attribute name, before attribute
// value, the three value states, after attribute value (quoted),
// self-closing start tag) do not depend on context, so the candidate is
// followed through them up to its ">" or the end of doc, and each attribute
// name it starts is checked. A candidate that is not a real tag gets the
// prefix inserted into text, which happens only in a mail that holds such
// literal text.
//
// Whether a linkNames name is renamed depends only on doc from the name's
// first byte on (the name, the white space and "=" after it, and at most
// linkWindow bytes of the value), never on the candidate that reached it, so
// every candidate decides the same there. That lookahead is linear in total
// and needs no memo: a name starts only after white space, "/" or a quote,
// so within the host linkKept reads only its first byte can start another
// name, and a white space run around "=" follows exactly one name.
//
// Candidates in the same state behave the same from there on, so the scan
// keeps one set of active states per rule and one for any tag (an NFA
// simulation) and runs in time linear in len(doc), whatever the nesting of
// candidates. Without an active candidate it jumps to the next "<". A doc
// without a matching attribute is returned as is, without a copy.
//
// doc is the mail's HTML body as served: valid UTF-8 without NUL (see
// clean() in internal/mime), served as charset=utf-8, so these are the bytes
// the browser tokenizes. A CR counts as white space, as after the input
// stream's newline normalization.
func neutralizeHTML(doc string, hosts appHosts) string {
	var (
		b       strings.Builder
		changed bool // b holds output
		last    int  // doc[:last] is written to b
		active  [len(neutralizeRules)]attrStates
		pending [len(neutralizeRules)]int // where a candidate's state machine starts, or -1
		anyTag  attrStates                // the candidates of any tag
	)
	for r := range pending {
		pending[r] = -1
	}
	for i := 0; i < len(doc); i++ {
		if anyTag == 0 && idle(&active, &pending, i) {
			j := strings.IndexByte(doc[i:], '<')
			if j < 0 {
				break
			}
			i += j
		}
		c := doc[i]
		cls := attrClassOf(c)
		// The rules' prefixes and linkNames are pairwise disjoint (no name
		// is a prefix of another or starts with a rule's prefix: shadowroot,
		// srcdoc, href, xlink:href, ping, attributename, http-equiv), so at
		// most one of them matches at i; the prefix goes in once.
		insert := false
		for r := range neutralizeRules {
			if pending[r] == i {
				// The byte after the tag name: tag name state, which goes
				// on as before attribute name does for these bytes.
				active[r] |= 1 << stBeforeName
				pending[r] = -1
			}
			if active[r] == 0 {
				continue
			}
			t := attrTable[active[r]][cls]
			active[r] = t.next
			if t.startsName && hasPrefixASCIIFold(doc[i:], neutralizeRules[r].prefix) {
				insert = true
			}
		}
		if anyTag != 0 {
			t := attrTable[anyTag][cls]
			anyTag = t.next
			if t.startsName && linkNameRenamed(doc, i, hosts) {
				insert = true
			}
		}
		if insert {
			if !changed {
				b.Grow(len(doc) + 64)
				changed = true
			}
			b.WriteString(doc[last:i])
			b.WriteString(neutralizePrefix)
			last = i
		}
		if c == '<' {
			for r, ru := range neutralizeRules {
				e := i + 1 + len(ru.tag)
				if e < len(doc) && hasPrefixASCIIFold(doc[i+1:], ru.tag) && isTagNameEnd(doc[e]) {
					pending[r] = e
				}
			}
			// Tag open state: an ASCII letter starts a tag name. From the
			// letter on, the candidate is in the tag name state.
			if i+1 < len(doc) && isASCIIAlpha(doc[i+1]) {
				anyTag |= 1 << stTagName
			}
		}
	}
	if !changed {
		return doc
	}
	b.WriteString(doc[last:])
	return b.String()
}

// linkNameRenamed reports whether the attribute name starting at doc[i] is
// one of linkNames that gets the prefix.
func linkNameRenamed(doc string, i int, hosts appHosts) bool {
	for _, n := range linkNames {
		e := i + len(n.name)
		if !hasPrefixASCIIFold(doc[i:], n.name) || (e < len(doc) && !isAttrNameEnd(doc[e])) {
			continue
		}
		if !n.conditional {
			return true
		}
		// After attribute name, before attribute value: a value only after
		// "="; ">" there ends the tag with an empty value.
		for e < len(doc) && attrClassOf(doc[e]) == clsSpace {
			e++
		}
		if e >= len(doc) || doc[e] != '=' {
			return true
		}
		e++
		for e < len(doc) && attrClassOf(doc[e]) == clsSpace {
			e++
		}
		if e >= len(doc) || doc[e] == '>' {
			return true
		}
		return !linkKept(doc, e, hosts)
	}
	return false
}

// isAttrNameEnd reports whether c ends an attribute name in the attribute
// name state.
func isAttrNameEnd(c byte) bool {
	return c == '=' || isTagNameEnd(c)
}

func isASCIIAlpha(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// idle reports whether no candidate is active at i or starts after it.
func idle(active *[len(neutralizeRules)]attrStates, pending *[len(neutralizeRules)]int, i int) bool {
	for r := range active {
		if active[r] != 0 || pending[r] >= i {
			return false
		}
	}
	return true
}

// isTagNameEnd reports whether c ends a tag name in the tag name state.
func isTagNameEnd(c byte) bool {
	return c == '/' || c == '>' || attrClassOf(c) == clsSpace
}

// hasPrefixASCIIFold reports whether s starts with the lowercase ASCII
// prefix, comparing ASCII letters case-insensitively. Unlike
// strings.EqualFold it folds no other characters (such as U+017F to "s"),
// as the HTML tokenizer lowercases only ASCII.
func hasPrefixASCIIFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for k := 0; k < len(prefix); k++ {
		c := s[k]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != prefix[k] {
			return false
		}
	}
	return true
}

// The tokenizer states a candidate start tag goes through, as in the HTML
// spec: the tag name state (13.2.5.8) for candidates of any tag, then the
// attribute states (13.2.5.32 to 13.2.5.40). A tag that ends (">") or a
// state outside the tag drops out of the set.
const (
	stBeforeName   = iota // before attribute name
	stTagName             // tag name (candidates of any tag)
	stName                // attribute name
	stAfterName           // after attribute name
	stBeforeValue         // before attribute value
	stDoubleQuoted        // attribute value (double-quoted)
	stSingleQuoted        // attribute value (single-quoted)
	stUnquoted            // attribute value (unquoted)
	stAfterQuoted         // after attribute value (quoted)
	stSelfClosing         // self-closing start tag
	numAttrStates
)

// attrStates is a set of the states above, one bit per state.
type attrStates uint16

// The byte classes the states above tell apart. A character reference in a
// value consumes only letters, digits, "#" and ";", so it never ends a
// value and needs no class of its own.
const (
	clsSpace  = iota // tab, LF, FF, CR, space
	clsSlash         // "/"
	clsGT            // ">"
	clsEq            // "="
	clsDQuote        // '"'
	clsSQuote        // "'"
	clsOther
	numAttrClasses
)

func attrClassOf(c byte) int {
	switch c {
	case '\t', '\n', '\f', '\r', ' ':
		return clsSpace
	case '/':
		return clsSlash
	case '>':
		return clsGT
	case '=':
		return clsEq
	case '"':
		return clsDQuote
	case '\'':
		return clsSQuote
	}
	return clsOther
}

// attrStep is one transition of a set of states on one byte class.
type attrStep struct {
	next attrStates
	// startsName: the byte starts a new attribute's name in at least one of
	// the states (the name's first byte is this byte).
	startsName bool
}

// attrTable holds the transition of every set of states on every byte class.
var attrTable = func() (t [1 << numAttrStates][numAttrClasses]attrStep) {
	for set := range t {
		for cls := range t[set] {
			for s := 0; s < numAttrStates; s++ {
				if set&(1<<s) == 0 {
					continue
				}
				next, starts := attrStateStep(s, cls)
				if next >= 0 {
					t[set][cls].next |= 1 << next
				}
				if starts {
					t[set][cls].startsName = true
				}
			}
		}
	}
	return t
}()

// attrStateStep is the spec's transition from state s on a byte of class
// cls: the next state (-1 when the tag ends), and whether the byte starts a
// new attribute's name. "Reconsume in" a state is a call on the same byte.
// EOF needs no case: it drops the tag, and the scan ends there.
func attrStateStep(s, cls int) (next int, startsName bool) {
	switch s {
	case stTagName:
		switch cls {
		case clsSpace, clsSlash, clsGT:
			return attrStateStep(stBeforeName, cls)
		}
		return stTagName, false
	case stBeforeName:
		switch cls {
		case clsSpace:
			return stBeforeName, false
		case clsSlash, clsGT:
			return attrStateStep(stAfterName, cls)
		case clsEq:
			// An attribute whose name starts with "=": no prefix matches.
			return stName, false
		}
		return stName, true
	case stName:
		switch cls {
		case clsSpace, clsSlash, clsGT:
			return attrStateStep(stAfterName, cls)
		case clsEq:
			return stBeforeValue, false
		}
		return stName, false
	case stAfterName:
		switch cls {
		case clsSpace:
			return stAfterName, false
		case clsSlash:
			return stSelfClosing, false
		case clsEq:
			return stBeforeValue, false
		case clsGT:
			return -1, false
		}
		return stName, true
	case stBeforeValue:
		switch cls {
		case clsSpace:
			return stBeforeValue, false
		case clsDQuote:
			return stDoubleQuoted, false
		case clsSQuote:
			return stSingleQuoted, false
		case clsGT:
			return -1, false
		}
		return stUnquoted, false
	case stDoubleQuoted:
		if cls == clsDQuote {
			return stAfterQuoted, false
		}
		return stDoubleQuoted, false
	case stSingleQuoted:
		if cls == clsSQuote {
			return stAfterQuoted, false
		}
		return stSingleQuoted, false
	case stUnquoted:
		switch cls {
		case clsSpace:
			return stBeforeName, false
		case clsGT:
			return -1, false
		}
		return stUnquoted, false
	case stAfterQuoted:
		switch cls {
		case clsSpace:
			return stBeforeName, false
		case clsSlash:
			return stSelfClosing, false
		case clsGT:
			return -1, false
		}
		return attrStateStep(stBeforeName, cls)
	case stSelfClosing:
		if cls == clsGT {
			return -1, false
		}
		return attrStateStep(stBeforeName, cls)
	}
	return -1, false
}
