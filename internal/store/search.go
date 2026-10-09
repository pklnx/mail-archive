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
	// Thread lists only the messages of one thread (a thread key from
	// MessageSummary.Thread).
	Thread string
	After  *time.Time
	Before *time.Time
	// Gone lists only messages no longer on any server: the owner's IMAP
	// locations (of Account, if set) exist and are all gone.
	Gone bool
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
	// Thread and Count are set by SearchThreads: the thread key and the
	// number of the thread's messages that match the filter.
	Thread string
	Count  int64
}

// SearchMessages lists messages newest first, optionally filtered by a
// full-text query, account, folder, sender, recipient, attachment, thread
// and date range.
func (s *Store) SearchMessages(ctx context.Context, f SearchFilter) ([]MessageSummary, error) {
	rows, err := s.q.SearchMessages(ctx, searchParams(f))
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

// SearchThreads lists one row per thread, newest first: the newest message
// of the thread that matches the filter, with the number of matches in the
// thread. Its cursor works like SearchMessages'.
func (s *Store) SearchThreads(ctx context.Context, f SearchFilter) ([]MessageSummary, error) {
	p := searchParams(f)
	rows, err := s.q.SearchThreads(ctx, db.SearchThreadsParams(p))
	if err != nil || len(rows) == 0 {
		return []MessageSummary{}, err
	}
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.ThreadKey)
	}
	counts, err := s.q.CountThreadMatches(ctx, db.CountThreadMatchesParams{
		ThreadKeys: keys, Query: p.Query, Pattern: p.Pattern, Owner: p.Owner, Account: p.Account, Folder: p.Folder,
		FromPattern: p.FromPattern, ToPattern: p.ToPattern, AttachmentPattern: p.AttachmentPattern,
		HasAttachment: p.HasAttachment, Thread: p.Thread, After: p.After, Before: p.Before,
	})
	if err != nil {
		return nil, err
	}
	count := make(map[string]int64, len(counts))
	for _, c := range counts {
		count[c.ThreadKey] = c.Matches
	}
	out := make([]MessageSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, MessageSummary{
			SHA256: r.Sha256, Size: r.Size, Subject: deref(r.Subject), From: deref(r.FromAddr),
			SentAt: r.SentAt, SortAt: derefTime(r.SortAt), Snippet: r.Snippet, HasAttachment: r.HasAttachment,
			Thread: r.ThreadKey, Count: max(count[r.ThreadKey], 1),
		})
	}
	return out, nil
}

func searchParams(f SearchFilter) db.SearchMessagesParams {
	p := db.SearchMessagesParams{
		Owner: f.Owner, Account: nonEmpty(f.Account), Folder: nonEmpty(f.Folder),
		FromPattern: likePattern(f.From), ToPattern: likePattern(f.To), AttachmentPattern: likePattern(f.Attachment),
		HasAttachment: f.HasAttachment || strings.TrimSpace(f.Attachment) != "",
		Thread:        nonEmpty(f.Thread), After: f.After, Before: f.Before, Gone: f.Gone, RowLimit: int32(min(max(f.Limit, 1), 500)), //nolint:gosec // clamped
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		pattern := "%" + escapeLike(q) + "%"
		p.Query, p.Pattern = &q, &pattern
	}
	if f.CursorAt != nil && f.CursorSHA != "" {
		p.CursorAt, p.CursorSha = f.CursorAt, &f.CursorSHA
	}
	return p
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
	// GoneAt is when a reconcile found the location missing on the server.
	GoneAt *time.Time
	// LastSeenAt is when the server last listed the location.
	LastSeenAt time.Time
}

// MessageDetail is a message's stored metadata and locations.
type MessageDetail struct {
	MessageSummary
	MessageID  string
	StoredPath string
	// InReplyTo is the Message-ID this message answers; Thread its thread
	// key.
	InReplyTo string
	Thread    string
	Locations []MessageLocation
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
		MessageID: deref(r.MessageID), StoredPath: r.StoredPath, InReplyTo: deref(r.InReplyTo), Thread: r.ThreadKey,
	}
	for _, l := range locs {
		d.Locations = append(d.Locations, MessageLocation{
			Account: l.Account, Folder: l.Folder, UID: l.Uid, Flags: l.Flags, InternalDate: l.InternalDate, Superseded: l.Superseded,
			GoneAt: l.GoneAt, LastSeenAt: l.LastSeenAt,
		})
	}
	return d, nil
}

