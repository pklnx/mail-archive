package mailbox

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

type scanned struct {
	from string
	off  int64
	data string
}

func scanAll(t testing.TB, in string) ([]scanned, error) {
	t.Helper()
	s := NewMboxScanner(strings.NewReader(in))
	var out []scanned
	for {
		m, err := s.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		data, err := io.ReadAll(m.Body)
		if err != nil {
			return out, err
		}
		out = append(out, scanned{m.From, m.Offset, string(data)})
	}
}

func TestMboxScannerSeparators(t *testing.T) {
	in := "\n\nFrom a@b Sat Mar  7 08:05:01 2026\n" +
		"Subject: one\n\n" +
		"From me: an unescaped line without a date\n" +
		"\n" +
		"From you, see you at 10:30\n" + // has a time but no year
		">From escaped\n" +
		">>From twice\n" +
		"\n" +
		"From - Sun Mar  8 10:00:00 2026\n" + // Thunderbird
		"Subject: two\r\n\r\nbody\r\n" +
		"From x@y Mon Mar  9 11:00:00 2026\n" + // no empty line before: body
		"\n" +
		"From 1234@xxx Tue Mar 10 12:00:00 +0000 2026\n" + // Google Takeout
		"Subject: three\n\nno final newline"
	got, err := scanAll(t, in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Subject: one\n\nFrom me: an unescaped line without a date\n\nFrom you, see you at 10:30\nFrom escaped\n>From twice\n",
		"Subject: two\r\n\r\nbody\r\nFrom x@y Mon Mar  9 11:00:00 2026\n",
		"Subject: three\n\nno final newline",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d messages: %+v", len(got), got)
	}
	for i := range want {
		if got[i].data != want[i] {
			t.Errorf("message %d:\n got %q\nwant %q", i, got[i].data, want[i])
		}
	}
	if got[0].off != 2 || !strings.HasPrefix(in[got[1].off:], "From - Sun") || !strings.HasPrefix(in[got[2].off:], "From 1234@") {
		t.Errorf("offsets %d %d %d", got[0].off, got[1].off, got[2].off)
	}
	if got[1].from != "From - Sun Mar  8 10:00:00 2026" {
		t.Errorf("From line %q", got[1].from)
	}
}

func TestMboxScannerFiles(t *testing.T) {
	if got, err := scanAll(t, ""); err != nil || len(got) != 0 {
		t.Errorf("empty: %v %v", got, err)
	}
	if _, err := scanAll(t, "Subject: not an mbox\n"); !errors.Is(err, ErrNotMbox) {
		t.Errorf("not mbox: %v", err)
	}
	if _, err := scanAll(t, "\x1f\x8b\x08\x00rest"); !errors.Is(err, ErrCompressed) {
		t.Errorf("gzip: %v", err)
	}
	if _, err := scanAll(t, "PK\x03\x04rest"); !errors.Is(err, ErrCompressed) {
		t.Errorf("zip: %v", err)
	}
}

