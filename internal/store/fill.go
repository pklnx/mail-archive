package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// A fill copies an IMAP mailbox once into an smtp account: the mail that
// was there before journaling started (#89). It writes into its own folders,
// "<prefix>/<IMAP folder>", and keeps its progress per IMAP folder, so an
// interrupted fill resumes where it stopped.

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

// ErrFillNotResumable is returned when resuming a fill that runs or ended
// well.
var ErrFillNotResumable = errors.New("only a failed, partial or cancelled fill can be resumed")

// MaxFillPrefix is the longest folder prefix a fill takes.
const MaxFillPrefix = 200

// Fill is a copy of an IMAP mailbox into an smtp account.
type Fill struct {
	ID, AccountID int64
	Host          string
	Port          int
	TLSMode       TLSMode
	Username      string
	// PasswordEnc is the sealed IMAP password; nil once the fill ended.
	PasswordEnc        []byte
	Included, Excluded []string
	// FolderPrefix names the fill's folders: "<prefix>/<IMAP folder>".
	FolderPrefix string
	// Cutoff: messages with an INTERNALDATE from an hour after it on are
	// left to journaling.
	Cutoff     time.Time
	Status     string
	Error      string
	CreatedBy  *int64
	CreatedAt  time.Time
	FinishedAt *time.Time
	Folders    []FillFolder
}

// FillCounts is what a fill did with the messages of a folder.
type FillCounts struct {
	Scanned     int // headers read
	Imported    int // locations added
	New         int // messages new to the archive
	AfterCutoff int // left to journaling
	Journaled   int // same Message-ID received over SMTP
	Present     int // already in the target folder
	Deleted     int // deleted by retention, not stored again
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
}

// FillFolder is a fill's progress in one IMAP folder.
type FillFolder struct {
	FillID int64
	// SourceName is the IMAP folder, FolderName the archive folder it
	// fills ("<prefix>/<SourceName>").
	SourceName, FolderName string
	FolderID               int64
	// SourceUIDValidity and LastUID are the server's: a resume continues
	// after LastUID while the UIDVALIDITY stays the same.
	SourceUIDValidity uint32
	LastUID           uint32
	Done              bool
	Counts            FillCounts
	Error             string
	// TargetUIDValidity and TargetLastUID are the archive folder's: fill
	// locations get the archive's own UIDs, like imported mail.
	TargetUIDValidity uint32
	TargetLastUID     uint32
}

// ValidFillPrefix checks a folder prefix for a fill.
func ValidFillPrefix(p string) error {
	switch {
	case p == "":
		return errors.New("the folder prefix must not be empty")
	case len(p) > MaxFillPrefix:
		return fmt.Errorf("the folder prefix is longer than %d bytes", MaxFillPrefix)
	case strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/"):
		return errors.New("the folder prefix must not start or end with /")
	case strings.EqualFold(p, SMTPFolder):
		return errors.New("the folder prefix must not be INBOX")
	case strings.IndexFunc(p, unicode.IsControl) >= 0:
		return errors.New("the folder prefix must not contain control characters")
	}
	return nil
}

