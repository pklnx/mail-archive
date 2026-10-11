package archive

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/attest"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

type attestSetup struct {
	st     *store.Store
	url    string
	dir    string
	key    ed25519.PrivateKey
	folder int64
	uid    uint32
}

func newAttestSetup(t *testing.T) *attestSetup {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	dir := filepath.Join(t.TempDir(), "attest")
	keyFile := filepath.Join(dir, attest.DefaultKeyFile)
	if _, err := attest.Keygen(keyFile, dir); err != nil {
		t.Fatal(err)
	}
	key, err := attest.LoadSigner(keyFile, dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	acc := &store.Account{Name: "a", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}}
	if err := st.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	f, err := st.GetOrCreateFolder(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	return &attestSetup{st: st, url: url, dir: dir, key: key, folder: f.ID}
}

func attestSHA(i int) string { return fmt.Sprintf("%064x", i) }

// save archives messages i (by SaveBatch, as sync does), each with a new
// location.
func (s *attestSetup) save(t *testing.T, ids ...int) {
	t.Helper()
	var metas []store.MessageMeta
	var locs []store.Location
	for _, i := range ids {
		s.uid++
		metas = append(metas, store.MessageMeta{SHA256: attestSHA(i), Size: 1, StoredPath: "x"})
		locs = append(locs, store.Location{FolderID: s.folder, UID: s.uid})
	}
	if _, err := s.st.SaveBatch(context.Background(), s.folder, s.uid, metas, locs); err != nil {
		t.Fatal(err)
	}
}

func (s *attestSetup) attest(t *testing.T) AttestResult {
	t.Helper()
	res, err := Attest(context.Background(), s.st, s.dir, s.key)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (s *attestSetup) conn(t *testing.T) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), s.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func (s *attestSetup) chainFile() string { return filepath.Join(s.dir, attest.ChainFile) }

func (s *attestSetup) checkFileMatchesDB(t *testing.T) {
	t.Helper()
	db, err := s.st.Attestations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	file, err := attest.ReadFile(s.chainFile())
	if err != nil {
		t.Fatal(err)
	}
	if len(file) != len(db) {
		t.Fatalf("file has %d entries, database %d", len(file), len(db))
	}
	trusted, _ := attest.LoadTrusted(s.dir)
	if err := attest.CheckChain(file, trusted); err != nil {
		t.Fatal(err)
	}
	for i := range db {
		if !file[i].Same(&db[i]) {
			t.Fatalf("entry %d differs", i+1)
		}
		b, err := os.ReadFile(attest.LeavesPath(s.dir, db[i].Seq))
		if err != nil {
			t.Fatal(err)
		}
		if got := hashHex(b); got != db[i].Leaves {
			t.Fatalf("leaves file of #%d: digest %s, signed %s", db[i].Seq, got, db[i].Leaves)
		}
	}
}

// leafLines returns the message lines of a leaves file.
func leafLines(t *testing.T, dir string, seq int64) []string {
	t.Helper()
	f, err := os.Open(attest.LeavesPath(dir, seq))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "+ ") {
			out = append(out, sc.Text())
		}
	}
	return out
}

func TestAttestChain(t *testing.T) {
	s := newAttestSetup(t)
	s.save(t, 1, 2)
	first := s.attest(t).Entry
	if first.Seq != 1 || first.MessagesCount != 2 || first.Total != 2 || first.Prev != attest.ZeroHash || first.MessagesAfter != 0 {
		t.Fatalf("first %+v", first)
	}
	if lines := leafLines(t, s.dir, 1); len(lines) != 2 || !strings.HasSuffix(lines[1], attestSHA(2)) {
		t.Fatalf("leaves %v", lines)
	}

	// Nothing new: an entry anyway, with an empty range.
	second := s.attest(t).Entry
	if second.Seq != 2 || second.MessagesCount != 0 || second.MessagesAfter != first.MessagesUpto ||
		second.MessagesUpto != first.MessagesUpto || second.Prev != first.Hash || second.Total != 2 {
		t.Fatalf("second %+v", second)
	}

	// A second location for an existing message is no new message.
	s.save(t, 1)
	if e := s.attest(t).Entry; e.MessagesCount != 0 || e.Total != 2 {
		t.Fatalf("duplicate: %+v", e)
	}
	s.save(t, 3)
	if e := s.attest(t).Entry; e.MessagesCount != 1 || e.Total != 3 {
		t.Fatalf("new message: %+v", e)
	}
	s.checkFileMatchesDB(t)
	for _, p := range []string{s.chainFile(), attest.LeavesPath(s.dir, 1)} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", p, fi.Mode(), err)
		}
	}
}

