package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// A fill copies an IMAP mailbox into an smtp account: once, the mail that
// was there before journaling started (#89), or again and again, what
// shows up in the mailbox later and never passed the journal (a recurring
// fill, #112). It writes into the account's journal folder (SMTPFolder),
// next to what arrives over SMTP; its locations have no envelope and name
// the fill. It keeps its progress per IMAP folder, so an interrupted run
// resumes where it stopped. Fills from before #112 wrote into folders of
// their own, "<prefix>/<IMAP folder>", and finish there; switched to
// recurring, their locations are copied into the journal folder.
//
// One fill per account is in progress at a time (status running). A
// recurring fill between runs has the outcome of its last run as its
// status and keeps its sealed password; stopping it cancels it and wipes
// the password.

// Fill states.
const (
	FillRunning   = "running"
	FillOK        = "ok"
	FillPartial   = "partial"
	FillFailed    = "failed"
	FillCancelled = "cancelled"
)

// ErrFillRunning is returned when the account already has a running fill.
var ErrFillRunning = errors.New("a fill of this account is running")

// ErrFillNotRunning is returned when a fill to cancel or finish has
// already ended, or a fill to resume has not.
var ErrFillNotRunning = errors.New("the fill is not running")

// ErrFillNotResumable is returned when resuming a fill that runs, ended
// well or keeps running.
var ErrFillNotResumable = errors.New("only a failed, partial or cancelled fill can be resumed")

// ErrFillKeepsRunning is returned when switching on a fill that already
// keeps running.
var ErrFillKeepsRunning = errors.New("the fill already keeps running")

// ErrFillNotRecurring is returned when changing the interval of a fill
// that does not keep running.
var ErrFillNotRecurring = errors.New("the fill does not keep running")

// ErrFillRule is returned for a new fill with both a cut-off and
// KeepRunning, or with neither: a recurring fill uses the two-hour rule
// instead of a cut-off.
var ErrFillRule = errors.New("a fill has either a cut-off or keeps running")

// ErrFillPrefix is returned for a new fill with a folder prefix: fills
// write into the journal folder.
var ErrFillPrefix = errors.New("a folder prefix is no longer supported: fills write into " + SMTPFolder)

// Fill is a copy of an IMAP mailbox into an smtp account.
type Fill struct {
	ID, AccountID int64
	Host          string
	Port          int
	TLSMode       TLSMode
	Username      string
	// PasswordEnc is the sealed IMAP password; nil once the fill ended,
	// kept between the runs of a recurring fill.
	PasswordEnc        []byte
	Included, Excluded []string
	// FolderPrefix is set only for fills from before #112, which write
	// into "<prefix>/<IMAP folder>"; empty for fills into the journal
	// folder.
	FolderPrefix string
	// Cutoff: messages with an INTERNALDATE from an hour after it on are
	// left to journaling. Nil for a fill that was recurring from the start.
	Cutoff *time.Time
	// RecurringSince is when the fill became recurring, nil for a one-time
	// fill; it then imports by the two-hour rule. KeepRunning: recurring
	// and active (not stopped). Interval is its own interval, nil for the
	// server's. RunStartedAt is the start of the current or last run.
	RecurringSince *time.Time
	KeepRunning    bool
	Interval       *time.Duration
	RunStartedAt   *time.Time
	// Unconfirmed are trash and spam folders the last run skipped: they
	// appeared after the fill started, and the fill's included folders do
	// not name them.
	Unconfirmed []string
	// Confirmed, for CreateFill only: the trash and spam folders the user
	// confirmed, which later runs of a recurring fill keep filling.
	Confirmed  []string
	Status     string
	Error      string
	CreatedBy  *int64
	CreatedAt  time.Time
	FinishedAt *time.Time
	Folders    []FillFolder
}

// Recurring reports whether the fill uses the two-hour rule: it keeps
// running or did until it was stopped.
func (f *Fill) Recurring() bool { return f.RecurringSince != nil }

// IntoJournal reports whether the fill writes into the journal folder:
// every fill since #112, and an earlier one once it became recurring.
func (f *Fill) IntoJournal() bool { return f.FolderPrefix == "" || f.Recurring() }

