// Package store persists accounts, folders and message metadata in
// PostgreSQL.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a unique constraint is violated.
var ErrConflict = errors.New("already exists")

// Store wraps a PostgreSQL connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to the database.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases all connections.
func (s *Store) Close() { s.pool.Close() }

// Migrate applies all pending schema migrations.
func (s *Store) Migrate(ctx context.Context) ([]string, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDBFromPool(s.pool)
	defer func(db *sql.DB) { _ = db.Close() }(db)
	provider, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		return nil, err
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return nil, err
	}
	applied := make([]string, 0, len(results))
	for _, r := range results {
		applied = append(applied, r.Source.Path)
	}
	return applied, nil
}

// TLSMode describes how to secure the IMAP connection.
type TLSMode string

// Supported TLS modes.
const (
	TLSModeTLS      TLSMode = "tls"      // implicit TLS (port 993)
	TLSModeSTARTTLS TLSMode = "starttls" // STARTTLS upgrade (port 143)
	TLSModeNone     TLSMode = "none"     // plaintext, only for local testing
)

// Account is an IMAP mailbox to archive.
type Account struct {
	ID              int64
	Name            string
	Host            string
	Port            int
	TLSMode         TLSMode
	Username        string
	PasswordEnc     []byte
	IncludedFolders []string // empty means all folders
	ExcludedFolders []string
	Enabled         bool
	CreatedAt       time.Time
}

const accountColumns = `id, name, host, port, tls_mode, username, password_enc, included_folders, excluded_folders, enabled, created_at`