func TestAttestCatchUp(t *testing.T) {
	s := newAttestSetup(t)
	s.save(t, 1, 2)
	s.attest(t)
	s.save(t, 3)
	s.attest(t)
	leaves2, err := os.ReadFile(attest.LeavesPath(s.dir, 2))
	if err != nil {
		t.Fatal(err)
	}

	// A crash between the database and the file: the last line and its
	// leaves file are missing. The next run copies them over.
	dropLastLine(t, s.chainFile())
	if err := os.Remove(attest.LeavesPath(s.dir, 2)); err != nil {
		t.Fatal(err)
	}
	res := s.attest(t)
	if res.CaughtUp != 1 || res.Entry.Seq != 3 {
		t.Fatalf("catch-up %+v", res)
	}
	if got, err := os.ReadFile(attest.LeavesPath(s.dir, 2)); err != nil || string(got) != string(leaves2) {
		t.Fatalf("rebuilt leaves differ: %v", err)
	}
	s.checkFileMatchesDB(t)
}

func TestAttestRefusesMismatch(t *testing.T) {
	s := newAttestSetup(t)
	s.save(t, 1)
	s.attest(t)
	s.save(t, 2)
	s.attest(t)
	good, err := os.ReadFile(s.chainFile())
	if err != nil {
		t.Fatal(err)
	}
	refused := func(name, want string) {
		t.Helper()
		before, _ := s.st.CountAttestations(context.Background())
		_, err := Attest(context.Background(), s.st, s.dir, s.key)
		if !errors.Is(err, ErrAttestMismatch) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want a mismatch naming %q", name, err, want)
		}
		if after, _ := s.st.CountAttestations(context.Background()); after != before {
			t.Errorf("%s: an attestation was written", name)
		}
		if err := os.WriteFile(s.chainFile(), good, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// One changed byte in a line.
	changed := strings.Replace(string(good), `"total":1`, `"total":7`, 1)
	if changed == string(good) {
		t.Fatal("no total to change")
	}
	_ = os.WriteFile(s.chainFile(), []byte(changed), 0o600)
	refused("changed line", "#1 differs")

	_ = os.WriteFile(s.chainFile(), append(good, "garbage\n"...), 0o600)
	refused("garbage line", "line 3")

	// The file is ahead (a restore), but an entry to import has no leaves
	// file: nothing is imported. Here: a new database next to the same
	// attestation directory.
	other := storetest.New(t)
	leaves2, _ := os.ReadFile(attest.LeavesPath(s.dir, 2))
	_ = os.Remove(attest.LeavesPath(s.dir, 2))
	if _, err := Attest(context.Background(), other, s.dir, s.key); !errors.Is(err, ErrAttestMismatch) ||
		!strings.Contains(err.Error(), "cannot import attestation #2") {
		t.Errorf("file ahead: %v", err)
	}
	if n, _ := other.CountAttestations(context.Background()); n != 0 {
		t.Errorf("%d attestation(s) imported", n)
	}
	_ = os.WriteFile(attest.LeavesPath(s.dir, 2), leaves2, 0o600)

	// A message removed while the file lagged: its leaves cannot be
	// rebuilt, nothing is written.
	dropLastLine(t, s.chainFile())
	good, _ = os.ReadFile(s.chainFile())
	c := s.conn(t)
	for _, sql := range []string{
		"DELETE FROM message_locations WHERE message_sha256 = $1",
		"DELETE FROM messages WHERE sha256 = $1",
	} {
		if _, err := c.Exec(context.Background(), sql, attestSHA(2)); err != nil {
			t.Fatal(err)
		}
	}
	refused("removed message", "cannot be rebuilt")
}

func TestAttestKeyAndLock(t *testing.T) {
	s := newAttestSetup(t)
	other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	if _, err := Attest(context.Background(), s.st, s.dir, other); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Errorf("untrusted key: %v", err)
	}
	unlock, ok, err := s.st.TryLockAttest(context.Background())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := Attest(context.Background(), s.st, s.dir, s.key); !errors.Is(err, ErrAttestRunning) {
		t.Errorf("locked: %v", err)
	}
	unlock()

	// After a rotation the chain goes on, old entries stay trusted.
	s.save(t, 1)
	s.attest(t)
	keyFile := filepath.Join(s.dir, attest.DefaultKeyFile)
	if _, _, err := attest.Rotate(keyFile, s.dir); err != nil {
		t.Fatal(err)
	}
	if s.key, err = attest.LoadSigner(keyFile, s.dir); err != nil {
		t.Fatal(err)
	}
	if e := s.attest(t).Entry; e.Seq != 2 {
		t.Fatalf("after rotation %+v", e)
	}
	s.checkFileMatchesDB(t)
}

