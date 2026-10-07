// Package mailbox writes and reads the mailbox formats that mail clients
// import: mboxrd files and Maildir directories.
package mailbox

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"time"
)

// lineBuffer bounds the memory used per line; longer lines are copied in
// pieces.
const lineBuffer = 64 << 10

// MboxWriter writes messages to one mbox file in the mboxrd variant: lines
// that start with any number of '>' followed by "From " get one more '>',
// which a reader removes again.
//
// The bytes of a message are kept as they are, CRLF included. Each message
// is followed by a blank line in its own line ending; a missing final
// newline is added, which is the only change a round trip does not undo.
type MboxWriter struct {
	w   *bufio.Writer
	buf *bufio.Reader
}

// NewMboxWriter returns a writer that appends messages to w.
func NewMboxWriter(w io.Writer) *MboxWriter {
	return &MboxWriter{w: bufio.NewWriterSize(w, lineBuffer), buf: bufio.NewReaderSize(nil, lineBuffer)}
}

// WriteMessage writes one message with a "From MAILER-DAEMON <date>"
// separator; date is shown in UTC. Everything is flushed to the underlying
// writer before it returns, also after an error from r, so the caller can
// cut the file back to where the message started.
func (m *MboxWriter) WriteMessage(r io.Reader, date time.Time) error {
	err := m.write(r, date)
	if ferr := m.w.Flush(); err == nil {
		err = ferr
	}
	return err
}

func (m *MboxWriter) write(r io.Reader, date time.Time) error {
	if _, err := m.w.WriteString("From MAILER-DAEMON " + date.UTC().Format(time.ANSIC) + "\n"); err != nil {
		return err
	}
	m.buf.Reset(r)
	atLineStart := true
	var tail [2]byte // the last two bytes written, to see the line ending
	n := 0
	for {
		chunk, err := m.buf.ReadSlice('\n')
		if len(chunk) > 0 {
			if atLineStart && isFromLine(chunk) {
				if err := m.w.WriteByte('>'); err != nil {
					return err
				}
			}
			if _, err := m.w.Write(chunk); err != nil {
				return err
			}
			atLineStart = chunk[len(chunk)-1] == '\n'
			n += len(chunk)
			if len(chunk) >= 2 {
				tail = [2]byte{chunk[len(chunk)-2], chunk[len(chunk)-1]}
			} else {
				tail = [2]byte{tail[1], chunk[0]}
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	end := "\n\n"
	switch {
	case n > 0 && !atLineStart:
		// No final newline: add one, then the blank line.
	case n >= 2 && tail == [2]byte{'\r', '\n'}:
		end = "\r\n"
	case n > 0:
		end = "\n"
	}
	_, err := m.w.WriteString(end)
	return err
}

// isFromLine reports whether a line starts with ">*From ". A line that
// starts with more '>' than fit into the line buffer is not recognized.
func isFromLine(line []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(line, ">"), []byte("From "))
}

// MboxMessage is one message read from an mbox file.
type MboxMessage struct {
	From string // the separator line without its line ending
	Data []byte
}

// MboxReader reads the messages of an mboxrd file, as written by
// MboxWriter. Every line that starts with "From " separates messages.
type MboxReader struct {
	r    *bufio.Reader
	from string // the separator of the next message
	err  error
}

// NewMboxReader returns a reader for the mbox file in r.
func NewMboxReader(r io.Reader) *MboxReader {
	return &MboxReader{r: bufio.NewReaderSize(r, lineBuffer)}
}

// ErrNotMbox is returned when the file does not start with a "From " line.
var ErrNotMbox = errors.New("not an mbox file: no From line at the start")

// Next returns the next message, or io.EOF after the last one. The whole
// message is held in memory.
func (m *MboxReader) Next() (*MboxMessage, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.from == "" {
		line, err := m.readLine()
		if errors.Is(err, io.EOF) && len(line) == 0 {
			m.err = io.EOF
			return nil, m.err
		}
		if err != nil && !errors.Is(err, io.EOF) {
			m.err = err
			return nil, err
		}
		if !bytes.HasPrefix(line, []byte("From ")) {
			m.err = ErrNotMbox
			return nil, m.err
		}
		m.from = string(bytes.TrimRight(line, "\r\n"))
	}
	msg := &MboxMessage{From: m.from}
	var data bytes.Buffer
	for {
		line, err := m.readLine()
		if len(line) > 0 {
			if bytes.HasPrefix(line, []byte("From ")) {
				m.from = string(bytes.TrimRight(line, "\r\n"))
				break
			}
			if line[0] == '>' && isFromLine(line) {
				line = line[1:]
			}
			data.Write(line)
		}
		if errors.Is(err, io.EOF) {
			m.err = io.EOF
			break
		}
		if err != nil {
			m.err = err
			return nil, err
		}
	}
	msg.Data = trimSeparator(data.Bytes())
	return msg, nil
}

// readLine reads one whole line including its ending.
func (m *MboxReader) readLine() ([]byte, error) {
	var long []byte
	for {
		chunk, err := m.r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long = append(long, chunk...)
			continue
		}
		if long != nil {
			return append(long, chunk...), err
		}
		return append([]byte(nil), chunk...), err
	}
}

// trimSeparator removes the blank line that ends each message: "\r\n" after
// a CRLF line, else "\n".
func trimSeparator(b []byte) []byte {
	switch {
	case bytes.HasSuffix(b, []byte("\r\n\r\n")):
		return b[:len(b)-2]
	case bytes.HasSuffix(b, []byte("\n\n")):
		return b[:len(b)-1]
	}
	return b
}
