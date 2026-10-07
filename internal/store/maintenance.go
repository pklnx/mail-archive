package store

import (
	"context"
	"time"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// StoredMessage is a messages row as verify sees it.
type StoredMessage struct {
	SHA256     string
	Size       int64
	StoredPath string
}

// ListMessagesAfter returns up to limit messages with a hash greater than
// after, ordered by hash.
func (s *Store) ListMessagesAfter(ctx context.Context, after string, limit int32) ([]StoredMessage, error) {
	rows, err := s.q.ListMessagesAfter(ctx, db.ListMessagesAfterParams{After: after, PageSize: limit})
	if err != nil {
		return nil, err
	}
	out := make([]StoredMessage, len(rows))
	for i, r := range rows {
		out[i] = StoredMessage{SHA256: r.Sha256, Size: r.Size, StoredPath: r.StoredPath}
	}
	return out, nil
}

// ExistingMessages returns which of the hashes have a messages row.
func (s *Store) ExistingMessages(ctx context.Context, hashes []string) (map[string]bool, error) {
	rows, err := s.q.ExistingMessages(ctx, hashes)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, h := range rows {
		out[h] = true
	}
	return out, nil
}

// MessagePlace is one place where a message was found, with its owner.
type MessagePlace struct {
	SHA256      string
	Owner       string // empty for accounts without owner
	Account     string
	Folder      string
	UIDValidity uint32
	UID         uint32
}

// LocationsOfMessages returns where the messages were found.
func (s *Store) LocationsOfMessages(ctx context.Context, hashes []string) ([]MessagePlace, error) {
	rows, err := s.q.LocationsOfMessages(ctx, hashes)
	if err != nil {
		return nil, err
	}
	out := make([]MessagePlace, len(rows))
	for i, r := range rows {
		out[i] = MessagePlace{
			SHA256: r.Sha256, Owner: r.Owner, Account: r.Account, Folder: r.Folder,
			UIDValidity: uint32(r.Uidvalidity), //nolint:gosec // stored from uint32
			UID:         uint32(r.Uid),         //nolint:gosec // stored from uint32
		}
	}
	return out, nil
}

// MaxLocationID returns the highest location ID, 0 without locations.
func (s *Store) MaxLocationID(ctx context.Context) (int64, error) {
	return s.q.MaxLocationID(ctx)
}

// ExportFolder is a folder selected for an export.
type ExportFolder struct {
	ID      int64
	Account string
	Name    string
}

// ListExportFolders returns the folders of the accounts, or only the one
// named folder, that belong to owner (nil: accounts without owner).
// Accounts of another owner are left out.
func (s *Store) ListExportFolders(ctx context.Context, owner *int64, accountIDs []int64, folder string) ([]ExportFolder, error) {
	rows, err := s.q.ListExportFolders(ctx, db.ListExportFoldersParams{AccountIds: accountIDs, Owner: owner, Folder: folder})
	if err != nil {
		return nil, err
	}
	out := make([]ExportFolder, len(rows))
	for i, r := range rows {
		out[i] = ExportFolder{ID: r.ID, Account: r.Account, Name: r.Folder}
	}
	return out, nil
}

// ExportLocation is a message in a folder, for an export.
type ExportLocation struct {
	SHA256       string
	Size         int64
	UIDValidity  uint32
	UID          uint32
	Flags        []string
	InternalDate *time.Time
	SentAt       *time.Time
}

// ListExportLocations returns up to limit locations of a folder with an ID
// up to maxID, after the given (uidvalidity, uid), if the folder belongs to
// owner. Start with afterUIDValidity and afterUID at -1.
func (s *Store) ListExportLocations(ctx context.Context, owner *int64, folderID, maxID, afterUIDValidity, afterUID int64, limit int32) ([]ExportLocation, error) {
	rows, err := s.q.ListExportLocations(ctx, db.ListExportLocationsParams{
		FolderID: folderID, Owner: owner, MaxID: maxID,
		AfterUidvalidity: afterUIDValidity, AfterUid: afterUID, PageSize: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ExportLocation, len(rows))
	for i, r := range rows {
		out[i] = ExportLocation{
			SHA256: r.Sha256, Size: r.Size, Flags: r.Flags, InternalDate: r.InternalDate, SentAt: r.SentAt,
			UIDValidity: uint32(r.Uidvalidity), //nolint:gosec // stored from uint32
			UID:         uint32(r.Uid),         //nolint:gosec // stored from uint32
		}
	}
	return out, nil
}

// ServerMajorVersion returns the major version of the PostgreSQL server.
func (s *Store) ServerMajorVersion(ctx context.Context) (int, error) {
	var num int
	if err := s.pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&num); err != nil {
		return 0, err
	}
	return num / 10000, nil
}