// FillCounts is what a fill did with the messages of a folder.
type FillCounts struct {
	Scanned     int // headers read
	Imported    int // locations added
	New         int // messages new to the archive
	AfterCutoff int // left to journaling
	Journaled   int // same Message-ID received over SMTP
	Present     int // already in the target folder
	Deleted     int // deleted by retention, not stored again
	Waiting     int // recurring fills: younger than two hours, for the next run
}

// Add adds o to c.
func (c *FillCounts) Add(o FillCounts) {
	c.Scanned += o.Scanned
	c.Imported += o.Imported
	c.New += o.New
	c.AfterCutoff += o.AfterCutoff
	c.Journaled += o.Journaled
	c.Present += o.Present
	c.Deleted += o.Deleted
	c.Waiting += o.Waiting
}

// FillFolder is a fill's progress in one IMAP folder.
type FillFolder struct {
	FillID int64
	// SourceName is the IMAP folder, FolderName the archive folder it
	// fills: the journal folder, or "<prefix>/<SourceName>" for an earlier
	// fill.
	SourceName, FolderName string
	FolderID               int64
	// SourceUIDValidity and LastUID are the server's: a resume continues
	// after LastUID while the UIDVALIDITY stays the same.
	SourceUIDValidity uint32
	LastUID           uint32
	Done              bool
	Counts            FillCounts
	Error             string
}

func fillFromDB(r db.SmtpFill) *Fill {
	f := &Fill{
		ID: r.ID, AccountID: r.AccountID, Host: r.Host, Port: int(r.Port), TLSMode: TLSMode(r.TlsMode),
		Username: r.Username, PasswordEnc: r.PasswordEnc, Included: r.IncludedFolders, Excluded: r.ExcludedFolders,
		FolderPrefix: deref(r.FolderPrefix), Cutoff: r.Cutoff, Status: r.Status, CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, RecurringSince: r.RecurringSince, KeepRunning: r.KeepRunning,
		Interval: secondsToInterval(r.IntervalSeconds), RunStartedAt: r.RunStartedAt, Unconfirmed: r.UnconfirmedFolders,
	}
	if r.Error != nil {
		f.Error = *r.Error
	}
	return f
}

func uid32(v int64) uint32 {
	if v < 0 || v > math.MaxUint32 {
		return 0
	}
	return uint32(v)
}

// CreateFill inserts a running fill for an smtp account and its sealed
// password in one transaction, like CreateAccountSealed. The fill writes
// into the account's journal folder. A fill with KeepRunning is recurring
// from the start: it has no cut-off, and its first run starts now. It
// fails with ErrFillRunning while another fill of the account runs, with
// ErrNotSMTP for other kinds of accounts, with ErrFillPrefix if f has a
// folder prefix, and with ErrFillRule unless it has either a cut-off or
// KeepRunning.
func (s *Store) CreateFill(ctx context.Context, f *Fill, seal func(id int64) ([]byte, error)) error {
	if f.FolderPrefix != "" {
		return ErrFillPrefix
	}
	if f.Port < 1 || f.Port > 65535 {
		return fmt.Errorf("invalid port %d", f.Port)
	}
	if (f.Cutoff == nil) != f.KeepRunning || (f.Interval != nil && !f.KeepRunning) {
		return ErrFillRule
	}
	var since *time.Time
	if f.KeepRunning {
		now := time.Now()
		since = &now
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		a, err := q.GetAccount(ctx, f.AccountID)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if AccountKind(a.Kind) != KindSMTP {
			return ErrNotSMTP
		}
		if a.RemovedAt != nil {
			return ErrNotFound
		}
		row, err := q.InsertFill(ctx, db.InsertFillParams{
			AccountID: f.AccountID, Host: f.Host, Port: int32(f.Port), TlsMode: string(f.TLSMode), //nolint:gosec // range checked above
			Username: f.Username, IncludedFolders: nonNil(f.Included), ExcludedFolders: nonNil(f.Excluded),
			Cutoff: f.Cutoff, CreatedBy: f.CreatedBy, KeepRunning: f.KeepRunning, IntervalSeconds: intervalToSeconds(f.Interval),
			RecurringSince: since, RunStartedAt: since,
		})
		if isUniqueViolation(err) {
			return ErrFillRunning
		}
		if err != nil {
			return err
		}
		f.ID, f.CreatedAt, f.Status = row.ID, row.CreatedAt, FillRunning
		f.RecurringSince, f.RunStartedAt = since, since
		enc, err := seal(f.ID)
		if err != nil {
			return err
		}
		f.PasswordEnc = enc
		if e := auditFrom(ctx); e != nil {
			e.Detail("fill", f.ID)
		}
		if err := one(q.SetFillPassword(ctx, db.SetFillPasswordParams{ID: f.ID, PasswordEnc: enc})); err != nil {
			return err
		}
		if f.KeepRunning {
			return confirmFillFolders(ctx, q, f.ID, f.AccountID, f.Confirmed)
		}
		return nil
	})
}

