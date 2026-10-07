package mailbox

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
)

// MboxScanner reads the messages of an mbox file one at a time, for files
// written by any mail program. Each message is streamed, so memory stays
// at about one line buffer however large a message is.
//
// A line separates messages if it starts with "From ", comes at the start
// of the file or after an empty line, and contains a time (hh:mm) and a
// four-digit year. That rejects most "From " lines in bodies that the
// writing program did not escape. The empty line before a separator is not
// part of the message. Lines starting with ">From ", ">>From " and so on
// lose one '>' (mboxrd; for mboxo this differs only for lines that began
// with ">From " originally).
type MboxScanner struct {
	r       *bufio.Reader
	offset  int64 // bytes consumed from r
	next    string
	nextOff int64
	cur     *scannedMessage
	err     error
	started bool
}

// ScannedMessage is one message of an mbox file. Read it before calling
// Next again; Next skips what is left of it.
type ScannedMessage struct {
	From   string // the separator line without its line ending
	Offset int64  // byte offset of the separator line in the file
	Body   io.Reader
}

// ErrCompressed is returned for gzip and zip files.
var ErrCompressed = errors.New("the file is compressed (gzip or zip); unpack it first")

// NewMboxScanner returns a scanner for the mbox file in r.
func NewMboxScanner(r io.Reader) *MboxScanner {
	return &MboxScanner{r: bufio.NewReaderSize(r, lineBuffer)}
}

var separatorDate = regexp.MustCompile(`\d{1,2}:\d{2}.*\d{4}`)

// isSeparator reports whether a complete line looks like a From_ line.
func isSeparator(line []byte) bool {
	return bytes.HasPrefix(line, []byte("From ")) && separatorDate.Match(line)
}

func isBlank(line []byte) bool {
	return len(line) == 1 && line[0] == '\n' || len(line) == 2 && line[0] == '\r' && line[1] == '\n'
}

// Next returns the next message, or io.EOF after the last one. A file that
// does not start with a From_ line (after empty lines) gives ErrNotMbox.
func (s *MboxScanner) Next() (*ScannedMessage, error) {
	if s.cur != nil {
		if _, err := io.Copy(io.Discard, s.cur); err != nil {
			s.err = err
		}
		s.cur = nil
	}
	if s.err != nil {
		return nil, s.err
	}
	if !s.started {
		s.started = true
		if err := s.findFirst(); err != nil {
			s.err = err
			return nil, err
		}
	}
	if s.next == "" {
		s.err = io.EOF
		return nil, io.EOF
	}
	s.cur = &scannedMessage{s: s, atLineStart: true}
	msg := &ScannedMessage{From: s.next, Offset: s.nextOff, Body: s.cur}
	s.next = ""
	return msg, nil
}

