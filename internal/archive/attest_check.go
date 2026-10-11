package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/pklnx/mail-archive/internal/attest"
	"github.com/pklnx/mail-archive/internal/store"
)

// Kinds of attestation findings.
const (
	FindingRemoved     = "removed"      // an attested message is gone, with no deletion recorded
	FindingUnattested  = "unattested"   // a row inside an attested range that the attestation does not list
	FindingAttestation = "attestation"  // an attestation no longer matches the database
	FindingAttestFile  = "attest-file"  // the attestation file or a leaves file is changed or missing
	FindingChain       = "chain"        // a broken chain, a bad signature or an untrusted key
	FindingAttestStale = "attest-stale" // the newest attestation is older than twice the interval
)

// AttestCheckOptions configures CheckAttestations.
type AttestCheckOptions struct {
	Dir string // attestation directory (<data dir>/attest)
	// PublicKeys, if set, are the only trusted keys, for example off-host
	// copies; otherwise signing.pub and the retired keys in Dir are.
	PublicKeys attest.Keys
	// File is the attestation file compared with the database (default
	// Dir/attestations.jsonl). Its leaves files are always read from Dir.
	File string
	// Copy tells that File is a copy kept elsewhere, not the file the
	// attestations are written to: it must then be a prefix of the
	// database chain, and entries the database lacks are findings.
	Copy bool
	// KeyExists tells that a signing key exists, so attestations are
	// expected every Interval (0: no schedule).
	KeyExists bool
	Interval  time.Duration
	OnFinding func(Finding)
	Now       func() time.Time
}

// AttestCheckResult summarizes CheckAttestations.
type AttestCheckResult struct {
	Attestations int // attestations in the database
	Findings     map[string]int
	// Unchecked is set when attestations exist but no trusted key does:
	// signatures were not checked.
	Unchecked bool
	Notes     []string
}

// Problems is the number of findings.
func (r *AttestCheckResult) Problems() int {
	n := 0
	for _, c := range r.Findings {
		n += c
	}
	return n
}

type attestChecker struct {
	st   *store.Store
	opts AttestCheckOptions
	res  *AttestCheckResult
}

func (c *attestChecker) report(f Finding) {
	c.res.Findings[f.Kind]++
	if c.opts.OnFinding != nil {
		c.opts.OnFinding(f)
	}
}

func (c *attestChecker) note(format string, args ...any) {
	c.res.Notes = append(c.res.Notes, fmt.Sprintf(format, args...))
}