// confirmFillFolders records the trash and spam folders the user confirmed
// for a recurring fill, as folders it fills: later runs skip such folders
// unless they already have a row (or the included folders name them).
func confirmFillFolders(ctx context.Context, q *db.Queries, fillID, accountID int64, names []string) error {
	if len(names) == 0 {
		return nil
	}
	journal, err := q.JournalFolder(ctx, db.JournalFolderParams{AccountID: accountID, Folder: SMTPFolder})
	if err != nil {
		return fmt.Errorf("the account's folder %s: %w", SMTPFolder, err)
	}
	for _, name := range names {
		if err := q.InsertFillFolder(ctx, db.InsertFillFolderParams{FillID: fillID, SourceName: name, FolderID: journal}); err != nil {
			return err
		}
	}
	return nil
}

// GetFill returns a fill with its folders.
func (s *Store) GetFill(ctx context.Context, id int64) (*Fill, error) {
	r, err := s.q.GetFill(ctx, id)
	if isNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f := fillFromDB(r)
	if f.Folders, err = s.fillFolders(ctx, id); err != nil {
		return nil, err
	}
	return f, nil
}

func (s *Store) fillFolders(ctx context.Context, id int64) ([]FillFolder, error) {
	rows, err := s.q.ListFillFolders(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]FillFolder, 0, len(rows))
	for _, r := range rows {
		ff := FillFolder{
			FillID: r.FillID, SourceName: r.SourceName, FolderName: r.FolderName, FolderID: r.FolderID,
			SourceUIDValidity: uid32(r.SourceUidvalidity), LastUID: uid32(r.LastUid), Done: r.Done,
			Counts: FillCounts{
				Scanned: int(r.Scanned), Imported: int(r.Imported), New: int(r.NewMessages), AfterCutoff: int(r.AfterCutoff),
				Journaled: int(r.Journaled), Present: int(r.Present), Deleted: int(r.Deleted), Waiting: int(r.Waiting),
			},
		}
		if r.Error != nil {
			ff.Error = *r.Error
		}
		out = append(out, ff)
	}
	return out, nil
}

// AccountFills returns the fills of an account with their folders, newest
// first.
func (s *Store) AccountFills(ctx context.Context, accountID int64) ([]Fill, error) {
	rows, err := s.q.ListAccountFills(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]Fill, 0, len(rows))
	for _, r := range rows {
		f := fillFromDB(r)
		if f.Folders, err = s.fillFolders(ctx, f.ID); err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, nil
}

// OwnerFills returns, by account ID, the fills of the owner's accounts
// that their cards show, with their folders, newest first: every fill
// that was ever recurring, and the newest fill of the account.
func (s *Store) OwnerFills(ctx context.Context, owner int64) (map[int64][]Fill, error) {
	rows, err := s.q.OwnerFills(ctx, &owner)
	if err != nil {
		return nil, err
	}
	out := map[int64][]Fill{}
	for _, r := range rows {
		f := fillFromDB(r)
		if f.Folders, err = s.fillFolders(ctx, f.ID); err != nil {
			return nil, err
		}
		out[f.AccountID] = append(out[f.AccountID], *f)
	}
	return out, nil
}

// RunningFills returns the fills in state running, without folders.
func (s *Store) RunningFills(ctx context.Context) ([]Fill, error) {
	return fillsFromDB(s.q.RunningFills(ctx))
}

// RecurringFills returns the active recurring fills between two runs, of
// enabled accounts that are not removed, without folders.
func (s *Store) RecurringFills(ctx context.Context) ([]Fill, error) {
	return fillsFromDB(s.q.RecurringFills(ctx))
}