// findFirst skips leading empty lines and reads the first separator.
func (s *MboxScanner) findFirst() error {
	if head, _ := s.r.Peek(4); bytes.HasPrefix(head, []byte{0x1f, 0x8b}) || bytes.HasPrefix(head, []byte("PK\x03\x04")) {
		return ErrCompressed
	}
	for {
		off := s.offset
		line, complete, err := s.readChunk()
		if len(line) == 0 && errors.Is(err, io.EOF) {
			return nil // an empty file has no messages
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if complete && isBlank(line) {
			continue
		}
		if !complete || !bytes.HasPrefix(line, []byte("From ")) {
			return ErrNotMbox
		}
		s.next, s.nextOff = trimEOL(line), off
		return nil
	}
}

// readChunk reads up to one line. complete is false for the first pieces of
// a line longer than the buffer. The returned slice is only valid until the
// next read.
func (s *MboxScanner) readChunk() (chunk []byte, complete bool, err error) {
	chunk, err = s.r.ReadSlice('\n')
	s.offset += int64(len(chunk))
	if errors.Is(err, bufio.ErrBufferFull) {
		return chunk, false, nil
	}
	return chunk, true, err
}

func trimEOL(line []byte) string {
	return string(bytes.TrimRight(line, "\r\n"))
}

// scannedMessage streams one message's lines out of the scanner.
type scannedMessage struct {
	s           *MboxScanner
	out         []byte // bytes ready for Read, from pos on
	pos         int
	pending     []byte // an empty line that may precede a separator
	atLineStart bool
	done        bool
}

func (m *scannedMessage) Read(p []byte) (int, error) {
	for m.pos == len(m.out) {
		if m.done {
			return 0, io.EOF
		}
		if err := m.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, m.out[m.pos:])
	m.pos += n
	return n, nil
}

// fill reads one chunk and decides what of it belongs to the message.
func (m *scannedMessage) fill() error {
	s := m.s
	off := s.offset
	chunk, complete, err := s.readChunk()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	m.out, m.pos = m.out[:0], 0
	if len(chunk) > 0 {
		switch {
		case !m.atLineStart:
			m.out = append(m.out, chunk...)
		case complete && isBlank(chunk):
			m.out = append(m.out, m.pending...)
			m.pending = append(m.pending[:0], chunk...)
		case complete && m.pending != nil && isSeparator(chunk):
			s.next, s.nextOff = trimEOL(chunk), off
			m.pending, m.done = nil, true
			return nil
		default:
			m.out = append(m.out, m.pending...)
			m.pending = nil
			if chunk[0] == '>' && isFromLine(chunk) {
				chunk = chunk[1:]
			}
			m.out = append(m.out, chunk...)
		}
		m.atLineStart = complete && chunk[len(chunk)-1] == '\n'
	}
	if errors.Is(err, io.EOF) {
		// The empty line before the end of the file is the separator
		// that mbox writers add after the last message.
		m.pending, m.done = nil, true
	}
	return nil
}

// ParseFromDate reads the date of a From_ line ("From sender Sat Mar  7
// 08:05:01 2026", optionally with a zone). Without a zone it is UTC. It
// returns the zero time if the line has no date it understands.
func ParseFromDate(line string) time.Time {
	fields := strings.Fields(strings.TrimPrefix(line, "From "))
	if len(fields) < 2 {
		return time.Time{}
	}
	rest := strings.Join(fields[1:], " ")
	for _, layout := range []string{
		"Mon Jan 2 15:04:05 2006",
		"Mon Jan 2 15:04:05 -0700 2006",
		"Mon Jan 2 15:04:05 MST 2006",
		"Mon Jan 2 15:04:05 2006 -0700",
		"Mon Jan 2 15:04 2006",
	} {
		if t, err := time.Parse(layout, rest); err == nil {
			return t
		}
	}
	return time.Time{}
}

// StatusFlags returns IMAP flags for the Status and X-Status headers that
// mail programs write into mbox files: R \Seen; A \Answered, F \Flagged,
// T \Draft, D \Deleted.
func StatusFlags(status, xStatus string) []string {
	var out []string
	if strings.ContainsRune(status, 'R') {
		out = append(out, `\Seen`)
	}
	for _, f := range []struct {
		c    rune
		flag string
	}{{'A', `\Answered`}, {'F', `\Flagged`}, {'T', `\Draft`}, {'D', `\Deleted`}} {
		if strings.ContainsRune(xStatus, f.c) {
			out = append(out, f.flag)
		}
	}
	return out
}

// CRLFReader stores messages the way IMAP delivers them: if the first line
// ends in CRLF, the bytes stay as they are; otherwise every LF that does not
// follow a CR becomes CRLF. Nothing else changes.
type CRLFReader struct {
	r       *bufio.Reader
	decided bool
	convert bool
	prevCR  bool
	buf     []byte
	out     []byte
	pos     int
	err     error
}

// NewCRLFReader wraps r.
func NewCRLFReader(r io.Reader) *CRLFReader {
	return &CRLFReader{r: bufio.NewReaderSize(r, lineBuffer)}
}

func (c *CRLFReader) Read(p []byte) (int, error) {
	for c.pos == len(c.out) {
		if c.err != nil {
			return 0, c.err
		}
		c.fill()
	}
	n := copy(p, c.out[c.pos:])
	c.pos += n
	return n, nil
}

func (c *CRLFReader) fill() {
	var chunk []byte
	if !c.decided {
		c.decided = true
		line, err := c.r.ReadSlice('\n')
		c.convert = !bytes.HasSuffix(line, []byte("\r\n"))
		chunk = line
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			c.err = err
		}
	} else {
		if c.buf == nil {
			c.buf = make([]byte, lineBuffer)
		}
		n, err := c.r.Read(c.buf)
		chunk = c.buf[:n]
		c.err = err
	}
	c.out, c.pos = c.out[:0], 0
	if !c.convert {
		c.out = append(c.out, chunk...)
		return
	}
	for _, b := range chunk {
		if b == '\n' && !c.prevCR {
			c.out = append(c.out, '\r')
		}
		c.out = append(c.out, b)
		c.prevCR = b == '\r'
	}
}
