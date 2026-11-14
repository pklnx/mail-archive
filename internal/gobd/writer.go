package gobd

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/keepia/archive/internal/attest"
)

// MaxZIPMessages is the most messages a ZIP export holds: archive/zip
// keeps about 300 bytes per entry in memory until the end, so 200,000
// entries stay within the export's 64 MiB budget.
const MaxZIPMessages = 200_000

// ErrMismatch is wrapped by AddFile when a message file's bytes do not
// match its SHA-256 or size; the caller lists the message as missing.
var ErrMismatch = errors.New("the file does not match its SHA-256 or size")

// Message is a message of the export: its file and its messages.csv row.
type Message struct {
	SHA256 string
	Size   int64
	// SortAt is the message's date (Date header, else arrival); it names
	// the file's month folder and fills date, time and date_utc.
	SortAt                                      time.Time
	From, To, Cc, Subject, MessageID, InReplyTo string
	HasAttachment                               bool
	AttachmentNames                             []string
	ArchivedAt                                  time.Time
	ArchiveSeq                                  int64
	Attestation                                 int64 // seq of the covering attestation; 0 for none
}

// Location is a locations.csv row: one selected place of a message.
type Location struct {
	SHA256, Owner, Account, AccountKind, Folder string
	UIDValidity, UID                            int64
	Flags                                       []string
	InternalDate                                *time.Time
	FirstSeen, LastSeen                         time.Time
	GoneAt                                      *time.Time
	EnvelopeFrom                                string
	EnvelopeTo                                  []string
	ReceivedAt, RetainUntil                     *time.Time
	Holds                                       []int64
}

// Missing is a missing.csv row: a selected message without a valid file.
type Missing struct {
	SHA256 string
	SortAt time.Time
	Reason string
}

// Options are what the caller knows before the first file.
type Options struct {
	// Statement holds every field the caller sets: ID, Created, Software,
	// Selection, TimeZone, Group, Owners, Resumed, FullChain, Leaves and
	// the attestation's seq and hash. The writer fills in the counts,
	// Complete, the SHA256SUMS digest and the signature.
	Statement attest.Statement
	// Location is the export time zone (TimeZone loaded).
	Location *time.Location
	// Signer signs the statement; nil writes it unsigned ("sig none").
	Signer ed25519.PrivateKey
	// MaxMessages, when above 0, bounds the number of message files
	// (MaxZIPMessages for a ZIP).
	MaxMessages int64
}

// Final is what Finish writes besides the tables.
type Final struct {
	// Chain is attest/attestations.jsonl: the attestation the statement
	// names, or with FullChain the whole chain up to it. Empty without an
	// attestation.
	Chain []attest.Entry
	// PublicKey and RetiredKeys are attest/signing.pub and
	// attest/retired-keys.pem; nil leaves the file out.
	PublicKey, RetiredKeys []byte
	// Leaves calls add for each leaves file to include, in increasing seq;
	// only with Statement.Leaves.
	Leaves func(add func(seq int64, r io.Reader) error) error
	// Files calls yield with the path of every message file again, in the
	// order of AddFile. The writer keeps no list per message; it checks
	// the second listing against a digest of the first.
	Files func(yield func(path string) error) error
}

// Result is a finished export.
type Result struct {
	Statement attest.Statement
	// SumsSHA256 is the digest of SHA256SUMS (the statement holds it too);
	// StatementSHA256 the digest of STATEMENT.txt.
	SumsSHA256, StatementSHA256 string
}

type phase int

const (
	phaseFiles phase = iota
	phaseMessages
	phaseLocations
	phaseDone
)

// Writer writes an export in the pass order of format v1: the message
// files (AddFile), messages.csv (AddMessageRow), locations.csv
// (AddLocationRow), then Finish: missing.csv, attest/, README.txt,
// index.xml, SHA256SUMS and STATEMENT.txt. Memory does not grow with the
// number of messages, except for the missing list (at most MaxMissing).
type Writer struct {
	sink  Sink
	opts  Options
	st    attest.Statement
	phase phase
	err   error

	missing    []Missing
	missingSet map[string]bool // built at the first location row
	owners     map[string]bool

	files     int64
	bytes     int64
	filesHash hash.Hash // over the paths of AddFile, in order
	rows      int64
	rowsHash  hash.Hash // over the paths of the messages.csv rows
	lastSeq   int64
	locations int64
	sums      []fileSum // every file but the message files, in order
	table     *tableFile
}

