package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// SMTPFolder is the only folder of an smtp account.
const SMTPFolder = "INBOX"

// ErrAddressTaken is returned for an address another smtp account
// receives for.
var ErrAddressTaken = errors.New("another smtp account receives for this address")

// ErrBlobGone is returned by ReceiveMessage when the message file, or the
// row its metadata relied on, disappeared before the transaction took the
// blob lock: verify moved it as an orphan. Delivering again stores it anew.
var ErrBlobGone = errors.New("the message file or row disappeared before it was committed")

// ErrNotReceiving is returned when a message arrives for an smtp account
// that is disabled or was removed meanwhile.
var ErrNotReceiving = errors.New("the account does not receive mail")

// NormalizeSMTPAddress checks an address an smtp account receives for and
// returns it in lower case. It must not carry a "+extension": that part of
// an incoming address encodes the original recipient.
func NormalizeSMTPAddress(addr string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndexByte(a, '@')
	switch {
	case at < 1 || at == len(a)-1 || strings.Count(a, "@") != 1:
		return "", fmt.Errorf("invalid address %q", addr)
	case strings.ContainsAny(a, "+ \t\r\n<>\"\\,;"):
		return "", fmt.Errorf("invalid address %q: no \"+\", spaces or quotes; the part after \"+\" is the original recipient", addr)
	}
	return a, nil
}

func normalizeSMTPAddresses(addrs []string) ([]string, error) {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		a, err := NormalizeSMTPAddress(addr)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out, nil
}

// CreateSMTPAccount creates an smtp account with its addresses and its
// folder in one transaction. It fails with ErrConflict if the owner has an
// account with the name, with ErrAddressTaken if another account receives
// for an address.
func (s *Store) CreateSMTPAccount(ctx context.Context, a *Account, addresses []string) error {
	addrs, err := normalizeSMTPAddresses(addresses)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return errors.New("an smtp account needs at least one address")
	}
	a.Kind = KindSMTP
	return s.inTx(ctx, func(q *db.Queries) error {
		if err := createAccount(ctx, q, a); err != nil {
			return err
		}
		if err := insertSMTPAddresses(ctx, q, a.ID, addrs); err != nil {
			return err
		}
		_, err := q.CreateSMTPFolder(ctx, db.CreateSMTPFolderParams{
			AccountID: a.ID, Name: SMTPFolder, Uidvalidity: time.Now().Unix() & 0xffffffff,
		})
		return err
	})
}

func insertSMTPAddresses(ctx context.Context, q *db.Queries, accountID int64, addrs []string) error {
	for _, addr := range addrs {
		n, err := q.InsertSMTPAddress(ctx, db.InsertSMTPAddressParams{Address: addr, AccountID: accountID})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%s: %w", addr, ErrAddressTaken)
		}
	}
	return nil
}

// ChangeSMTPAddresses adds and removes addresses of an smtp account,
// checked against ref like UpdateAccount. An address of another account
// fails with ErrAddressTaken; the account keeps at least one address.
func (s *Store) ChangeSMTPAddresses(ctx context.Context, ref AccountRef, add, remove []string) error {
	add, err := normalizeSMTPAddresses(add)
	if err != nil {
		return err
	}
	remove, err = normalizeSMTPAddresses(remove)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		r, err := lockAccount(ctx, q, ref)
		if err != nil {
			return err
		}
		if AccountKind(r.Kind) != KindSMTP {
			return fmt.Errorf("%q is not an smtp account", r.Name)
		}
		for _, addr := range remove {
			n, err := q.DeleteSMTPAddress(ctx, db.DeleteSMTPAddressParams{Address: addr, AccountID: r.ID})
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("address %s: %w", addr, ErrNotFound)
			}
		}
		existing, err := q.AccountSMTPAddresses(ctx, r.ID)
		if err != nil {
			return err
		}
		var fresh []string
		for _, addr := range add {
			if !slices.Contains(existing, addr) {
				fresh = append(fresh, addr)
			}
		}
		if err := insertSMTPAddresses(ctx, q, r.ID, fresh); err != nil {
			return err
		}
		if len(existing)+len(fresh) == 0 {
			return errors.New("an smtp account needs at least one address")
		}
		return one(q.BumpAccountVersion(ctx, r.ID))
	})
}

// SMTPAddresses returns the addresses of all smtp accounts by account ID.
func (s *Store) SMTPAddresses(ctx context.Context) (map[int64][]string, error) {
	rows, err := s.q.ListSMTPAddresses(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]string)
	for _, r := range rows {
		out[r.AccountID] = append(out[r.AccountID], r.Address)
	}
	return out, nil
}

// LastReceived returns when each smtp account last accepted a message.
func (s *Store) LastReceived(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := s.q.LastReceived(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]time.Time, len(rows))
	for _, r := range rows {
		out[r.AccountID] = *r.LastSyncedAt
	}
	return out, nil
}

// SMTPRoute is the account an address belongs to.
type SMTPRoute struct {
	AccountID int64
	Enabled   bool
	Removed   bool
}

