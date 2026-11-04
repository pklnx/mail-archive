package gobd

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
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
		return openZIPEntry(e.zf)
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
	case zf.Method != zip.Deflate:
		// The export's writer uses only Deflate (ZipSink).
		e.problem = fmt.Sprintf("uses compression method %d, not Deflate", zf.Method)
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
//
// archive/zip reads only the central directory, while a streaming
// unpacker reads the local headers. So the prescan also checks each
// record's local entry, in the same walk: the local entries must follow
// each other in the order of the records, from offset 0 up to the
// central directory without a gap (no entry the directory does not
// list), and each local header must have the record's name, flags and
// method, and with its data descriptor the record's CRC-32 and sizes.
// This is the layout archive/zip's writer produces. It costs two small
// reads per entry and no memory per entry.
//
// Together with openZIPEntry this keeps one rule: every byte before the
// central directory is accounted for exactly once, by a local header, a
// data descriptor, or compressed data that the decompressor consumes in
// full. The prescan accounts for the headers and descriptors and the
// extent of each entry's data; openZIPEntry, the only way verify reads an
// entry, requires the Deflate stream to end exactly at the compressed
// size, so nothing can hide behind the end of a stream.
func prescanZIP(r io.ReaderAt, size int64) error {
	dirOffset, err := zipDirectoryOffset(r, size)
	if err != nil {
		return err
	}
	br := bufio.NewReader(io.NewSectionReader(r, dirOffset, size-dirOffset))
	var hdr [46]byte
	var rec [maxZIPRecordData]byte
	buf := new(localBuf) // reused for every entry
	var n, data, pos int64
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			break // the end of the records; archive/zip stops here too
		}
		if binary.LittleEndian.Uint32(hdr[0:]) != 0x02014b50 {
			break
		}
		nameLen := int(binary.LittleEndian.Uint16(hdr[28:]))
		extraLen := int(binary.LittleEndian.Uint16(hdr[30:]))
		v := nameLen + extraLen + int(binary.LittleEndian.Uint16(hdr[32:]))
		n++
		data += int64(v)
		switch {
		case n > maxZIPEntries:
			return fmt.Errorf("more than %d entries", maxZIPEntries)
		case v > maxZIPRecordData:
			return fmt.Errorf("entry %d has a name, extra field and comment of %d bytes", n, v)
		case data > maxZIPDirData:
			return errors.New("the central directory is larger than an export's")
		}
		if _, err := io.ReadFull(br, rec[:v]); err != nil {
			break // a truncated record; archive/zip stops here too
		}
		c, err := centralRecord(hdr[:], rec[:nameLen], rec[nameLen:nameLen+extraLen])
		if err != nil {
			return fmt.Errorf("entry %q: %w", rec[:nameLen], err)
		}
		end, err := checkLocal(r, pos, dirOffset, &c, buf)
		if err != nil {
			return fmt.Errorf("entry %q: %w", rec[:nameLen], err)
		}
		pos = end
	}
	if pos != dirOffset {
		return errors.New("data between the last entry and the central directory (a local entry the directory does not list)")
	}
	return nil
}

// central is what checkLocal compares of a central directory record.
type central struct {
	name                 []byte
	flags, method        uint16
	crc                  uint32
	comp, uncomp, offset uint64
}

func centralRecord(hdr, name, extra []byte) (central, error) {
	c := central{
		name:   name,
		flags:  binary.LittleEndian.Uint16(hdr[8:]),
		method: binary.LittleEndian.Uint16(hdr[10:]),
		crc:    binary.LittleEndian.Uint32(hdr[16:]),
		comp:   uint64(binary.LittleEndian.Uint32(hdr[20:])),
		uncomp: uint64(binary.LittleEndian.Uint32(hdr[24:])),
		offset: uint64(binary.LittleEndian.Uint32(hdr[42:])),
	}
	const max32 = 0xffffffff
	if c.uncomp != max32 && c.comp != max32 && c.offset != max32 {
		return c, nil
	}
	// The zip64 extra field (id 1) holds, in this order, each value whose
	// 32-bit field is 0xffffffff.
	for len(extra) >= 4 {
		id, sz := binary.LittleEndian.Uint16(extra), int(binary.LittleEndian.Uint16(extra[2:]))
		if sz > len(extra)-4 {
			break
		}
		f := extra[4 : 4+sz]
		extra = extra[4+sz:]
		if id != 1 {
			continue
		}
		for _, v := range []*uint64{&c.uncomp, &c.comp, &c.offset} {
			if *v != max32 {
				continue
			}
			if len(f) < 8 {
				return c, errors.New("short zip64 extra field")
			}
			*v, f = binary.LittleEndian.Uint64(f), f[8:]
		}
		return c, nil
	}
	return c, errors.New("zip64 sizes without a zip64 extra field")
}