type fileSum struct{ path, sum string }

// NewWriter starts an export into sink.
func NewWriter(sink Sink, opts Options) (*Writer, error) {
	st := opts.Statement
	if opts.Location == nil {
		return nil, errors.New("gobd: no time zone")
	}
	if !slices.IsSorted(st.Owners) || len(slices.Compact(slices.Clone(st.Owners))) != len(st.Owners) {
		return nil, errors.New("gobd: owners must be sorted and unique")
	}
	w := &Writer{sink: sink, opts: opts, st: st, owners: map[string]bool{}, filesHash: sha256.New(), rowsHash: sha256.New()}
	for _, o := range st.Owners {
		w.owners[o] = true
	}
	return w, nil
}

// AddFile writes the .eml file of m from body (pass B). It checks the
// bytes against m.SHA256 and m.Size while copying. On a mismatch it takes
// the file back and returns an error wrapping ErrMismatch (on a read error
// of body, that error); the caller then passes the message to AddMissing
// and goes on. A ZIP cannot take a file back, so there the error also
// wraps ErrCannotUndo and the export is broken: every later call fails.
func (w *Writer) AddFile(m Message, body io.Reader) error {
	if err := w.check(phaseFiles); err != nil {
		return err
	}
	if !isSHA(m.SHA256) || m.Size < 0 {
		return w.fail(fmt.Errorf("gobd: bad message %q", m.SHA256))
	}
	if w.opts.MaxMessages > 0 && w.files >= w.opts.MaxMessages {
		return w.fail(fmt.Errorf("gobd: more than %d messages", w.opts.MaxMessages))
	}
	path := MessagePath(m.SHA256, m.SortAt, w.opts.Location)
	f, err := w.sink.Create(path)
	if err != nil {
		return w.fail(err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, m.Size+1))
	if err != nil {
		// A read error of one message file: like a mismatch, the caller
		// lists the message as missing, if the file can be taken back.
		readErr := fmt.Errorf("%s: %w", path, err)
		if aerr := f.Abort(); aerr != nil {
			return w.fail(errors.Join(readErr, aerr))
		}
		return readErr
	}
	if n != m.Size || hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		mismatch := fmt.Errorf("%s: %w", path, ErrMismatch)
		if err := f.Abort(); err != nil {
			return w.fail(errors.Join(mismatch, err))
		}
		return mismatch
	}
	if err := f.Commit(); err != nil {
		return w.fail(err)
	}
	w.files++
	w.bytes += m.Size
	_, _ = io.WriteString(w.filesHash, path+"\n")
	return nil
}

// Resumable is a Sink that can open a file it already holds: the
// DirSink of a resumed export.
type Resumable interface {
	Existing(path string) (io.ReadCloser, error)
}

// KeepFile takes the message file of m that the sink already holds from
// an interrupted run (pass B of a resumed export), if its bytes match
// m.SHA256 and m.Size, and counts it as AddFile would. It returns false and
// changes nothing when the file is missing, different or cannot be read:
// the caller then writes it with AddFile, which replaces it. The archive's
// copy of the message is not read for a kept file.
func (w *Writer) KeepFile(m Message) (bool, error) {
	if err := w.check(phaseFiles); err != nil {
		return false, err
	}
	rs, ok := w.sink.(Resumable)
	if !ok {
		return false, nil
	}
	if !isSHA(m.SHA256) || m.Size < 0 {
		return false, w.fail(fmt.Errorf("gobd: bad message %q", m.SHA256))
	}
	if w.opts.MaxMessages > 0 && w.files >= w.opts.MaxMessages {
		return false, w.fail(fmt.Errorf("gobd: more than %d messages", w.opts.MaxMessages))
	}
	path := MessagePath(m.SHA256, m.SortAt, w.opts.Location)
	f, err := rs.Existing(path)
	if err != nil {
		return false, nil
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, m.Size+1))
	if err != nil || n != m.Size || hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return false, nil
	}
	w.files++
	w.bytes += m.Size
	_, _ = io.WriteString(w.filesHash, path+"\n")
	return true, nil
}

// AddMissing lists a message of the selection without a valid file. It
// may be called until the first location row: a missing message appears
// only in missing.csv, not in messages.csv or locations.csv.
func (w *Writer) AddMissing(m Missing) error {
	if err := w.check(phaseMessages); err != nil {
		return err
	}
	if !isSHA(m.SHA256) || m.Reason == "" {
		return w.fail(fmt.Errorf("gobd: bad missing message %q", m.SHA256))
	}
	if len(w.missing) >= MaxMissing {
		return w.fail(fmt.Errorf("gobd: more than %d messages are missing; run verify", MaxMissing))
	}
	w.missing = append(w.missing, m)
	return nil
}

