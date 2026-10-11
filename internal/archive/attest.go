package archive

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/pklnx/mail-archive/internal/attest"
	"github.com/pklnx/mail-archive/internal/store"
)

// ErrAttestRunning is returned by Attest while another attestation is
// written.
var ErrAttestRunning = errors.New("an attestation is being written by another process")

// ErrAttestMismatch is returned when the attestation file and the database
// disagree, or the database chain is broken. Nothing is written then.
var ErrAttestMismatch = errors.New("the attestation file and the database disagree")

// AttestResult tells what Attest wrote.
type AttestResult struct {
	Entry attest.Entry
	// CaughtUp counts attestations that were in the database but not in
	// the file (a crash between the two writes) and were copied to it.
	CaughtUp int
}

// Attest writes an attestation: it covers the messages archived and the
// deletions recorded since the previous one, is chained to it and signed
// with key. It is stored in the database and appended to the attestation
// file in dir, with its leaves file. Before, entries the file lacks are
// copied from the database. dir is the attestation directory
// (<data dir>/attest).
func Attest(ctx context.Context, st *store.Store, dir string, key ed25519.PrivateKey) (AttestResult, error) {
	var res AttestResult
	unlock, ok, err := st.TryLockAttest(ctx)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, ErrAttestRunning
	}
	defer unlock()

	trusted, err := attest.LoadTrusted(dir)
	if err != nil {
		return res, err
	}
	if !trusted[attest.EncodeKey(key.Public().(ed25519.PublicKey))] {
		return res, errors.New("the signing key is not trusted: its public key is not in " + attest.PublicKeyFile)
	}
	chain, n, err := catchUp(ctx, st, dir, trusted)
	res.CaughtUp = n
	if err != nil {
		return res, err
	}
	var prev *attest.Entry
	if len(chain) > 0 {
		prev = &chain[len(chain)-1]
	}

	leaves, err := attest.CreateLeaves(dir)
	if err != nil {
		return res, err
	}
	defer leaves.Abort()
	e := attest.Next(prev)
	err = st.WithAttestSnapshot(ctx, func(snap *store.AttestSnapshot) error {
		// A deleted newest row can make the maximum smaller than where the
		// previous range ended; the range then stays empty.
		e.MessagesUpto = max(e.MessagesAfter, snap.Bounds.MessagesUpto)
		e.DeletionsUpto = max(e.DeletionsAfter, snap.Bounds.DeletionsUpto)
		e.Total = snap.Bounds.Total
		e.Time = time.Now()
		if err := snap.Messages(ctx, e.MessagesAfter, e.MessagesUpto, leaves.Message); err != nil {
			return err
		}
		if err := snap.Deletions(ctx, e.DeletionsAfter, e.DeletionsUpto, leaves.Deletion); err != nil {
			return err
		}
		sum, messages, deletions, err := leaves.Finish()
		if err != nil {
			return err
		}
		e.Leaves, e.MessagesCount, e.DeletionsCount = sum, messages, deletions
		e.Sign(key)
		return snap.Insert(ctx, &e)
	})
	if err != nil {
		return res, err
	}
	// From here on, a failure leaves the database one entry ahead; the
	// next run copies it to the file.
	res.Entry = e
	if err := leaves.Commit(e.Seq); err != nil {
		return res, err
	}
	return res, attest.AppendFile(filepath.Join(dir, attest.ChainFile), &e)
}

// catchUp checks the database chain and compares the attestation file
// with it. Entries only in the database are appended to the file, with a
// leaves file rebuilt from the database. Any other difference is an
// ErrAttestMismatch, and nothing is written. It returns the database
// chain and how many entries were appended.
func catchUp(ctx context.Context, st *store.Store, dir string, trusted attest.Keys) ([]attest.Entry, int, error) {
	if err := attest.RemoveStaleLeaves(dir); err != nil {
		return nil, 0, err
	}
	chain, err := st.Attestations(ctx)
	if err != nil {
		return nil, 0, err
	}
	if err := attest.CheckChain(chain, trusted); err != nil {
		return nil, 0, fmt.Errorf("%w: database chain: %w", ErrAttestMismatch, err)
	}
	path := filepath.Join(dir, attest.ChainFile)
	file, err := attest.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrAttestMismatch, err)
	}
	for i := range min(len(file), len(chain)) {
		if !file[i].Same(&chain[i]) {
			return nil, 0, fmt.Errorf("%w: attestation #%d differs between %s and the database", ErrAttestMismatch, chain[i].Seq, path)
		}
	}
	if len(file) > len(chain) {
		return nil, 0, fmt.Errorf("%w: %s has %d attestation(s) the database does not have, from #%d "+
			"(was the database restored from an older backup?)", ErrAttestMismatch, path, len(file)-len(chain), len(chain)+1)
	}
	for i := len(file); i < len(chain); i++ {
		e := &chain[i]
		if err := rebuildLeaves(ctx, st, dir, e); err != nil {
			return nil, i - len(file), err
		}
		if err := attest.AppendFile(path, e); err != nil {
			return nil, i - len(file), err
		}
	}
	return chain, len(chain) - len(file), nil
}

// rebuildLeaves writes the leaves file of e from the database. It fails
// with ErrAttestMismatch if the result does not match e's digest.
func rebuildLeaves(ctx context.Context, st *store.Store, dir string, e *attest.Entry) error {
	leaves, err := attest.CreateLeaves(dir)
	if err != nil {
		return err
	}
	defer leaves.Abort()
	if err := st.RebuildLeaves(ctx, e, leaves.LeavesWriter); err != nil {
		return err
	}
	sum, _, _, err := leaves.Finish()
	if err != nil {
		return err
	}
	if sum != e.Leaves {
		return fmt.Errorf("%w: the leaves of attestation #%d cannot be rebuilt from the database "+
			"(were messages removed?)", ErrAttestMismatch, e.Seq)
	}
	return leaves.Commit(e.Seq)
}

// Attester writes attestations on a schedule, for the web server.
type Attester struct {
	Store *store.Store
	Dir   string // attestation directory
	Key   ed25519.PrivateKey
	// Interval: an attestation is written when the newest one is at least
	// this old, or there is none.
	Interval time.Duration
	// CheckEvery is how often that is checked (default ten minutes).
	CheckEvery time.Duration
	Logger     *slog.Logger
}

// Run checks at once and then every CheckEvery until ctx ends.
func (a *Attester) Run(ctx context.Context) {
	every := a.CheckEvery
	if every <= 0 {
		every = 10 * time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := a.RunOnce(ctx); err != nil && ctx.Err() == nil {
			a.Logger.Error("attestation failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce writes an attestation if one is due.
func (a *Attester) RunOnce(ctx context.Context) error {
	last, err := a.Store.LastAttestation(ctx)
	if err != nil {
		return err
	}
	if last != nil && time.Since(last.Time) < a.Interval {
		return nil
	}
	res, err := Attest(ctx, a.Store, a.Dir, a.Key)
	if errors.Is(err, ErrAttestRunning) {
		a.Logger.Debug("attestation skipped", "reason", err)
		return nil
	}
	if res.CaughtUp > 0 {
		a.Logger.Info("copied attestations from the database to the file", "count", res.CaughtUp)
	}
	if err != nil {
		return err
	}
	a.Logger.Info("attestation written", "seq", res.Entry.Seq, "messages", res.Entry.MessagesCount,
		"deletions", res.Entry.DeletionsCount, "total", res.Entry.Total)
	return nil
}