func fillsFromDB(rows []db.SmtpFill, err error) ([]Fill, error) {
	if err != nil {
		return nil, err
	}
	out := make([]Fill, 0, len(rows))
	for _, r := range rows {
		out = append(out, *fillFromDB(r))
	}
	return out, nil
}

// FillFolder returns the progress of a fill in an IMAP folder, creating it
// if new. The archive folder is the account's journal folder; for a
// one-time fill from before #112 it is "<prefix>/<source>", created if new
// with the fill's start as UIDVALIDITY, like imported mail.
func (s *Store) FillFolder(ctx context.Context, f *Fill, source string) (*FillFolder, error) {
	var out *FillFolder
	err := s.inTx(ctx, func(q *db.Queries) error {
		var folderID int64
		if f.IntoJournal() {
			id, err := q.JournalFolder(ctx, db.JournalFolderParams{AccountID: f.AccountID, Folder: SMTPFolder})
			if isNoRows(err) {
				return fmt.Errorf("the account has no folder %s", SMTPFolder)
			}
			if err != nil {
				return err
			}
			folderID = id
		} else {
			folder, err := q.GetOrCreateFolder(ctx, db.GetOrCreateFolderParams{AccountID: f.AccountID, Name: f.FolderPrefix + "/" + source})
			if err != nil {
				return err
			}
			if folder.Uidvalidity == 0 {
				v := f.CreatedAt.Unix()
				if v < 1 || v > math.MaxUint32 {
					return fmt.Errorf("fill start %v does not fit a UIDVALIDITY", f.CreatedAt)
				}
				if err := one(q.ResetFolder(ctx, db.ResetFolderParams{ID: folder.ID, Uidvalidity: v})); err != nil {
					return err
				}
			}
			folderID = folder.ID
		}
		if err := q.InsertFillFolder(ctx, db.InsertFillFolderParams{FillID: f.ID, SourceName: source, FolderID: folderID}); err != nil {
			return err
		}
		r, err := q.GetFillFolder(ctx, db.GetFillFolderParams{FillID: f.ID, SourceName: source})
		if err != nil {
			return err
		}
		out = &FillFolder{
			FillID: r.FillID, SourceName: r.SourceName, FolderName: r.FolderName, FolderID: r.FolderID,
			SourceUIDValidity: uid32(r.SourceUidvalidity), LastUID: uid32(r.LastUid), Done: r.Done,
			Counts: FillCounts{
				Scanned: int(r.Scanned), Imported: int(r.Imported), New: int(r.NewMessages), AfterCutoff: int(r.AfterCutoff),
				Journaled: int(r.Journaled), Present: int(r.Present), Deleted: int(r.Deleted), Waiting: int(r.Waiting),
			},
		}
		if r.Error != nil {
			out.Error = *r.Error
		}
		return nil
	})
	return out, err
}

func updateFillFolder(ctx context.Context, q *db.Queries, ff *FillFolder) error {
	var errText *string
	if ff.Error != "" {
		errText = &ff.Error
	}
	c := ff.Counts
	return q.UpdateFillFolder(ctx, db.UpdateFillFolderParams{
		FillID: ff.FillID, SourceName: ff.SourceName,
		SourceUidvalidity: int64(ff.SourceUIDValidity), LastUid: int64(ff.LastUID), Done: ff.Done,
		Scanned: int32(c.Scanned), Imported: int32(c.Imported), NewMessages: int32(c.New), //nolint:gosec // counts of one folder
		AfterCutoff: int32(c.AfterCutoff), Journaled: int32(c.Journaled), Present: int32(c.Present), //nolint:gosec // counts of one folder
		Deleted: int32(c.Deleted), Waiting: int32(c.Waiting), Error: errText, //nolint:gosec // counts of one folder
	})
}

// UpdateFillFolder records a fill's progress in a folder.
func (s *Store) UpdateFillFolder(ctx context.Context, ff *FillFolder) error {
	return updateFillFolder(ctx, s.q, ff)
}