func fillFromDB(r db.SmtpFill) *Fill {
	f := &Fill{
		ID: r.ID, AccountID: r.AccountID, Host: r.Host, Port: int(r.Port), TLSMode: TLSMode(r.TlsMode),
		Username: r.Username, PasswordEnc: r.PasswordEnc, Included: r.IncludedFolders, Excluded: r.ExcludedFolders,
		FolderPrefix: r.FolderPrefix, Cutoff: r.Cutoff, Status: r.Status, CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
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
// password in one transaction, like CreateAccountSealed. It fails with
// ErrFillRunning while another fill of the account runs, with ErrConflict
// when another fill of the account uses the prefix, and with ErrNotSMTP
// for other kinds of accounts.
func (s *Store) CreateFill(ctx context.Context, f *Fill, seal func(id int64) ([]byte, error)) error {
	if err := ValidFillPrefix(f.FolderPrefix); err != nil {
		return err
	}
	if f.Port < 1 || f.Port > 65535 {
		return fmt.Errorf("invalid port %d", f.Port)
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
		inUse, err := q.FillPrefixInUse(ctx, db.FillPrefixInUseParams{AccountID: f.AccountID, FolderPrefix: f.FolderPrefix})
		if err != nil {
			return err
		}
		if inUse {
			return fmt.Errorf("another fill of the account uses the prefix %q: %w", f.FolderPrefix, ErrConflict)
		}
		row, err := q.InsertFill(ctx, db.InsertFillParams{
			AccountID: f.AccountID, Host: f.Host, Port: int32(f.Port), TlsMode: string(f.TLSMode), //nolint:gosec // range checked above
			Username: f.Username, IncludedFolders: nonNil(f.Included), ExcludedFolders: nonNil(f.Excluded),
			FolderPrefix: f.FolderPrefix, Cutoff: f.Cutoff, CreatedBy: f.CreatedBy,
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrFillRunning
		}
		if err != nil {
			return err
		}
		f.ID, f.CreatedAt, f.Status = row.ID, row.CreatedAt, FillRunning
		enc, err := seal(f.ID)
		if err != nil {
			return err
		}
		f.PasswordEnc = enc
		if e := auditFrom(ctx); e != nil {
			e.Detail("fill", f.ID)
		}
		return one(q.SetFillPassword(ctx, db.SetFillPasswordParams{ID: f.ID, PasswordEnc: enc}))
	})
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
				Journaled: int(r.Journaled), Present: int(r.Present), Deleted: int(r.Deleted),
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

// LatestFills returns the newest fill of each account of the owner, with
// its folders, by account ID.
func (s *Store) LatestFills(ctx context.Context, owner int64) (map[int64]*Fill, error) {
	rows, err := s.q.LatestFills(ctx, &owner)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*Fill, len(rows))
	for _, r := range rows {
		f := fillFromDB(r)
		if f.Folders, err = s.fillFolders(ctx, f.ID); err != nil {
			return nil, err
		}
		out[f.AccountID] = f
	}
	return out, nil
}

// RunningFills returns the fills in state running, without folders.
func (s *Store) RunningFills(ctx context.Context) ([]Fill, error) {
	rows, err := s.q.RunningFills(ctx)
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
// and its archive folder ("<prefix>/<source>") if new. A new archive folder
// gets the fill's start as UIDVALIDITY, like imported mail.
func (s *Store) FillFolder(ctx context.Context, f *Fill, source string) (*FillFolder, error) {
	var out *FillFolder
	err := s.inTx(ctx, func(q *db.Queries) error {
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
		if err := q.InsertFillFolder(ctx, db.InsertFillFolderParams{FillID: f.ID, SourceName: source, FolderID: folder.ID}); err != nil {
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
				Journaled: int(r.Journaled), Present: int(r.Present), Deleted: int(r.Deleted),
			},
			TargetUIDValidity: uid32(r.FolderUidvalidity), TargetLastUID: uid32(r.FolderLastUid),
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
		Deleted: int32(c.Deleted), Error: errText, //nolint:gosec // counts of one folder
	})
}

// UpdateFillFolder records a fill's progress in a folder.
func (s *Store) UpdateFillFolder(ctx context.Context, ff *FillFolder) error {
	return updateFillFolder(ctx, s.q, ff)
}

// SaveFillBatch is SaveBatch for a fill: it stores the messages and their
// locations in the archive folder, advances it to targetLastUID and records
// ff (the server's UID and the counts) in the same transaction, so a resume
// never repeats a committed batch. With no messages it records only ff.
// The counts in ff must already include the batch; ff.Counts.New and
// ff.Counts.Deleted are corrected here by what was actually stored.
func (s *Store) SaveFillBatch(ctx context.Context, ff *FillFolder, targetLastUID uint32, metas []MessageMeta, locs []Location) (SaveResult, error) {
	before := ff.Counts
	res, err := s.saveBatch(ctx, ff.FolderID, targetLastUID, metas, locs, func(q *db.Queries, r SaveResult) error {
		ff.Counts.New += r.New
		ff.Counts.Deleted += r.Skipped
		ff.Counts.Imported -= r.Skipped
		return updateFillFolder(ctx, q, ff)
	})
	if err != nil {
		ff.Counts = before
		return SaveResult{}, err
	}
	ff.TargetLastUID = max(ff.TargetLastUID, targetLastUID)
	return res, nil
}

// FinishFill ends a running fill and wipes its password. A fill that was
// cancelled meanwhile stays cancelled: ErrFillNotRunning.
func (s *Store) FinishFill(ctx context.Context, id int64, status, errText string) error {
	var e *string
	if errText != "" {
		e = &errText
	}
	n, err := s.q.FinishFill(ctx, db.FinishFillParams{ID: id, Status: status, Error: e})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrFillNotRunning
	}
	return nil
}

// ResumeFill sets a failed, partial or cancelled fill running again with a
// newly sealed password. It fails with ErrFillRunning while another fill of
// the account runs.
func (s *Store) ResumeFill(ctx context.Context, id int64, seal func(id int64) ([]byte, error)) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		enc, err := seal(id)
		if err != nil {
			return err
		}
		n, err := q.ResumeFill(ctx, db.ResumeFillParams{ID: id, PasswordEnc: enc})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrFillRunning
		}
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := q.FillStatus(ctx, id); isNoRows(err) {
				return ErrNotFound
			}
			return ErrFillNotResumable
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("fill", id)
		}
		return nil
	})
}

// CancelFill stops a running fill and wipes its password. The process
// running it notices between two batches.
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

// FirstReceived returns the first message the smtp account received that is
// still stored, or nil if it has none.
func (s *Store) FirstReceived(ctx context.Context, accountID int64) (*FirstMessage, error) {
	r, err := s.q.FirstReceived(ctx, accountID)
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