// AddMessageRow writes the messages.csv row of a message whose file was
// written (pass A1), in the order of AddFile.
func (w *Writer) AddMessageRow(m Message) error {
	if err := w.advance(phaseMessages); err != nil {
		return err
	}
	loc := w.opts.Location
	switch {
	case m.ArchiveSeq <= w.lastSeq:
		return w.fail(fmt.Errorf("gobd: archive_seq %d out of order", m.ArchiveSeq))
	case m.Attestation < 0 || m.Attestation > w.st.AttestationSeq:
		return w.fail(fmt.Errorf("gobd: message %s names attestation %d, the export has %d", m.SHA256, m.Attestation, w.st.AttestationSeq))
	}
	w.lastSeq = m.ArchiveSeq
	path := MessagePath(m.SHA256, m.SortAt, loc)
	w.rows++
	_, _ = io.WriteString(w.rowsHash, path+"\n")
	var r row
	r.text(m.SHA256).text(path).int(m.Size).
		date(formatDate(m.SortAt, loc)).text(formatClock(m.SortAt, loc)).text(formatUTC(m.SortAt)).
		text(m.From).text(m.To).text(m.Cc).text(m.Subject).text(m.MessageID).text(m.InReplyTo).
		int(btoi(m.HasAttachment)).text(strings.Join(m.AttachmentNames, "|")).
		text(formatUTC(m.ArchivedAt)).int(m.ArchiveSeq)
	if m.Attestation > 0 {
		r.int(m.Attestation)
	} else {
		r.empty()
	}
	return w.writeRow(r)
}

// AddLocationRow writes a locations.csv row (pass A2). Every owner must be
// one of the statement's owners.
func (w *Writer) AddLocationRow(l Location) error {
	if err := w.advance(phaseLocations); err != nil {
		return err
	}
	if !isSHA(l.SHA256) {
		return w.fail(fmt.Errorf("gobd: bad location %q", l.SHA256))
	}
	if w.missingSet == nil {
		w.missingSet = make(map[string]bool, len(w.missing))
		for _, m := range w.missing {
			w.missingSet[m.SHA256] = true
		}
	}
	if w.missingSet[l.SHA256] {
		return w.fail(fmt.Errorf("gobd: a location of the missing message %s (missing messages appear only in missing.csv)", l.SHA256))
	}
	if l.Owner != "" && !w.owners[l.Owner] {
		return w.fail(fmt.Errorf("gobd: location owner %q is not one of the statement's owners", l.Owner))
	}
	if l.Owner == "" && w.st.Group != nil {
		return w.fail(errors.New("gobd: a location without owner in an export of a customer group"))
	}
	holds := make([]string, len(l.Holds))
	for i, h := range l.Holds {
		holds[i] = strconv.FormatInt(h, 10)
	}
	var r row
	r.text(l.SHA256).text(l.Owner).text(l.Account).text(l.AccountKind).text(l.Folder).
		int(l.UIDValidity).int(l.UID).text(strings.Join(l.Flags, " ")).text(formatOptUTC(l.InternalDate)).
		text(formatUTC(l.FirstSeen)).text(formatUTC(l.LastSeen)).text(formatOptUTC(l.GoneAt)).
		text(l.EnvelopeFrom).text(strings.Join(l.EnvelopeTo, " ")).text(formatOptUTC(l.ReceivedAt)).
		text(formatOptUTC(l.RetainUntil)).text(strings.Join(holds, " "))
	w.locations++
	return w.writeRow(r)
}

