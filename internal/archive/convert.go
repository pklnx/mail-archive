package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
)

// Batch limits of an offline conversion (#392, part 3): a batch ends after
// this many files or bytes, whichever comes first. The extra disk space a
// conversion needs is at most one batch.
const (
	ConvertBatchFiles = 200
	ConvertBatchBytes = 256 << 20
)

// ConvertOptions configures Enable and Disable.
type ConvertOptions struct {
	DataDir string
	KEK     []byte // the configured archive key
	// Out receives the progress lines; nil discards them.
	Out    io.Writer
	Logger *slog.Logger
	Now    func() time.Time // default time.Now

	// BatchFiles and BatchBytes override the batch limits (tests).
	BatchFiles int
	BatchBytes int64
	// AfterBatch, for tests, runs after each committed batch; an error
	// ends the run as an interruption would.
	AfterBatch func(batch int) error
	// Tick drives the check of the writers lock's connection (default:
	// every store.WritersPing); OnLost, for tests, is called after a lost
	// lock cancelled the run.
	Tick   <-chan time.Time
	OnLost func(error)
}

// ConvertResult is what Enable or Disable did.
type ConvertResult struct {
	// State is the state at the end: store.BlobOn, store.BlobOff, or the
	// transitional state of an unfinished run.
	State string
	// Unchanged is set when the state already was the target: nothing
	// was converted.
	Unchanged bool
	Converted int64 // files converted by this run
	// Corrupt lists the files of the last pass that could not be
	// converted (relative paths).
	Corrupt []string
	// MovedAside lists the encrypted files Disable could not decrypt and
	// moved from messages/ to orphans/<time>/.
	MovedAside []string
	// MovedTo is that orphans/<time> directory.
	MovedTo string
	// TempMoved lists the files moved from tmp/ to orphans/<time>/tmp/
	// at the start (converted there with the orphans by Enable).
	TempMoved []string
	// Passes is the number of passes over the files this run made.
	Passes int
}

// ErrBlobLockHeld means verify, a sweep, a purge or a retention run holds
// the blob lock, so a conversion cannot start.
var ErrBlobLockHeld = errors.New("verify, a retention sweep or a purge is running; start again when it has finished")

// ErrRekeyInProgress means a rekey has not finished; a conversion cannot
// start before it.
var ErrRekeyInProgress = errors.New("a rekey is in progress; run `encryption rekey` to finish it first")

// Enable turns encryption at rest on: it encrypts every message file in
// messages/ and orphans/, in the foreground, and resumes an interrupted
// run (#392, part 3; maintainer decision 5). It runs only while no
// writer runs: it takes the writers lock exclusively and the blob lock
// exclusively, without waiting, and refuses otherwise.
//
// Starting from off, it first writes the keyring file with a new
// generation and records the state encrypting with that keyring as the
// database copy, so that a database copy recorded off before can no
// longer match the file. From decrypting it changes direction (the
// cursor starts again). The state becomes on only after a complete pass
// from the start that found nothing left to convert.
func Enable(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts ConvertOptions) (*ConvertResult, error) {
	return convertArchive(ctx, st, blobs, opts, true)
}

// Disable turns encryption at rest off: it decrypts every message file in
// messages/ and orphans/, as Enable encrypts them. An encrypted file in
// messages/ that cannot be decrypted (damaged, or under a data key the
// keyring lacks) is moved to orphans/<time>/, so that no encrypted file
// is left in messages/; without that, a later change of the keyring file
// would make the next writer derive the state encrypting from it. The
// state becomes off (store.ClearBlobEncryption) after a complete pass
// from the start; the keyring file stays and matches the database copy,
// so that writers do not walk the message files at start.
func Disable(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts ConvertOptions) (*ConvertResult, error) {
	return convertArchive(ctx, st, blobs, opts, false)
}

// defaults fills in the unset options.
func (opts *ConvertOptions) defaults() {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.BatchFiles <= 0 {
		opts.BatchFiles = ConvertBatchFiles
	}
	if opts.BatchBytes <= 0 {
		opts.BatchBytes = ConvertBatchBytes
	}
}

// offline is the set of locks an offline command (`encryption enable`,
// `disable`, `rekey`) holds for its whole run.
type offline struct {
	// ctx ends when the writers lock's connection fails; its cause is the
	// lost lock.
	ctx     context.Context
	cancel  context.CancelCauseFunc
	lock    *store.WritersLock
	ticker  *time.Ticker
	unlock  func() // the keyring lock
	orphans string // orphans/<time> of this run
}