// What #39's export writes comes back byte for byte.
func TestMboxScannerReadsExport(t *testing.T) {
	messages := []string{
		"Subject: crlf\r\n\r\nFrom here\r\n>From there\r\n",
		"Subject: lf\n\nends with a blank line\n\n",
		"Subject: mixed\n\nline\r\nline\n",
		"Subject: long\n\n" + strings.Repeat("y", 3*lineBuffer) + "\n",
	}
	var buf bytes.Buffer
	w := NewMboxWriter(&buf)
	for _, m := range messages {
		if err := w.WriteMessage(strings.NewReader(m), date); err != nil {
			t.Fatal(err)
		}
	}
	got, err := scanAll(t, buf.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(messages) {
		t.Fatalf("got %d messages", len(got))
	}
	for i, m := range messages {
		if got[i].data != m {
			t.Errorf("message %d: got %q, want %q", i, short([]byte(got[i].data)), short([]byte(m)))
		}
		if !ParseFromDate(got[i].from).Equal(date) {
			t.Errorf("date %v", ParseFromDate(got[i].from))
		}
	}
}

// A message with a 10 MB line streams through in bounded memory.
func TestMboxScannerLongLine(t *testing.T) {
	const size = 10 << 20
	in := io.MultiReader(
		strings.NewReader("From a Sat Mar  7 08:05:01 2026\nSubject: big\n\n"),
		io.LimitReader(repeatReader('x'), size),
		strings.NewReader("\n\nFrom b Sat Mar  7 08:05:01 2026\nSubject: next\n"),
	)
	s := NewMboxScanner(in)
	m, err := s.Next()
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	n, err := io.Copy(io.Discard, m.Body)
	runtime.ReadMemStats(&after)
	if err != nil || n != int64(len("Subject: big\n\n"))+size+1 {
		t.Fatalf("read %d, %v", n, err)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 4<<20 {
		t.Errorf("allocated %d bytes for a 10 MB line", grown)
	}
	m, err = s.Next()
	if err != nil || m.From != "From b Sat Mar  7 08:05:01 2026" {
		t.Fatalf("next: %+v %v", m, err)
	}
}

type repeatReader byte

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

// Next skips what is left of a message that was not read.
func TestMboxScannerSkipsUnread(t *testing.T) {
	s := NewMboxScanner(strings.NewReader("From a Sat Mar  7 08:05:01 2026\nSubject: one\n\nFrom b Sat Mar  7 09:05:01 2026\nSubject: two\n"))
	if _, err := s.Next(); err != nil {
		t.Fatal(err)
	}
	m, err := s.Next()
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := io.ReadAll(m.Body); string(data) != "Subject: two\n" {
		t.Errorf("second message %q", data)
	}
}

func TestParseFromDate(t *testing.T) {
	for in, want := range map[string]time.Time{
		"From a Sat Mar  7 08:05:01 2026":                  time.Date(2026, 3, 7, 8, 5, 1, 0, time.UTC),
		"From - Sat Mar 07 08:05:01 +0100 2026":            time.Date(2026, 3, 7, 7, 5, 1, 0, time.UTC),
		"From a@b.c Sat Mar  7 08:05 2026":                 time.Date(2026, 3, 7, 8, 5, 0, 0, time.UTC),
		"From MAILER-DAEMON Sat Mar 7 08:05:01 2026 -0500": time.Date(2026, 3, 7, 13, 5, 1, 0, time.UTC),
		"From nobody":    {},
		"From a someday": {},
	} {
		if got := ParseFromDate(in); !got.Equal(want) {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}

func TestStatusFlags(t *testing.T) {
	if got := StatusFlags("RO", "AF"); !slices.Equal(got, []string{`\Seen`, `\Answered`, `\Flagged`}) {
		t.Errorf("got %v", got)
	}
	if got := StatusFlags("O", ""); got != nil {
		t.Errorf("unread: %v", got)
	}
}

func TestCRLFReader(t *testing.T) {
	for in, want := range map[string]string{
		"Subject: x\r\n\r\nbody\nbare lf stays\n": "Subject: x\r\n\r\nbody\nbare lf stays\n",
		"Subject: x\n\nbody\n":                    "Subject: x\r\n\r\nbody\r\n",
		// Mixed: only bare LFs change, existing CRLFs stay single.
		"Subject: x\n\npasted\r\nline\n": "Subject: x\r\n\r\npasted\r\nline\r\n",
		"no newline":                     "no newline",
		"":                               "",
		"a\rb\n":                         "a\rb\r\n",
		strings.Repeat("z", 2*lineBuffer) + "\nend\n": strings.Repeat("z", 2*lineBuffer) + "\r\nend\r\n",
	} {
		got, err := io.ReadAll(NewCRLFReader(strings.NewReader(in)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%q: got %q, want %q", short([]byte(in)), short(got), short([]byte(want)))
		}
	}
	// A CR at the end of one read and the LF at the start of the next.
	r := NewCRLFReader(io.MultiReader(strings.NewReader("a\n"), &oneByte{s: "b\r\nc\n"}))
	if got, _ := io.ReadAll(r); string(got) != "a\r\nb\r\nc\r\n" {
		t.Errorf("split CRLF: %q", got)
	}
}

type oneByte struct{ s string }

func (o *oneByte) Read(p []byte) (int, error) {
	if o.s == "" {
		return 0, io.EOF
	}
	p[0] = o.s[0]
	o.s = o.s[1:]
	return 1, nil
}

func TestDecodeModifiedUTF7(t *testing.T) {
	for in, want := range map[string]string{
		"INBOX":                  "INBOX",
		"Entw&APw-rfe":           "Entwürfe",
		"Gel&APY-schte Elemente": "Gelöschte Elemente",
		"&-":                     "&",
		"&ZeVnLIqe-":             "日本語",
		"&2D3eAA-":               "😀",
	} {
		if got, err := DecodeModifiedUTF7(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"&APw", "&A-", "ä"} {
		if _, err := DecodeModifiedUTF7(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const oneMessage = "From a Sat Mar  7 08:05:01 2026\nSubject: x\n\nbody\n"

func folderNames(folders []SourceFolder) []string {
	var out []string
	for _, f := range folders {
		out = append(out, f.Name)
	}
	slices.Sort(out)
	return out
}

func TestListMbox(t *testing.T) {
	root := t.TempDir()
	// Thunderbird
	write(t, filepath.Join(root, "tb", "Inbox"), oneMessage)
	write(t, filepath.Join(root, "tb", "Inbox.msf"), "// <!-- <mdb:mork:z v=\"1.4\"/> -->")
	write(t, filepath.Join(root, "tb", "Inbox.sbd", "Work"), "\n"+oneMessage)
	write(t, filepath.Join(root, "tb", "Inbox.sbd", "Work.sbd", "2024"), oneMessage)
	write(t, filepath.Join(root, "tb", "Trash"), "")
	// Apple Mail
	write(t, filepath.Join(root, "apple", "Archive.mbox", "mbox"), oneMessage)
	write(t, filepath.Join(root, "apple", "Archive.mbox", "table_of_contents"), "\x00\x01binary")
	write(t, filepath.Join(root, "apple", "Archive.mbox", "Old.mbox", "mbox"), oneMessage)
	// A single file
	write(t, filepath.Join(root, "single", "Sent.mbox"), oneMessage)
	write(t, filepath.Join(root, "takeout.mbox.gz"), "\x1f\x8b\x08rest")
	// A symlink and a FIFO are skipped.
	if err := os.Symlink(filepath.Join(root, "single", "Sent.mbox"), filepath.Join(root, "tb", "Link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "tb", "Fifo"), 0o600); err != nil {
		t.Fatal(err)
	}

	folders, skipped, err := ListMbox(filepath.Join(root, "tb"))
	if err != nil {
		t.Fatal(err)
	}
	if got := folderNames(folders); !slices.Equal(got, []string{"Inbox", "Inbox/Work", "Inbox/Work/2024"}) {
		t.Errorf("Thunderbird: %v", got)
	}
	reasons := map[string]bool{}
	for _, s := range skipped {
		reasons[s.Path] = true
	}
	for _, p := range []string{"Inbox.msf", "Trash", "Link", "Fifo"} {
		if !reasons[p] {
			t.Errorf("%s not skipped: %+v", p, skipped)
		}
	}

	folders, _, err = ListMbox(filepath.Join(root, "apple"))
	if err != nil {
		t.Fatal(err)
	}
	if got := folderNames(folders); !slices.Equal(got, []string{"Archive", "Archive/Old"}) {
		t.Errorf("Apple: %v", got)
	}
	folders, _, err = ListMbox(filepath.Join(root, "apple", "Archive.mbox"))
	if err != nil {
		t.Fatal(err)
	}
	if got := folderNames(folders); !slices.Equal(got, []string{"Archive", "Old"}) {
		t.Errorf("Apple bundle: %v", got)
	}
	folders, _, err = ListMbox(filepath.Join(root, "single", "Sent.mbox"))
	if err != nil || len(folders) != 1 || folders[0].Name != "Sent" {
		t.Errorf("single file: %+v %v", folders, err)
	}
	if _, _, err := ListMbox(filepath.Join(root, "takeout.mbox.gz")); !errors.Is(err, ErrCompressed) {
		t.Errorf("gzip: %v", err)
	}
	if _, _, err := ListMbox(filepath.Join(root, "tb", "Inbox.msf")); !errors.Is(err, ErrNotMbox) {
		t.Errorf("not mbox: %v", err)
	}
}

func TestListMaildir(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"", ".Sent", ".Archiv.2024", ".Entw&APw-rfe"} {
		for _, sub := range []string{"cur", "new", "tmp"} {
			if err := os.MkdirAll(filepath.Join(root, dir, sub), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(t, filepath.Join(root, "dovecot-uidlist"), "")
	write(t, filepath.Join(root, "subscriptions"), "")
	write(t, filepath.Join(root, ".Sent", "maildirfolder"), "")
	write(t, filepath.Join(root, "cur", "200.host:2,S"), "b")
	write(t, filepath.Join(root, "new", "100.host"), "a")
	write(t, filepath.Join(root, "cur", "300.host:2,FR"), "c")
	write(t, filepath.Join(root, "tmp", "400.host"), "half")
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "cur", "500.host:2,")); err != nil {
		t.Fatal(err)
	}

	folders, _, err := ListMaildir(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := folderNames(folders); !slices.Equal(got, []string{"Archiv/2024", "Entwürfe", "INBOX", "Sent"}) {
		t.Errorf("folders %v", got)
	}
	files, skipped, err := MaildirFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f.Path))
	}
	if !slices.Equal(names, []string{"100.host", "200.host:2,S", "300.host:2,FR"}) || len(skipped) != 1 {
		t.Errorf("files %v, skipped %v", names, skipped)
	}
	if !slices.Equal(files[2].Flags, []string{`\Flagged`, `\Answered`}) {
		t.Errorf("flags %v", files[2].Flags)
	}
	if _, _, err := ListMaildir(filepath.Join(root, "cur")); err == nil {
		t.Error("cur accepted as a Maildir")
	}
}

func TestCheckFolderName(t *testing.T) {
	for _, bad := range []string{"", "a\x00b", "tab\there", "\xff", strings.Repeat("x", MaxFolderNameLen+1), "a\u0085b"} {
		if CheckFolderName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"INBOX", "Archiv/2024", "Gelöschte Elemente", "日本語"} {
		if err := CheckFolderName(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

func FuzzMboxScanner(f *testing.F) {
	f.Add("From a Sat Mar  7 08:05:01 2026\nSubject: x\n\n>From y\n\nFrom b Sat Mar  7 08:05:01 2026\n\n")
	f.Add("\r\nFrom a 10:00 2026\r\n\r\n\r\n")
	f.Add("From \n")
	f.Fuzz(func(t *testing.T, in string) {
		got, err := scanAll(t, in)
		if err != nil {
			return
		}
		// Output never grows beyond the input.
		total := 0
		for _, m := range got {
			total += len(m.data) + len(m.from)
			if m.off < 0 || m.off > int64(len(in)) || !strings.HasPrefix(in[m.off:], m.from) {
				t.Fatalf("offset %d for %q", m.off, m.from)
			}
		}
		if total > len(in) {
			t.Fatalf("output %d bytes from %d", total, len(in))
		}
	})
}