// LookupSMTPAddress returns the account that receives for a normalized
// address, or ErrNotFound.
func (s *Store) LookupSMTPAddress(ctx context.Context, addr string) (SMTPRoute, error) {
	r, err := s.q.LookupSMTPAddress(ctx, addr)
	if errors.Is(err, pgx.ErrNoRows) {
		return SMTPRoute{}, ErrNotFound
	}
	if err != nil {
		return SMTPRoute{}, err
	}
	return SMTPRoute{AccountID: r.ID, Enabled: r.Enabled, Removed: r.Removed}, nil
}

// Envelope is the SMTP envelope of a received message.
type Envelope struct {
	MailFrom   string
	RcptTo     []string // original recipients
	ReceivedAt time.Time
	ClientIP   netip.Addr
	HELO       string
}

// ReceiveResult tells what ReceiveMessage stored.
type ReceiveResult struct {
	UID uint32
	// New is set when the message got a new location; otherwise it was
	// already received and only its recipients were merged.
	New bool
	// AddedRecipients counts recipients new to the envelope.
	AddedRecipients int
	// NewMessage is set when the message was new to the archive.
	NewMessage bool
}

// ReceiveMessage stores a message received over SMTP for an smtp account,
// with its envelope, in one transaction. A message the folder already holds
// (a retry, or a delivery Postfix split into several transactions) gets
// no second location: its envelope gains the new recipients instead. It
// fails with ErrNotReceiving if the account is disabled or removed.
//
// The message file is written before, without a lock. The transaction
// takes the blob lock shared, like sync and import, and then checks with
// fileExists that the file is still there, so verify can never have moved
// it as an orphan; nor, for metadata that relied on an existing row
// (meta.Known), the row. Otherwise it fails with ErrBlobGone.
func (s *Store) ReceiveMessage(ctx context.Context, accountID int64, meta MessageMeta, env Envelope, fileExists func() bool) (ReceiveResult, error) {
	var res ReceiveResult
	err := s.inTx(ctx, func(q *db.Queries) error {
		res = ReceiveResult{}
		if err := q.LockBlobsShared(ctx, blobLockClass); err != nil {
			return fmt.Errorf("wait for the blob lock: %w", err)
		}
		if !fileExists() {
			return ErrBlobGone
		}
		if meta.Known {
			exists, err := q.MessageExists(ctx, meta.SHA256)
			if err != nil {
				return err
			}
			if !exists {
				return ErrBlobGone
			}
		}
		acc, err := q.ShareAccount(ctx, accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotReceiving
		}
		if err != nil {
			return err
		}
		if AccountKind(acc.Kind) != KindSMTP || !acc.Enabled || acc.Removed {
			return ErrNotReceiving
		}
		folder, err := q.LockFolder(ctx, db.LockFolderParams{AccountID: accountID, Name: SMTPFolder})
		if err != nil {
			return fmt.Errorf("lock folder: %w", err)
		}
		rcpts := uniqueRecipients(nil, env.RcptTo)
		existing, err := q.FindReceivedLocation(ctx, db.FindReceivedLocationParams{Sha256: meta.SHA256, FolderID: folder.ID})
		switch {
		case err == nil:
			merged := uniqueRecipients(existing.RcptTo, rcpts)
			res.UID = uint32(existing.Uid) //nolint:gosec // UIDs stay below 2^32
			res.AddedRecipients = len(merged) - len(existing.RcptTo)
			if res.AddedRecipients > 0 {
				if err := q.SetEnvelopeRecipients(ctx, db.SetEnvelopeRecipientsParams{
					LocationID: existing.ID, RcptTo: merged, RcptText: strings.Join(merged, "\n"),
				}); err != nil {
					return err
				}
			}
			return q.TouchReceivedFolder(ctx, folder.ID)
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		n, err := q.InsertMessage(ctx, insertMessageParams(&meta))
		if err != nil {
			return fmt.Errorf("insert message %s: %w", meta.SHA256, err)
		}
		res.NewMessage = n == 1
		uid, err := q.NextUID(ctx, folder.ID)
		if err != nil {
			return err
		}
		received := env.ReceivedAt
		locID, err := q.InsertReceivedLocation(ctx, db.InsertReceivedLocationParams{
			MessageSha256: meta.SHA256, FolderID: folder.ID, Uidvalidity: folder.Uidvalidity, Uid: uid,
			InternalDate: &received,
		})
		if err != nil {
			return fmt.Errorf("insert location: %w", err)
		}
		var ip *netip.Addr
		if env.ClientIP.IsValid() {
			ip = &env.ClientIP
		}
		err = q.InsertEnvelope(ctx, db.InsertEnvelopeParams{
			LocationID: locID, MailFrom: env.MailFrom, RcptTo: rcpts, RcptText: strings.Join(rcpts, "\n"),
			ReceivedAt: received, ClientIp: ip, Helo: env.HELO,
		})
		if err != nil {
			return fmt.Errorf("insert envelope: %w", err)
		}
		res.UID, res.New, res.AddedRecipients = uint32(uid), true, len(rcpts) //nolint:gosec // UIDs stay below 2^32
		return nil
	})
	return res, err
}

// uniqueRecipients appends the recipients of add that have not been seen,
// ignoring case, to have.
func uniqueRecipients(have, add []string) []string {
	out := slices.Clone(have)
	if out == nil {
		out = []string{}
	}
	for _, r := range add {
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, r) }) {
			out = append(out, r)
		}
	}
	return out
}
