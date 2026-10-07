package mailbox

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var date = time.Date(2026, 3, 7, 9, 5, 1, 0, time.FixedZone("CET", 3600))

func TestMboxRoundTrip(t *testing.T) {
	messages := []string{
		"Subject: crlf\r\n\r\nFrom here\r\n>From there\r\n>>From everywhere\r\n",
		"Subject: lf\n\nFrom the start\n\nends with a blank line\n\n",
		"From : at the very start\nx\n",
		"Subject: >From in a line\r\n\r\nnot From at the start\r\n",
		"Subject: lone CR\r\n\r\nline\r\r\n",
		"Subject: long\n\n" + strings.Repeat("x", 3*lineBuffer) + "\nFrom after a long line\n",
		"\r\n",
	}
	var buf bytes.Buffer
	w := NewMboxWriter(&buf)
	for _, m := range messages {
		if err := w.WriteMessage(strings.NewReader(m), date); err != nil {
			t.Fatal(err)
		}
	}
	out := buf.String()
	if !strings.HasPrefix(out, "From MAILER-DAEMON Sat Mar  7 08:05:01 2026\n") {
		t.Errorf("separator: %q", out[:50])
	}
	for _, want := range []string{"\r\n>From here\r\n>>From there\r\n>>>From everywhere\r\n\r\nFrom MAILER-DAEMON", "\n>From the start\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	got := readAll(t, &buf)
	if len(got) != len(messages) {
		t.Fatalf("read %d messages, want %d", len(got), len(messages))
	}
	for i := range messages {
		if sha256.Sum256(got[i]) != sha256.Sum256([]byte(messages[i])) {
			t.Errorf("message %d: got %q, want %q", i, short(got[i]), short([]byte(messages[i])))
		}
	}
}

// A missing final newline is the one change a round trip does not undo.
func TestMboxAddsFinalNewline(t *testing.T) {
	for in, want := range map[string]string{
		"Subject: x\r\n\r\nno newline": "Subject: x\r\n\r\nno newline\n",
		"":                             "\n",
	} {
		var buf bytes.Buffer
		if err := NewMboxWriter(&buf).WriteMessage(strings.NewReader(in), date); err != nil {
			t.Fatal(err)
		}
		got := readAll(t, &buf)
		if len(got) != 1 || string(got[0]) != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// After a failing reader, everything written so far is in the file, so
// the caller can cut it back.
func TestMboxWriterFlushesOnError(t *testing.T) {
	var buf bytes.Buffer
	boom := errors.New("boom")
	err := NewMboxWriter(&buf).WriteMessage(io.MultiReader(strings.NewReader("Subject: x\n"), errReader{boom}), date)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasSuffix(buf.String(), "Subject: x\n") {
		t.Errorf("not flushed: %q", buf.String())
	}
}

func TestMboxReaderRejectsOtherFiles(t *testing.T) {
	if _, err := NewMboxReader(strings.NewReader("Subject: x\n")).Next(); !errors.Is(err, ErrNotMbox) {
		t.Errorf("err = %v", err)
	}
	if _, err := NewMboxReader(strings.NewReader("")).Next(); !errors.Is(err, io.EOF) {
		t.Errorf("empty file: err = %v", err)
	}
}

func TestMaildirFlags(t *testing.T) {
	got := MaildirFlags([]string{`\Seen`, `$Label1`, `\Answered`, `\DELETED`, `\Recent`, `\Draft`, `\Flagged`, `\Seen`})
	if got != "DFRST" {
		t.Errorf("MaildirFlags = %q", got)
	}
	if f := IMAPFlags("1.abc.mail-archive:2,FS"); !slices.Equal(f, []string{`\Flagged`, `\Seen`}) {
		t.Errorf("IMAPFlags = %v", f)
	}
	if f := IMAPFlags("1.abc.host"); f != nil {
		t.Errorf("IMAPFlags without info = %v", f)
	}
}

func TestMaildirRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "INBOX")
	md, err := CreateMaildir(dir, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	messages := map[string][]string{
		"Subject: one\r\n\r\nbody\r\n": {`\Seen`, `\Answered`},
		"Subject: two\n\nno newline":   nil,
	}
	for body, flags := range messages {
		path, err := md.Deliver(strings.NewReader(body), MaildirMessage{Unique: "1772870701." + string(rune('a'+len(flags))), Flags: flags, Date: date})
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 || !fi.ModTime().Equal(date) {
			t.Errorf("%s: mode %v, mtime %v", path, fi.Mode(), fi.ModTime())
		}
	}
	entries, err := ReadMaildir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(messages) {
		t.Fatalf("entries: %+v", entries)
	}
	for _, e := range entries {
		data, err := os.ReadFile(e.Path)
		if err != nil {
			t.Fatal(err)
		}
		flags, ok := messages[string(data)]
		if !ok {
			t.Errorf("unexpected content %q", data)
			continue
		}
		want := slices.Clone(flags)
		slices.Sort(want)
		got := slices.Clone(e.Flags)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: flags %v, want %v", e.Path, e.Flags, flags)
		}
	}
	if tmp, _ := os.ReadDir(filepath.Join(dir, "tmp")); len(tmp) != 0 {
		t.Errorf("tmp not empty: %v", tmp)
	}
}

func TestMaildirDeliverFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	md, err := CreateMaildir(dir, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = md.Deliver(io.MultiReader(strings.NewReader("Subject: x\n"), errReader{errors.New("boom")}), MaildirMessage{Unique: "1.x"})
	if err == nil {
		t.Fatal("no error")
	}
	for _, sub := range []string{"cur", "new", "tmp"} {
		if e, _ := os.ReadDir(filepath.Join(dir, sub)); len(e) != 0 {
			t.Errorf("%s not empty: %v", sub, e)
		}
	}
	for _, bad := range []string{"", "a/b", "a:2,S", ".hidden", "a\x00"} {
		if _, err := md.Deliver(strings.NewReader("x"), MaildirMessage{Unique: bad}); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func readAll(t *testing.T, r io.Reader) [][]byte {
	t.Helper()
	mr := NewMboxReader(r)
	var out [][]byte
	for {
		m, err := mr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(m.From, "From MAILER-DAEMON ") {
			t.Errorf("From line %q", m.From)
		}
		out = append(out, m.Data)
	}
}

func short(b []byte) []byte {
	if len(b) > 80 {
		return b[:80]
	}
	return b
}