// beginOffline takes the locks of an offline command: the writers lock
// and the blob lock exclusively, without waiting (refused while a writer,
// verify, a sweep or a purge runs), a watch of the lock's connection, and
// the keyring lock. Then it moves the files left in tmp/ aside into
// res.TempMoved. The caller calls close.
func beginOffline(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts ConvertOptions, command string, res *ConvertResult) (_ *offline, err error) {
	o := &offline{}
	defer func() {
		if err != nil {
			o.close()
		}
	}()
	if o.lock, err = st.TryLockWritersExclusive(ctx, command); err != nil {
		return nil, err
	}
	// The blob lock on the writers lock's connection, released with it.
	if ok, err := o.lock.TryBlobLock(ctx); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrBlobLockHeld
	}
	// A session lock ends with its session: when the lock connection
	// fails, the run stops at the next file, as a writer stops (review
	// round 1 of #513, note 1).
	o.ctx, o.cancel = context.WithCancelCause(ctx)
	tick := opts.Tick
	if tick == nil {
		o.ticker = time.NewTicker(store.WritersPing)
		tick = o.ticker.C
	}
	o.lock.Watch(tick, func(err error) {
		opts.Logger.Error("encryption at rest: the conversion stops", "err", err)
		o.cancel(err)
		if opts.OnLost != nil {
			opts.OnLost(err)
		}
	})
	// The keyring lock before any state change: no other encryption
	// command changes the keyring while this runs.
	if o.unlock, err = blobcrypt.LockKeyring(opts.DataDir); err != nil {
		return nil, err
	}
	// No writer runs, so a file in tmp/ belongs to no write in progress:
	// it goes to orphans/<time>/tmp/, where it is converted with the
	// orphans (review round 1 of #513, A1).
	o.orphans = blobstore.OrphansDir + "/" + opts.Now().UTC().Format("20060102T150405Z")
	if res.TempMoved, err = blobs.MoveTempAside(o.orphans); err != nil {
		return nil, err
	}
	if len(res.TempMoved) > 0 {
		fmt.Fprintf(opts.Out, "moved %d file(s) left in tmp/ by an interrupted write to %s/tmp/\n", len(res.TempMoved), o.orphans)
	}
	return o, nil
}

// close releases the locks.
func (o *offline) close() {
	if o.unlock != nil {
		o.unlock()
	}
	if o.cancel != nil {
		o.cancel(nil)
	}
	if o.ticker != nil {
		o.ticker.Stop()
	}
	if o.lock != nil {
		o.lock.Release()
	}
}

