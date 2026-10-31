package web

import "strings"

// linkWindow is how many bytes of an href value linkKept reads: the scheme,
// the host and the port have to end within them (#459). It keeps the
// lookahead from every attribute name bounded.
const linkWindow = 320

// appHosts is the set of host names the app runs under, normalized by
// normalizeLinkHost: links to them are renamed by neutralizeHTML.
type appHosts map[string]struct{}

// newAppHosts builds the set from host names (without port). Empty names are
// left out.
func newAppHosts(names ...string) appHosts {
	h := make(appHosts, len(names))
	for _, n := range names {
		if n = normalizeLinkHost(n); n != "" {
			h[n] = struct{}{}
		}
	}
	return h
}

func (h appHosts) has(name string) bool {
	_, ok := h[normalizeLinkHost(name)]
	return ok
}

// normalizeLinkHost lowercases a host name and strips brackets and trailing
// dots, so that "LOCALHOST.", "localhost" and "[::1]", "::1" compare equal.
// A trailing dot names the same host to DNS; the Host check does not strip
// it, but a link with one is renamed all the same.
func normalizeLinkHost(name string) string {
	return strings.TrimRight(strings.Trim(strings.ToLower(name), "[]"), ".")
}

// linkKept reports whether the href (or xlink:href) value at doc[at:] stays a
// link (#459). at is the value's opening quote, or its first byte if it is
// unquoted. The value is kept only if its markup spells out literally, within
// linkWindow bytes:
//
//   - after leading ASCII white space and C0 controls (the URL parser skips
//     them), "mailto:" or "tel:" (ASCII case-insensitive); or
//   - "http://", "https://" (ASCII case-insensitive) or "//", then a host of
//     ASCII letters, digits, "-", "_" and ".", then an optional ":" and
//     digits, then the end of the value, "/", "?" or "#"; and the host is
//     not one of hosts and does not end in a number unless it is a plain
//     dotted IPv4 address.
//
// Everything else is renamed: relative links, other schemes, character
// references, tabs or newlines before the host ends, "\", "@", "%", IPv6
// literals and non-ASCII hosts. So the browser's URL parser reads the same
// scheme and host as this check, whatever the base URL: once a literal "/",
// "?", "#" or the end of the value follows the host, no later byte can
// change it.
func linkKept(doc string, at int, hosts appHosts) bool {
	if at >= len(doc) {
		return false
	}
	var quote byte
	if c := doc[at]; c == '"' || c == '\'' {
		quote = c
		at++
	}
	// isEnd reports whether c ends the value.
	isEnd := func(c byte) bool {
		if quote != 0 {
			return c == quote
		}
		return c == '>' || attrClassOf(c) == clsSpace
	}
	limit := min(at+linkWindow, len(doc))
	i := at
	for i < limit && doc[i] <= ' ' && !isEnd(doc[i]) {
		i++
	}
	v := doc[i:limit]
	switch {
	case hasPrefixASCIIFold(v, "mailto:"), hasPrefixASCIIFold(v, "tel:"):
		return true
	case hasPrefixASCIIFold(v, "http://"):
		i += len("http://")
	case hasPrefixASCIIFold(v, "https://"):
		i += len("https://")
	case strings.HasPrefix(v, "//"):
		i += len("//")
	default:
		return false
	}
	host := i
	for i < limit && isLinkHostByte(doc[i]) {
		i++
	}
	name := doc[host:i]
	if i < limit && doc[i] == ':' {
		i++
		for i < limit && '0' <= doc[i] && doc[i] <= '9' {
			i++
		}
	}
	if i >= limit {
		return false
	}
	if c := doc[i]; !isEnd(c) && c != '/' && c != '?' && c != '#' {
		return false
	}
	name = normalizeLinkHost(name)
	if name == "" || (isNumericHost(name) && !isDottedQuad(name)) {
		return false
	}
	return !hosts.has(name)
}

func isLinkHostByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.'
}

// isNumericHost reports whether the host (lowercase, without trailing dots)
// ends in a number as the WHATWG URL standard defines it: its last label is
// all decimal digits, or "0x" followed by hex digits. The URL parser reads
// such a host as an IPv4 address in one of many forms ("127.1",
// "2130706433", "0x7f.1").
func isNumericHost(name string) bool {
	last := name[strings.LastIndexByte(name, '.')+1:]
	if last == "" {
		return false
	}
	if strings.HasPrefix(last, "0x") {
		return strings.Trim(last[2:], "0123456789abcdef") == ""
	}
	return strings.Trim(last, "0123456789") == ""
}

// isDottedQuad reports whether name is an IPv4 address in its canonical
// form: four decimal numbers from 0 to 255 without leading zeros. The URL
// parser serializes every IPv4 address so, so it compares as a string.
func isDottedQuad(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 || (len(p) > 1 && p[0] == '0') || strings.Trim(p, "0123456789") != "" {
			return false
		}
		n := 0
		for k := 0; k < len(p); k++ {
			n = n*10 + int(p[k]-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}
