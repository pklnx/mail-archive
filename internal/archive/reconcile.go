package archive

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/store"
)

// DefaultMaxReconcileMessages caps the folders a reconcile lists. Listing
// keeps one UID and one interned flag string per message, about 50 MB at
// the cap.
const DefaultMaxReconcileMessages = 2_000_000

// SyncOptions change what one sync does beyond copying new messages.
type SyncOptions struct {
	// Reconcile compares every selected folder with the server whose last
	// reconcile is at least ReconcileMinAge old, whatever the Syncer's
	// ReconcileInterval. Without it, only folders due by the interval are
	// compared.
	Reconcile       bool
	ReconcileMinAge time.Duration
}

// reconcileDue reports whether a folder last reconciled at last should be
// compared with the server now. The interval gets 10 % slack, so that a
// daily reconcile does not slip to the sync after next.
func (s *Syncer) reconcileDue(last *time.Time, opts SyncOptions, now time.Time) bool {
	switch {
	case opts.Reconcile:
		return last == nil || now.Sub(*last) >= opts.ReconcileMinAge
	case s.ReconcileInterval > 0:
		return last == nil || now.Sub(*last) >= s.ReconcileInterval-s.ReconcileInterval/10
	}
	return false
}

// reconcileFolder lists the UIDs and flags of the examined folder and
// records which locations are gone. Nothing is written unless the server
// listed every message.
func (s *Syncer) reconcileFolder(ctx context.Context, conn *imapsync.Conn, folder *store.Folder) (store.FolderReconcile, error) {
	n := conn.Messages()
	limit := s.MaxReconcileMessages
	if limit == 0 {
		limit = DefaultMaxReconcileMessages
	}
	if n > limit {
		return store.FolderReconcile{}, fmt.Errorf("%d messages, more than the limit of %d", n, limit)
	}
	uids := make([]int64, 0, n)
	flags := make([]string, 0, n)
	if n > 0 {
		// Most messages share a few flag combinations: keep one string each.
		intern := map[string]string{}
		var key []byte
		err := imapsync.ListFlags(conn, func(uid uint32, fl []imap.Flag) {
			key = flagKey(key[:0], fl)
			k, ok := intern[string(key)]
			if !ok {
				k = string(key)
				intern[k] = k
			}
			uids = append(uids, int64(uid))
			flags = append(flags, k)
		})
		if err != nil {
			return store.FolderReconcile{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return store.FolderReconcile{}, err
	}
	return s.Store.ReconcileFolder(ctx, folder.ID, folder.UIDValidity, uids, flags)
}

// flagKey appends the flags as stored (see storedFlags), sorted and
// space-separated.
func flagKey(dst []byte, flags []imap.Flag) []byte {
	sorted := make([]string, 0, len(flags))
	for _, f := range flags {
		if !isRecent(string(f)) && f != "" {
			sorted = append(sorted, string(f))
		}
	}
	slices.Sort(sorted)
	for i, f := range sorted {
		if i > 0 {
			dst = append(dst, ' ')
		}
		dst = append(dst, f...)
	}
	return dst
}

// storedFlags drops \Recent, which only describes the current session.
// The IMAP client already spells system flags in their usual case.
func storedFlags(flags []string) []string {
	return slices.DeleteFunc(flags, isRecent)
}

// isRecent matches \Recent (IMAP4rev1 only, set per session).
func isRecent(flag string) bool { return strings.EqualFold(flag, `\Recent`) }

// warnOnLoss logs a warning when a folder lost many messages at once: at
// least 100, or at least 10 % of the ones that were there. Only counts and
// the folder name are logged.
func warnOnLoss(log *slog.Logger, r store.FolderReconcile) {
	if r.Gone > 0 && (r.Gone >= 100 || r.Gone*10 >= r.PresentBefore) {
		log.Warn("many messages no longer on the server", "gone", r.Gone, "before", r.PresentBefore)
	}
}

// reconcileVanished marks the stored folders that the filters select but
// the server no longer lists. listed are the names from a successful LIST.
func (s *Syncer) reconcileVanished(ctx context.Context, a *store.Account, listed []imapsync.Folder,
	opts SyncOptions, now time.Time, counts *store.ReconcileCounts, log *slog.Logger) error {
	states, err := s.Store.ListFolderStates(ctx, a.ID)
	if err != nil {
		return err
	}
	for _, f := range states {
		if !FolderSelected(f.Name, a.IncludedFolders, a.ExcludedFolders) || !s.reconcileDue(f.LastReconciledAt, opts, now) {
			continue
		}
		if slices.ContainsFunc(listed, func(l imapsync.Folder) bool { return l.Name == f.Name }) {
			continue
		}
		n, err := s.Store.MarkFolderVanished(ctx, f.ID)
		if err != nil {
			return err
		}
		counts.Folders++
		counts.Gone += n
		if n > 0 {
			log.Warn("folder no longer on the server", "folder", f.Name, "gone", n)
		}
	}
	return nil
}
