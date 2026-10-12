package archive

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/attest"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// check runs CheckAttestations on st with the setup's directory and
// returns the findings.
func (s *attestSetup) check(t *testing.T, st *store.Store, opts AttestCheckOptions) ([]Finding, *AttestCheckResult) {
	t.Helper()
	var found []Finding
	opts.Dir = s.dir
	opts.OnFinding = func(f Finding) { found = append(found, f) }
	res, err := CheckAttestations(context.Background(), st, opts)
	if err != nil {
		t.Fatal(err)
	}
	return found, res
}

// tamper returns a connection on which the deletion guards of retention
// (#55) are off, as for someone with owner rights on the database.
func (s *attestSetup) tamper(t *testing.T) *pgx.Conn {
	t.Helper()
	c := s.conn(t)
	for _, sql := range []string{
		"ALTER TABLE message_locations DISABLE TRIGGER message_locations_retention_guard",
		"ALTER TABLE messages DISABLE TRIGGER messages_tombstone_guard",
	} {
		if _, err := c.Exec(context.Background(), sql); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func (s *attestSetup) removeMessage(t *testing.T, i int) {
	t.Helper()
	c := s.tamper(t)
	for _, sql := range []string{
		"DELETE FROM message_locations WHERE message_sha256 = $1",
		"DELETE FROM messages WHERE sha256 = $1",
	} {
		if _, err := c.Exec(context.Background(), sql, attestSHA(i)); err != nil {
			t.Fatal(err)
		}
	}
}

// seqOf returns the seq of message i in the setup's database.
func (s *attestSetup) seqOf(t *testing.T, i int) int64 {
	t.Helper()
	var seq int64
	if err := s.conn(t).QueryRow(context.Background(), "SELECT seq FROM messages WHERE sha256 = $1", attestSHA(i)).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func kinds(found []Finding) []string {
	var out []string
	for _, f := range found {
		out = append(out, f.Kind)
	}
	return out
}

func TestCheckAttestationsClean(t *testing.T) {
	s := newAttestSetup(t)
	found, res := s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if len(found) != 0 || res.Attestations != 0 || len(res.Notes) != 0 {
		t.Fatalf("no attestations yet, with a key: %v %+v", found, res)
	}
	s.save(t, 1, 2)
	s.attest(t)
	s.save(t, 3)
	s.attest(t)
	found, res = s.check(t, s.st, AttestCheckOptions{KeyExists: true, Interval: time.Hour})
	if len(found) != 0 || res.Attestations != 2 || res.Unchecked || len(res.Notes) != 0 {
		t.Fatalf("clean: %v %+v", found, res)
	}

	// The job stopped: older than twice the interval.
	found, _ = s.check(t, s.st, AttestCheckOptions{KeyExists: true, Interval: time.Hour,
		Now: func() time.Time { return time.Now().Add(3 * time.Hour) }})
	if !slices.Equal(kinds(found), []string{FindingAttestStale}) {
		t.Fatalf("stale: %v", found)
	}
	// Without a key, no schedule is expected.
	found, _ = s.check(t, s.st, AttestCheckOptions{Interval: time.Hour,
		Now: func() time.Time { return time.Now().Add(3 * time.Hour) }})
	if len(found) != 0 {
		t.Fatalf("stale without a key: %v", found)
	}
}

func TestCheckAttestationsOff(t *testing.T) {
	s := newAttestSetup(t)
	found, res := s.check(t, s.st, AttestCheckOptions{})
	if len(found) != 0 || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "tamper evidence off") {
		t.Fatalf("off: %v %+v", found, res)
	}
}

// The "Done when" case of #66: a removed message is named, with the
// attestation that covered it, and nothing else is reported.
func TestCheckAttestationsRemoved(t *testing.T) {
	s := newAttestSetup(t)
	s.save(t, 1, 2, 3)
	e := s.attest(t).Entry
	s.removeMessage(t, 2)
	found, _ := s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if len(found) != 1 || found[0].Kind != FindingRemoved || found[0].Path != attestSHA(2) ||
		!strings.Contains(found[0].Detail, "attested by #1 on "+e.Time.Format("2006-01-02")) {
		t.Fatalf("removed: %+v", found)
	}

	// The leaves file is gone too: the mismatch is still reported, only
	// without the hash.
	if err := os.Remove(attest.LeavesPath(s.dir, 1)); err != nil {
		t.Fatal(err)
	}
	found, _ = s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if !slices.Equal(kinds(found), []string{FindingAttestFile, FindingAttestation}) ||
		!strings.Contains(found[1].Detail, "covers 3 message(s) and 0 deletion(s), the database has 2 and 0") {
		t.Fatalf("no leaves file: %+v", found)
	}
}

func TestCheckAttestationsFileAndRows(t *testing.T) {
	s := newAttestSetup(t)
	s.save(t, 1)
	s.save(t, 1, 2) // the duplicate draws a number that stays unused
	s.attest(t)
	seq1, seq2 := s.seqOf(t, 1), s.seqOf(t, 2)
	if seq2-seq1 < 2 {
		t.Fatalf("no gap between %d and %d", seq1, seq2)
	}

	// A row inserted into the attested range afterwards.
	c := s.tamper(t)
	if _, err := c.Exec(context.Background(), `INSERT INTO messages (seq, sha256, size, stored_path)
		OVERRIDING SYSTEM VALUE VALUES ($1, $2, 1, 'x')`, seq1+1, attestSHA(9)); err != nil {
		t.Fatal(err)
	}
	found, _ := s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if len(found) != 1 || found[0].Kind != FindingUnattested || found[0].Path != attestSHA(9) {
		t.Fatalf("unattested: %+v", found)
	}
	if _, err := c.Exec(context.Background(), "DELETE FROM messages WHERE sha256 = $1", attestSHA(9)); err != nil {
		t.Fatal(err)
	}

	// One changed byte in the attestation file.
	good, err := os.ReadFile(s.chainFile())
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(good), `"total":2`, `"total":3`, 1)
	if err := os.WriteFile(s.chainFile(), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	found, _ = s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if len(found) != 1 || found[0].Kind != FindingAttestFile || !strings.Contains(found[0].Detail, "#1 differs") {
		t.Fatalf("changed file: %+v", found)
	}
	// A line that does not parse.
	_ = os.WriteFile(s.chainFile(), append(good, "garbage\n"...), 0o600)
	found, _ = s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if len(found) != 1 || found[0].Kind != FindingAttestFile || !strings.Contains(found[0].Detail, "line 2") {
		t.Fatalf("garbage: %+v", found)
	}
	_ = os.WriteFile(s.chainFile(), good, 0o600)

	// A changed leaves file.
	leaves := attest.LeavesPath(s.dir, 1)
	lb, _ := os.ReadFile(leaves)
	_ = os.WriteFile(leaves, append(lb, '\n'), 0o600)
	found, _ = s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if len(found) != 1 || found[0].Kind != FindingAttestFile || !strings.Contains(found[0].Detail, "signed digest") {
		t.Fatalf("changed leaves: %+v", found)
	}
	_ = os.WriteFile(leaves, lb, 0o600)

	// The file lags (a crash before the append): a note, no finding. A
	// copy kept elsewhere that is ahead of the database is a finding.
	dropLastLine(t, s.chainFile())
	found, res := s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if len(found) != 0 || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "the next attestation copies them") {
		t.Fatalf("file lags: %v %+v", found, res)
	}
	other := storetest.New(t)
	_ = os.WriteFile(s.chainFile(), good, 0o600)
	found, _ = s.check(t, other, AttestCheckOptions{KeyExists: true, Copy: true})
	if len(found) != 1 || found[0].Kind != FindingAttestFile || !strings.Contains(found[0].Detail, "the database lacks") {
		t.Fatalf("copy ahead: %+v", found)
	}
}

// Deletions recorded with the delete (as retention does; here by hand,
// past its guards) are not findings; a delete without a record is.
func TestCheckAttestationsDeletions(t *testing.T) {
	s := newAttestSetup(t)
	s.save(t, 1, 2, 3)
	s.attest(t)
	c := s.tamper(t)
	tx, err := c.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"INSERT INTO message_deletions (message_seq, sha256, reason) SELECT seq, sha256, 'retention' FROM messages WHERE sha256 = $1",
		"DELETE FROM message_locations WHERE message_sha256 = $1",
		"DELETE FROM messages WHERE sha256 = $1",
	} {
		if _, err := tx.Exec(context.Background(), sql, attestSHA(1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	found, res := s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if len(found) != 0 || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "1 deletion(s) not yet covered") {
		t.Fatalf("recorded deletion: %v %+v", found, res)
	}
	if e := s.attest(t).Entry; e.DeletionsCount != 1 || e.Total != 2 {
		t.Fatalf("second attestation: %+v", e)
	}
	found, res = s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if len(found) != 0 || len(res.Notes) != 0 {
		t.Fatalf("signed deletion: %v %+v", found, res)
	}

	s.removeMessage(t, 3)
	found, _ = s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if len(found) != 1 || found[0].Kind != FindingRemoved || found[0].Path != attestSHA(3) {
		t.Fatalf("unrecorded delete: %+v", found)
	}
}

func TestCheckAttestationsKeys(t *testing.T) {
	s := newAttestSetup(t)
	s.save(t, 1)
	s.attest(t)

	// Only the given keys are trusted: another key reports every entry.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	found, _ := s.check(t, s.st, AttestCheckOptions{KeyExists: true, PublicKeys: attest.Keys{attest.EncodeKey(otherPub): true}})
	if !slices.Equal(kinds(found), []string{FindingChain}) || !strings.Contains(found[0].Detail, "not trusted") {
		t.Fatalf("untrusted: %+v", found)
	}
	pub := attest.EncodeKey(s.key.Public().(ed25519.PublicKey))
	if found, _ := s.check(t, s.st, AttestCheckOptions{KeyExists: true, PublicKeys: attest.Keys{pub: true}}); len(found) != 0 {
		t.Fatalf("trusted: %+v", found)
	}

	// Rotation: the old key stays trusted through retired-keys.pem, and
	// the change is a note.
	keyFile := filepath.Join(s.dir, attest.DefaultKeyFile)
	if _, _, err := attest.Rotate(keyFile, s.dir); err != nil {
		t.Fatal(err)
	}
	key, err := attest.LoadSigner(keyFile, s.dir)
	if err != nil {
		t.Fatal(err)
	}
	s.key = key
	s.attest(t)
	found, res := s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if len(found) != 0 || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "#2 is signed with a new key") {
		t.Fatalf("rotation: %v %+v", found, res)
	}

	// No trusted key at all: the rest is checked, the result is
	// incomplete.
	for _, f := range []string{attest.PublicKeyFile, attest.RetiredKeysFile} {
		if err := os.Remove(filepath.Join(s.dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	s.removeMessage(t, 1)
	found, res = s.check(t, s.st, AttestCheckOptions{KeyExists: true})
	if !res.Unchecked || !slices.Equal(kinds(found), []string{FindingRemoved}) {
		t.Fatalf("no trusted key: %v %+v", found, res)
	}
}

// A database restored from a dump older than the attestation file: the
// catch-up imports the newer attestations, moves the sequence past them
// and renumbers rows archived meanwhile; the check then names exactly the
// messages the restore lost.
func TestAttestRestore(t *testing.T) {
	ctx := context.Background()
	s := newAttestSetup(t)
	s.save(t, 1, 2)
	s.attest(t)
	s.save(t, 3, 4)
	s.attest(t) // the dump below lacks this one and messages 3 and 4

	// The restored database: as at the dump, then message 5 archived
	// before the catch-up ran. It drew the number message 3 had.
	restored, url := storetest.NewWithURL(t)
	r := &attestSetup{st: restored, url: url, dir: s.dir, key: s.key}
	acc := &store.Account{Name: "a", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}}
	if err := restored.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	f, err := restored.GetOrCreateFolder(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	r.folder = f.ID
	r.save(t, 1, 2)
	if s.seqOf(t, 2) != r.seqOf(t, 2) {
		t.Fatal("the restored database numbers differently")
	}
	r.save(t, 5)
	seq3, seq5 := s.seqOf(t, 3), r.seqOf(t, 5)
	if seq5 != seq3 {
		t.Fatalf("message 5 got %d, message 3 had %d", seq5, seq3)
	}

	res, err := CatchUpAttestations(ctx, restored, s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 2 || res.Renumbered != 1 || res.CaughtUp != 0 {
		t.Fatalf("catch-up: %+v", res)
	}
	seq4 := s.seqOf(t, 4)
	if got := r.seqOf(t, 5); got <= seq4 {
		t.Fatalf("message 5 renumbered to %d, not past %d", got, seq4)
	}
	r.save(t, 6)
	if got := r.seqOf(t, 6); got <= seq4 {
		t.Fatalf("a new message got %d, inside the imported range", got)
	}

	found, _ := r.check(t, restored, AttestCheckOptions{KeyExists: true})
	if !slices.Equal(kinds(found), []string{FindingRemoved, FindingRemoved}) ||
		found[0].Path != attestSHA(3) || found[1].Path != attestSHA(4) ||
		!strings.Contains(found[0].Detail, "attested by #2") {
		t.Fatalf("after restore: %+v", found)
	}

	// Message 3 comes back (sync or import): a note, not a finding.
	r.save(t, 3)
	found, chk := r.check(t, restored, AttestCheckOptions{KeyExists: true})
	if len(found) != 1 || found[0].Path != attestSHA(4) ||
		!slices.ContainsFunc(chk.Notes, func(n string) bool { return strings.HasPrefix(n, "re-archived: "+attestSHA(3)) }) {
		t.Fatalf("re-archived: %+v %+v", found, chk)
	}

	// The chain goes on in the restored database, covering 5, 6 and 3.
	if e := r.attest(t).Entry; e.Seq != 3 || e.MessagesCount != 3 {
		t.Fatalf("next attestation: %+v", e)
	}
	r.checkFileMatchesDB(t)
}

func TestCheckAttestFileOffline(t *testing.T) {
	s := newAttestSetup(t)
	s.save(t, 1, 2)
	s.attest(t)
	s.save(t, 3)
	s.attest(t)
	trusted, err := attest.LoadTrusted(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := CheckAttestFile(s.chainFile(), s.dir, trusted)
	if err != nil || len(res.Problems) != 0 || res.Attestations != 2 {
		t.Fatalf("offline: %+v %v", res, err)
	}

	// A removal without a deletion record shows in the totals.
	s.removeMessage(t, 1)
	s.attest(t)
	res, err = CheckAttestFile(s.chainFile(), s.dir, trusted)
	if err != nil || len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "#3: total 2, expected 3") {
		t.Fatalf("removal in the totals: %+v %v", res, err)
	}

	if err := os.Remove(attest.LeavesPath(s.dir, 2)); err != nil {
		t.Fatal(err)
	}
	res, err = CheckAttestFile(s.chainFile(), s.dir, trusted)
	if err != nil || len(res.Problems) != 2 || !strings.Contains(res.Problems[0], "#2 is missing") {
		t.Fatalf("no leaves: %+v %v", res, err)
	}
	if _, err := CheckAttestFile(filepath.Join(s.dir, "nope.jsonl"), s.dir, trusted); err == nil {
		t.Fatal("a missing file is no error")
	}
}
