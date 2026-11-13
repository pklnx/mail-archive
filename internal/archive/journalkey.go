package archive

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/pklnx/mail-archive/internal/store"
)

// A fill and a journal import leave a mailbox message out as journaled
// only when the account received the same message over SMTP (#4 of the
// security fixes, advisory GHSA-w6jg-ffvw-q4f5): the same Message-ID, the
// same Date as an instant to the second, the same From addresses (case and
// display name ignored) and the same Subject after decoding and unfolding.
// The Message-ID alone let a sender keep any mailbox message out of the
// archive by sending a forged one with its Message-ID through the journal.
//
// journalKey is the only comparison, applied to the values ParseHeaders
// gives on both sides: the stored columns of the journaled message, which
// BuildMeta filled from ParseHeaders, and the mailbox copy's header. Every
// doubt goes towards a duplicate: a missing Message-ID, Date or From, or a
// value an older version stored differently, gives no match, and the
// message is imported.

// journalKey identifies a message for the journaled check. The digest
// covers the normalised From and Subject, so that a fill keeps 32 bytes per
// listed message instead of both header values.
type journalKey struct {
	messageID string
	date      int64 // Unix seconds
	digest    [32]byte
}

// newJournalKey returns the key of a message's header values; ok is false
// when the message can never count as journaled: no Message-ID, no Date
// that parses, or no From. An empty Subject matches an empty one: the
// header is optional.
func newJournalKey(messageID string, date *time.Time, from, subject string) (journalKey, bool) {
	id := clean(strings.Trim(strings.TrimSpace(messageID), "<>"), maxHeaderField)
	if id == "" || date == nil {
		return journalKey{}, false
	}
	addrs := fromAddresses(clean(from, maxHeaderField))
	if addrs == "" {
		return journalKey{}, false
	}
	subject = norm.NFC.String(clean(subject, maxHeaderField))
	return journalKey{
		messageID: id,
		date:      date.Unix(),
		digest:    sha256.Sum256([]byte(addrs + "\x00" + subject)),
	}, true
}

// headersKey is the journal key of a parsed header.
func headersKey(h Headers) (journalKey, bool) {
	return newJournalKey(h.MessageID, h.Date, h.From, h.Subject)
}

var angleAddr = regexp.MustCompile(`<([^<>]*)>`)

// fromAddresses returns the mailbox addresses of a decoded From value,
// lowercased and joined with ",", without display names, which servers
// re-quote and re-encode. A decoded display name may no longer parse
// ("Doe, Jöhn <john@example.com>"): then the content of every <...> counts,
// and failing that the whole value.
func fromAddresses(from string) string {
	var addrs []string
	if list, err := mail.ParseAddressList(from); err == nil {
		for _, a := range list {
			addrs = append(addrs, a.Address)
		}
	} else if m := angleAddr.FindAllStringSubmatch(from, -1); len(m) > 0 {
		for _, s := range m {
			addrs = append(addrs, s[1])
		}
	} else {
		addrs = []string{from}
	}
	out := addrs[:0]
	for _, a := range addrs {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			out = append(out, a)
		}
	}
	return strings.Join(out, ",")
}

// journaledRowFactor bounds the candidate rows of one batch: this many per
// message. Copies beyond it go unrecognised and are stored twice; a cut can
// cause duplicates, never a gap.
const journaledRowFactor = 10

// journaledKeys returns which of keys the account received over SMTP. The
// store pre-selects candidates by Message-ID and Date; the comparison is
// journalKey's.
func journaledKeys(ctx context.Context, st *store.Store, accountID int64, keys []journalKey,
	log *slog.Logger) (map[journalKey]bool, error) {
	out := map[journalKey]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(keys))
	dates := make([]time.Time, 0, len(keys))
	seenID, seenDate := map[string]bool{}, map[int64]bool{}
	for _, k := range keys {
		if !seenID[k.messageID] {
			seenID[k.messageID] = true
			ids = append(ids, k.messageID)
		}
		if !seenDate[k.date] {
			seenDate[k.date] = true
			dates = append(dates, time.Unix(k.date, 0))
		}
	}
	maxRows := journaledRowFactor * len(keys)
	rows, err := st.JournaledCandidates(ctx, accountID, ids, dates, maxRows)
	if err != nil {
		return nil, err
	}
	if len(rows) >= maxRows {
		log.Warn("too many journaled messages with these Message-IDs; some copies may be stored twice",
			"candidates", len(rows), "messages", len(keys))
	}
	for _, r := range rows {
		if k, ok := newJournalKey(r.MessageID, r.SentAt, r.From, r.Subject); ok {
			out[k] = true
		}
	}
	return out, nil
}