// runErr is err of a run in o, or the lost lock that ended it.
func (o *offline) runErr(err error) error {
	if cause := context.Cause(o.ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return err
}

func convertArchive(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts ConvertOptions, encrypt bool) (*ConvertResult, error) {
	opts.defaults()
	command := "disable"
	if encrypt {
		command = "enable"
	}
	res := &ConvertResult{}
	off, err := beginOffline(ctx, st, blobs, opts, command, res)
	if err != nil {
		return nil, err
	}
	defer off.close()
	ctx = off.ctx
	orphanDir := off.orphans

	atOpts := AtRestOptions{DataDir: opts.DataDir, KEK: opts.KEK, Logger: opts.Logger, keyringHeld: true}
	row, err := st.BlobEncryption(ctx)
	if err != nil {
		return nil, err
	}
	if !encrypt && blobOff(row) {
		// Off, derived from the files as at a writer start, but always with
		// a walk of messages/: a dump recorded off restored next to files
		// encrypted later, or an encrypted file put back from orphans/,
		// shows up as encrypting and is decrypted (review rounds 1 and 2
		// of #513).
		if row, err = deriveFromFiles(ctx, st, blobs, row, atOpts, true); err != nil {
			return nil, err
		}
	}
	state := store.BlobOff
	if row != nil {
		state = row.State
	}
	target, transit := store.BlobOff, store.BlobDecrypting
	if encrypt {
		target, transit = store.BlobOn, store.BlobEncrypting
	}
	switch state {
	case target:
		res.State, res.Unchanged = state, true
		if encrypt && len(res.TempMoved) > 0 {
			// Already on: the moved files are encrypted right away.
			if err := encryptMoved(ctx, st, blobs, row, atOpts, res); err != nil {
				return res, err
			}
		}
		return res, nil
	case store.BlobRekeying:
		return nil, ErrRekeyInProgress
	}
	if opts.KEK == nil {
		return nil, fmt.Errorf("%w: `encryption %s` needs it", ErrNoArchiveKey, command)
	}

	var ring *blobcrypt.Keyring
	if state == store.BlobOff {
		// Only Enable gets here (Disable returned above).
		if ring, err = startEnable(ctx, st, row, atOpts, opts.Out); err != nil {
			return nil, err
		}
	} else {
		// A conversion in either direction, or on (Disable): the copies
		// are reconciled as at a writer's start.
		if ring, err = needKeyring(ctx, st, row, atOpts); err != nil {
			return nil, err
		}
		if state != transit {
			// on → decrypting, encrypting → decrypting or back: the
			// cursor starts again.
			if err := st.SetBlobEncryption(ctx, transit, store.KeyringCopy{}); err != nil {
				return nil, err
			}
			fmt.Fprintf(opts.Out, "encryption at rest: %s → %s\n", state, transit)
		}
	}
	if row, err = st.BlobEncryption(ctx); err != nil {
		return nil, err
	}
	if row == nil || row.State != transit {
		now := store.BlobOff
		if row != nil {
			now = row.State
		}
		return nil, fmt.Errorf("encryption at rest changed during start-up (state %q, want %s); start again", now, transit)
	}
	if err := blobs.SetKeys(ring, encrypt); err != nil {
		return nil, err
	}
	conv, err := blobs.NewConversion(encrypt)
	if err != nil {
		return nil, err
	}
	res.State = transit
	c := &converter{
		ctx: ctx, st: st, blobs: blobs, conv: conv, opts: opts, encrypt: encrypt,
		gen: row.Generation, cursor: row.Cursor, done: row.FilesDone, corrupt: row.FilesCorrupt,
		res: res, orphanDir: orphanDir,
	}
	if err := c.run(); err != nil {
		return res, off.runErr(err)
	}
	// Both guarded by the generation the run worked at.
	if encrypt {
		err = st.FinishBlobEncryption(ctx, c.gen)
	} else {
		err = st.FinishBlobDecryption(ctx, c.gen)
	}
	if err != nil {
		return res, err
	}
	res.State = target
	return res, nil
}

// encryptMoved encrypts the files MoveTempAside moved while the state is
// already on, so that a later enable leaves no plaintext from tmp/.
func encryptMoved(ctx context.Context, st *store.Store, blobs *blobstore.Store, row *store.BlobEncryption, opts AtRestOptions, res *ConvertResult) error {
	if opts.KEK == nil {
		return fmt.Errorf("%w: files moved from tmp/ cannot be encrypted", ErrNoArchiveKey)
	}
	ring, err := needKeyring(ctx, st, row, opts)
	if err != nil {
		return err
	}
	if err := blobs.SetKeys(ring, true); err != nil {
		return err
	}
	conv, err := blobs.NewConversion(true)
	if err != nil {
		return err
	}
	for _, rel := range res.TempMoved {
		out, err := conv.Orphan(rel)
		if err != nil {
			return err
		}
		if out == blobstore.ConvertDone {
			res.Converted++
		}
	}
	return conv.Commit()
}

// startEnable is the step from off to encrypting: the keyring file gets a
// new generation, written before the first file is encrypted, and the
// state encrypting is recorded with it as the database copy (through
// store.SetBlobEncryption, which continues the generation of an off row).
// A database copy recorded off with the same archive id is merged in
// (the data keys are the union).
func startEnable(ctx context.Context, st *store.Store, row *store.BlobEncryption, opts AtRestOptions, out io.Writer) (*blobcrypt.Keyring, error) {
	// The caller holds the keyring lock.
	path := blobcrypt.KeyringPath(opts.DataDir)
	ring, err := offKeyring(row, opts)
	if err != nil {
		return nil, err
	}
	if err := ring.NextGeneration(); err != nil {
		return nil, err
	}
	if err := blobcrypt.WriteKeyringFile(path, ring); err != nil {
		return nil, err
	}
	data, err := ring.Marshal()
	if err != nil {
		return nil, err
	}
	if err := st.SetBlobEncryption(ctx, store.BlobEncrypting, store.KeyringCopy{
		Keyring: data, ID: ring.ArchiveID(), Generation: int64(ring.Generation()), CurrentKey: ring.CurrentKey(), //nolint:gosec // generations stay far below 2^63
	}); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "encryption at rest: off → encrypting (keyring generation %d, data key %08x)\n", ring.Generation(), ring.CurrentKey())
	return ring, nil
}

