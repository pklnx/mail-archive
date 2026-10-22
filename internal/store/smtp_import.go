package store

import (
	"context"
	"errors"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// An import of files into an smtp account (#152) stores mbox files, a
// Maildir or .eml files in the account's journal folder, with a fill's
// rules: next to what arrives over SMTP, without an envelope, and nothing
// the account received over SMTP. Every run has a row; the locations it
// added name it, so the message view shows the label.

// SMTPImport is one run of an import of files into an smtp account.
type SMTPImport struct {
	ID, AccountID int64
	// Label names the origin in the message view.
	Label string
	// Format is mbox, maildir or eml; Source the path as given.
	Format, Source string
	CreatedAt      time.Time
}

// MaxImportLabel is the longest label of an import, in characters.
const MaxImportLabel = 200

// ErrImportLabel is returned for an empty or too long label, or one with
// control characters.
var ErrImportLabel = errors.New("the label must be 1 to 200 characters without control characters")

// CheckImportLabel checks the label of an import.
func CheckImportLabel(label string) error {
	if label == "" || utf8.RuneCountInString(label) > MaxImportLabel || !utf8.ValidString(label) {
		return ErrImportLabel
	}
	for _, c := range label {
		if unicode.IsControl(c) {
			return ErrImportLabel
		}
	}
	return nil
}

// CreateSMTPImport records a run of an import of files into an smtp
// account. It fails with ErrNotSMTP for other kinds of accounts and with
// ErrNotFound for a removed or missing one. A pending audit entry in ctx
// gets the import's ID and is written in the same transaction.
func (s *Store) CreateSMTPImport(ctx context.Context, imp *SMTPImport) error {
	if err := CheckImportLabel(imp.Label); err != nil {
		return err
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		a, err := q.GetAccount(ctx, imp.AccountID)
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
		row, err := q.InsertSMTPImport(ctx, db.InsertSMTPImportParams{
			AccountID: imp.AccountID, Label: imp.Label, Format: imp.Format, Source: imp.Source,
		})
		if err != nil {
			return err
		}
		imp.ID, imp.CreatedAt = row.ID, row.CreatedAt
		if e := auditFrom(ctx); e != nil {
			e.Detail("import", imp.ID)
		}
		return nil
	})
}

// JournalFolderID returns the ID of an smtp account's journal folder, or
// ErrNotFound if it has none.
func (s *Store) JournalFolderID(ctx context.Context, accountID int64) (int64, error) {
	id, err := s.q.JournalFolder(ctx, db.JournalFolderParams{AccountID: accountID, Folder: SMTPFolder})
	if isNoRows(err) {
		return 0, ErrNotFound
	}
	return id, err
}

// SaveJournalImportBatch is SaveFillBatch for an import of files: it stores
// the messages in the journal folder (folderID) under its row lock, numbers
// the new locations after what deliveries added meanwhile, and names the
// import in them. Messages deleted by retention are left out (res.Skipped),
// and so are those the folder already holds (present). The caller holds
// the blob lock (the import holds the account's sync lock for writing).
func (s *Store) SaveJournalImportBatch(ctx context.Context, folderID, importID int64, metas []MessageMeta, locs []Location) (SaveResult, int, error) {
	return s.saveIntoJournal(ctx, folderID, SMTPFolder, metas, locs, nil, &importID, nil)
}
