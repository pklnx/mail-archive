package gobd

import (
	"archive/zip"
	"bufio"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/pklnx/mail-archive/internal/attest"
)

// Finding kinds of Verify.
const (
	KindManifest    = "manifest"     // SHA256SUMS missing, malformed, or not the digest the statement names
	KindChanged     = "changed"      // a file's bytes differ from SHA256SUMS or its name
	KindMissingFile = "missing-file" // a file that SHA256SUMS or a table names is not there
	KindExtraFile   = "extra-file"   // a file of the format that SHA256SUMS does not list
	KindName        = "name"         // a path or entry an export cannot have
	KindIndex       = "index"        // a table or index.xml is malformed or inconsistent
	KindStatement   = "statement"    // STATEMENT.txt is missing, malformed, badly signed or untrusted
	KindChain       = "chain"        // the attestation does not match the statement, the copy or the leaves
	KindIncomplete  = "incomplete"   // the export lists missing messages
	KindOwner       = "owner"        // a location's owner is not one of the statement's owners
	KindGroup       = "group"        // the statement names another customer group than expected
)

// FindingKinds lists the kinds in the order a summary names them.
var FindingKinds = []string{KindManifest, KindChanged, KindMissingFile, KindExtraFile, KindName, KindIndex,
	KindStatement, KindChain, KindIncomplete, KindOwner, KindGroup}

// Size bounds of the small files Verify reads whole.
const (
	maxIndexSize   = 1 << 20
	maxReadmeSize  = 1 << 20
	maxKeysSize    = 1 << 20
	maxChainLine   = 4096
	maxChainLength = 100_000
)

// Finding is one problem Verify found.
type Finding struct {
	Kind   string
	Path   string
	Detail string
}

// VerifyOptions are the inputs besides the export.
type VerifyOptions struct {
	// Trusted are the keys the customer trusts (--public-key). Nil trusts
	// the keys in the export's attest/ directory, which shows only that
	// the export is consistent.
	Trusted attest.Keys
	// Attestations is the customer's off-host copy of attestations.jsonl;
	// "" for none. Leaves files in a leaves/ directory next to it are used
	// when the export has none.
	Attestations string
	// ExpectGroup, when set, is the customer group the statement must name
	// (case-insensitive, trimmed; "none" for an export without a group).
	ExpectGroup *string
	// OnFinding is called with each finding as it is found.
	OnFinding func(Finding)
}

// VerifyResult is what Verify found.
type VerifyResult struct {
	// Statement is the parsed statement, nil if it could not be read.
	Statement *attest.Statement
	// Findings counts the findings per kind.
	Findings map[string]int
	// Notes say what could not be checked (no trusted key, no copy).
	Notes []string
	// Messages is the number of message files checked.
	Messages int64
}

// Problems is the number of findings.
func (r *VerifyResult) Problems() int {
	n := 0
	for _, c := range r.Findings {
		n += c
	}
	return n
}

// eml flags.
const (
	fPresent uint8 = 1 << iota
	fListed
	fRow
	fLocated
)

// eml is a message file of the export, by its name. The table holds one
// per message, about 80 bytes, and nothing that grows with the file.
type eml struct {
	sha       [32]byte
	year      uint16
	month     uint8
	flags     uint8
	zf        *zip.File
	entrySize int64 // declared or on disk
	size      int64 // from messages.csv
	seq, att  int64
}

func (e *eml) path() string {
	return fmt.Sprintf("%s%04d/%02d/%s.eml", MailPrefix, e.year, e.month, hex.EncodeToString(e.sha[:]))
}

func cmpEML(a, b eml) int {
	return cmp.Or(bytes.Compare(a.sha[:], b.sha[:]), cmp.Compare(a.year, b.year), cmp.Compare(a.month, b.month))
}

type verifier struct {
	opts VerifyOptions
	res  *VerifyResult
	c    *container
	st   *attest.Statement

	fixed  map[string]*rawEntry
	leaves map[int64]*rawEntry
	odd    []string
	emls   []eml

	sumsRead bool
	sums     map[string]string // every listed file but the message files
	missing  map[[32]byte]bool
	rowOrder []int // indexes into emls in messages.csv order
	owners   map[string]bool

	chain   []attest.Entry // attest/attestations.jsonl, if it is valid
	trusted attest.Keys    // nil: each key checks itself only

	copyRead    bool
	copyEntries []attest.Entry
	copyErr     error

	messages, locations, missingRows, bytes int64
}