// SaveFillBatch is SaveBatch for a fill: it stores the messages and their
// locations in the fill's archive folder and records ff (the server's UID
// and the counts) in the same transaction, so a resume never repeats a
// committed batch. With no messages it records only ff. The counts in ff
// must already include the batch; ff.Counts.New, Imported, Present and
// Deleted are corrected here by what was actually stored.
//
// The journal folder is shared with deliveries over SMTP (ReceiveMessage),
// so the batch takes the folder's row lock, checks again inside the
// transaction which messages the folder holds, and only then numbers the
// new locations. The UIDs and UIDVALIDITY in locs are ignored; the
// locations name the fill. The caller holds the blob lock (the fill holds
// the account's sync lock for writing), so the transaction takes only the
// attestation fence.
func (s *Store) SaveFillBatch(ctx context.Context, ff *FillFolder, metas []MessageMeta, locs []Location) (SaveResult, error) {
	before := ff.Counts
	fillID := ff.FillID
	res, _, err := s.saveIntoJournal(ctx, ff.FolderID, ff.FolderName, metas, locs, &fillID, nil,
		func(q *db.Queries, res SaveResult, present int) error {
			ff.Counts = before
			ff.Counts.Deleted += res.Skipped
			ff.Counts.Present += present
			ff.Counts.Imported -= res.Skipped + present
			ff.Counts.New += res.New
			return updateFillFolder(ctx, q, ff)
		})
	if err != nil {
		ff.Counts = before
		return SaveResult{}, err
	}
	return res, nil
}

// saveIntoJournal is the core of SaveFillBatch and SaveJournalImportBatch:
// it stores a batch, in the order of the hashes, in a folder that
// deliveries over SMTP write into too. In one transaction it takes the
// folder's row lock, leaves out messages deleted by retention (res.Skipped)
// and those the folder holds already (present, checked again under the
// lock), numbers the new locations with AllocateUIDs and calls done, if
// set, with the outcome. The locations name the fill or the import; the
// UIDs and UIDVALIDITY in locs are ignored. The caller holds the blob lock,
// so the transaction takes only the attestation fence.
func (s *Store) saveIntoJournal(ctx context.Context, folderID int64, folderName string, metas []MessageMeta, locs []Location,
	fillID, importID *int64, done func(q *db.Queries, res SaveResult, present int) error) (SaveResult, int, error) {
	metas, locs = sortBatch(metas, locs)
	var (
		res     SaveResult
		present int
	)
	err := s.inWriteTx(ctx, false, func(q *db.Queries) error {
		res, present = SaveResult{}, 0
		folder, err := q.LockFolderByID(ctx, folderID)
		if err != nil {
			return fmt.Errorf("lock folder: %w", err)
		}
		skip, err := skipDeleted(ctx, q, folder.ID, metas, locs)
		if err != nil {
			return err
		}
		type pending struct {
			meta *MessageMeta
			loc  Location
		}
		var add []pending
		seen := map[string]bool{}
		for i := range metas {
			m := &metas[i]
			if skip[m.SHA256] {
				res.Skipped++
				continue
			}
			// Under the row lock no delivery can add a location meanwhile.
			there := seen[m.SHA256]
			if !there {
				if there, err = q.LocationInFolder(ctx, db.LocationInFolderParams{FolderID: folder.ID, Sha256: m.SHA256}); err != nil {
					return err
				}
			}
			seen[m.SHA256] = true
			if there {
				present++
				continue
			}
			n, err := q.InsertMessage(ctx, insertMessageParams(m))
			if err != nil {
				return fmt.Errorf("insert message %s: %w", m.SHA256, err)
			}
			res.New += int(n)
			add = append(add, pending{m, locs[i]})
		}
		if len(add) > 0 {
			last, err := q.AllocateUIDs(ctx, db.AllocateUIDsParams{ID: folder.ID, N: int64(len(add))})
			if err != nil {
				return err
			}
			if last > math.MaxUint32 {
				return fmt.Errorf("the folder %s has no UIDs left", folderName)
			}
			first := last - int64(len(add)) + 1
			for i, p := range add {
				flags := p.loc.Flags
				if flags == nil {
					flags = []string{}
				}
				var internal *time.Time
				if !p.loc.InternalDate.IsZero() {
					internal = &p.loc.InternalDate
				}
				err := q.InsertFilledLocation(ctx, db.InsertFilledLocationParams{
					MessageSha256: p.meta.SHA256, FolderID: folder.ID, Uidvalidity: folder.Uidvalidity, Uid: first + int64(i),
					Flags: flags, InternalDate: internal, FillID: fillID, ImportID: importID,
				})
				if err != nil {
					return fmt.Errorf("insert location: %w", err)
				}
			}
		}
		if done != nil {
			return done(q, res, present)
		}
		return nil
	})
	if err != nil {
		return SaveResult{}, 0, err
	}
	return res, present, nil
}

