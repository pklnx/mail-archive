package archive

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// A message over the size cap whose UID is unknown: go-imap beta.8 never
// passes such a message to a fetch (a FETCH without UID before its body
// goes to its handler of unsolicited data), so holdBefore is tested on its
// own. The folder's last UID stays, queued messages are not saved, and the
// log says so.
func TestHoldBeforeUnknownUID(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	acc := &store.Account{Name: "mail", Host: "127.0.0.1", Port: 143, TLSMode: store.TLSModeNone, Username: "u", Enabled: true}
	if err := st.CreateAccountSealed(ctx, acc, func(int64) ([]byte, error) { return []byte("x"), nil }); err != nil {
		t.Fatal(err)
	}
	folder, err := st.GetOrCreateFolder(ctx, acc.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveBatch(ctx, folder.ID, 5, nil, nil); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	s := &Syncer{Store: st}
	w := newFolderWriter(st, folder.ID, 10, nil)
	w.metas = append(w.metas, store.MessageMeta{SHA256: strings.Repeat("a", 64)})
	w.locs = append(w.locs, store.Location{FolderID: folder.ID, UID: 6})
	w.maxUID = 6
	lim := &imapsync.LimitError{What: imapsync.LimitMessageSize, Limit: imapsync.MaxMessageSize}
	err = s.holdBefore(ctx, w, folder.ID, lim, lim, slog.New(slog.NewTextHandler(&logs, nil)))
	var stuck *StuckError
	if !errors.As(err, &stuck) || stuck.UID != 0 || strings.Contains(err.Error(), "UID") {
		t.Fatalf("err = %v", err)
	}
	got, err := st.GetOrCreateFolder(ctx, acc.ID, "A")
	if err != nil || got.LastUID != 5 {
		t.Fatalf("last UID %d (%v), want 5", got.LastUID, err)
	}
	if len(w.metas) != 1 || !strings.Contains(logs.String(), "came without its UID") {
		t.Errorf("queued %d; log %s", len(w.metas), logs.String())
	}
}