// converter runs the passes of one Enable or Disable.
type converter struct {
	ctx     context.Context
	st      *store.Store
	blobs   *blobstore.Store
	conv    *blobstore.Conversion
	opts    ConvertOptions
	encrypt bool
	// rekey re-encrypts under the current data key (Rekey); conv is a
	// rekey conversion then, and encrypt is set for plaintext stragglers.
	rekey bool
	gen   int64

	cursor        string // where the current pass resumes ("" its start)
	done, corrupt int64  // recorded counters
	res           *ConvertResult

	// The current batch and pass.
	batchFiles int
	batchBytes int64
	batches    int
	passFound  int64 // files converted in this pass
	last       string

	orphanDir string // orphans/<time> of this run, for MoveAside

	// Progress of this run.
	started   time.Time
	startFrac float64
	files     int64
	lastPct   int
}

// cursorOrphans is the cursor once messages/ is done: the orphans/ phase.
const cursorOrphans = blobstore.OrphansDir + "/"

// run makes passes until one started from the beginning finds nothing
// left to convert. A pass resumed from a cursor never ends the run: it is
// followed by a pass from the start.
func (c *converter) run() error {
	c.started = c.opts.Now()
	c.startFrac = c.fraction(c.cursor)
	for {
		clean := c.cursor == ""
		if clean {
			c.corrupt = 0
			c.res.Corrupt = nil
			if c.rekey {
				// The keys a clean pass finds named decide which old data
				// keys stay.
				c.conv.ResetNamed()
			}
		}
		c.passFound, c.lastPct = 0, -1
		c.res.Passes++
		verb := "decrypting"
		switch {
		case c.rekey:
			verb = "re-encrypting"
		case c.encrypt:
			verb = "encrypting"
		}
		switch {
		case !clean:
			fmt.Fprintf(c.opts.Out, "%s: resuming after %s\n", verb, c.cursor)
		case c.res.Passes > 1:
			fmt.Fprintf(c.opts.Out, "checking that no file is left (a full pass) ...\n")
			verb = "checking"
		default:
			fmt.Fprintf(c.opts.Out, "%s the message files ...\n", verb)
		}
		if err := c.pass(verb); err != nil {
			return err
		}
		if clean && c.passFound == 0 {
			fmt.Fprintf(c.opts.Out, "done: %d file(s) converted by this run, %d corrupt\n", c.res.Converted, c.corrupt)
			return nil
		}
		c.cursor = ""
	}
}

// pass walks messages/ and then orphans/ from c.cursor, in batches.
func (c *converter) pass(verb string) error {
	if !strings.HasPrefix(c.cursor, cursorOrphans) {
		err := c.blobs.MessagesAfter(c.cursor, func(hash string, size int64) error {
			if err := c.ctx.Err(); err != nil {
				return err
			}
			out, err := c.conv.Message(hash)
			if err != nil {
				return err
			}
			if out == blobstore.ConvertCorrupt {
				if err := c.messageCorrupt(hash); err != nil {
					return err
				}
			}
			return c.count(out, hash, size, verb)
		})
		if err != nil {
			return err
		}
		c.cursor = cursorOrphans
	}
	after := "" // c.cursor is cursorOrphans, or a path below orphans/ when resumed
	if c.cursor != cursorOrphans {
		after = c.cursor
	}
	err := c.blobs.OrphansAfter(after, func(rel string, size int64) error {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		out, err := c.conv.Orphan(rel)
		if err != nil {
			return err
		}
		if out == blobstore.ConvertCorrupt {
			c.noteCorrupt(rel)
			c.opts.Logger.Warn("encryption at rest: cannot convert a file in orphans/; it stays as it is", "path", rel)
		}
		return c.count(out, rel, size, verb)
	})
	if err != nil {
		return err
	}
	// The pass is complete: the next one starts from the beginning.
	return c.commit("", verb)
}

