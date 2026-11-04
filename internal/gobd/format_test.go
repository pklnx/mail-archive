package gobd

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/pklnx/mail-archive/internal/attest"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func TestTextValue(t *testing.T) {
	long := strings.Repeat("ä", MaxValueBytes) // 2 bytes each
	for in, want := range map[string]string{
		"":                            "",
		"plain":                       "plain",
		`say "hi"`:                    "say 'hi'",
		"a;b":                         "a;b",
		"line1\r\nline2":              "line1  line2",
		"tab\there":                   "tab here",
		"nul\x00bel\x07":              "nul bel ",
		"c1\u0085x":                   "c1 x",
		"ls\u2028ps\u2029":            "ls ps ",
		"=SUM(A1)":                    "'=SUM(A1)",
		"+49":                         "'+49",
		"-50 %":                       "'-50 %",
		"@home":                       "'@home",
		"\t=x":                        " =x",
		"Grüße 漢字 🙂":                  "Grüße 漢字 🙂",
		"bad\xffutf8":                 "bad\uFFFDutf8",
		long:                          long[:MaxValueBytes],
		"=" + long:                    "'=" + long[:MaxValueBytes-2],
		strings.Repeat("a", 5) + "\"": "aaaaa'",
	} {
		got := textValue(in)
		if got != want {
			t.Errorf("textValue(%.40q) = %.40q, want %.40q", in, got, want)
		}
		if !validText(got) {
			t.Errorf("textValue(%.40q) = %.40q is not valid for verify", in, got)
		}
	}
}

// 31.12. 23:30 UTC is 01.01. in Berlin: the date, time and month folder
// follow the export time zone, date_utc stays UTC.
func TestDatesAcrossTimeZone(t *testing.T) {
	loc := berlin(t)
	at := time.Date(2024, 12, 31, 23, 30, 0, 0, time.UTC)
	if d, c, u := formatDate(at, loc), formatClock(at, loc), formatUTC(at); d != "01.01.2025" || c != "00:30:00" || u != "2024-12-31T23:30:00Z" {
		t.Fatalf("got %s %s %s", d, c, u)
	}
	sha := strings.Repeat("a", 64)
	if p := MessagePath(sha, at, loc); p != "mail/2025/01/"+sha+".eml" || p != pathFromDate(sha, formatDate(at, loc)) {
		t.Fatalf("path %s", p)
	}
	summer := time.Date(2024, 6, 30, 22, 30, 0, 0, time.UTC) // UTC+2
	if d, c := formatDate(summer, loc), formatClock(summer, loc); d != "01.07.2024" || c != "00:30:00" {
		t.Fatalf("summer: %s %s", d, c)
	}
	far := time.Date(12000, 1, 1, 0, 0, 0, 0, time.UTC)
	if d, u := formatDate(far, loc), formatUTC(far); d != "31.12.9999" || !validUTC(u) {
		t.Fatalf("year 12000: %s %s", d, u)
	}
}

func TestIndexXMLGolden(t *testing.T) {
	got := IndexXML("mail-archive v1.5.0")
	golden := filepath.Join("testdata", "index.xml")
	if *update {
		if err := os.WriteFile(golden, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("index.xml differs from %s (run with -update after checking)", golden)
	}
	if !bytes.HasPrefix(got, []byte(`<?xml version="1.0" encoding="UTF-8"?>`+"\n")) {
		t.Fatal("UTF-8 not declared")
	}
	dec := xml.NewDecoder(bytes.NewReader(got))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() != "EOF" {
				t.Fatalf("not well-formed: %v", err)
			}
			break
		}
	}
	if err := checkIndex(bytes.NewReader(got)); err != nil {
		t.Fatal(err)
	}
	// The columns in index.xml are the CSV headers, and the primary keys
	// lead (the DTD's order).
	for _, tb := range tables {
		for i, k := range tb.keys {
			if tb.columns[i].name != k {
				t.Errorf("%s: key %s is not column %d", tb.name, k, i+1)
			}
		}
		for _, c := range tb.columns {
			if !bytes.Contains(got, []byte("<Name>"+c.name+"</Name>")) {
				t.Errorf("%s.%s not described", tb.name, c.name)
			}
		}
	}
}

