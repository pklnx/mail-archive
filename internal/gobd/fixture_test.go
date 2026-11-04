package gobd

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the tests need Europe/Berlin on any machine

	"github.com/pklnx/mail-archive/internal/attest"
)

func testKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
}

func otherKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
}

func berlin(t testing.TB) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fixture is a small archive: five messages of three owners (two of them
// in the export's group), two attestations with their leaves, and one
// message attested by neither.
type fixture struct {
	key       ed25519.PrivateKey
	chain     []attest.Entry
	leaves    map[int64][]byte
	msgs      []fixMsg
	locs      []Location
	missing   []Missing
	st        attest.Statement
	fullChain bool
	withLeave bool
	unsigned  bool
}

type fixMsg struct {
	m    Message
	body []byte
}

func newFixture(t testing.TB) *fixture {
	t.Helper()
	f := &fixture{key: testKey(), leaves: map[int64][]byte{}}
	mk := func(seq int64, subject string, at time.Time, att int64) fixMsg {
		body := []byte("From: anna@example.org\r\nSubject: " + subject + "\r\n\r\nHello " + subject + "\r\n")
		return fixMsg{body: body, m: Message{
			SHA256: shaOf(body), Size: int64(len(body)), SortAt: at,
			From: "anna@example.org", To: "ben@example.org", Subject: subject,
			MessageID: "<" + subject + "@example.org>", ArchivedAt: at.Add(time.Hour), ArchiveSeq: seq, Attestation: att,
		}}
	}
	// seq 2 and 5 belong to other customers: in the leaves, not exported.
	f.msgs = []fixMsg{
		mk(1, "=SUM(A1) Rechnung", time.Date(2024, 3, 5, 9, 30, 0, 0, time.UTC), 1),
		mk(3, `Silvester "Feier"`, time.Date(2024, 12, 31, 23, 30, 0, 0, time.UTC), 1),
		mk(4, "Angebot", time.Date(2025, 2, 1, 8, 0, 0, 0, time.UTC), 2),
		mk(6, "Neu", time.Date(2025, 3, 1, 8, 0, 0, 0, time.UTC), 0),
	}
	f.msgs[0].m.HasAttachment, f.msgs[0].m.AttachmentNames = true, []string{"rechnung.pdf", "-x.xml"}
	f.msgs[1].m.Cc = "line1\r\nline2\ttab"
	others := map[int64]string{2: shaOf([]byte("kim's mail")), 5: shaOf([]byte("someone else"))}
	byS := map[int64]string{}
	for _, m := range f.msgs {
		byS[m.m.ArchiveSeq] = m.m.SHA256
	}
	for k, v := range others {
		byS[k] = v
	}
	var prev *attest.Entry
	for i, upto := range []int64{3, 5} {
		e := attest.Next(prev)
		var b bytes.Buffer
		w, _ := attest.NewLeavesWriter(&b)
		for s := e.MessagesAfter + 1; s <= upto; s++ {
			if err := w.Message(s, byS[s]); err != nil {
				t.Fatal(err)
			}
		}
		sum, n, _, _ := w.Finish()
		e.MessagesUpto, e.MessagesCount, e.Total, e.Leaves = upto, n, upto, sum
		e.Time = time.Date(2025, 2, 2+i, 3, 0, 0, 0, time.UTC)
		e.Sign(f.key)
		f.chain = append(f.chain, e)
		f.leaves[e.Seq] = b.Bytes()
		prev = &f.chain[len(f.chain)-1]
	}
	gone := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	for i, m := range f.msgs {
		f.locs = append(f.locs, Location{
			SHA256: m.m.SHA256, Owner: []string{"anna", "ben"}[i%2], Account: "work", AccountKind: "imap", Folder: "INBOX",
			UIDValidity: 1, UID: int64(i + 1), Flags: []string{`\Seen`}, FirstSeen: m.m.ArchivedAt, LastSeen: m.m.ArchivedAt,
		})
	}
	// The first message is also in a second folder.
	f.locs = append(f.locs, Location{
		SHA256: f.msgs[0].m.SHA256, Owner: "anna", Account: "work", AccountKind: "imap", Folder: "Archiv/2024",
		UIDValidity: 2, UID: 9, FirstSeen: f.msgs[0].m.ArchivedAt, LastSeen: f.msgs[0].m.ArchivedAt, GoneAt: &gone,
	})
	f.st = attest.Statement{
		ID: "0123456789abcdef0123456789abcdef", Created: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
		Software: "v1.5.0", Selection: `{"group":"Meier GmbH"}`, TimeZone: "Europe/Berlin",
		Group:  &attest.StatementGroup{ID: 7, Name: "Meier GmbH"},
		Owners: []string{"anna", "ben"},
	}
	return f
}

// write writes the export to out, a directory or (".zip") a ZIP.
func (f *fixture) write(t testing.TB, out string) *Result {
	t.Helper()
	st := f.st
	last := f.chain[len(f.chain)-1]
	st.AttestationSeq, st.AttestationHash = last.Seq, last.Hash
	st.FullChain, st.Leaves = f.fullChain, f.withLeave
	opts := Options{Statement: st, Location: berlin(t), Signer: f.key}
	if f.unsigned {
		opts.Signer = nil
	}
	var sink Sink
	var zf *os.File
	if strings.HasSuffix(out, ".zip") {
		var err error
		zf, err = os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		sink = NewZipSink(zf, st.Created)
		opts.MaxMessages = MaxZIPMessages
	} else {
		s, err := NewDirSink(out)
		if err != nil {
			t.Fatal(err)
		}
		sink = s
	}
	w, err := NewWriter(sink, opts)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, m := range f.msgs {
		if err := w.AddFile(m.m, bytes.NewReader(m.body)); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, MessagePath(m.m.SHA256, m.m.SortAt, opts.Location))
	}
	for _, m := range f.missing {
		if err := w.AddMissing(m); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range f.msgs {
		if err := w.AddMessageRow(m.m); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range f.locs {
		if err := w.AddLocationRow(l); err != nil {
			t.Fatal(err)
		}
	}
	chain := f.chain[len(f.chain)-1:]
	if f.fullChain {
		chain = f.chain
	}
	pub, _ := attest.MarshalPublicKey(f.key.Public().(ed25519.PublicKey))
	final := Final{
		Chain: chain, PublicKey: pub,
		Files: func(yield func(string) error) error {
			for _, p := range paths {
				if err := yield(p); err != nil {
					return err
				}
			}
			return nil
		},
	}
	if f.withLeave {
		final.Leaves = func(add func(int64, io.Reader) error) error {
			for _, seq := range slices.Sorted(func(yield func(int64) bool) {
				for k := range f.leaves {
					if !yield(k) {
						return
					}
				}
			}) {
				if err := add(seq, bytes.NewReader(f.leaves[seq])); err != nil {
					return err
				}
			}
			return nil
		}
	}
	res, err := w.Finish(final)
	if err != nil {
		t.Fatal(err)
	}
	if zf != nil {
		if err := zf.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return res
}

// trusted is the fixture's key as a trusted set (--public-key).
func (f *fixture) trusted() attest.Keys {
	return attest.Keys{attest.EncodeKey(f.key.Public().(ed25519.PublicKey)): true}
}

// copyFile writes the fixture's full chain and leaves as the customer's
// off-host copy and returns the attestations.jsonl path.
func (f *fixture) copyFile(t testing.TB, chain []attest.Entry, leaves bool) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, attest.ChainFile)
	for i := range chain {
		if err := attest.AppendFile(p, &chain[i]); err != nil {
			t.Fatal(err)
		}
	}
	if leaves {
		if err := os.MkdirAll(filepath.Join(dir, attest.LeavesDir), 0o700); err != nil {
			t.Fatal(err)
		}
		for seq, b := range f.leaves {
			if err := os.WriteFile(attest.LeavesPath(dir, seq), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return p
}

// verify runs Verify and returns the findings as "kind path" lines.
func verify(t testing.TB, p string, opts VerifyOptions) ([]Finding, *VerifyResult) {
	t.Helper()
	var got []Finding
	opts.OnFinding = func(f Finding) { got = append(got, f) }
	res, err := Verify(p, opts)
	if err != nil {
		t.Fatal(err)
	}
	return got, res
}

func hasFinding(fs []Finding, kind, pathPart string) bool {
	for _, f := range fs {
		if f.Kind == kind && strings.Contains(f.Path+" "+f.Detail, pathPart) {
			return true
		}
	}
	return false
}

func kinds(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Kind+" "+f.Path+" ("+f.Detail+")")
	}
	return out
}

// readTree reads every file of a directory export.
func readTree(t testing.TB, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// writeTree writes files as a directory export into a new directory.
func writeTree(t testing.TB, files map[string][]byte) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "export")
	for name, b := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// reseal rewrites SHA256SUMS for files and signs the statement again with
// key, after mutate changed files and the statement. The result is
// consistent in manifest and signature, so only a check of the content
// itself can catch the change.
func reseal(t testing.TB, files map[string][]byte, key ed25519.PrivateKey, mutate func(files map[string][]byte, st *attest.Statement)) {
	t.Helper()
	st, err := attest.ParseStatement(files[StatementFile])
	if err != nil {
		t.Fatal(err)
	}
	mutate(files, st)
	var sums strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(string(files[SumsFile]), "\n"), "\n") {
		_, path, _ := strings.Cut(line, "  ")
		if b, ok := files[path]; ok && !strings.HasPrefix(path, MailPrefix) {
			sums.WriteString(sumsLine(shaOf(b), path))
		} else {
			sums.WriteString(line + "\n")
		}
	}
	files[SumsFile] = []byte(sums.String())
	st.SHA256SUMS = shaOf(files[SumsFile])
	if err := st.Sign(key); err != nil {
		t.Fatal(err)
	}
	files[StatementFile] = st.File()
}

// zipEntryT is one entry of a crafted ZIP.
type zipEntryT struct {
	name string
	data []byte
	mode os.FileMode // 0: a regular file, 0600
}

// readZIP lists the entries of a ZIP in order.
func readZIP(t testing.TB, p string) []zipEntryT {
	t.Helper()
	zr, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zr.Close() }()
	var out []zipEntryT
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, zipEntryT{name: f.Name, data: b})
	}
	return out
}

// writeZIP writes entries as a ZIP and returns its path.
func writeZIP(t testing.TB, entries []zipEntryT) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "crafted.zip")
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := e.mode
		if mode == 0 {
			mode = 0o600
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
