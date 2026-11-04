package gobd

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cleanZIP writes the fixture as a ZIP and returns its path and entries.
func cleanZIP(t *testing.T) (*fixture, string, []zipEntryT) {
	t.Helper()
	f := newFixture(t)
	p := filepath.Join(t.TempDir(), "export.zip")
	f.write(t, p)
	return f, p, readZIP(t, p)
}

// Each crafted entry that an unpacker could read differently from verify
// is a name finding (B4 of the plan review).
func TestVerifyZIPSafety(t *testing.T) {
	f, _, entries := cleanZIP(t)
	byName := map[string][]byte{}
	for _, e := range entries {
		byName[e.name] = e.data
	}
	eml := MessagePath(f.msgs[0].m.SHA256, f.msgs[0].m.SortAt, berlin(t))
	nfc := "attest/caf\u00e9.txt"
	nfd := "attest/cafe\u0301.txt"
	for name, c := range map[string]struct {
		edit func([]zipEntryT) []zipEntryT
		kind string
		part string
	}{
		"dot-dot": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: "mail/../../etc/cron.d/x", data: []byte("x")})
		}, KindName, "path with an empty, . or .. element"},
		"absolute": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: "/tmp/x", data: []byte("x")})
		}, KindName, "absolute path"},
		"backslash": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: `mail\x.eml`, data: []byte("x")})
		}, KindName, "backslash"},
		"drive": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: "C:/x", data: []byte("x")})
		}, KindName, "drive letter"},
		"two messages.csv": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: MessagesFile, data: []byte("evil")})
		}, KindName, "duplicate entry"},
		"Messages.csv": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: "Messages.csv", data: byName[MessagesFile]})
		}, KindName, "differs from messages.csv only in case"},
		"upper-case eml": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: strings.ToUpper(eml[:len(eml)-4]) + ".eml", data: byName[eml]})
		}, KindName, "differs from " + eml},
		"NFC/NFD pair": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: nfc, data: []byte("a")}, zipEntryT{name: nfd, data: []byte("b")})
		}, KindName, "unexpected file"},
		"unexpected file": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: "run.sh", data: []byte("x")})
		}, KindName, "run.sh"},
		"directory entry": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: "mail/", mode: os.ModeDir | 0o700})
		}, KindName, "directory entry"},
		"symlink entry": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: "attest/link", data: []byte("/etc/passwd"), mode: os.ModeSymlink | 0o777})
		}, KindName, "symbolic link"},
		"same eml twice": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: eml, data: []byte("evil")})
		}, KindName, "appears twice"},
		"oversized eml": {func(es []zipEntryT) []zipEntryT {
			for i := range es {
				if es[i].name == eml {
					es[i].data = append(bytes.Clone(es[i].data), bytes.Repeat([]byte("A"), 1<<20)...)
				}
			}
			return es
		}, KindName, "larger than expected"},
		"oversized statement": {func(es []zipEntryT) []zipEntryT {
			for i := range es {
				if es[i].name == StatementFile {
					es[i].data = bytes.Repeat([]byte("A"), 9<<20)
				}
			}
			return es
		}, KindName, "larger than expected"},
		"leaves without statement": {func(es []zipEntryT) []zipEntryT {
			return append(es, zipEntryT{name: LeavesPath(1), data: f.leaves[1]})
		}, KindName, "does not include this leaves file"},
	} {
		t.Run(name, func(t *testing.T) {
			es := c.edit(append([]zipEntryT(nil), entries...))
			p := writeZIP(t, es)
			fs, _ := verify(t, p, VerifyOptions{Trusted: f.trusted()})
			if !hasFinding(fs, c.kind, c.part) {
				t.Fatalf("no %s %q finding:\n%s", c.kind, c.part, strings.Join(kinds(fs), "\n"))
			}
		})
	}
	// NFC and NFD of the same name count as duplicates of each other.
	if fold(nfc) != fold(nfd) {
		t.Fatal("NFC and NFD fold differently")
	}
	// The rewritten clean ZIP has no findings, so each case above is
	// caused by its edit.
	fs, _ := verify(t, writeZIP(t, entries), VerifyOptions{Trusted: f.trusted()})
	if len(fs) != 0 {
		t.Fatalf("rewritten clean ZIP: %v", kinds(fs))
	}
}

// A ZIP with more central directory records than an export can have is
// refused before archive/zip reads it into memory.
func TestVerifyZIPTooManyEntries(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for i := 0; i <= maxZIPEntries; i++ {
		h := &zip.FileHeader{Name: "x", Method: zip.Store}
		if _, err := zw.CreateRaw(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prescanZIP(bytes.NewReader(b.Bytes()), int64(b.Len())); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("prescan: %v", err)
	}
	p := filepath.Join(t.TempDir(), "many.zip")
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, _ := verify(t, p, VerifyOptions{})
	if !hasFinding(fs, KindName, "more than") {
		t.Fatalf("findings: %v", kinds(fs[:min(len(fs), 5)]))
	}
}

// A ZIP with data before the archive (a self-extractor stub, or an
// archive glued to another) is refused: archive/zip would then read
// records from another offset than the prescan.
func TestVerifyZIPPrependedData(t *testing.T) {
	_, p, _ := cleanZIP(t)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	glued := append([]byte("MZ stub"), b...)
	if err := prescanZIP(bytes.NewReader(glued), int64(len(glued))); err == nil || !strings.Contains(err.Error(), "data before") {
		t.Fatalf("prescan: %v", err)
	}
	// One long name is refused too.
	var z bytes.Buffer
	zw := zip.NewWriter(&z)
	if _, err := zw.Create(strings.Repeat("a", maxZIPRecordData+1)); err != nil {
		t.Fatal(err)
	}
	_ = zw.Close()
	if err := prescanZIP(bytes.NewReader(z.Bytes()), int64(z.Len())); err == nil {
		t.Fatal("long name accepted")
	}
	// A truncated ZIP is a finding, not a crash.
	cut := filepath.Join(t.TempDir(), "cut.zip")
	if err := os.WriteFile(cut, b[:len(b)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	fs, _ := verify(t, cut, VerifyOptions{})
	if len(fs) != 1 || !hasFinding(fs, KindName, "not a readable export ZIP") {
		t.Fatalf("truncated: %v", kinds(fs))
	}
}

// zipDirectoryOffset finds the same central directory as archive/zip,
// also for zip64.
func TestZIPDirectoryOffsetMatchesArchiveZip(t *testing.T) {
	_, p, _ := cleanZIP(t)
	b, _ := os.ReadFile(p)
	off, err := zipDirectoryOffset(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(b[off:]) != 0x02014b50 {
		t.Fatalf("offset %d is not a central directory record", off)
	}
	// Force zip64 records: archive/zip writes them with more than 65535
	// entries.
	var z bytes.Buffer
	zw := zip.NewWriter(&z)
	for i := 0; i < 70_000; i++ {
		if _, err := zw.CreateRaw(&zip.FileHeader{Name: "x", Method: zip.Store}); err != nil {
			t.Fatal(err)
		}
	}
	_ = zw.Close()
	zb := z.Bytes()
	off, err = zipDirectoryOffset(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(zb[off:]) != 0x02014b50 || len(zr.File) != 70_000 {
		t.Fatalf("zip64: offset %d, %d files", off, len(zr.File))
	}
}