func TestParseSums(t *testing.T) {
	sha := strings.Repeat("a", 64)
	ok := sumsLine(sha, "README.txt") + sumsLine(sha, "mail/2024/01/"+sha+".eml")
	var paths []string
	digest, err := ParseSums(strings.NewReader(ok), func(p, s string) error { paths = append(paths, p); return nil })
	if err != nil || len(paths) != 2 || digest != shaOf([]byte(ok)) {
		t.Fatalf("%v %v %s", err, paths, digest)
	}
	for name, in := range map[string]string{
		"binary marker":  sha + " *README.txt\n",
		"one space":      sha + " README.txt\n",
		"upper hex":      strings.ToUpper(sha) + "  README.txt\n",
		"short hash":     sha[1:] + "  README.txt\n",
		"crlf":           sha + "  README.txt\r\n",
		"no newline":     sha + "  README.txt",
		"backslash":      `\` + sha + `  a\nb` + "\n",
		"space in path":  sha + "  a b\n",
		"empty path":     sha + "  \n",
		"long line":      sha + "  " + strings.Repeat("a", 600) + "\n",
		"non-ascii path": sha + "  ä\n",
		"comment":        "# x\n",
	} {
		if _, err := ParseSums(strings.NewReader(in), func(string, string) error { return nil }); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestScanTable(t *testing.T) {
	cols := missingColumns
	sha := strings.Repeat("b", 64)
	good := `"` + sha + `";"mail/2024/01/` + sha + `.eml";"gone"` + "\r\n"
	var rows [][]string
	var bad []string
	run := func(in string) {
		rows, bad = nil, nil
		err := scanTable(strings.NewReader(in), cols, func(_ int, v []string) { rows = append(rows, append([]string(nil), v...)) },
			func(n int, why string) { bad = append(bad, why) })
		if err != nil {
			t.Fatal(err)
		}
	}
	run(header(cols) + good + good)
	if len(rows) != 2 || len(bad) != 0 || rows[0][2] != "gone" {
		t.Fatalf("good: %v %v", rows, bad)
	}
	for name, line := range map[string]string{
		"unquoted text": sha + `;"x";"y"` + "\r\n",
		"lf only":       `"` + sha + `";"x";"y"` + "\n",
		"too many":      `"` + sha + `";"x";"y";"z"` + "\r\n",
		"too few":       `"` + sha + `";"x"` + "\r\n",
		"quote inside":  `"` + sha + `";"x"y";"z"` + "\r\n",
		"formula":       `"` + sha + `";"x";"=1+1"` + "\r\n",
		"tab":           `"` + sha + `";"x";"a` + "\t" + `b"` + "\r\n",
		"bad sha":       `"` + sha[1:] + `";"x";"y"` + "\r\n",
		"empty reason":  `"` + sha + `";"x";""` + "\r\n",
		"no line end":   `"` + sha + `";"x";"y"`,
		"too long":      `"` + sha + `";"` + strings.Repeat("x", maxLine) + `";"y"` + "\r\n",
		"invalid utf-8": `"` + sha + `";"x";"` + "\xff" + `"` + "\r\n",
		"unterminated":  `"` + sha + `";"x;y` + "\r\n",
	} {
		run(header(cols) + line + good)
		if len(bad) != 1 || (name != "no line end" && len(rows) != 1) {
			t.Errorf("%s: rows %d, bad %v", name, len(rows), bad)
		}
	}
	run(strings.Replace(header(cols), "reason", "grund", 1) + good)
	if len(bad) != 1 {
		t.Errorf("header: %v", bad)
	}
	// Integers and dates.
	mc := messageColumns
	for _, c := range []struct {
		v  string
		ok bool
	}{{"0", true}, {"12", true}, {"012", false}, {"-1", false}, {"1e3", false}, {"99999999999999999999", false}} {
		if got := validPlain(c.v, mc[2]); got != c.ok {
			t.Errorf("integer %q: %v", c.v, got)
		}
	}
	for _, c := range []struct {
		v  string
		ok bool
	}{{"01.01.2025", true}, {"1.1.2025", false}, {"31.02.2025", false}, {"2025-01-01", false}} {
		if got := validPlain(c.v, mc[3]); got != c.ok {
			t.Errorf("date %q: %v", c.v, got)
		}
	}
}