// Relation of a conversation member to the message it was listed for.
const (
	RelationSelf   = "self"
	RelationParent = "parent" // the message it answers
	RelationReply  = "reply"  // answers it, or names it in References
	RelationThread = "thread" // another message of the thread
)

// ConversationEntry is one message of a conversation.
type ConversationEntry struct {
	SHA256   string
	Subject  string
	From     string
	SentAt   *time.Time
	SortAt   time.Time
	Relation string
}

// Conversation is the conversation of a message, oldest first. Truncated
// says that older messages beyond the limit were left out; Total counts
// them all.
type Conversation struct {
	Messages  []ConversationEntry
	Total     int64
	Truncated bool
}

// MaxConversation is the number of messages GetConversation returns.
const MaxConversation = 200

// GetConversation returns the messages of a message's thread and its
// direct parent and replies that the user may see. Like GetMessageDetail,
// a message the user may not see is ErrNotFound.
func (s *Store) GetConversation(ctx context.Context, owner int64, sha256 string) (*Conversation, error) {
	if _, err := s.q.GetMessageSummary(ctx, db.GetMessageSummaryParams{Sha256: sha256, Owner: owner}); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := s.q.ListConversation(ctx, db.ListConversationParams{Owner: owner, Sha256: sha256, RowLimit: MaxConversation})
	if err != nil {
		return nil, err
	}
	total, err := s.q.CountConversation(ctx, db.CountConversationParams{Owner: owner, Sha256: sha256})
	if err != nil {
		return nil, err
	}
	c := &Conversation{Messages: make([]ConversationEntry, len(rows)), Total: total, Truncated: total > int64(len(rows))}
	for i, r := range rows {
		// The query returns the newest first.
		c.Messages[len(rows)-1-i] = ConversationEntry{
			SHA256: r.Sha256, Subject: deref(r.Subject), From: deref(r.FromAddr),
			SentAt: r.SentAt, SortAt: derefTime(r.SortAt), Relation: r.Relation,
		}
	}
	return c, nil
}

// FolderCount is the number of distinct archived messages in a folder.
type FolderCount struct {
	Name         string
	Messages     int64
	LastSyncedAt *time.Time
	// LastReconciledAt is when the folder was last compared with the server.
	LastReconciledAt *time.Time
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
			a.Folders = append(a.Folders, FolderCount{
				Name: *r.Folder, Messages: r.Messages, LastSyncedAt: r.LastSyncedAt, LastReconciledAt: r.LastReconciledAt,
			})
		}
	}
	return out, nil
}

// IndexVersion is the version of the extraction behind IndexData. Rows
// with a lower index_version are filled again by reindex. Version 2 added
// recipients and attachments, version 3 the links between replies; rows
// from before version 2 stay at 0.
const IndexVersion = 3

// IndexData is what search needs from a message beyond its headers.
type IndexData struct {
	BodyText string // plain text for full-text search
	To       string // decoded To header
	Cc       string // decoded Cc header
	// AttachmentNames are sanitized file names without newlines.
	AttachmentNames []string
	HasAttachment   bool
	// InReplyTo, ReferenceIDs and ThreadID link replies to their originals
	// (message IDs without angle brackets).
	InReplyTo    string
	ReferenceIDs []string
	ThreadID     string
}

// referenceIDs never returns nil: reference_ids is NOT NULL.
func (d IndexData) referenceIDs() []string {
	if d.ReferenceIDs == nil {
		return []string{}
	}
	return d.ReferenceIDs
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
			AttachmentNames: m.attachmentNames(), HasAttachment: m.HasAttachment,
			InReplyTo: m.InReplyTo, ReferenceIds: m.referenceIDs(), ThreadID: m.ThreadID, IndexVersion: IndexVersion,
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