// Finish writes the rest of the export (pass C), closes the sink and
// returns the signed statement.
func (w *Writer) Finish(f Final) (*Result, error) {
	if err := w.advance(phaseDone); err != nil {
		return nil, err
	}
	st := &w.st
	if err := w.checkChain(f.Chain); err != nil {
		return nil, w.fail(err)
	}
	if err := w.writeMissing(); err != nil {
		return nil, w.fail(err)
	}
	if len(f.Chain) > 0 {
		if err := w.writeFile(ChainFile, func(out io.Writer) error {
			for i := range f.Chain {
				b, err := attest.MarshalLine(&f.Chain[i])
				if err != nil {
					return err
				}
				if _, err := out.Write(b); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, w.fail(err)
		}
	}
	for _, kf := range []struct {
		path string
		data []byte
	}{{PublicKeyFile, f.PublicKey}, {RetiredFile, f.RetiredKeys}} {
		if kf.data != nil {
			if err := w.writeBytes(kf.path, kf.data); err != nil {
				return nil, w.fail(err)
			}
		}
	}
	if st.Leaves {
		if f.Leaves == nil {
			return nil, w.fail(errors.New("gobd: the statement says leaves, but none were given"))
		}
		last := int64(0)
		err := f.Leaves(func(seq int64, r io.Reader) error {
			if seq <= last || seq > st.AttestationSeq {
				return fmt.Errorf("gobd: leaves file %d out of order", seq)
			}
			last = seq
			return w.writeFile(LeavesPath(seq), func(out io.Writer) error {
				_, err := io.Copy(out, r)
				return err
			})
		})
		if err != nil {
			return nil, w.fail(err)
		}
	}

	st.Messages, st.Locations, st.Missing, st.Bytes = w.files, w.locations, int64(len(w.missing)), w.bytes
	st.Complete = len(w.missing) == 0
	if err := w.writeBytes(ReadmeFile, []byte(Readme(st, w.opts.Signer != nil))); err != nil {
		return nil, w.fail(err)
	}
	if err := w.writeBytes(IndexFile, IndexXML("mail-archive "+st.Software)); err != nil {
		return nil, w.fail(err)
	}
	// Every file SHA256SUMS lists is on disk before it is written.
	if sy, ok := w.sink.(interface{ Sync() error }); ok {
		if err := sy.Sync(); err != nil {
			return nil, w.fail(err)
		}
	}
	sums, err := w.writeSums(f.Files)
	if err != nil {
		return nil, w.fail(err)
	}
	st.SHA256SUMS = sums
	if err := st.Sign(w.opts.Signer); err != nil {
		return nil, w.fail(err)
	}
	file := st.File()
	if err := w.writeBytes(StatementFile, file); err != nil {
		return nil, w.fail(err)
	}
	if err := w.sink.Close(); err != nil {
		return nil, w.fail(err)
	}
	stSum := sha256.Sum256(file)
	return &Result{Statement: *st, SumsSHA256: sums, StatementSHA256: hex.EncodeToString(stSum[:])}, nil
}

func (w *Writer) checkChain(chain []attest.Entry) error {
	st := &w.st
	if st.AttestationSeq == 0 {
		if len(chain) > 0 || st.FullChain {
			return errors.New("gobd: a chain without an attestation in the statement")
		}
		return nil
	}
	want := int64(1)
	if st.FullChain {
		want = st.AttestationSeq
	}
	if int64(len(chain)) != want {
		return fmt.Errorf("gobd: the chain has %d entries, the statement needs %d", len(chain), want)
	}
	last := chain[len(chain)-1]
	if last.Seq != st.AttestationSeq || last.Hash != st.AttestationHash {
		return errors.New("gobd: the chain does not end with the statement's attestation")
	}
	return nil
}

// writeSums writes SHA256SUMS: the message files (listed again by files,
// checked against the digest of AddFile), then every other file.
func (w *Writer) writeSums(files func(yield func(string) error) error) (string, error) {
	var sum string
	err := w.writeFile(SumsFile, func(out io.Writer) error {
		h := sha256.New()
		bw := bufio.NewWriter(io.MultiWriter(out, h))
		listed := sha256.New()
		var n int64
		if files != nil {
			err := files(func(path string) error {
				sha, ok := strings.CutSuffix(path[strings.LastIndexByte(path, '/')+1:], ".eml")
				if !ok || !isSHA(sha) || !strings.HasPrefix(path, MailPrefix) {
					return fmt.Errorf("gobd: %q is not a message path", path)
				}
				n++
				_, _ = io.WriteString(listed, path+"\n")
				_, err := bw.WriteString(sumsLine(sha, path))
				return err
			})
			if err != nil {
				return err
			}
		}
		if n != w.files || string(listed.Sum(nil)) != string(w.filesHash.Sum(nil)) {
			return errors.New("gobd: the message files listed for SHA256SUMS differ from the ones written")
		}
		for _, s := range w.sums {
			if _, err := bw.WriteString(sumsLine(s.sum, s.path)); err != nil {
				return err
			}
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		sum = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	return sum, err
}

func (w *Writer) writeMissing() error {
	return w.writeFile(MissingFile, func(out io.Writer) error {
		bw := bufio.NewWriter(out)
		_, _ = bw.WriteString(header(missingColumns))
		for _, m := range w.missing {
			var r row
			r.text(m.SHA256).text(MessagePath(m.SHA256, m.SortAt, w.opts.Location)).text(m.Reason)
			_, _ = bw.WriteString(r.String())
		}
		return bw.Flush()
	})
}

// writeFile writes one file through write and records its digest for
// SHA256SUMS (except SHA256SUMS and STATEMENT.txt themselves).
func (w *Writer) writeFile(path string, write func(io.Writer) error) error {
	f, err := w.sink.Create(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	if err := write(io.MultiWriter(f, h)); err != nil {
		_ = f.Abort()
		return err
	}
	if err := f.Commit(); err != nil {
		return err
	}
	if path != SumsFile && path != StatementFile {
		w.sums = append(w.sums, fileSum{path, hex.EncodeToString(h.Sum(nil))})
	}
	return nil
}

func (w *Writer) writeBytes(path string, data []byte) error {
	return w.writeFile(path, func(out io.Writer) error {
		_, err := out.Write(data)
		return err
	})
}

// advance moves to phase p, closing the tables of the phases in between.
func (w *Writer) advance(p phase) error {
	if w.err != nil {
		return w.err
	}
	if p < w.phase {
		return w.fail(errors.New("gobd: calls out of order"))
	}
	for w.phase < p {
		if err := w.closeTable(); err != nil {
			return w.fail(err)
		}
		w.phase++
		var err error
		switch w.phase {
		case phaseMessages:
			err = w.openTable(MessagesFile, messageColumns)
		case phaseLocations:
			if w.rows != w.files || string(w.rowsHash.Sum(nil)) != string(w.filesHash.Sum(nil)) {
				err = fmt.Errorf("gobd: messages.csv has %d rows that differ from the %d files written", w.rows, w.files)
			} else {
				err = w.openTable(LocationsFile, locationColumns)
			}
		}
		if err != nil {
			return w.fail(err)
		}
	}
	return nil
}

func (w *Writer) check(max phase) error {
	if w.err != nil {
		return w.err
	}
	if w.phase > max {
		return w.fail(errors.New("gobd: calls out of order"))
	}
	return nil
}

// Err returns the error that broke the export, nil while it can go on.
// AddFile's errors for one bad message file leave it nil.
func (w *Writer) Err() error { return w.err }

func (w *Writer) fail(err error) error {
	if w.err == nil {
		w.err = err
	}
	return err
}

// tableFile is the CSV file being written.
type tableFile struct {
	path string
	f    SinkFile
	bw   *bufio.Writer
	h    hash.Hash
}

func (w *Writer) openTable(path string, cols []column) error {
	f, err := w.sink.Create(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	w.table = &tableFile{path: path, f: f, h: h, bw: bufio.NewWriterSize(io.MultiWriter(f, h), 64<<10)}
	_, err = w.table.bw.WriteString(header(cols))
	return err
}

func (w *Writer) writeRow(r row) error {
	if _, err := w.table.bw.WriteString(r.String()); err != nil {
		return w.fail(err)
	}
	return nil
}

func (w *Writer) closeTable() error {
	t := w.table
	if t == nil {
		return nil
	}
	w.table = nil
	if err := t.bw.Flush(); err != nil {
		return err
	}
	if err := t.f.Commit(); err != nil {
		return err
	}
	w.sums = append(w.sums, fileSum{t.path, hex.EncodeToString(t.h.Sum(nil))})
	return nil
}

// row builds one CSV record.
type row struct {
	b strings.Builder
	n int
}

func (r *row) sep() {
	if r.n > 0 {
		r.b.WriteByte(';')
	}
	r.n++
}

func (r *row) text(s string) *row {
	r.sep()
	r.b.WriteString(`"` + textValue(s) + `"`)
	return r
}

func (r *row) int(n int64) *row {
	r.sep()
	r.b.WriteString(strconv.FormatInt(n, 10))
	return r
}

func (r *row) date(s string) *row {
	r.sep()
	r.b.WriteString(s)
	return r
}

func (r *row) empty() *row {
	r.sep()
	return r
}

func (r *row) String() string { return r.b.String() + "\r\n" }

func btoi(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