func scanAccount(row pgx.Row) (*Account, error) {
	var a Account
	var mode string
	err := row.Scan(&a.ID, &a.Name, &a.Host, &a.Port, &mode, &a.Username, &a.PasswordEnc, &a.IncludedFolders, &a.ExcludedFolders, &a.Enabled, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.TLSMode = TLSMode(mode)
	return &a, nil
}

// CreateAccount inserts a new account and sets its ID.
func (s *Store) CreateAccount(ctx context.Context, a *Account) error {
	if a.IncludedFolders == nil {
		a.IncludedFolders = []string{}
	}
	if a.ExcludedFolders == nil {
		a.ExcludedFolders = []string{}
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO accounts (name, host, port, tls_mode, username, password_enc, included_folders, excluded_folders, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at`,
		a.Name, a.Host, a.Port, string(a.TLSMode), a.Username, a.PasswordEnc, a.IncludedFolders, a.ExcludedFolders, a.Enabled,
	).Scan(&a.ID, &a.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("account %q: %w", a.Name, ErrConflict)
	}
	return err
}

// UpdatePassword replaces an account's encrypted password.
func (s *Store) UpdatePassword(ctx context.Context, id int64, passwordEnc []byte) error {
	return s.exec1(ctx, `UPDATE accounts SET password_enc = $2, updated_at = now() WHERE id = $1`, id, passwordEnc)
}

// SetAccountEnabled enables or disables an account.
func (s *Store) SetAccountEnabled(ctx context.Context, id int64, enabled bool) error {
	return s.exec1(ctx, `UPDATE accounts SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
}

// SetFolderFilters replaces the include and exclude folder lists.
func (s *Store) SetFolderFilters(ctx context.Context, id int64, included, excluded []string) error {
	if included == nil {
		included = []string{}
	}
	if excluded == nil {
		excluded = []string{}
	}
	return s.exec1(ctx, `UPDATE accounts SET included_folders = $2, excluded_folders = $3, updated_at = now() WHERE id = $1`, id, included, excluded)
}

// DeleteAccount removes an account that has no archived data yet.
func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM message_locations l JOIN folders f ON f.id = l.folder_id
		WHERE f.account_id = $1`, id).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("account has %d archived message locations; disable it instead", n)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM folders WHERE account_id = $1`, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// GetAccountByName looks up an account by its unique name.
func (s *Store) GetAccountByName(ctx context.Context, name string) (*Account, error) {
	return scanAccount(s.pool.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE name = $1`, name))
}

// ListAccounts returns all accounts ordered by name.
func (s *Store) ListAccounts(ctx context.Context) ([]*Account, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Folder is the sync state of one IMAP folder.
type Folder struct {
	ID          int64
	AccountID   int64
	Name        string
	UIDValidity uint32
	LastUID     uint32
}

// GetOrCreateFolder returns the sync state for a folder, creating it if new.
func (s *Store) GetOrCreateFolder(ctx context.Context, accountID int64, name string) (*Folder, error) {
	f := Folder{AccountID: accountID, Name: name}
	var uidValidity, lastUID int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO folders (account_id, name) VALUES ($1, $2)
		ON CONFLICT (account_id, name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id, uidvalidity, last_uid`, accountID, name).Scan(&f.ID, &uidValidity, &lastUID)
	if err != nil {
		return nil, err
	}
	f.UIDValidity, f.LastUID = uint32(uidValidity), uint32(lastUID) //nolint:gosec // stored from uint32
	return &f, nil
}

// ResetFolder records a new UIDVALIDITY and restarts the folder from UID 0.
func (s *Store) ResetFolder(ctx context.Context, folderID int64, uidValidity uint32) error {
	return s.exec1(ctx, `UPDATE folders SET uidvalidity = $2, last_uid = 0 WHERE id = $1`, folderID, int64(uidValidity))
}

// MessageMeta is the parsed metadata of a message.
type MessageMeta struct {
	SHA256     string
	Size       int64
	MessageID  string
	Subject    string
	From       string
	SentAt     *time.Time
	StoredPath string
}

// Location is where a message was found on a server.
type Location struct {
	FolderID     int64
	UIDValidity  uint32
	UID          uint32
	Flags        []string
	InternalDate time.Time
}

// MessageExists reports whether metadata for the hash is already stored.
func (s *Store) MessageExists(ctx context.Context, sha256 string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM messages WHERE sha256 = $1)`, sha256).Scan(&exists)
	return exists, err
}

// SaveBatch stores message metadata and locations for a batch and advances
// the folder's last UID, all in one transaction. It returns how many messages
// were new to the archive.
func (s *Store) SaveBatch(ctx context.Context, folderID int64, lastUID uint32, metas []MessageMeta, locs []Location) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	newCount := 0
	for i := range metas {
		m := &metas[i]
		tag, err := tx.Exec(ctx, `
			INSERT INTO messages (sha256, size, message_id, subject, from_addr, sent_at, stored_path)
			VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6, $7)
			ON CONFLICT (sha256) DO NOTHING`,
			m.SHA256, m.Size, m.MessageID, m.Subject, m.From, m.SentAt, m.StoredPath)
		if err != nil {
			return 0, fmt.Errorf("insert message %s: %w", m.SHA256, err)
		}
		newCount += int(tag.RowsAffected())

		l := locs[i]
		flags := l.Flags
		if flags == nil {
			flags = []string{}
		}
		var internal *time.Time
		if !l.InternalDate.IsZero() {
			internal = &l.InternalDate
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid, flags, internal_date)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (folder_id, uidvalidity, uid)
			DO UPDATE SET flags = EXCLUDED.flags, last_seen_at = now()`,
			m.SHA256, l.FolderID, int64(l.UIDValidity), int64(l.UID), flags, internal)
		if err != nil {
			return 0, fmt.Errorf("insert location: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE folders SET last_uid = GREATEST(last_uid, $2), last_synced_at = now() WHERE id = $1`,
		folderID, int64(lastUID)); err != nil {
		return 0, err
	}
	return newCount, tx.Commit(ctx)
}

// TouchFolder marks a folder as synced without new messages.
func (s *Store) TouchFolder(ctx context.Context, folderID int64) error {
	return s.exec1(ctx, `UPDATE folders SET last_synced_at = now() WHERE id = $1`, folderID)
}

// SyncRun records the outcome of syncing one account.
type SyncRun struct {
	ID              int64
	AccountID       int64
	Status          string
	MessagesFetched int
	MessagesNew     int
	Error           string
}

// StartSyncRun creates a running sync record.
func (s *Store) StartSyncRun(ctx context.Context, accountID int64) (*SyncRun, error) {
	r := SyncRun{AccountID: accountID, Status: "running"}
	err := s.pool.QueryRow(ctx, `INSERT INTO sync_runs (account_id) VALUES ($1) RETURNING id`, accountID).Scan(&r.ID)
	return &r, err
}

// FinishSyncRun stores the final state of a sync run.
func (s *Store) FinishSyncRun(ctx context.Context, r *SyncRun) error {
	return s.exec1(ctx, `
		UPDATE sync_runs SET finished_at = now(), status = $2, messages_fetched = $3, messages_new = $4, error = NULLIF($5, '')
		WHERE id = $1`, r.ID, r.Status, r.MessagesFetched, r.MessagesNew, r.Error)
}

// AccountStats summarizes the archive for one account.
type AccountStats struct {
	Account      string
	Enabled      bool
	Folders      int
	Locations    int64
	LastRunAt    *time.Time
	LastStatus   *string
	LastRunError *string
}

// Stats returns per-account statistics and the number of unique messages.
func (s *Store) Stats(ctx context.Context) ([]AccountStats, int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.name, a.enabled,
		       (SELECT count(*) FROM folders f WHERE f.account_id = a.id),
		       (SELECT count(*) FROM message_locations l JOIN folders f ON f.id = l.folder_id WHERE f.account_id = a.id),
		       r.started_at, r.status, r.error
		FROM accounts a
		LEFT JOIN LATERAL (
			SELECT started_at, status, error FROM sync_runs
			WHERE account_id = a.id ORDER BY started_at DESC LIMIT 1
		) r ON TRUE
		ORDER BY a.name`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AccountStats
	for rows.Next() {
		var st AccountStats
		if err := rows.Scan(&st.Account, &st.Enabled, &st.Folders, &st.Locations, &st.LastRunAt, &st.LastStatus, &st.LastRunError); err != nil {
			return nil, 0, err
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unique int64
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM messages`).Scan(&unique)
	return out, unique, err
}

func (s *Store) exec1(ctx context.Context, sql string, args ...any) error {
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetMessage returns the metadata of one archived message.
func (s *Store) GetMessage(ctx context.Context, sha256 string) (*MessageMeta, error) {
	var m MessageMeta
	var msgID, subject, from *string
	err := s.pool.QueryRow(ctx, `
		SELECT sha256, size, message_id, subject, from_addr, sent_at, stored_path
		FROM messages WHERE sha256 = $1`, sha256).
		Scan(&m.SHA256, &m.Size, &msgID, &subject, &from, &m.SentAt, &m.StoredPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.MessageID, m.Subject, m.From = deref(msgID), deref(subject), deref(from)
	return &m, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
