package gobd

import (
	"archive/zip"
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// Limits for a ZIP that Verify reads. archive/zip keeps every central
// directory record in memory (about 250 bytes plus name, extra field and
// comment), so Verify counts the records first, in constant memory, and
// refuses a ZIP above these bounds before archive/zip reads it. An export
// has at most MaxZIPMessages message files, a few fixed files and one
// leaves file per attestation; its records are about 120 bytes each.
const (
	maxZIPEntries = 300_000
	// maxZIPRecordData bounds name, extra field and comment of one record;
	// maxZIPDirData all of them together.
	maxZIPRecordData = 1024
	maxZIPDirData    = 64 << 20
	// maxDeflateRatio is above Deflate's largest expansion (about 1032).
	maxDeflateRatio = 1100
)

// entryKind is what a container entry is.
type entryKind int

const (
	entryFile entryKind = iota
	entryDir
	entryOther // a symbolic link, device or anything but a file
)

// rawEntry is an entry as the container lists it, before its name is
// checked.
type rawEntry struct {
	name string
	kind entryKind
	size int64 // uncompressed (ZIP, declared) or on disk
	// problem is set when the entry cannot be read safely (encrypted,
	// unknown compression).
	problem string
	zf      *zip.File // ZIP only
}

// container is an export to verify: a directory or a ZIP. It lists its
// entries through walk, one at a time, so that a directory of a million
// files is never held as a list.
type container struct {
	root  *os.Root    // directory
	zr    *zip.Reader // ZIP
	file  *os.File    // ZIP
	isZIP bool
}

func (c *container) close() {
	if c.root != nil {
		_ = c.root.Close()
	}
	if c.file != nil {
		_ = c.file.Close()
	}
}

// open opens an entry for reading.
func (c *container) open(e *rawEntry) (io.ReadCloser, error) {
	if e.zf != nil {
		return e.zf.Open()
	}
	if c.root == nil {
		return nil, fs.ErrNotExist
	}
	return openRegular(c.root, e.name)
}

// openContainer opens path as a directory or, for a regular file, a ZIP.
// An error means it is neither; a damaged ZIP is a finding instead.
func openContainer(p string, finding func(kind, path, detail string)) (*container, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		root, err := os.OpenRoot(p)
		if err != nil {
			return nil, err
		}
		return &container{root: root}, nil
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is neither a directory nor a file", p)
	}
	f, err := os.Open(p) //nolint:gosec // the export the user asked to verify
	if err != nil {
		return nil, err
	}
	c := &container{file: f, isZIP: true}
	if err := prescanZIP(f, fi.Size()); err != nil {
		finding(KindName, path.Base(p), "not a readable export ZIP: "+err.Error())
		return c, nil //nolint:nilerr // a damaged ZIP is a finding, not an error
	}
	zr, err := zip.NewReader(f, fi.Size())
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		finding(KindName, path.Base(p), "not a readable export ZIP: "+err.Error())
		return c, nil //nolint:nilerr // a damaged ZIP is a finding, not an error
	}
	c.zr = zr
	return c, nil
}

// walk calls fn for every entry. In a directory it does not descend into
// a directory that an export cannot have.
func (c *container) walk(fn func(e *rawEntry), finding func(kind, path, detail string)) error {
	if c.zr != nil {
		for _, zf := range c.zr.File {
			fn(zipEntry(zf))
		}
		return nil
	}
	if c.root == nil {
		return nil
	}
	return fs.WalkDir(c.root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			finding(KindName, name, "cannot be read: "+err.Error())
			return nil //nolint:nilerr // reported as a finding; the walk goes on
		}
		if name == "." {
			return nil
		}
		switch {
		case d.IsDir():
			if !allowedDir(name) {
				finding(KindName, name+"/", "unexpected directory")
				return fs.SkipDir
			}
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				finding(KindName, name, "cannot be read: "+err.Error())
				return nil //nolint:nilerr // reported as a finding; the walk goes on
			}
			fn(&rawEntry{name: name, kind: entryFile, size: info.Size()})
		default:
			fn(&rawEntry{name: name, kind: entryOther})
		}
		return nil
	})
}

func zipEntry(zf *zip.File) *rawEntry {
	e := &rawEntry{name: zf.Name, kind: entryFile, zf: zf}
	mode := zf.Mode()
	switch {
	case mode.IsDir() || strings.HasSuffix(zf.Name, "/"):
		e.kind = entryDir
	case !mode.IsRegular():
		e.kind = entryOther
	}
	switch {
	case zf.UncompressedSize64 > 1<<62:
		e.problem = "declares an impossible size"
	case zf.Flags&0x1 != 0:
		e.problem = "is encrypted"
	case zf.Method != zip.Store && zf.Method != zip.Deflate:
		e.problem = fmt.Sprintf("uses compression method %d", zf.Method)
	case zf.Method == zip.Store && zf.UncompressedSize64 != zf.CompressedSize64:
		e.problem = "is stored with two different sizes"
	case zf.UncompressedSize64 > maxDeflateRatio*zf.CompressedSize64+1024:
		// Deflate cannot expand more than about 1032 times; a larger
		// declared size would only make verify read for a long time.
		e.problem = "declares a size Deflate cannot reach"
	}
	e.size = int64(min(zf.UncompressedSize64, 1<<62)) //nolint:gosec // bounded above
	return e
}

