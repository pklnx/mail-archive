package archive

import (
	"regexp"
	"strings"
)

// Limits for the message IDs that link replies to their originals.
const (
	// maxMessageIDLen drops IDs that no real client sends.
	maxMessageIDLen = 250
	// maxReferences keeps the first and the last IDs of a long References
	// header, as RFC 5322 suggests trimming it.
	maxReferences = 50
)

var bracketedID = regexp.MustCompile(`<([^<>]*)>`)

// messageIDs returns the message IDs in an In-Reply-To or References header
// in header order, without angle brackets and normalized like the stored
// Message-ID. A header without brackets is split at whitespace, keeping
// only tokens with an "@" so that comments are not taken for IDs. Empty and
// over-long IDs are dropped.
func messageIDs(header string) []string {
	var raw []string
	if m := bracketedID.FindAllStringSubmatch(header, -1); len(m) > 0 {
		for _, g := range m {
			raw = append(raw, g[1])
		}
	} else {
		for _, f := range strings.Fields(header) {
			if strings.Contains(f, "@") {
				raw = append(raw, f)
			}
		}
	}
	ids := make([]string, 0, len(raw))
	for _, r := range raw {
		id := clean(r, maxHeaderField)
		if id != "" && len(id) <= maxMessageIDLen {
			ids = append(ids, id)
		}
	}
	return ids
}

// trimReferences keeps the first ID and the last maxReferences-1 IDs.
func trimReferences(ids []string) []string {
	if len(ids) <= maxReferences {
		return ids
	}
	out := append([]string{ids[0]}, ids[len(ids)-(maxReferences-1):]...)
	return out
}

// threadID is the first References ID, else the In-Reply-To ID, else the
// message's own ID, else empty. It depends only on the message's bytes.
func threadID(h Headers) string {
	switch {
	case len(h.References) > 0:
		return h.References[0]
	case h.InReplyTo != "":
		return h.InReplyTo
	default:
		return h.MessageID
	}
}