// Verify checks an export, a directory or a ZIP, offline: names, the
// manifest, every file's digest, the tables against each other and the
// statement, the statement's signature, the attestation against the
// statement and the customer's copy, the leaves, the owners and the group.
// It reports every finding and returns an error only when path cannot be
// opened at all.
//
// Memory: about 80 bytes per message for the table of message files, a
// line of a table at a time (at most 1 MiB), the missing list (at most
// MaxMissing), and for a ZIP what archive/zip keeps per entry, bounded by
// prescanZIP.
func Verify(p string, opts VerifyOptions) (*VerifyResult, error) {
	v := &verifier{
		opts: opts, res: &VerifyResult{Findings: map[string]int{}},
		fixed: map[string]*rawEntry{}, leaves: map[int64]*rawEntry{},
		sums: map[string]string{}, missing: map[[32]byte]bool{}, owners: map[string]bool{},
	}
	c, err := openContainer(p, v.find)
	if err != nil {
		return nil, err
	}
	defer c.close()
	v.c = c
	if c.isZIP && c.zr == nil {
		return v.res, nil // the ZIP itself is the finding
	}
	if err := c.walk(v.classify, v.find); err != nil {
		return nil, err
	}
	v.sortEMLs()
	v.reportOdd()
	v.readStatement()
	v.checkLeavesNames()
	v.readSums()
	v.readTables()
	v.checkMessageFiles()
	v.checkSmallFiles()
	v.checkChain()
	v.checkLeaves()
	v.crossCheck()
	return v.res, nil
}

func (v *verifier) find(kind, path, detail string) {
	v.res.Findings[kind]++
	if v.opts.OnFinding != nil {
		v.opts.OnFinding(Finding{Kind: kind, Path: path, Detail: detail})
	}
}

func (v *verifier) note(s string) { v.res.Notes = append(v.res.Notes, s) }

// requiredFiles must be in every export.
var requiredFiles = []string{ReadmeFile, IndexFile, MessagesFile, LocationsFile, MissingFile}

var fixedFiles = map[string]bool{
	ReadmeFile: true, IndexFile: true, MessagesFile: true, LocationsFile: true, MissingFile: true,
	SumsFile: true, StatementFile: true, ChainFile: true, PublicKeyFile: true, RetiredFile: true,
}

// classify sorts an entry into the fixed files, the message files and the
// leaves files, and reports anything else.
func (v *verifier) classify(e *rawEntry) {
	switch {
	case e.kind == entryDir:
		v.find(KindName, e.name, "directory entry (an export ZIP has none)")
		return
	case e.kind == entryOther:
		v.find(KindName, e.name, "not a regular file (symbolic link or other)")
		return
	case e.problem != "":
		v.find(KindName, e.name, "entry "+e.problem)
		return
	}
	if why := unsafePath(e.name); why != "" {
		v.find(KindName, e.name, why)
		return
	}
	if fixedFiles[e.name] {
		if v.fixed[e.name] != nil {
			v.find(KindName, e.name, "duplicate entry")
			return
		}
		v.fixed[e.name] = e
		return
	}
	if m, ok := parseEMLPath(e.name); ok {
		m.flags = fPresent
		m.entrySize = e.size
		m.zf = e.zf
		v.emls = append(v.emls, m)
		return
	}
	if seq, ok := parseLeavesPath(e.name); ok {
		if v.leaves[seq] != nil {
			v.find(KindName, e.name, "duplicate entry")
			return
		}
		v.leaves[seq] = e
		return
	}
	v.odd = append(v.odd, e.name)
}