// openRegular opens a file under root and refuses anything that is not a
// regular file by the time it is open.
func openRegular(root *os.Root, name string) (io.ReadCloser, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("not a regular file")
	}
	return f, nil
}

// allowedDir reports whether a directory may appear in an export.
func allowedDir(name string) bool {
	switch {
	case name == "mail", name == "attest", name == "attest/leaves":
		return true
	case len(name) == len("mail/2024") && strings.HasPrefix(name, MailPrefix):
		return allDigits(name[5:])
	case len(name) == len("mail/2024/01") && strings.HasPrefix(name, MailPrefix) && name[9] == '/':
		return allDigits(name[5:9]) && validMonth(name[10:])
	}
	return false
}

// prescanZIP finds the central directory as archive/zip does and walks
// its records with constant memory, refusing a ZIP with more records or
// more name, extra and comment bytes than an export can have. It also
// refuses data before the archive (archive/zip's base offset), so that
// both read the same records.
func prescanZIP(r io.ReaderAt, size int64) error {
	dirOffset, err := zipDirectoryOffset(r, size)
	if err != nil {
		return err
	}
	br := bufio.NewReader(io.NewSectionReader(r, dirOffset, size-dirOffset))
	var hdr [46]byte
	var n, data int64
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return nil //nolint:nilerr // the end of the records; archive/zip stops here too
		}
		if binary.LittleEndian.Uint32(hdr[0:]) != 0x02014b50 {
			return nil
		}
		v := int64(binary.LittleEndian.Uint16(hdr[28:])) + int64(binary.LittleEndian.Uint16(hdr[30:])) +
			int64(binary.LittleEndian.Uint16(hdr[32:]))
		n++
		data += v
		switch {
		case n > maxZIPEntries:
			return fmt.Errorf("more than %d entries", maxZIPEntries)
		case v > maxZIPRecordData:
			return fmt.Errorf("entry %d has a name, extra field and comment of %d bytes", n, v)
		case data > maxZIPDirData:
			return errors.New("the central directory is larger than an export's")
		}
		if _, err := br.Discard(int(v)); err != nil {
			return nil //nolint:nilerr // a truncated record; archive/zip stops here too
		}
	}
}

// zipDirectoryOffset is readDirectoryEnd of archive/zip: the end record
// in the last 1 KiB or 65 KiB, the zip64 end record when the end record
// says so, and the directory's offset. A non-zero base offset (data
// before the archive) is refused.
func zipDirectoryOffset(r io.ReaderAt, size int64) (int64, error) {
	var buf []byte
	var endOffset int64 = -1
	for _, bLen := range []int64{1024, 65 * 1024} {
		bLen = min(bLen, size)
		buf = make([]byte, bLen)
		if _, err := r.ReadAt(buf, size-bLen); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if p := findEndSignature(buf); p >= 0 {
			buf = buf[p:]
			endOffset = size - bLen + int64(p)
			break
		}
		if bLen == size {
			break
		}
	}
	if endOffset < 0 {
		return 0, errors.New("no end of central directory")
	}
	records := uint64(binary.LittleEndian.Uint16(buf[10:]))
	dirSize := uint64(binary.LittleEndian.Uint32(buf[12:]))
	dirOffset := uint64(binary.LittleEndian.Uint32(buf[16:]))
	if records == 0xffff || dirSize == 0xffffffff || dirOffset == 0xffffffff {
		if loc := endOffset - 20; loc >= 0 {
			var lb [20]byte
			if _, err := r.ReadAt(lb[:], loc); err != nil {
				return 0, err
			}
			if binary.LittleEndian.Uint32(lb[0:]) == 0x07064b50 && binary.LittleEndian.Uint32(lb[4:]) == 0 &&
				binary.LittleEndian.Uint32(lb[16:]) == 1 {
				p := binary.LittleEndian.Uint64(lb[8:])
				if p > uint64(size) { //nolint:gosec // a file size is not negative
					return 0, errors.New("bad zip64 end record")
				}
				var eb [56]byte
				if _, err := r.ReadAt(eb[:], int64(p)); err != nil { //nolint:gosec // bounded by size above
					return 0, err
				}
				if binary.LittleEndian.Uint32(eb[0:]) != 0x06064b50 {
					return 0, errors.New("bad zip64 end record")
				}
				endOffset = int64(p) //nolint:gosec // bounded by size above
				dirSize = binary.LittleEndian.Uint64(eb[40:])
				dirOffset = binary.LittleEndian.Uint64(eb[48:])
			}
		}
	}
	if dirSize > uint64(size) || dirOffset > uint64(size) { //nolint:gosec // a file size is not negative
		return 0, errors.New("bad central directory location")
	}
	if base := endOffset - int64(dirSize) - int64(dirOffset); base != 0 { //nolint:gosec // both bounded by size
		return 0, errors.New("data before or inside the archive (not written by mail-archive)")
	}
	return int64(dirOffset), nil //nolint:gosec // bounded by size
}

func findEndSignature(b []byte) int {
	const endLen = 22
	for i := len(b) - endLen; i >= 0; i-- {
		if b[i] == 'P' && b[i+1] == 'K' && b[i+2] == 0x05 && b[i+3] == 0x06 {
			n := int(b[i+endLen-2]) | int(b[i+endLen-1])<<8
			if n+endLen+i > len(b) {
				return -1
			}
			return i
		}
	}
	return -1
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func validMonth(s string) bool {
	return len(s) == 2 && allDigits(s) && s >= "01" && s <= "12"
}