// CheckAttestations checks the tamper-evidence chain: the chain in the
// database, the attestation file against it, and every attestation
// against today's database, by rebuilding its leaves. A mismatch is
// explained with the leaves file: removed messages, rows inserted into an
// attested range, changed deletions. It reads no message files. Errors
// other than findings (database, context) end it early.
func CheckAttestations(ctx context.Context, st *store.Store, opts AttestCheckOptions) (*AttestCheckResult, error) {
	if opts.File == "" {
		opts.File = filepath.Join(opts.Dir, attest.ChainFile)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	c := &attestChecker{st: st, opts: opts, res: &AttestCheckResult{Findings: map[string]int{}}}
	chain, err := st.Attestations(ctx)
	if err != nil {
		return c.res, err
	}
	c.res.Attestations = len(chain)
	file, ferr := attest.ReadFile(opts.File)
	if ferr != nil {
		c.report(Finding{Kind: FindingAttestFile, Path: opts.File, Detail: ferr.Error()})
	}
	if len(chain) == 0 && len(file) == 0 {
		if !opts.KeyExists {
			c.note("tamper evidence off: no attestation key; create one with attest keygen")
		}
		return c.res, nil
	}

	trusted := opts.PublicKeys
	if trusted == nil {
		if trusted, err = attest.LoadTrusted(opts.Dir); err != nil {
			return c.res, err
		}
	}
	if len(trusted) == 0 {
		// Still check everything else, with the keys the entries carry.
		c.res.Unchecked = true
		c.note("signatures not checked: no trusted public key (%s is missing; pass --public-key)",
			filepath.Join(opts.Dir, attest.PublicKeyFile))
		trusted = attest.Keys{}
		for _, e := range slices.Concat(chain, file) {
			trusted[e.Key] = true
		}
	}

	r := attest.ExamineChain(chain, trusted)
	for _, p := range r.Problems {
		c.report(Finding{Kind: FindingChain, Path: "database", Detail: p})
	}
	for _, seq := range r.KeyChanges {
		c.note("attestation #%d is signed with a new key (key rotation)", seq)
	}
	if ferr == nil {
		c.compareFile(file, chain, trusted)
	}
	for i := range chain {
		if err := c.checkEntry(ctx, &chain[i]); err != nil {
			return c.res, err
		}
	}

	if len(chain) > 0 {
		last := chain[len(chain)-1]
		if opts.KeyExists && opts.Interval > 0 && opts.Now().Sub(last.Time) > 2*opts.Interval {
			c.report(Finding{Kind: FindingAttestStale, Path: fmt.Sprintf("attestation #%d", last.Seq),
				Detail: fmt.Sprintf("the newest attestation is from %s; one is due every %s", last.Time.Format(time.RFC3339), opts.Interval)})
		}
		n, err := st.CountDeletionsAfter(ctx, last.DeletionsUpto)
		if err != nil {
			return c.res, err
		}
		if n > 0 {
			c.note("%d deletion(s) not yet covered by an attestation; the next one signs them", n)
		}
	}
	return c.res, nil
}

// compareFile compares the attestation file with the database chain. One
// side lagging is a note: the next attestation copies the database's
// entries to the file, and imports the file's (after a restore).
func (c *attestChecker) compareFile(file, chain []attest.Entry, trusted attest.Keys) {
	path := c.opts.File
	for i := range min(len(file), len(chain)) {
		if !file[i].Same(&chain[i]) {
			c.report(Finding{Kind: FindingAttestFile, Path: path,
				Detail: fmt.Sprintf("attestation #%d differs from the database", chain[i].Seq)})
		}
	}
	switch {
	case len(chain) > len(file):
		c.note("%s lacks %d attestation(s) from #%d that the database has; the next attestation copies them",
			path, len(chain)-len(file), len(file)+1)
	case len(file) > len(chain) && c.opts.Copy:
		c.report(Finding{Kind: FindingAttestFile, Path: path,
			Detail: fmt.Sprintf("has %d attestation(s) from #%d that the database lacks", len(file)-len(chain), len(chain)+1)})
	case len(file) > len(chain):
		for _, p := range attest.ExamineChain(file, trusted).Problems {
			c.report(Finding{Kind: FindingAttestFile, Path: path, Detail: p})
		}
		c.note("%s has %d attestation(s) from #%d that the database lacks (restored from an older backup?); "+
			"serve or the next attestation imports them", path, len(file)-len(chain), len(chain)+1)
	}
}

// checkEntry checks one attestation against today's database and its
// leaves file. Both are only hashed, as they can hold millions of lines;
// they are read into memory only to explain a mismatch.
func (c *attestChecker) checkEntry(ctx context.Context, e *attest.Entry) error {
	leavesPath := attest.LeavesPath(c.opts.Dir, e.Seq)
	leavesOK := true
	if err := checkLeavesDigest(c.opts.Dir, e); err != nil {
		c.report(Finding{Kind: FindingAttestFile, Path: leavesPath, Detail: err.Error()})
		leavesOK = false
	}
	sum, messages, deletions, err := c.rebuild(ctx, e, io.Discard)
	if err != nil || sum == e.Leaves {
		return err
	}
	var listed *attest.Leaves
	if leavesOK {
		if listed, err = readLeaves(c.opts.Dir, e); err != nil {
			c.report(Finding{Kind: FindingAttestFile, Path: leavesPath, Detail: err.Error()})
		}
	}
	if listed == nil {
		// Without the leaves file, only the counts tell what changed.
		c.report(Finding{Kind: FindingAttestation, Path: fmt.Sprintf("attestation #%d", e.Seq),
			Detail: fmt.Sprintf("no longer matches the database: it covers %d message(s) and %d deletion(s), "+
				"the database has %d and %d for its range", e.MessagesCount, e.DeletionsCount, messages, deletions)})
		return nil
	}
	var rebuilt bytes.Buffer
	if _, _, _, err := c.rebuild(ctx, e, &rebuilt); err != nil {
		return err
	}
	now, err := attest.ParseLeaves(rebuilt.Bytes())
	if err != nil {
		return err
	}
	found, err := c.diffLeaves(ctx, e, listed, now)
	if err != nil {
		return err
	}
	if !found {
		c.report(Finding{Kind: FindingAttestation, Path: fmt.Sprintf("attestation #%d", e.Seq),
			Detail: "its leaves cannot be rebuilt from the database"})
	}
	return nil
}

// rebuild writes the leaves of e as today's database has them to w.
func (c *attestChecker) rebuild(ctx context.Context, e *attest.Entry, w io.Writer) (sum string, messages, deletions int64, err error) {
	lw, err := attest.NewLeavesWriter(w)
	if err != nil {
		return "", 0, 0, err
	}
	if err := c.st.RebuildLeaves(ctx, e, lw); err != nil {
		return "", 0, 0, err
	}
	return lw.Finish()
}

// diffLeaves reports how the database (now) differs from what attestation
// e listed. It returns whether it found a difference to report.
func (c *attestChecker) diffLeaves(ctx context.Context, e *attest.Entry, listed, now *attest.Leaves) (bool, error) {
	attested := fmt.Sprintf("attested by #%d on %s", e.Seq, e.Time.Format("2006-01-02"))
	found := false
	a, b := listed.Messages, now.Messages
	for len(a) > 0 || len(b) > 0 {
		switch {
		case len(b) == 0 || (len(a) > 0 && a[0].Seq < b[0].Seq):
			// Listed, gone from its place.
			exists, err := c.st.MessageExists(ctx, a[0].SHA256)
			if err != nil {
				return found, err
			}
			if exists {
				c.note("re-archived: %s (%s as #%d) is archived again under a new number", a[0].SHA256, attested, a[0].Seq)
			} else {
				c.report(Finding{Kind: FindingRemoved, Path: a[0].SHA256, Detail: attested})
			}
			found = true
			a = a[1:]
		case len(a) == 0 || b[0].Seq < a[0].Seq:
			c.report(Finding{Kind: FindingUnattested, Path: b[0].SHA256,
				Detail: fmt.Sprintf("seq %d is inside the range of attestation #%d, which does not list it", b[0].Seq, e.Seq)})
			found = true
			b = b[1:]
		default:
			if a[0].SHA256 != b[0].SHA256 {
				c.report(Finding{Kind: FindingAttestation, Path: b[0].SHA256,
					Detail: fmt.Sprintf("seq %d holds another message than %s (%s)", b[0].Seq, a[0].SHA256, attested)})
				found = true
			}
			a, b = a[1:], b[1:]
		}
	}
	if !slices.Equal(listed.Deletions, now.Deletions) {
		c.report(Finding{Kind: FindingAttestation, Path: fmt.Sprintf("attestation #%d", e.Seq),
			Detail: fmt.Sprintf("its %d deletion(s) differ from the %d the database has for its range", len(listed.Deletions), len(now.Deletions))})
		found = true
	}
	return found, nil
}

// checkLeavesDigest hashes the leaves file of e in dir and compares it
// with the signed digest.
func checkLeavesDigest(dir string, e *attest.Entry) error {
	f, err := os.Open(attest.LeavesPath(dir, e.Seq)) //nolint:gosec // in the attestation directory
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the leaves file of attestation #%d is missing", e.Seq)
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != e.Leaves {
		return fmt.Errorf("the leaves file of attestation #%d does not match its signed digest", e.Seq)
	}
	return nil
}

// readLeaves reads and parses the leaves file of e from dir, after
// checking it against the signed digest.
func readLeaves(dir string, e *attest.Entry) (*attest.Leaves, error) {
	data, err := os.ReadFile(attest.LeavesPath(dir, e.Seq)) //nolint:gosec // in the attestation directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("the leaves file of attestation #%d is missing", e.Seq)
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != e.Leaves {
		return nil, fmt.Errorf("the leaves file of attestation #%d does not match its signed digest", e.Seq)
	}
	l, err := attest.ParseLeaves(data)
	if err != nil {
		return nil, fmt.Errorf("attestation #%d: %w", e.Seq, err)
	}
	if int64(len(l.Messages)) != e.MessagesCount || int64(len(l.Deletions)) != e.DeletionsCount {
		return nil, fmt.Errorf("the leaves file of attestation #%d does not have the signed counts", e.Seq)
	}
	return l, nil
}

// OfflineCheckResult summarizes CheckAttestFile.
type OfflineCheckResult struct {
	Attestations int
	Problems     []string
	KeyChanges   []int64
}

// CheckAttestFile checks an attestation file without a database: the
// chain, hashes, signatures and keys, every leaves file in leavesDir/leaves against its signed
// digest and counts, and each total against the previous one and the
// leaves. leavesDir is usually the directory of the file.
func CheckAttestFile(path, leavesDir string, trusted attest.Keys) (*OfflineCheckResult, error) {
	entries, err := attest.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	r := attest.ExamineChain(entries, trusted)
	res := &OfflineCheckResult{Attestations: len(entries), Problems: r.Problems, KeyChanges: r.KeyChanges}
	var prevTotal int64
	for i := range entries {
		e := &entries[i]
		l, err := readLeaves(leavesDir, e)
		if err != nil {
			res.Problems = append(res.Problems, err.Error())
		} else {
			// Rows archived since the previous attestation and deleted
			// before this one were never in its total.
			var older int64
			for _, d := range l.Deletions {
				if d.MessageSeq <= e.MessagesAfter {
					older++
				}
			}
			if want := prevTotal + e.MessagesCount - older; e.Total != want {
				res.Problems = append(res.Problems, fmt.Sprintf("attestation #%d: total %d, expected %d from the previous total and its leaves",
					e.Seq, e.Total, want))
			}
		}
		prevTotal = e.Total
	}
	return res, nil
}