// messageCorrupt handles a message file that cannot be converted. A
// plaintext one stays in place (decision 5: corrupt files are never
// encrypted; verify reports it). An encrypted one is moved to
// orphans/<time>/ by Disable.
func (c *converter) messageCorrupt(hash string) error {
	rel := blobstore.RelPath(hash)
	if c.rekey {
		// It stays where it is, and its data key stays in the keyring.
		rel = blobstore.EncPath(hash)
		c.opts.Logger.Warn("encryption at rest: cannot re-encrypt a message file; it stays as it is", "path", rel)
		fmt.Fprintf(c.opts.Out, "cannot re-encrypt %s (damaged, or under a data key the keyring lacks): left as it is, see verify\n", rel)
		c.noteCorrupt(rel)
		return nil
	}
	if !c.encrypt {
		rel = blobstore.EncPath(hash)
		c.res.MovedTo = c.orphanDir
		if err := c.blobs.MoveAside(rel, c.res.MovedTo); err != nil {
			return fmt.Errorf("move %s aside: %w", rel, err)
		}
		c.res.MovedAside = append(c.res.MovedAside, rel)
		c.opts.Logger.Warn("encryption at rest: cannot decrypt a message file; moved it aside", "path", rel, "to", c.res.MovedTo)
		fmt.Fprintf(c.opts.Out, "cannot decrypt %s (damaged, or under a data key the keyring lacks): moved to %s\n", rel, c.res.MovedTo)
	} else {
		c.opts.Logger.Warn("encryption at rest: a message file does not match its hash; it stays plaintext", "path", rel)
		fmt.Fprintf(c.opts.Out, "corrupt %s (its content does not match its name): left as it is, see verify\n", rel)
	}
	c.noteCorrupt(rel)
	return nil
}

// maxCorruptListed bounds ConvertResult.Corrupt; the count is complete.
const maxCorruptListed = 1000

func (c *converter) noteCorrupt(rel string) {
	if len(c.res.Corrupt) < maxCorruptListed {
		c.res.Corrupt = append(c.res.Corrupt, rel)
	}
}

// count adds a file to the batch and commits the batch when it is full.
func (c *converter) count(out blobstore.ConvertOutcome, key string, size int64, verb string) error {
	switch out {
	case blobstore.ConvertDone:
		c.passFound++
		c.done++
		c.res.Converted++
	case blobstore.ConvertCorrupt:
		c.corrupt++
	}
	c.files++
	c.batchFiles++
	c.batchBytes += size
	c.last = key
	if c.batchFiles >= c.opts.BatchFiles || c.batchBytes >= c.opts.BatchBytes {
		return c.commit(key, verb)
	}
	return nil
}

// commit makes the batch durable, removes its redundant copies, and
// records cursor (the last key of the batch; "" at the end of a pass).
func (c *converter) commit(cursor, verb string) error {
	if err := c.conv.Commit(); err != nil {
		return err
	}
	if err := c.st.SaveBlobProgress(c.ctx, c.gen, cursor, c.done, c.corrupt); err != nil {
		return err
	}
	c.cursor = cursor
	c.batchFiles, c.batchBytes = 0, 0
	c.batches++
	c.progress(cursor, verb)
	if c.opts.AfterBatch != nil {
		return c.opts.AfterBatch(c.batches)
	}
	return nil
}

func (c *converter) fraction(cursor string) float64 { return ConversionFraction(cursor) }

// ConversionFraction is how far through the archive a conversion with
// this cursor is, from 0 to 1: the first four hex digits of the last hash
// done ÷ 65536; 1 in orphans/; 0 at the start of a pass.
func ConversionFraction(cursor string) float64 {
	switch {
	case cursor == "":
		return 0
	case strings.HasPrefix(cursor, cursorOrphans):
		return 1
	}
	v, err := strconv.ParseUint(cursor[:4], 16, 32)
	if err != nil {
		return 0
	}
	return float64(v+1) / 65536
}

// progress prints a line when the percentage changed: percent, files per
// second and the time left of this pass.
func (c *converter) progress(cursor, verb string) {
	frac := c.fraction(cursor)
	if cursor == "" {
		frac = 1
	}
	pct := int(frac * 100)
	if pct == c.lastPct {
		return
	}
	c.lastPct = pct
	elapsed := c.opts.Now().Sub(c.started)
	rate := 0.0
	if s := elapsed.Seconds(); s > 0 {
		rate = float64(c.files) / s
	}
	left := "-"
	if moved := frac - c.startFrac; moved > 0 && frac < 1 {
		left = (time.Duration(float64(elapsed) * (1 - frac) / moved)).Round(time.Second).String()
	} else if frac >= 1 {
		left = "0s"
	}
	fmt.Fprintf(c.opts.Out, "%s: %3d%%  %.0f files/s  %s left  (%d converted, %d corrupt)\n", verb, pct, rate, left, c.done, c.corrupt)
	if frac >= 1 {
		// The next pass measures its own time.
		c.started, c.startFrac, c.files = c.opts.Now(), 0, 0
	}
}
