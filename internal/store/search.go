package store

import (
	"context"
	"errors"
	"fmt"
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

// SearchFilter selects messages for a listing. Empty fields are ignored,
// except Owner: only messages found in that user's accounts are listed.
type SearchFilter struct {
	Owner   int64
	Query   string
	Account string
	Folder  string
	// From, To and Attachment match a substring of the sender, of a To or
	// Cc recipient, or of an attachment file name, in any case.
	From       string
	To         string
	Attachment string
	// HasAttachment lists only messages with an attachment. Attachment
	// implies it.
	HasAttachment bool
	After         *time.Time
	Before        *time.Time
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
	// HasAttachment is false for messages not reindexed since attachments
	// were recorded.
	HasAttachment bool
}

// SearchMessages lists messages newest first, optionally filtered by a
// full-text query, account, folder, sender, recipient, attachment and date
// range.
func (s *Store) SearchMessages(ctx context.Context, f SearchFilter) ([]MessageSummary, error) {
	p := db.SearchMessagesParams{
		Owner: f.Owner, Account: nonEmpty(f.Account), Folder: nonEmpty(f.Folder),
		FromPattern: likePattern(f.From), ToPattern: likePattern(f.To), AttachmentPattern: likePattern(f.Attachment),
		HasAttachment: f.HasAttachment || strings.TrimSpace(f.Attachment) != "",
		After:         f.After, Before: f.Before, RowLimit: int32(min(max(f.Limit, 1), 500)), //nolint:gosec // clamped
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
			SentAt: r.SentAt, SortAt: derefTime(r.SortAt), Snippet: r.Snippet, HasAttachment: r.HasAttachment,
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
	// Superseded: stored under an older UIDVALIDITY of the folder. The server
	// renumbered the folder since; the location is kept as history.
	Superseded bool
}

// MessageDetail is a message's stored metadata and locations.
type MessageDetail struct {
	MessageSummary
	MessageID  string
	StoredPath string
	Locations  []MessageLocation
}

// GetMessageDetail returns metadata and the user's own locations of one
// message. Messages not found in any of the user's accounts are ErrNotFound.
func (s *Store) GetMessageDetail(ctx context.Context, owner int64, sha256 string) (*MessageDetail, error) {
	r, err := s.q.GetMessageSummary(ctx, db.GetMessageSummaryParams{Sha256: sha256, Owner: owner})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	locs, err := s.q.ListLocations(ctx, db.ListLocationsParams{Sha256: sha256, Owner: owner})
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
			Account: l.Account, Folder: l.Folder, UID: l.Uid, Flags: l.Flags, InternalDate: l.InternalDate, Superseded: l.Superseded,
		})
	}
	return d, nil
}

// FolderCount is the number of distinct archived messages in a folder.
type FolderCount struct {
	Name         string
	Messages     int64
	LastSyncedAt *time.Time
}

// AccountFolders is an account with its folders, for navigation.
type AccountFolders struct {
	Name    string
	Kind    AccountKind
	Enabled bool
	Removed bool
	Folders []FolderCount
}

// ListAccountFolders returns a user's accounts with their folders and counts.
func (s *Store) ListAccountFolders(ctx context.Context, owner int64) ([]AccountFolders, error) {
	rows, err := s.q.ListFolderCounts(ctx, owner)
	if err != nil {
		return nil, err
	}
	var out []AccountFolders
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].Name != r.Account {
			out = append(out, AccountFolders{Name: r.Account, Kind: AccountKind(r.Kind), Enabled: r.Enabled, Removed: r.Removed, Folders: []FolderCount{}})
		}
		if r.Folder != nil {
			a := &out[len(out)-1]
			a.Folders = append(a.Folders, FolderCount{Name: *r.Folder, Messages: r.Messages, LastSyncedAt: r.LastSyncedAt})
		}
	}
	return out, nil
}

// IndexVersion is the version of the extraction behind IndexData. Rows
// with a lower index_version are filled again by reindex. Version 2 added
// recipients and attachments; rows from before stay at 0.
const IndexVersion = 2

// IndexData is what search needs from a message beyond its headers.
type IndexData struct {
	BodyText string // plain text for full-text search
	To       string // decoded To header
	Cc       string // decoded Cc header
	// AttachmentNames are sanitized file names without newlines.
	AttachmentNames []string
	HasAttachment   bool
}

// attachmentNames joins file names for the attachment_names column.
func (d IndexData) attachmentNames() string { return strings.Join(d.AttachmentNames, "\n") }

// Unindexed is a message whose index data is missing or from an older
// IndexVersion.
type Unindexed struct {
	SHA256     string
	StoredPath string
}

// ListUnindexed returns up to limit messages below IndexVersion whose hash
// sorts after the given one. Passing the last hash of each batch walks the
// table once.
func (s *Store) ListUnindexed(ctx context.Context, after string, limit int) ([]Unindexed, error) {
	rows, err := s.q.ListUnindexed(ctx, db.ListUnindexedParams{
		IndexVersion: IndexVersion, After: after, RowLimit: int32(min(max(limit, 1), 10000)), //nolint:gosec // clamped
	})
	if err != nil {
		return nil, err
	}
	out := make([]Unindexed, 0, len(rows))
	for _, r := range rows {
		out = append(out, Unindexed{SHA256: r.Sha256, StoredPath: r.StoredPath})
	}
	return out, nil
}

// HasUnindexed reports whether any message needs a reindex.
func (s *Store) HasUnindexed(ctx context.Context) (bool, error) {
	return s.q.HasUnindexed(ctx, IndexVersion)
}

// Indexed is the extracted index data of one message.
type Indexed struct {
	SHA256 string
	IndexData
}

// SetIndexData stores extracted index data for a batch of messages in one
// transaction and returns how many rows it updated. Rows that a newer
// version already extracted stay as they are.
func (s *Store) SetIndexData(ctx context.Context, batch []Indexed) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	updated := 0
	for _, m := range batch {
		n, err := q.SetIndexData(ctx, db.SetIndexDataParams{
			Sha256: m.SHA256, BodyText: m.BodyText, ToAddr: m.To, CcAddr: m.Cc,
			AttachmentNames: m.attachmentNames(), HasAttachment: m.HasAttachment, IndexVersion: IndexVersion,
		})
		if err != nil {
			return 0, fmt.Errorf("update message %s: %w", m.SHA256, err)
		}
		updated += int(n)
	}
	return updated, tx.Commit(ctx)
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

// likePattern returns an ILIKE pattern that matches s as a substring, or
// nil for an empty s.
func likePattern(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	p := "%" + escapeLike(s) + "%"
	return &p
}

// escapeLike escapes LIKE wildcards so user input matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