// sortBatch returns the messages of a batch and their locations ordered by
// hash, so that concurrent batches lock the rows of messages in the same
// order and cannot deadlock.
func sortBatch(metas []MessageMeta, locs []Location) ([]MessageMeta, []Location) {
	if slices.IsSortedFunc(metas, func(a, b MessageMeta) int { return cmp.Compare(a.SHA256, b.SHA256) }) {
		return metas, locs
	}
	idx := make([]int, len(metas))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return cmp.Compare(metas[a].SHA256, metas[b].SHA256) })
	m := make([]MessageMeta, len(metas))
	l := make([]Location, len(locs))
	for i, j := range idx {
		m[i], l[i] = metas[j], locs[j]
	}
	return m, l
}

// FinishFill ends a running fill, or the run of a recurring fill, with
// status. A one-time fill's password is wiped; a recurring fill keeps it
// for the next run, and the run's outcome counts in its health in the same
// transaction. A fill that was cancelled meanwhile stays cancelled:
// ErrFillNotRunning.
func (s *Store) FinishFill(ctx context.Context, id int64, status, errText string) error {
	var e *string
	if errText != "" {
		e = &errText
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		keepRunning, err := q.FinishFill(ctx, db.FinishFillParams{ID: id, Status: status, Error: e})
		if isNoRows(err) {
			return ErrFillNotRunning
		}
		if err != nil || !keepRunning {
			return err
		}
		switch status {
		case FillOK, FillPartial:
			return q.RecordFillSuccess(ctx, id)
		case FillFailed:
			return q.RecordFillFailure(ctx, id)
		}
		return nil
	})
}

// StartFillRun starts the next run of an active recurring fill at now:
// running, with the folders to visit again and the counts of the new run.
// It fails with ErrFillRunning while another fill of the account runs, and
// with ErrFillNotRunning when the fill was stopped or already runs.
func (s *Store) StartFillRun(ctx context.Context, id int64, now time.Time) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		n, err := q.StartFillRun(ctx, db.StartFillRunParams{ID: id, StartedAt: &now})
		if isUniqueViolation(err) {
			return ErrFillRunning
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrFillNotRunning
		}
		return q.ResetFillRun(ctx, id)
	})
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ResumeFill sets a failed, partial or cancelled fill running again with a
// newly sealed password. A one-time fill continues where it stopped; a
// stopped recurring fill keeps running again and starts a new run. It
// fails with ErrFillRunning while another fill of the account runs, and
// with ErrFillNotResumable for a fill that runs, ended well or keeps
// running.
func (s *Store) ResumeFill(ctx context.Context, id int64, seal func(id int64) ([]byte, error)) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		enc, err := seal(id)
		if err != nil {
			return err
		}
		recurring, err := q.ResumeFill(ctx, db.ResumeFillParams{ID: id, PasswordEnc: enc})
		if isUniqueViolation(err) {
			return ErrFillRunning
		}
		if isNoRows(err) {
			if _, err := q.FillStatus(ctx, id); isNoRows(err) {
				return ErrNotFound
			}
			return ErrFillNotResumable
		}
		if err != nil {
			return err
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("fill", id)
		}
		if recurring {
			return q.ResetFillRun(ctx, id)
		}
		return nil
	})
}

