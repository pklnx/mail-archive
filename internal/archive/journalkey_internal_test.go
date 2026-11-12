package archive

import (
	"strings"
	"testing"
)

// keyOf is the journal key of a message with these header lines (LF).
func keyOf(t *testing.T, header string) (journalKey, bool) {
	t.Helper()
	raw := strings.ReplaceAll(header, "\n", "\r\n") + "\r\n\r\nbody\r\n"
	return headersKey(ParseHeaders(strings.NewReader(raw)))
}

const keyBase = "Message-ID: <x@example.com>\nFrom: Boss <boss@example.com>\nSubject: real contract\nDate: Mon, 1 Jun 2026 12:00:00 +0200"

// TestJournalKeyVariations: header forms servers and gateways produce for
// the same message give the same key (#4 of the security fixes).
func TestJournalKeyVariations(t *testing.T) {
	for _, c := range []struct{ name, a, b string }{
		{"message-id spacing", "Message-ID: <x@example.com>", "Message-ID:   <x@example.com>  "},
		{"date zones", "Date: Mon, 1 Jun 2026 12:00:00 +0200", "Date: 1 Jun 2026 10:00:00 GMT"},
		{"date EDT", "Date: Mon, 1 Jun 2026 12:00:00 +0200", "Date: Mon, 01 Jun 2026 06:00:00 -0400 (EDT)"},
		{"fractional second", "Date: Mon, 1 Jun 2026 12:00:00 +0200", "Date: Mon, 1 Jun 2026 12:00:00.5 +0200"},
		{"from case and quoting", `From: "Doe, John" <John@Example.COM>`, "From: =?UTF-8?Q?Doe=2C_J=C3=B6hn?= <john@example.com>"},
		{"from comment", "From: john@example.com (John)", `From: "Doe, John" <John@Example.COM>`},
		{"from bare", "From: john@example.com", "From: John <JOHN@example.com>"},
		{"subject folded", "Subject: real contract for\n the year", "Subject: real   contract for the year"},
		{"subject B vs Q", "Subject: =?UTF-8?B?R3LDvMOfZQ==?=", "Subject: =?ISO-8859-1?Q?Gr=FC=DFe?="},
		{"subject encoded vs raw", "Subject: =?ISO-8859-1?Q?Gr=FC=DFe?=", "Subject: Grüße"},
		{"subject NFC vs NFD", "Subject: Grüße", "Subject: Grüße"},
		{"subject split encoded words", "Subject: =?UTF-8?Q?Gr=C3=BC?=\n =?ISO-8859-1?Q?=DFe?=", "Subject: Grüße"},
		{"no subject vs empty", "Subject:", "X-Subject: gone"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ka, okA := keyOf(t, replaceField(keyBase, c.a))
			kb, okB := keyOf(t, replaceField(keyBase, c.b))
			if !okA || !okB || ka != kb {
				t.Errorf("keys differ:\n%s %v %+v\n%s %v %+v", c.a, okA, ka, c.b, okB, kb)
			}
		})
	}
}

// TestJournalKeyDiffers: a forged copy that differs in one value has
// another key.
func TestJournalKeyDiffers(t *testing.T) {
	for _, c := range []struct{ name, field string }{
		{"message-id", "Message-ID: <y@example.com>"},
		{"date", "Date: Mon, 1 Jun 2026 12:00:01 +0200"},
		{"from", "From: Boss <attacker@evil.example>"},
		{"from with a second address", "From: Boss <boss@example.com>, attacker@evil.example"},
		{"address in an encoded display name", "From: =?UTF-8?Q?=3Cboss@example.com=3E?= <attacker@evil.example>"},
		{"subject", "Subject: real contract!"},
		{"subject case", "Subject: Real contract"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ka, okA := keyOf(t, keyBase)
			kb, okB := keyOf(t, replaceField(keyBase, c.field))
			if !okA || !okB || ka == kb {
				t.Errorf("same key for %q: %v %v", c.field, okA, okB)
			}
		})
	}
}

// TestJournalKeyMissing: without a Message-ID, a Date that parses or a
// From, a message never counts as journaled.
func TestJournalKeyMissing(t *testing.T) {
	for _, field := range []string{"Message-ID", "Date", "From"} {
		if _, ok := keyOf(t, replaceField(keyBase, "X-"+field+": gone")); ok {
			t.Errorf("key without %s", field)
		}
	}
	if _, ok := keyOf(t, replaceField(keyBase, "Date: someday")); ok {
		t.Error("key with an unparseable Date")
	}
	if _, ok := keyOf(t, replaceField(keyBase, "From: <>")); ok {
		t.Error("key with an empty From")
	}
}

// TestJournalKeyFromFallback: a decoded display name that no longer parses
// falls back to the <...> parts, and failing that to the whole value.
func TestJournalKeyFromFallback(t *testing.T) {
	for in, want := range map[string]string{
		"Doe, Jöhn <John@Example.com>":     "john@example.com",
		"<boss@corp> <attacker@evil>":      "boss@corp,attacker@evil",
		"not an address":                   "not an address",
		"a@example.com, B <B@Example.com>": "a@example.com,b@example.com",
		"":                                 "",
		"   ":                              "",
	} {
		if got := fromAddresses(clean(in, maxHeaderField)); got != want {
			t.Errorf("fromAddresses(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestJournalKeyIdempotent: the key of the stored values (what BuildMeta
// stores from ParseHeaders) is the key of the parsed header, which the
// journaled side relies on.
func TestJournalKeyIdempotent(t *testing.T) {
	for _, header := range []string{
		keyBase,
		replaceField(keyBase, "From: =?UTF-8?Q?Doe=2C_J=C3=B6hn?= <john@example.com>"),
		replaceField(keyBase, "Subject: =?UTF-8?Q?Gru=CC=88=C3=9Fe?=\n  folded"),
		replaceField(keyBase, "Date: Mon, 1 Jun 2026 12:00:00.75 +0200"),
		replaceField(keyBase, "Subject: "+strings.Repeat("ä", 1500)),
	} {
		raw := strings.ReplaceAll(header, "\n", "\r\n") + "\r\n\r\nbody\r\n"
		h := ParseHeaders(strings.NewReader(raw))
		want, ok := headersKey(h)
		// The stored Date has microseconds, the Message-ID no <>.
		stored := h.Date.Truncate(1000)
		got, ok2 := newJournalKey(h.MessageID, &stored, h.From, h.Subject)
		if !ok || !ok2 || got != want {
			t.Errorf("%q: stored key %+v, parsed %+v", header, got, want)
		}
	}
}

// replaceField replaces the header line with the field name of line in
// header.
func replaceField(header, line string) string {
	name, _, _ := strings.Cut(line, ":")
	name = strings.TrimPrefix(name, "X-")
	lines := strings.Split(header, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.ToLower(l), strings.ToLower(name)+":") {
			lines[i] = line
			return strings.Join(lines, "\n")
		}
	}
	return header + "\n" + line
}