// The writer checks each message file against its SHA-256 and size and
// takes a bad one back; a ZIP cannot.
func TestWriterMismatch(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "export")
	sink, err := NewDirSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(sink, Options{Statement: f.st, Location: berlin(t)})
	if err != nil {
		t.Fatal(err)
	}
	m := f.msgs[0]
	err = w.AddFile(m.m, strings.NewReader("other bytes"))
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(MessagePath(m.m.SHA256, m.m.SortAt, berlin(t))))); !os.IsNotExist(err) {
		t.Fatalf("the bad file stayed: %v", err)
	}
	// A longer body than the size is a mismatch too, not a truncation.
	if err := w.AddFile(m.m, bytes.NewReader(append(bytes.Clone(m.body), 'x'))); !errors.Is(err, ErrMismatch) {
		t.Fatalf("longer: %v", err)
	}
	// A read error of the body is reported and the file taken back.
	if err := w.AddFile(m.m, iotest.ErrReader(errors.New("disk error"))); err == nil || !strings.Contains(err.Error(), "disk error") {
		t.Fatalf("read error: %v", err)
	}
	// The writer goes on after a mismatch.
	if err := w.AddFile(m.m, bytes.NewReader(m.body)); err != nil {
		t.Fatal(err)
	}

	var zb bytes.Buffer
	zw, _ := NewWriter(NewZipSink(&zb, f.st.Created), Options{Statement: f.st, Location: berlin(t)})
	err = zw.AddFile(m.m, strings.NewReader("other bytes"))
	if !errors.Is(err, ErrMismatch) || !errors.Is(err, ErrCannotUndo) {
		t.Fatalf("zip mismatch: %v", err)
	}
	if err := zw.AddFile(f.msgs[1].m, bytes.NewReader(f.msgs[1].body)); err == nil {
		t.Fatal("a broken ZIP writer went on")
	}
}

// The writer refuses input that would make an export inconsistent.
func TestWriterGuards(t *testing.T) {
	f := newFixture(t)
	start := func() *Writer {
		t.Helper()
		sink, err := NewDirSink(filepath.Join(t.TempDir(), "export"))
		if err != nil {
			t.Fatal(err)
		}
		st := f.st
		st.AttestationSeq, st.AttestationHash = 2, f.chain[1].Hash
		w, err := NewWriter(sink, Options{Statement: st, Location: berlin(t), MaxMessages: 2})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	add := func(w *Writer, i int) error { return w.AddFile(f.msgs[i].m, bytes.NewReader(f.msgs[i].body)) }

	w := start()
	_ = add(w, 0)
	_ = add(w, 1)
	if err := add(w, 2); err == nil {
		t.Error("MaxMessages not enforced")
	}

	w = start()
	_ = add(w, 0)
	if err := w.AddMessageRow(f.msgs[1].m); err != nil {
		t.Fatal(err) // the row is written; the check is at the end of the table
	}
	if err := w.AddLocationRow(f.locs[0]); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Errorf("rows that differ from the files: %v", err)
	}

	w = start()
	_ = add(w, 0)
	_ = w.AddMessageRow(f.msgs[0].m)
	l := f.locs[0]
	l.Owner = "kim"
	if err := w.AddLocationRow(l); err == nil {
		t.Error("an owner outside the statement's owners was written")
	}

	w = start()
	_ = add(w, 0)
	_ = w.AddMessageRow(f.msgs[0].m)
	if err := add(w, 1); err == nil {
		t.Error("a file after the message rows")
	}

	w = start()
	_ = add(w, 0)
	_ = add(w, 1)
	_ = w.AddMessageRow(f.msgs[1].m)
	if err := w.AddMessageRow(f.msgs[0].m); err == nil {
		t.Error("archive_seq out of order")
	}

	w = start()
	_ = add(w, 0)
	_, err := w.Finish(Final{Chain: f.chain[1:], Files: func(yield func(string) error) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "differ") {
		t.Errorf("SHA256SUMS listing that differs from the files: %v", err)
	}

	w = start()
	_, err = w.Finish(Final{Chain: f.chain[:1]})
	if err == nil {
		t.Error("a chain that does not end with the statement's attestation")
	}
}

// The same input with the same clock and id gives the same ZIP bytes.
func TestZIPDeterministic(t *testing.T) {
	var got [][]byte
	for range 2 {
		f := newFixture(t)
		p := filepath.Join(t.TempDir(), "export.zip")
		f.write(t, p)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, b)
	}
	if !bytes.Equal(got[0], got[1]) {
		t.Fatal("two ZIPs of the same input differ")
	}
	// Every entry has the statement's time, not the clock's.
	zr, err := zip.NewReader(bytes.NewReader(got[0]), int64(len(got[0])))
	if err != nil {
		t.Fatal(err)
	}
	created := newFixture(t).st.Created
	for _, f := range zr.File {
		if !f.Modified.Equal(created) || f.Mode().Perm() != 0o600 {
			t.Fatalf("%s: time %v, mode %v", f.Name, f.Modified, f.Mode())
		}
	}
}