func TestAttesterRunOnce(t *testing.T) {
	s := newAttestSetup(t)
	a := &Attester{Store: s.st, Dir: s.dir, Key: s.key, Interval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for range 2 {
		if err := a.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.st.CountAttestations(context.Background()); n != 1 {
		t.Errorf("%d attestations, want 1 (the second was not due)", n)
	}
}

// TestAttestConcurrentWriters runs writers that keep their transaction
// open for a while after the insert, next to attestations. Every message
// must be in exactly one leaves file.
func TestAttestConcurrentWriters(t *testing.T) {
	s := newAttestSetup(t)
	ctx := context.Background()
	const writers, perWriter = 20, 5
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter+100)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := pgx.Connect(ctx, s.url)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = c.Close(ctx) }()
			for i := range perWriter {
				err := pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
					// As inWriteTx does.
					if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1, 0)", 0x6d64); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, "INSERT INTO messages (sha256, size, stored_path) VALUES ($1, 1, 'x')",
						attestSHA(1000+w*perWriter+i)); err != nil {
						return err
					}
					time.Sleep(time.Duration(rand.IntN(40)) * time.Millisecond)
					return nil
				})
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	stop := make(chan struct{})
	attested := make(chan struct{})
	go func() {
		defer close(attested)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := Attest(ctx, s.st, s.dir, s.key); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(stop)
	<-attested
	s.attest(t) // covers whatever came after the last one
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	chain, err := s.st.Attestations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, e := range chain {
		for _, l := range leafLines(t, s.dir, e.Seq) {
			seen[l[strings.LastIndexByte(l, ' ')+1:]]++
		}
	}
	if len(seen) != writers*perWriter {
		t.Errorf("%d messages attested, want %d (over %d attestations)", len(seen), writers*perWriter, len(chain))
	}
	for sha, n := range seen {
		if n != 1 {
			t.Errorf("%s attested %d times", sha, n)
		}
	}
	if last := chain[len(chain)-1]; last.Total != writers*perWriter {
		t.Errorf("total %d", last.Total)
	}
	s.checkFileMatchesDB(t)
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func dropLastLine(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.TrimSuffix(string(b), "\n")
	i := strings.LastIndexByte(s, '\n')
	if err := os.WriteFile(path, []byte(s[:i+1]), 0o600); err != nil {
		t.Fatal(err)
	}
}
