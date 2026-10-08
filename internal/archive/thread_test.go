package archive

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestMessageIDs(t *testing.T) {
	long := strings.Repeat("x", maxMessageIDLen) + "@example.com"
	cases := []struct {
		header string
		want   []string
	}{
		{"<a@example.com>", []string{"a@example.com"}},
		{"<a@example.com> <b@example.com>", []string{"a@example.com", "b@example.com"}},
		{"<a@example.com>,<b@example.com>", []string{"a@example.com", "b@example.com"}},
		{"<a@example.com> (Alice's message of Monday)", []string{"a@example.com"}},
		{"a@example.com b@example.com", []string{"a@example.com", "b@example.com"}}, // no brackets
		{"a@example.com (comment)", []string{"a@example.com"}},
		{"<>  < >", []string{}},
		{"<" + long + "> <ok@example.com>", []string{"ok@example.com"}},
		{"<bad\xff\x00@example.com>", []string{"bad�@example.com"}},
		{"", []string{}},
	}
	for _, c := range cases {
		if got := messageIDs(c.header); !slices.Equal(got, c.want) {
			t.Errorf("messageIDs(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

func TestTrimReferences(t *testing.T) {
	var ids []string
	for i := range 80 {
		ids = append(ids, fmt.Sprintf("m%d@x", i))
	}
	got := trimReferences(ids)
	if len(got) != maxReferences || got[0] != "m0@x" || got[1] != "m31@x" || got[len(got)-1] != "m79@x" {
		t.Errorf("trimReferences = %v", got)
	}
	if short := ids[:3]; !slices.Equal(trimReferences(short), short) {
		t.Error("short list changed")
	}
}

func TestParseHeadersThread(t *testing.T) {
	parse := func(headers string) Headers {
		return ParseHeaders(strings.NewReader(strings.ReplaceAll(headers, "\n", "\r\n") + "\r\n\r\nbody\r\n"))
	}

	// Folded References, several IDs in In-Reply-To: the first one counts.
	h := parse("Message-ID: <c@x>\nIn-Reply-To: <b@x> <other@x>\nReferences: <a@x>\n <b@x>")
	if h.InReplyTo != "b@x" || !slices.Equal(h.References, []string{"a@x", "b@x"}) || h.ThreadID != "a@x" {
		t.Errorf("reply: %+v", h)
	}

	// The thread ID falls back to In-Reply-To, then to the own ID.
	if h := parse("Message-ID: <b@x>\nIn-Reply-To: <a@x>"); h.ThreadID != "a@x" || len(h.References) != 0 {
		t.Errorf("In-Reply-To only: %+v", h)
	}
	if h := parse("Message-ID: <a@x>"); h.ThreadID != "a@x" || h.InReplyTo != "" {
		t.Errorf("original: %+v", h)
	}
	if h := parse("Subject: no ids"); h.ThreadID != "" {
		t.Errorf("without IDs: %+v", h)
	}
	// Broken headers give empty values, not an error.
	if h := parse("Message-ID: <a@x>\nIn-Reply-To: (comment only)\nReferences: <>"); h.InReplyTo != "" || len(h.References) != 0 || h.ThreadID != "a@x" {
		t.Errorf("broken: %+v", h)
	}
}