// The directory sink uses 0700 and 0600.
func TestDirSinkModes(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "export")
	f.write(t, dir)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v", p, fi.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDirSink(dir); err == nil {
		t.Fatal("NewDirSink into an existing directory")
	}
}

// What the files say: the CSV rules on a written export, and the README's
// group line.
func TestWrittenTables(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "export")
	res := f.write(t, dir)
	files := readTree(t, dir)
	msgs := string(files[MessagesFile])
	for _, want := range []string{
		header(messageColumns),
		`"'=SUM(A1) Rechnung"`, // formula neutralized
		`"Silvester 'Feier'"`,  // " becomes '
		`"line1  line2 tab"`,   // CR, LF, TAB become spaces
		`;01.01.2025;"00:30:00";"2024-12-31T23:30:00Z";`,
		`"mail/2025/01/` + f.msgs[1].m.SHA256 + `.eml"`,
		`;1;"rechnung.pdf|-x.xml";`, // has_attachment and names
		";6;\r\n",                   // archive_seq 6, no attestation
	} {
		if !strings.Contains(msgs, want) {
			t.Errorf("messages.csv lacks %q:\n%s", want, msgs)
		}
	}
	if bytes.HasPrefix(files[MessagesFile], []byte("\xef\xbb\xbf")) {
		t.Error("BOM")
	}
	if !strings.Contains(string(files[LocationsFile]), `"Archiv/2024";2;9;"";"";`) {
		t.Errorf("locations.csv:\n%s", files[LocationsFile])
	}
	readme := string(files[ReadmeFile])
	for _, want := range []string{"Kundengruppe: Meier GmbH (Nr. 7)", "Customer group: Meier GmbH (no. 7)",
		"Bcc", "instance", "Instanz", "sha256sum -c SHA256SUMS", "\r\n"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README lacks %q", want)
		}
	}
	st, err := attest.ParseStatement(files[StatementFile])
	if err != nil {
		t.Fatal(err)
	}
	if st.Messages != 4 || st.Locations != 5 || st.Missing != 0 || !st.Complete || st.SHA256SUMS != shaOf(files[SumsFile]) ||
		res.StatementSHA256 != shaOf(files[StatementFile]) {
		t.Fatalf("statement: %+v", st)
	}
	// The order of SHA256SUMS: the message files first.
	if !strings.HasPrefix(string(files[SumsFile]), f.msgs[0].m.SHA256+"  mail/") {
		t.Fatalf("SHA256SUMS:\n%s", files[SumsFile])
	}
}