// unsafePath says why a name can escape or confuse an unpacker, or "".
func unsafePath(name string) string {
	switch {
	case name == "":
		return "empty name"
	case !isPrintableUTF8(name):
		return "name with control characters or invalid UTF-8"
	case strings.HasPrefix(name, "/"):
		return "absolute path"
	case strings.Contains(name, `\`):
		return "backslash in path"
	case strings.HasSuffix(name, "/"):
		return "directory entry (an export ZIP has none)"
	}
	for i, el := range strings.Split(name, "/") {
		switch {
		case el == "", el == ".", el == "..":
			return "path with an empty, . or .. element"
		case i == 0 && strings.Contains(el, ":"):
			return "drive letter or colon in path"
		}
	}
	return ""
}

func isPrintableUTF8(s string) bool {
	for _, r := range s {
		if r < ' ' || r == 0x7f || r == '�' || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

// parseEMLPath reads mail/YYYY/MM/<sha>.eml, exactly as MessagePath
// writes it.
func parseEMLPath(name string) (eml, bool) {
	const n = len("mail/2024/01/") + 64 + len(".eml")
	if len(name) != n || !strings.HasPrefix(name, MailPrefix) || name[9] != '/' || name[12] != '/' ||
		!strings.HasSuffix(name, ".eml") || !allDigits(name[5:9]) || !validMonth(name[10:12]) || !isSHA(name[13:77]) {
		return eml{}, false
	}
	var m eml
	_, _ = hex.Decode(m.sha[:], []byte(name[13:77]))
	// ParseUint with the bit size of the field: the bound is in the parse.
	y, err := strconv.ParseUint(name[5:9], 10, 16)
	if err != nil {
		return eml{}, false
	}
	mo, err := strconv.ParseUint(name[10:12], 10, 8)
	if err != nil {
		return eml{}, false
	}
	m.year, m.month = uint16(y), uint8(mo)
	return m, true
}

// parseLeavesPath reads attest/leaves/<seq>.txt with seq in canonical
// decimal form.
func parseLeavesPath(name string) (int64, bool) {
	s, ok := strings.CutPrefix(name, LeavesPrefix)
	if !ok {
		return 0, false
	}
	s, ok = strings.CutSuffix(s, ".txt")
	if !ok || !allDigits(s) || s[0] == '0' || len(s) > 18 {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// sortEMLs sorts the message files and reports a message that appears
// twice (the same name twice, or in two month folders).
func (v *verifier) sortEMLs() {
	slices.SortFunc(v.emls, cmpEML)
	out := v.emls[:0]
	for i := range v.emls {
		if len(out) > 0 && out[len(out)-1].sha == v.emls[i].sha {
			v.find(KindName, v.emls[i].path(), "the same message file appears twice")
			continue
		}
		out = append(out, v.emls[i])
	}
	v.emls = out
}

// findEML returns the message file with this sha and its index, or nil.
func (v *verifier) findEML(sha [32]byte) (*eml, int) {
	i, ok := slices.BinarySearchFunc(v.emls, sha, func(e eml, s [32]byte) int { return bytes.Compare(e.sha[:], s[:]) })
	if !ok {
		return nil, -1
	}
	return &v.emls[i], i
}

func findOK(e *eml, _ int) bool { return e != nil }

// fold is the key under which two names count as the same for an
// unpacker on a case-insensitive or normalizing file system.
func fold(name string) string { return strings.ToLower(norm.NFC.String(name)) }

// reportOdd reports names that are not part of the format, saying when
// one differs from a name of the format only in case or Unicode form.
func (v *verifier) reportOdd() {
	for _, name := range v.odd {
		f := fold(name)
		detail := "unexpected file"
		if fixedFiles[f] || fixedFoldsTo(f) != "" {
			detail = "differs from " + cmp.Or(fixedFoldsTo(f), f) + " only in case or Unicode form"
		} else if m, ok := parseEMLPath(f); ok && findOK(v.findEML(m.sha)) {
			detail = "differs from " + m.path() + " only in case or Unicode form"
		} else if _, ok := parseLeavesPath(f); ok {
			detail = "differs from " + f + " only in case or Unicode form"
		}
		v.find(KindName, name, detail)
	}
}

func fixedFoldsTo(f string) string {
	for name := range fixedFiles {
		if fold(name) == f {
			return name
		}
	}
	return ""
}

// readAll reads a fixed file whole, up to limit bytes, and returns its
// bytes; ok is false if it is missing, too large or unreadable (reported).
func (v *verifier) readAll(name string, limit int64, kind string) ([]byte, bool) {
	e := v.fixed[name]
	if e == nil {
		return nil, false
	}
	if e.size > limit {
		v.find(KindName, name, "larger than expected")
		return nil, false
	}
	r, err := v.c.open(e)
	if err != nil {
		v.find(kind, name, "cannot be read: "+err.Error())
		return nil, false
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	switch {
	case err != nil:
		v.find(kind, name, "cannot be read: "+err.Error())
		return nil, false
	case int64(len(data)) > limit:
		v.find(KindName, name, "larger than expected")
		return nil, false
	}
	return data, true
}

func (v *verifier) readStatement() {
	data, ok := v.readAll(StatementFile, attest.MaxStatementSize, KindStatement)
	if !ok {
		if v.fixed[StatementFile] == nil {
			v.find(KindStatement, StatementFile, "missing")
		}
		return
	}
	st, err := attest.ParseStatement(data)
	if err != nil {
		v.find(KindStatement, StatementFile, err.Error())
		return
	}
	v.st = st
	v.res.Statement = st
	for _, o := range st.Owners {
		v.owners[textValue(o)] = true
	}

	switch {
	case v.opts.Trusted != nil:
		v.trusted = v.opts.Trusted
	default:
		keys := v.exportKeys()
		if len(keys) > 0 {
			v.trusted = keys
			v.note("no --public-key given: the keys in the export's attest/ directory were trusted, which shows only that the export is consistent")
		} else {
			v.note("no trusted key: the signatures were checked against the keys they name only")
		}
	}
	err = st.Verify(v.trusted)
	switch {
	case errors.Is(err, attest.ErrUnsigned) && v.opts.Trusted == nil:
		v.note("the statement is not signed (the archive had no attestation key)")
	case err != nil:
		v.find(KindStatement, StatementFile, err.Error())
	}
}

// exportKeys reads attest/signing.pub and attest/retired-keys.pem.
func (v *verifier) exportKeys() attest.Keys {
	keys := attest.Keys{}
	for _, name := range []string{PublicKeyFile, RetiredFile} {
		data, ok := v.readAll(name, maxKeysSize, KindChain)
		if !ok {
			continue
		}
		pubs, err := attest.ParsePublicKeys(data)
		if err != nil {
			v.find(KindChain, name, err.Error())
			continue
		}
		for _, p := range pubs {
			keys[attest.EncodeKey(p)] = true
		}
	}
	return keys
}

// checkLeavesNames reports leaves files and an attestation file that the
// statement does not announce.
func (v *verifier) checkLeavesNames() {
	if v.st == nil {
		return
	}
	for seq := range v.leaves {
		if !v.st.Leaves || seq > v.st.AttestationSeq {
			v.find(KindName, LeavesPath(seq), "unexpected file (the statement does not include this leaves file)")
			delete(v.leaves, seq)
		}
	}
	if v.st.AttestationSeq == 0 && v.fixed[ChainFile] != nil {
		v.find(KindName, ChainFile, "unexpected file (the statement names no attestation)")
		delete(v.fixed, ChainFile)
	}
}

func (v *verifier) readSums() {
	e := v.fixed[SumsFile]
	if e == nil {
		v.find(KindManifest, SumsFile, "missing")
		return
	}
	r, err := v.c.open(e)
	if err != nil {
		v.find(KindManifest, SumsFile, "cannot be read: "+err.Error())
		return
	}
	defer func() { _ = r.Close() }()
	digest, err := ParseSums(r, v.sumsLine)
	if err != nil {
		v.find(KindManifest, SumsFile, err.Error())
		return
	}
	v.sumsRead = true
	if v.st != nil && digest != v.st.SHA256SUMS {
		v.find(KindManifest, SumsFile, "does not match the digest in "+StatementFile)
	}
	for _, name := range requiredFiles {
		if v.fixed[name] == nil && v.sums[name] == "" {
			v.find(KindMissingFile, name, "missing")
		}
	}
	for i := range v.emls {
		if m := &v.emls[i]; m.flags&fPresent != 0 && m.flags&fListed == 0 {
			v.find(KindExtraFile, m.path(), "not in "+SumsFile)
		}
	}
	for name := range v.fixed {
		if name != SumsFile && name != StatementFile && v.sums[name] == "" {
			v.find(KindExtraFile, name, "not in "+SumsFile)
		}
	}
	for seq := range v.leaves {
		if v.sums[LeavesPath(seq)] == "" {
			v.find(KindExtraFile, LeavesPath(seq), "not in "+SumsFile)
		}
	}
}

func (v *verifier) sumsLine(path, sum string) error {
	if m, ok := parseEMLPath(path); ok {
		if hex.EncodeToString(m.sha[:]) != sum {
			v.find(KindManifest, path, "the digest in "+SumsFile+" is not the file's name")
		}
		e, _ := v.findEML(m.sha)
		switch {
		case e == nil || e.year != m.year || e.month != m.month:
			v.find(KindMissingFile, path, "listed in "+SumsFile)
		case e.flags&fListed != 0:
			v.find(KindManifest, path, "listed twice")
		default:
			e.flags |= fListed
		}
		return nil
	}
	_, isLeaves := parseLeavesPath(path)
	if (!fixedFiles[path] && !isLeaves) || path == SumsFile || path == StatementFile {
		v.find(KindManifest, path, "an unexpected path is listed")
		return nil
	}
	if v.sums[path] != "" {
		v.find(KindManifest, path, "listed twice")
		return nil
	}
	v.sums[path] = sum
	present := v.fixed[path] != nil
	if seq, ok := parseLeavesPath(path); ok {
		present = v.leaves[seq] != nil
	}
	if !present {
		v.find(KindMissingFile, path, "listed in "+SumsFile)
	}
	return nil
}

// compareSum reports a file whose digest differs from SHA256SUMS.
func (v *verifier) compareSum(name, digest string) {
	if want := v.sums[name]; want != "" && want != digest {
		v.find(KindChanged, name, "does not match "+SumsFile)
	}
}

// readTable reads a fixed CSV file, hashing it on the way, and calls fn
// for each record that has the format of cols.
func (v *verifier) readTable(name string, cols []column, fn func(line int, vals []string)) {
	e := v.fixed[name]
	if e == nil {
		return
	}
	r, err := v.c.open(e)
	if err != nil {
		v.find(KindIndex, name, "cannot be read: "+err.Error())
		return
	}
	defer func() { _ = r.Close() }()
	h := sha256.New()
	err = scanTable(io.TeeReader(r, h), cols, fn, func(line int, why string) {
		v.find(KindIndex, name, fmt.Sprintf("line %d: %s", line, why))
	})
	if err != nil {
		v.find(KindIndex, name, "cannot be read: "+err.Error())
		return
	}
	v.compareSum(name, hex.EncodeToString(h.Sum(nil)))
}

func (v *verifier) readTables() {
	v.readTable(MissingFile, missingColumns, v.missingRow)
	lastSeq := int64(0)
	v.readTable(MessagesFile, messageColumns, func(line int, vals []string) {
		v.messageRow(line, vals, &lastSeq)
	})
	v.readTable(LocationsFile, locationColumns, v.locationRow)
}

func (v *verifier) missingRow(line int, vals []string) {
	bad := func(why string) { v.find(KindIndex, MissingFile, fmt.Sprintf("line %d: %s", line, why)) }
	v.missingRows++
	sha, ok := decodeSHA(vals[0])
	m, pathOK := parseEMLPath(vals[1])
	switch {
	case !ok:
		bad("sha256 is not a SHA-256")
	case !pathOK || m.sha != sha:
		bad("file is not the message's path")
	case vals[2] == "":
		bad("reason is empty")
	case v.missing[sha]:
		bad("the message is listed twice")
	case len(v.missing) >= MaxMissing:
		if len(v.missing) == MaxMissing {
			bad(fmt.Sprintf("more than %d rows", MaxMissing))
		}
	default:
		v.missing[sha] = true
	}
}

func (v *verifier) messageRow(line int, vals []string, lastSeq *int64) {
	bad := func(why string) { v.find(KindIndex, MessagesFile, fmt.Sprintf("line %d: %s", line, why)) }
	sha, ok := decodeSHA(vals[0])
	if !ok {
		bad("sha256 is not a SHA-256")
		return
	}
	v.messages++
	size, _ := strconv.ParseInt(vals[2], 10, 64)
	v.bytes += size
	if vals[1] != pathFromDate(vals[0], vals[3]) {
		bad("file does not match sha256 and date")
	}
	seq, _ := strconv.ParseInt(vals[15], 10, 64)
	if seq <= *lastSeq {
		bad("archive_seq is not in increasing order")
	}
	*lastSeq = max(*lastSeq, seq)
	var att int64
	if vals[16] != "" {
		att, _ = strconv.ParseInt(vals[16], 10, 64)
		if v.st != nil && att > v.st.AttestationSeq {
			bad(fmt.Sprintf("attestation %d is newer than the statement's", att))
		}
	}
	if v.missing[sha] {
		bad("the message is also in " + MissingFile)
	}
	e, idx := v.findEML(sha)
	if e == nil || e.flags&(fPresent|fListed) == 0 {
		if e == nil || e.flags&fListed == 0 {
			v.find(KindMissingFile, vals[1], "named in "+MessagesFile)
		}
		return
	}
	if e.path() != vals[1] {
		bad("file differs from the message file's path " + e.path())
		return
	}
	if e.flags&fRow != 0 {
		bad("the message is listed twice")
		return
	}
	e.flags |= fRow
	e.size, e.seq, e.att = size, seq, att
	v.rowOrder = append(v.rowOrder, idx)
}

func (v *verifier) locationRow(line int, vals []string) {
	v.locations++
	sha, ok := decodeSHA(vals[0])
	if !ok {
		v.find(KindIndex, LocationsFile, fmt.Sprintf("line %d: sha256 is not a SHA-256", line))
		return
	}
	if e, _ := v.findEML(sha); e != nil && e.flags&fRow != 0 {
		e.flags |= fLocated
	} else {
		// Missing messages appear only in missing.csv, so the foreign key
		// to messages always holds.
		v.find(KindIndex, LocationsFile, fmt.Sprintf("line %d: refers to a message that is not in %s", line, MessagesFile))
	}
	if v.st == nil {
		return
	}
	switch owner := vals[1]; {
	case owner == "" && v.st.Group != nil:
		v.find(KindOwner, LocationsFile, fmt.Sprintf("line %d: a place without owner in an export of a customer group", line))
	case owner != "" && !v.owners[owner]:
		v.find(KindOwner, LocationsFile, fmt.Sprintf("line %d: owner %q is not one of the statement's owners", line, owner))
	}
}

// checkMessageFiles reads every listed message file that messages.csv
// names and compares its bytes with its name and size.
func (v *verifier) checkMessageFiles() {
	for i := range v.emls {
		m := &v.emls[i]
		if m.flags&fPresent == 0 {
			continue
		}
		path := m.path()
		if m.flags&fRow == 0 {
			if m.flags&fListed != 0 {
				v.find(KindIndex, path, "not in "+MessagesFile)
			}
			continue
		}
		if m.entrySize > m.size {
			v.find(KindName, path, "larger than expected")
			continue
		}
		r, err := v.c.open(&rawEntry{name: path, zf: m.zf})
		if err != nil {
			v.find(KindChanged, path, "cannot be read: "+err.Error())
			continue
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(r, m.size+1))
		_ = r.Close()
		v.res.Messages++
		switch {
		case err != nil:
			v.find(KindChanged, path, "cannot be read: "+err.Error())
		case n != m.size:
			v.find(KindChanged, path, fmt.Sprintf("%d bytes, %s says %d", n, MessagesFile, m.size))
		case !bytes.Equal(h.Sum(nil), m.sha[:]):
			v.find(KindChanged, path, "content does not match its SHA-256")
		}
	}
}

// checkSmallFiles hashes README.txt, index.xml, the keys and the leaves
// files and checks index.xml.
func (v *verifier) checkSmallFiles() {
	if data, ok := v.readAll(ReadmeFile, maxReadmeSize, KindIndex); ok {
		v.compareSum(ReadmeFile, sum(data))
	}
	if data, ok := v.readAll(IndexFile, maxIndexSize, KindIndex); ok {
		if err := checkIndex(bytes.NewReader(data)); err != nil {
			v.find(KindIndex, IndexFile, err.Error())
		}
		v.compareSum(IndexFile, sum(data))
	}
	for _, name := range []string{PublicKeyFile, RetiredFile} {
		if data, ok := v.readAll(name, maxKeysSize, KindChain); ok {
			v.compareSum(name, sum(data))
		}
	}
	for _, seq := range slices.Sorted(maps.Keys(v.leaves)) {
		name := LeavesPath(seq)
		r, err := v.c.open(v.leaves[seq])
		if err != nil {
			v.find(KindChain, name, "cannot be read: "+err.Error())
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, r)
		_ = r.Close()
		if err != nil {
			v.find(KindChain, name, "cannot be read: "+err.Error())
			continue
		}
		v.compareSum(name, hex.EncodeToString(h.Sum(nil)))
	}
}

// checkChain checks attest/attestations.jsonl against the statement, the
// trusted keys and the customer's copy.
func (v *verifier) checkChain() {
	st := v.st
	if st == nil || st.AttestationSeq == 0 {
		if v.opts.Attestations != "" && st != nil {
			v.note("the export names no attestation; --attestations was not used")
		}
		return
	}
	e := v.fixed[ChainFile]
	if e == nil {
		if v.sums[ChainFile] == "" {
			v.find(KindMissingFile, ChainFile, "the statement names attestation #"+strconv.FormatInt(st.AttestationSeq, 10))
		}
		return
	}
	r, err := v.c.open(e)
	if err != nil {
		v.find(KindChain, ChainFile, "cannot be read: "+err.Error())
		return
	}
	h := sha256.New()
	entries, err := readChain(io.TeeReader(r, h), st)
	_ = r.Close()
	if err != nil {
		v.find(KindChain, ChainFile, err.Error())
		return
	}
	v.compareSum(ChainFile, hex.EncodeToString(h.Sum(nil)))
	want := int64(1)
	if st.FullChain {
		want = st.AttestationSeq
		if want > maxChainLength {
			want = maxChainLength
		}
	}
	if int64(len(entries)) != want {
		v.find(KindChain, ChainFile, fmt.Sprintf("%d entries, the statement says %d", len(entries), want))
		return
	}
	last := entries[len(entries)-1]
	if last.Seq != st.AttestationSeq || last.Hash != st.AttestationHash {
		v.find(KindChain, ChainFile, fmt.Sprintf("ends with attestation #%d, the statement names #%d %s", last.Seq, st.AttestationSeq, st.AttestationHash))
		return
	}
	ok := true
	if st.FullChain {
		trusted := v.trusted
		if trusted == nil {
			trusted = attest.Keys{}
			for _, e := range entries {
				trusted[e.Key] = true
			}
		}
		for _, p := range attest.ExamineChain(entries, trusted).Problems {
			v.find(KindChain, ChainFile, p)
			ok = false
		}
	} else if err := last.Check(v.trustedFor(&last)); err != nil {
		v.find(KindChain, ChainFile, err.Error())
		ok = false
	}
	if ok {
		v.chain = entries
	}
	v.compareCopy(entries)
}

func (v *verifier) trustedFor(e *attest.Entry) attest.Keys {
	if v.trusted != nil {
		return v.trusted
	}
	return attest.Keys{e.Key: true}
}

// readChain reads the attestation lines of an export, a line at a time,
// at most as many as the statement can need.
func readChain(r io.Reader, st *attest.Statement) ([]attest.Entry, error) {
	limit := int64(1)
	if st.FullChain {
		limit = min(st.AttestationSeq, maxChainLength)
	}
	br := bufio.NewReaderSize(r, maxChainLine)
	var out []attest.Entry
	for n := 1; ; n++ {
		line, err := br.ReadSlice('\n')
		switch {
		case errors.Is(err, io.EOF) && len(line) == 0:
			return out, nil
		case errors.Is(err, io.EOF):
			return nil, fmt.Errorf("line %d is incomplete (no newline)", n)
		case errors.Is(err, bufio.ErrBufferFull):
			return nil, fmt.Errorf("line %d is too long", n)
		case err != nil:
			return nil, err
		}
		if int64(len(out)) >= limit {
			return nil, fmt.Errorf("more entries than the statement says (%d)", limit)
		}
		e, err := attest.ParseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, e)
	}
}

// compareCopy checks the exported entries against the customer's copy:
// the one entry must be in it, a full chain must be a prefix of it.
func (v *verifier) compareCopy(entries []attest.Entry) {
	if v.opts.Attestations == "" {
		v.note("the attestation was not compared with an off-host copy (--attestations)")
		return
	}
	cp, err := v.readCopy()
	if err != nil {
		v.find(KindChain, v.opts.Attestations, "cannot be read: "+err.Error())
		return
	}
	for i := range entries {
		e := &entries[i]
		if e.Seq < 1 || e.Seq > int64(len(cp)) || !cp[e.Seq-1].Same(e) {
			what := "is not in the copy"
			if v.st.FullChain {
				what = "the chain is not a prefix of the copy"
			}
			v.find(KindChain, ChainFile, fmt.Sprintf("attestation #%d: %s %s", e.Seq, what, v.opts.Attestations))
			return
		}
	}
}

var errNoCopy = errors.New("no copy")

// readCopy reads and checks the customer's copy once.
func (v *verifier) readCopy() ([]attest.Entry, error) {
	if v.opts.Attestations == "" {
		return nil, errNoCopy
	}
	if v.copyRead {
		return v.copyEntries, v.copyErr
	}
	v.copyRead = true
	cp, err := attest.ReadFile(v.opts.Attestations)
	if err == nil && len(cp) == 0 {
		err = errors.New("no attestations in it")
	}
	if err == nil {
		trusted := v.trusted
		if trusted == nil {
			trusted = attest.Keys{}
			for _, e := range cp {
				trusted[e.Key] = true
			}
		}
		for _, p := range attest.ExamineChain(cp, trusted).Problems {
			v.find(KindChain, v.opts.Attestations, p)
		}
	}
	v.copyEntries, v.copyErr = cp, err
	return cp, err
}

// checkLeaves checks each exported message's (archive_seq, sha256)
// against the leaves file of its attestation, and its seq against the
// attestation's range, where the attestation and the leaves are known.
func (v *verifier) checkLeaves() {
	st := v.st
	if st == nil || st.AttestationSeq == 0 {
		return
	}
	known := map[int64]*attest.Entry{}
	for i := range v.chain {
		known[v.chain[i].Seq] = &v.chain[i]
	}
	if cp, err := v.readCopy(); err == nil {
		for i := range cp {
			if cp[i].Seq <= st.AttestationSeq {
				known[cp[i].Seq] = &cp[i]
			}
		}
	}
	byAtt := map[int64][]int{}
	newest := known[st.AttestationSeq]
	for _, i := range v.rowOrder {
		m := &v.emls[i]
		if m.att == 0 {
			if newest != nil && m.seq <= newest.MessagesUpto {
				v.find(KindIndex, MessagesFile, fmt.Sprintf("%s: archive_seq %d is covered by attestation #%d, but its attestation is empty", m.path(), m.seq, newest.Seq))
			}
			continue
		}
		byAtt[m.att] = append(byAtt[m.att], i)
	}
	copyLeaves := ""
	if v.opts.Attestations != "" {
		copyLeaves = filepath.Join(filepath.Dir(v.opts.Attestations), attest.LeavesDir)
	}
	if !st.Leaves && copyLeaves != "" {
		if fi, err := os.Stat(copyLeaves); err != nil || !fi.IsDir() {
			copyLeaves = ""
		}
	}
	if !st.Leaves && copyLeaves == "" && len(byAtt) > 0 {
		v.note("the messages were not checked against leaves files (none in the export or next to --attestations)")
	}
	unknown := 0
	for _, a := range slices.Sorted(maps.Keys(byAtt)) {
		e := known[a]
		if e == nil {
			// The leaves file is still covered by the signed statement
			// (through SHA256SUMS); only its digest and range cannot be
			// compared with the attestation.
			unknown++
		} else {
			for _, i := range byAtt[a] {
				if m := &v.emls[i]; m.seq <= e.MessagesAfter || m.seq > e.MessagesUpto {
					v.find(KindChain, m.path(), fmt.Sprintf("archive_seq %d is outside attestation #%d (%d to %d)", m.seq, a, e.MessagesAfter+1, e.MessagesUpto))
				}
			}
		}
		switch {
		case st.Leaves:
			le := v.leaves[a]
			if le == nil {
				if v.sums[LeavesPath(a)] == "" {
					v.find(KindMissingFile, LeavesPath(a), "the statement includes the leaves")
				}
				continue
			}
			r, err := v.c.open(le)
			if err != nil {
				v.find(KindChain, LeavesPath(a), "cannot be read: "+err.Error())
				continue
			}
			v.scanLeaves(LeavesPath(a), r, a, e, byAtt[a])
			_ = r.Close()
		case copyLeaves != "" && e != nil:
			p := filepath.Join(copyLeaves, strconv.FormatInt(a, 10)+".txt")
			r, err := os.Open(p) //nolint:gosec // next to the copy the user named
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					v.find(KindChain, p, "missing next to the copy")
				} else {
					v.find(KindChain, p, "cannot be read: "+err.Error())
				}
				continue
			}
			v.scanLeaves(p, r, a, e, byAtt[a])
			_ = r.Close()
		}
	}
	if unknown > 0 {
		v.note(fmt.Sprintf("%d attestation(s) that messages name are neither in the export nor in --attestations; their ranges and leaves digests were not compared", unknown))
	}
}

// scanLeaves merges a leaves file (in seq order) with the messages of its
// attestation (in seq order) and reports each message it does not list
// with the same SHA-256.
// e is nil when the attestation is not known; then the digest is not
// compared.
func (v *verifier) scanLeaves(name string, r io.Reader, seq int64, e *attest.Entry, msgs []int) {
	slices.SortFunc(msgs, func(a, b int) int { return cmp.Compare(v.emls[a].seq, v.emls[b].seq) })
	next := 0
	report := func(m *eml, why string) {
		v.find(KindChain, m.path(), fmt.Sprintf("archive_seq %d: %s in leaves file #%d", m.seq, why, seq))
	}
	digest, err := attest.ScanLeaves(r, func(l attest.Leaf) error {
		for next < len(msgs) && v.emls[msgs[next]].seq < l.Seq {
			report(&v.emls[msgs[next]], "missing")
			next++
		}
		if next < len(msgs) && v.emls[msgs[next]].seq == l.Seq {
			if m := &v.emls[msgs[next]]; hex.EncodeToString(m.sha[:]) != l.SHA256 {
				report(m, "another SHA-256")
			}
			next++
		}
		return nil
	})
	if err != nil {
		v.find(KindChain, name, err.Error())
		return
	}
	for ; next < len(msgs); next++ {
		report(&v.emls[msgs[next]], "missing")
	}
	if e != nil && digest != e.Leaves {
		v.find(KindChain, name, fmt.Sprintf("does not match the leaves digest of attestation #%d", e.Seq))
	}
}

// crossCheck compares the tables with each other and the statement, and
// the group with --expect-group.
func (v *verifier) crossCheck() {
	for _, i := range v.rowOrder {
		if m := &v.emls[i]; m.flags&fLocated == 0 {
			v.find(KindIndex, LocationsFile, m.path()+": the message has no row in "+LocationsFile)
		}
	}
	st := v.st
	if st == nil {
		return
	}
	for _, c := range []struct {
		name      string
		got, want int64
	}{
		{MessagesFile, v.messages, st.Messages},
		{LocationsFile, v.locations, st.Locations},
		{MissingFile, v.missingRows, st.Missing},
		{MessagesFile + " (bytes)", v.bytes, st.Bytes},
	} {
		if v.fixed[strings.TrimSuffix(c.name, " (bytes)")] != nil && c.got != c.want {
			v.find(KindIndex, c.name, fmt.Sprintf("%d, the statement says %d", c.got, c.want))
		}
	}
	if !st.Complete {
		v.find(KindIncomplete, MissingFile, fmt.Sprintf("%d message(s) of the selection have no valid file", st.Missing))
	}
	if v.opts.ExpectGroup != nil {
		want := strings.ToLower(strings.TrimSpace(*v.opts.ExpectGroup))
		got := "none"
		if st.Group != nil {
			got = strings.ToLower(st.Group.Name)
		}
		if got != want {
			v.find(KindGroup, StatementFile, fmt.Sprintf("the export is of %s, not %q", GroupLabel(st), *v.opts.ExpectGroup))
		}
	}
}

// GroupLabel names the statement's customer group for a message.
func GroupLabel(st *attest.Statement) string {
	if st.Group == nil {
		return "no customer group"
	}
	return fmt.Sprintf("customer group %s (no. %d)", strconv.Quote(st.Group.Name), st.Group.ID)
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func decodeSHA(s string) ([32]byte, bool) {
	var out [32]byte
	if !isSHA(s) {
		return out, false
	}
	_, _ = hex.Decode(out[:], []byte(s))
	return out, true
}
