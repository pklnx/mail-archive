package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Snippet highlight markers (Unicode private use area). Clients split the
// snippet on them instead of interpreting any markup.
const (
	HighlightStart = ""
	HighlightEnd   = ""
)

// SearchFilter selects messages for a listing. Empty fields are ignored.
type SearchFilter struct {
	Query   string
	Account string
	Folder  string
	After   *time.Time
	Before  *time.Time
	// Cursor continues a previous page (the last row's SortAt and SHA256).
	CursorAt  *time.Time
	CursorSHA string
	Limit     int
}

// MessageSummary is one row of a listing.
type MessageSummary struct {
	SHA256  string
	Size    int64
	Subject string
	From    string
	SentAt  *time.Time
	SortAt  time.Time
	Snippet string
}

// SearchMessages lists messages newest first, optionally filtered by a
// full-text query, account, folder and date range.
func (s *Store) SearchMessages(ctx context.Context, f SearchFilter) ([]MessageSummary, error) {
	p := db.SearchMessagesParams{
		Account: nonEmpty(f.Account), Folder: nonEmpty(f.Folder),
		After: f.After, Before: f.Before, RowLimit: int32(min(max(f.Limit, 1), 500)), //nolint:gosec // clamped
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		pattern := "%" + escapeLike(q) + "%"
		p.Query, p.Pattern = &q, &pattern
	}
	if f.CursorAt != nil && f.CursorSHA != "" {
		p.CursorAt, p.CursorSha = f.CursorAt, &f.CursorSHA
	}
	rows, err := s.q.SearchMessages(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]MessageSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, MessageSummary{
			SHA256: r.Sha256, Size: r.Size, Subject: deref(r.Subject), From: deref(r.FromAddr),
			SentAt: r.SentAt, SortAt: derefTime(r.SortAt), Snippet: r.Snippet,
		})
	}
	return out, nil
}

// MessageLocation is one place where a message was found.
type MessageLocation struct {
	Account      string
	Folder       string
	UID          int64
	Flags        []string
	InternalDate *time.Time
}

// MessageDetail is a message's stored metadata and locations.
type MessageDetail struct {
	MessageSummary
	MessageID  string
	StoredPath string
	Locations  []MessageLocation
}

// GetMessageDetail returns metadata and locations of one message.
func (s *Store) GetMessageDetail(ctx context.Context, sha256 string) (*MessageDetail, error) {
	r, err := s.q.GetMessageSummary(ctx, sha256)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	locs, err := s.q.ListLocations(ctx, sha256)
	if err != nil {
		return nil, err
	}
	d := &MessageDetail{
		MessageSummary: MessageSummary{
			SHA256: r.Sha256, Size: r.Size, Subject: deref(r.Subject), From: deref(r.FromAddr),
			SentAt: r.SentAt, SortAt: derefTime(r.SortAt),
		},
		MessageID: deref(r.MessageID), StoredPath: r.StoredPath,
	}
	for _, l := range locs {
		d.Locations = append(d.Locations, MessageLocation{
			Account: l.Account, Folder: l.Folder, UID: l.Uid, Flags: l.Flags, InternalDate: l.InternalDate,
		})
	}
	return d, nil
}

// FolderCount is the number of archived message locations in a folder.
type FolderCount struct {
	Name         string
	Messages     int64
	LastSyncedAt *time.Time
}

// AccountFolders is an account with its folders, for navigation.
type AccountFolders struct {
	Name    string
	Enabled bool
	Folders []FolderCount
}

// ListAccountFolders returns all accounts with their folders and counts.
func (s *Store) ListAccountFolders(ctx context.Context) ([]AccountFolders, error) {
	rows, err := s.q.ListFolderCounts(ctx)
	if err != nil {
		return nil, err
	}
	var out []AccountFolders
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].Name != r.Account {
			out = append(out, AccountFolders{Name: r.Account, Enabled: r.Enabled, Folders: []FolderCount{}})
		}
		if r.Folder != nil {
			a := &out[len(out)-1]
			a.Folders = append(a.Folders, FolderCount{Name: *r.Folder, Messages: r.Messages, LastSyncedAt: r.LastSyncedAt})
		}
	}
	return out, nil
}

// Unindexed is a message whose body text has not been extracted yet.
type Unindexed struct {
	SHA256     string
	StoredPath string
}

// ListUnindexed returns up to limit messages without extracted body text.
func (s *Store) ListUnindexed(ctx context.Context, limit int) ([]Unindexed, error) {
	rows, err := s.q.ListUnindexed(ctx, int32(min(max(limit, 1), 10000))) //nolint:gosec // clamped
	if err != nil {
		return nil, err
	}
	out := make([]Unindexed, 0, len(rows))
	for _, r := range rows {
		out = append(out, Unindexed{SHA256: r.Sha256, StoredPath: r.StoredPath})
	}
	return out, nil
}

// SetBodyText stores extracted body text for full-text search.
func (s *Store) SetBodyText(ctx context.Context, sha256, text string) error {
	return s.q.SetBodyText(ctx, db.SetBodyTextParams{Sha256: sha256, BodyText: &text})
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// escapeLike escapes LIKE wildcards so user input matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