// SetFillRecurring switches an ended fill on: from now on it keeps running
// every interval (nil: the server's), with a newly sealed password, and
// its first run starts at once. confirmed are trash and spam folders the
// user confirmed. A one-time fill's folders are scanned again from the
// start, so that its first run imports what the cut-off left out; a fill
// from before #112 is copied into the journal folder by that run. It fails with ErrFillRunning while the fill or
// another fill of the account runs, with ErrFillKeepsRunning when it
// already keeps running, and with ErrNotFound for a removed account.
func (s *Store) SetFillRecurring(ctx context.Context, id int64, interval *time.Duration, confirmed []string,
	seal func(id int64) ([]byte, error)) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		f, err := q.GetFill(ctx, id)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		a, err := q.GetAccount(ctx, f.AccountID)
		if err != nil {
			return err
		}
		switch {
		case a.RemovedAt != nil:
			return ErrNotFound
		case f.Status == FillRunning:
			return ErrFillRunning
		case f.KeepRunning:
			return ErrFillKeepsRunning
		}
		enc, err := seal(id)
		if err != nil {
			return err
		}
		n, err := q.SetFillRecurring(ctx, db.SetFillRecurringParams{ID: id, IntervalSeconds: intervalToSeconds(interval), PasswordEnc: enc})
		if isUniqueViolation(err) {
			return ErrFillRunning
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrFillRunning // started meanwhile
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("fill", id)
		}
		if err := q.ResetFillRun(ctx, id); err != nil {
			return err
		}
		if f.RecurringSince == nil {
			// The cut-off moved the folders' last UIDs past messages the
			// new rule imports. The rescan is cheap: what the fill imported
			// is found without the bodies (FillImported, LocationInFolder).
			if err := q.RewindFillFolders(ctx, id); err != nil {
				return err
			}
		}
		return confirmFillFolders(ctx, q, id, f.AccountID, confirmed)
	})
}

// SetFillInterval changes the interval of an active recurring fill (nil:
// the server's). It fails with ErrFillNotRecurring for other fills.
func (s *Store) SetFillInterval(ctx context.Context, id int64, interval *time.Duration) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		n, err := q.SetFillInterval(ctx, db.SetFillIntervalParams{ID: id, IntervalSeconds: intervalToSeconds(interval)})
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := q.FillStatus(ctx, id); isNoRows(err) {
				return ErrNotFound
			}
			return ErrFillNotRecurring
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("fill", id)
		}
		return nil
	})
}

// SetFillUnconfirmed records the trash and spam folders a run of a
// recurring fill skipped.
func (s *Store) SetFillUnconfirmed(ctx context.Context, id int64, folders []string) error {
	return s.q.SetFillUnconfirmed(ctx, db.SetFillUnconfirmedParams{ID: id, Folders: nonNil(folders)})
}

// CancelFill stops a running fill, or a recurring fill between runs, and
// wipes its password; a recurring fill no longer keeps running. The
// process running it notices between two batches.
func (s *Store) CancelFill(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		n, err := q.CancelFill(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := q.FillStatus(ctx, id); isNoRows(err) {
				return ErrNotFound
			}
			return ErrFillNotRunning
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("fill", id)
		}
		return nil
	})
}

// FillStatus returns the state of a fill.
func (s *Store) FillStatus(ctx context.Context, id int64) (string, error) {
	st, err := s.q.FillStatus(ctx, id)
	if isNoRows(err) {
		return "", ErrNotFound
	}
	return st, err
}

// JournaledMessageIDs returns which of ids the account received over SMTP
// (a location with an envelope). Empty IDs never match: messages without a
// Message-ID store NULL.
func (s *Store) JournaledMessageIDs(ctx context.Context, accountID int64, ids []string) (map[string]bool, error) {
	var want []string
	for _, id := range ids {
		if id != "" {
			want = append(want, id)
		}
	}
	out := map[string]bool{}
	if len(want) == 0 {
		return out, nil
	}
	got, err := s.q.JournaledMessageIDs(ctx, db.JournaledMessageIDsParams{AccountID: accountID, Ids: want})
	if err != nil {
		return nil, err
	}
	for _, id := range got {
		out[id] = true
	}
	return out, nil
}

// FirstMessage is the first message an smtp account received that is still
// stored, a sanity check for a fill's cut-off.
type FirstMessage struct {
	ReceivedAt time.Time
	Subject    string
}

// FirstReceived returns the first message the smtp account received over
// SMTP that is still stored, or nil if it has none. Mail a fill imported
// into the journal folder does not count.
func (s *Store) FirstReceived(ctx context.Context, accountID int64) (*FirstMessage, error) {
	r, err := s.q.FirstReceived(ctx, db.FirstReceivedParams{AccountID: accountID, Folder: SMTPFolder})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m := &FirstMessage{}
	if r.InternalDate != nil {
		m.ReceivedAt = *r.InternalDate
	}
	if r.Subject != nil {
		m.Subject = *r.Subject
	}
	return m, nil
}