// localBuf holds what checkLocal reads; one for the whole walk, so the
// walk allocates nothing per entry.
type localBuf struct {
	lh   [30]byte
	name [maxZIPRecordData]byte
	d    [24]byte
}

// checkLocal checks the local entry of c, which must start at pos, and
// returns where it ends.
func checkLocal(r io.ReaderAt, pos, limit int64, c *central, buf *localBuf) (int64, error) {
	if c.offset != uint64(pos) { //nolint:gosec // pos is not negative
		return 0, errors.New("its local header is not where the previous entry ends (data between entries, or entries out of order)")
	}
	lh := buf.lh[:]
	if pos+30 > limit {
		return 0, errors.New("its local header runs into the central directory")
	}
	if _, err := r.ReadAt(lh, pos); err != nil {
		return 0, err
	}
	nameLen := int64(binary.LittleEndian.Uint16(lh[26:]))
	extraLen := int64(binary.LittleEndian.Uint16(lh[28:]))
	switch {
	case binary.LittleEndian.Uint32(lh[0:]) != 0x04034b50:
		return 0, errors.New("no local header at its offset")
	case nameLen != int64(len(c.name)):
		return 0, errors.New("the local header has another name than the central directory")
	case binary.LittleEndian.Uint16(lh[6:]) != c.flags || binary.LittleEndian.Uint16(lh[8:]) != c.method:
		return 0, errors.New("the local header has other flags or another method than the central directory")
	}
	name := buf.name[:nameLen]
	if _, err := r.ReadAt(name, pos+30); err != nil {
		return 0, err
	}
	if !bytes.Equal(name, c.name) {
		return 0, errors.New("the local header has another name than the central directory")
	}
	if c.comp > uint64(limit) { //nolint:gosec // limit is not negative
		return 0, errors.New("its data runs into the central directory")
	}
	end := pos + 30 + nameLen + extraLen + int64(c.comp) //nolint:gosec // bounded by limit above
	if c.flags&0x8 == 0 {
		// Sizes and CRC in the local header itself.
		if binary.LittleEndian.Uint32(lh[14:]) != c.crc || !sameSize(lh[18:], c.comp) || !sameSize(lh[22:], c.uncomp) {
			return 0, errors.New("the local header has another CRC-32 or size than the central directory")
		}
	} else {
		// archive/zip's writer puts zeros in the local header when a data
		// descriptor follows; some unpackers prefer non-zero local sizes.
		if binary.LittleEndian.Uint32(lh[14:]) != 0 || binary.LittleEndian.Uint32(lh[18:]) != 0 || binary.LittleEndian.Uint32(lh[22:]) != 0 {
			return 0, errors.New("the local header has a CRC-32 or sizes although a data descriptor follows")
		}
		// The data descriptor after the data: signature, CRC-32 and the
		// two sizes, 8 bytes each when a size is above 0xffffffff (as
		// archive/zip writes it; a zip64 offset alone keeps 4-byte sizes).
		dlen := int64(16)
		if c.comp > 0xffffffff || c.uncomp > 0xffffffff {
			dlen = 24
		}
		if end+dlen > limit {
			return 0, errors.New("its data descriptor runs into the central directory")
		}
		d := buf.d[:]
		if _, err := r.ReadAt(d[:dlen], end); err != nil {
			return 0, err
		}
		comp, uncomp := uint64(binary.LittleEndian.Uint32(d[8:])), uint64(binary.LittleEndian.Uint32(d[12:]))
		if dlen == 24 {
			comp, uncomp = binary.LittleEndian.Uint64(d[8:]), binary.LittleEndian.Uint64(d[16:])
		}
		if binary.LittleEndian.Uint32(d[0:]) != 0x08074b50 || binary.LittleEndian.Uint32(d[4:]) != c.crc ||
			comp != c.comp || uncomp != c.uncomp {
			return 0, errors.New("its data descriptor differs from the central directory")
		}
		end += dlen
	}
	if end > limit {
		return 0, errors.New("its data runs into the central directory")
	}
	return end, nil
}

// sameSize compares a 32-bit size field of a local header with a size; a
// zip64 size is 0xffffffff there.
func sameSize(b []byte, v uint64) bool {
	f := uint64(binary.LittleEndian.Uint32(b))
	return f == v || (f == 0xffffffff && v >= 0xffffffff)
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

// zipEntryError is a ZIP entry whose data does not hold what its records
// say. Verify reports it as a name finding.
type zipEntryError struct{ detail string }

func (e *zipEntryError) Error() string { return "ZIP entry " + e.detail }

// Pools for openZIPEntry: one bufio reader and one decompressor per open
// entry, reused, so reading 200,000 entries does not allocate 200,000 of
// each.
var (
	bufPool   = sync.Pool{New: func() any { return bufio.NewReaderSize(nil, 32<<10) }}
	flatePool sync.Pool
)

// openZIPEntry reads an entry's raw bytes (zf.OpenRaw, exactly
// CompressedSize64 of them) and decompresses them itself, instead of
// zf.Open, which stops at the end of the Deflate stream and never checks
// that the stream filled the compressed size. At the end of the stream it
// requires that every compressed byte was consumed, and it checks the
// CRC-32 and the uncompressed size itself (OpenRaw skips both).
func openZIPEntry(zf *zip.File) (io.ReadCloser, error) {
	if zf.Method != zip.Deflate {
		return nil, &zipEntryError{fmt.Sprintf("uses compression method %d, not Deflate", zf.Method)}
	}
	raw, err := zf.OpenRaw()
	if err != nil {
		return nil, err
	}
	br := bufPool.Get().(*bufio.Reader)
	br.Reset(raw)
	cr := &countingReader{r: br}
	var fr io.ReadCloser
	if v := flatePool.Get(); v != nil {
		fr = v.(io.ReadCloser)
		if err := fr.(flate.Resetter).Reset(cr, nil); err != nil {
			return nil, err
		}
	} else {
		fr = flate.NewReader(cr)
	}
	return &zipEntryReader{zf: zf, br: br, cr: cr, fr: fr, crc: crc32.NewIEEE()}, nil
}

// countingReader counts the bytes the decompressor consumes. It is an
// io.ByteReader, so flate reads from it byte by byte and never ahead.
type countingReader struct {
	r *bufio.Reader
	n uint64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += uint64(n) //nolint:gosec // n is not negative
	return n, err
}

func (c *countingReader) ReadByte() (byte, error) {
	b, err := c.r.ReadByte()
	if err == nil {
		c.n++
	}
	return b, err
}

type zipEntryReader struct {
	zf   *zip.File
	br   *bufio.Reader
	cr   *countingReader
	fr   io.ReadCloser
	crc  hash.Hash32
	n    uint64
	done error
}

func (z *zipEntryReader) Read(p []byte) (int, error) {
	if z.done != nil {
		return 0, z.done
	}
	n, err := z.fr.Read(p)
	_, _ = z.crc.Write(p[:n])
	z.n += uint64(n) //nolint:gosec // n is not negative
	switch {
	case z.n > z.zf.UncompressedSize64:
		z.done = &zipEntryError{"decompresses to more bytes than its records say"}
	case errors.Is(err, io.EOF):
		switch {
		case z.cr.n != z.zf.CompressedSize64:
			z.done = &zipEntryError{fmt.Sprintf("has %d bytes after the end of its Deflate stream (data hidden inside the entry)",
				z.zf.CompressedSize64-z.cr.n)}
		case z.n != z.zf.UncompressedSize64:
			z.done = &zipEntryError{"decompresses to fewer bytes than its records say"}
		case z.crc.Sum32() != z.zf.CRC32:
			z.done = &zipEntryError{"does not match its CRC-32"}
		default:
			z.done = io.EOF
		}
	case err != nil:
		z.done = &zipEntryError{"has damaged Deflate data: " + err.Error()}
	}
	return n, z.done
}

func (z *zipEntryReader) Close() error {
	if z.fr == nil {
		return nil
	}
	_ = z.fr.Close()
	flatePool.Put(z.fr)
	z.br.Reset(nil)
	bufPool.Put(z.br)
	z.fr, z.br = nil, nil
	return nil
}