// ImportedKey identifies a message a recurring fill imported, without its
// body: the Message-ID, the INTERNALDATE in whole seconds and the size. A
// message moved to another IMAP folder keeps all three.
type ImportedKey struct {
	MessageID    string
	InternalDate int64 // Unix seconds
	Size         int64
}

// NewImportedKey is the key of a message on the IMAP server.
func NewImportedKey(messageID string, internalDate time.Time, size int64) ImportedKey {
	return ImportedKey{MessageID: messageID, InternalDate: internalDate.Unix(), Size: size}
}

// FillImported returns which of the messages with these Message-IDs the
// fill imported, by ImportedKey: the pre-check of a recurring fill, which
// then needs no body for them. Only the fill's own locations count, so a
// journaled copy or another mailbox's never does.
func (s *Store) FillImported(ctx context.Context, fillID int64, ids []string) (map[ImportedKey]bool, error) {
	var want []string
	for _, id := range ids {
		if id != "" {
			want = append(want, id)
		}
	}
	out := map[ImportedKey]bool{}
	if len(want) == 0 {
		return out, nil
	}
	rows, err := s.q.FillImported(ctx, db.FillImportedParams{FillID: &fillID, Ids: want})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.InternalDate != nil {
			out[NewImportedKey(r.MessageID, *r.InternalDate, r.Size)] = true
		}
	}
	return out, nil
}

// convertBatch is how many locations ConvertLegacyFill copies in one
// transaction.
const convertBatch = 5000

// ConvertLegacyFill copies the locations of a fill from before #112 into
// the account's journal folder, once per message and only for messages the
// folder does not hold yet, and points the fill's IMAP folders at it,
// scanned again from the start. It writes no files: the messages are
// stored already. The fill's own folders and their locations stay. It
// returns how many locations it copied; a fill without folders of its own
// copies none. The caller holds the account's sync lock.
func (s *Store) ConvertLegacyFill(ctx context.Context, f *Fill) (int, error) {
	journal, err := s.q.JournalFolder(ctx, db.JournalFolderParams{AccountID: f.AccountID, Folder: SMTPFolder})
	if err != nil {
		return 0, fmt.Errorf("the account's folder %s: %w", SMTPFolder, err)
	}
	legacy, err := s.q.LegacyFillFolders(ctx, db.LegacyFillFoldersParams{FillID: f.ID, JournalID: journal})
	if err != nil || len(legacy) == 0 {
		return 0, err
	}
	copied, after := 0, ""
	for {
		var got []string
		// A message that retention deletes meanwhile fails the batch
		// (23503); the next attempt leaves it out.
		for attempt := 0; ; attempt++ {
			got, err = s.convertLegacyBatch(ctx, f.ID, journal, legacy, after)
			var pgErr *pgconn.PgError
			if err == nil || attempt == 2 || !errors.As(err, &pgErr) || pgErr.Code != "23503" {
				break
			}
		}
		if err != nil {
			return copied, err
		}
		copied += len(got)
		if len(got) < convertBatch {
			break
		}
		after = slices.Max(got)
	}
	return copied, s.q.MoveFillFolders(ctx, db.MoveFillFoldersParams{FillID: f.ID, JournalID: journal})
}

func (s *Store) convertLegacyBatch(ctx context.Context, fillID, journal int64, legacy []int64, after string) ([]string, error) {
	var got []string
	err := s.inTx(ctx, func(q *db.Queries) error {
		// Numbered under the folder's row lock, like a fill's batch.
		folder, err := q.LockFolderByID(ctx, journal)
		if err != nil {
			return fmt.Errorf("lock folder: %w", err)
		}
		got, err = q.ConvertLegacyFillBatch(ctx, db.ConvertLegacyFillBatchParams{
			JournalID: journal, Uidvalidity: folder.Uidvalidity, LastUid: folder.LastUid, FillID: fillID,
			Folders: legacy, After: after, MaxRows: convertBatch,
		})
		if err != nil || len(got) == 0 {
			return err
		}
		last, err := q.AllocateUIDs(ctx, db.AllocateUIDsParams{ID: journal, N: int64(len(got))})
		if err != nil {
			return err
		}
		if last > math.MaxUint32 {
			return fmt.Errorf("the folder %s has no UIDs left", SMTPFolder)
		}
		return nil
	})
	return got, err
}
