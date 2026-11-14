package archive_test

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/imapsync"
	"github.com/keepia/archive/internal/imaptest"
	"github.com/keepia/archive/internal/store"
)

// reconcileScript serves an INBOX of exists messages that the archive
// already synced, and answers the reconcile's flag listing with flags(i)
// for messages 1 to responses.
func reconcileScript(t *testing.T, exists, responses int, flags func(i int) string) *imaptest.Script {
	return imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := sendList(c, "INBOX"); err != nil {
			return err
		}
		if err := expectExamine(c, "INBOX", exists, exists+1); err != nil {
			return err
		}
		tag, err := c.Expect("UID FETCH")
		if err != nil {
			return err
		}
		var b strings.Builder
		for i := 1; i <= responses; i++ {
			fmt.Fprintf(&b, "* %d FETCH (UID %d FLAGS (%s))\r\n", i, i, flags(i))
		}
		b.WriteString(tag + " OK done\r\n")
		if _, err := io.WriteString(c, b.String()); err != nil {
			return nil //nolint:nilerr // the client closed early
		}
		return c.Serve()
	})
}

// syncedAccount adds an account on s whose INBOX the archive synced up to
// UID lastUID.
func (f *fixture) syncedAccount(s *imaptest.Script, lastUID uint32) *store.Account {
	f.t.Helper()
	acc := f.scriptAccount("mail", s)
	folder, err := f.store.GetOrCreateFolder(f.ctx, acc.ID, "INBOX")
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.ResetFolder(f.ctx, folder.ID, 1); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.store.SaveBatch(f.ctx, folder.ID, lastUID, nil, nil); err != nil {
		f.t.Fatal(err)
	}
	return acc
}

// The reconcile's UID and flag lists are allocated once, for as many
// messages as the listing accepts: EXISTS + 1,000, at most the limit. A
// limit error names its setting only when the configured limit was the
// bound.
func TestReconcileListingLimits(t *testing.T) {
	seen := func(int) string { return `\Seen` }
	for _, tc := range []struct {
		name                     string
		exists, limit, responses int
		wantCap                  int
		wantErr                  string
	}{
		{"EXISTS + 1,000", 1, 0, 1001, 1001, ""},
		{"one more", 1, 0, 1002, 1001, "server exceeded the limit on FETCH responses (1001)"},
		{"the setting", 10, 10, 11, 10, "the folder has more than 10 messages: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := reconcileScript(t, tc.exists, tc.responses, seen)
			f := newFixture(t)
			f.syncer.MaxReconcileMessages = uint32(tc.limit) //nolint:gosec // test sizes
			acc := f.syncedAccount(s, uint32(tc.exists))     //nolint:gosec // test sizes
			var gotLen, gotCap int
			archive.SetAfterListFlags(f.syncer, func(n, c int) { gotLen, gotCap = n, c })
			r := f.syncer.SyncAccountWith(f.ctx, acc, archive.SyncOptions{Reconcile: true})
			if gotCap != tc.wantCap || gotLen > gotCap {
				t.Errorf("listed %d, capacity %d; want capacity %d", gotLen, gotCap, tc.wantCap)
			}
			named := strings.Contains(fmt.Sprint(r.Err), "raise "+config.EnvReconcileMaxMessages)
			switch {
			case tc.wantErr == "" && r.Err != nil:
				t.Errorf("err = %v", r.Err)
			case tc.wantErr != "" && (r.Err == nil || !strings.Contains(r.Err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want %q", r.Err, tc.wantErr)
			case named != (tc.limit != 0):
				t.Errorf("err = %v: names the setting %v, want %v", r.Err, named, tc.limit != 0)
			}
		})
	}
}

// A folder whose distinct flag combinations take more than 4 MiB is not
// reconciled: the run is partial and nothing is written.
func TestReconcileFlagSetCap(t *testing.T) {
	// 4,200 messages × 100 flags of 10 B: about 4.6 MB of distinct sets.
	const n = 4200
	s := reconcileScript(t, n, n, func(i int) string {
		fl := make([]string, 100)
		for j := range fl {
			fl[j] = fmt.Sprintf("k%04d_%03d", i, j)
		}
		return strings.Join(fl, " ")
	})
	f := newFixture(t)
	acc := f.syncedAccount(s, n)
	r := f.syncer.SyncAccountWith(f.ctx, acc, archive.SyncOptions{Reconcile: true})
	if r.Err == nil || !strings.Contains(r.Err.Error(), "INBOX: reconcile: the folder's distinct flag combinations take more than 4 MiB") ||
		r.Reconcile.Folders != 0 {
		t.Fatalf("result %+v", r)
	}
	runs, _ := f.store.LastRuns(f.ctx)
	if st := runs[acc.ID].Status; st != "partial" {
		t.Errorf("status %s", st)
	}
}

// The cap counts each distinct set's map entry, not only its bytes: 50,000
// sets of 1-4 bytes (about 200 KB of keys, but about 4.4 MB of map) trip
// it (review of #535).
func TestReconcileFlagSetCapShortKeys(t *testing.T) {
	const n = 50_000
	s := reconcileScript(t, n, n, func(i int) string { return strconv.FormatInt(int64(i), 36) })
	f := newFixture(t)
	acc := f.syncedAccount(s, n)
	r := f.syncer.SyncAccountWith(f.ctx, acc, archive.SyncOptions{Reconcile: true})
	if r.Err == nil || !strings.Contains(r.Err.Error(), "distinct flag combinations take more than 4 MiB") {
		t.Fatalf("result %+v", r)
	}
}

// listScript answers LIST with n folders of 1,000-byte names.
func listScript(t *testing.T, n int) *imaptest.Script {
	return imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		tag, err := c.Expect("LIST")
		if err != nil {
			return err
		}
		var b strings.Builder
		pad := strings.Repeat("x", 990)
		for i := range n {
			fmt.Fprintf(&b, "* LIST () \"/\" \"%s%09d\"\r\n", pad, i)
		}
		b.WriteString(tag + " OK done\r\n")
		if _, err := io.WriteString(c, b.String()); err != nil {
			return nil //nolint:nilerr // the client closed early
		}
		return c.Serve()
	})
}

// A login check may read more than 16 MiB when the folder limit is raised
// (about 1.1 KiB per folder), so that an honest list of 16,500 long
// folder names (about 17 MB) passes. At the default limit the check fails
// on the folder limit, and the error names its setting.
func TestCheckLoginReadScalesWithFolders(t *testing.T) {
	const n = 16_500
	s := listScript(t, n)
	acc := &store.Account{Host: s.Host, Port: s.Port, TLSMode: store.TLSModeNone, Username: "u"}
	raised := &archive.Syncer{IMAP: archive.IMAPSettings{MaxFolders: 20_000}}
	folders, err := raised.CheckLogin(t.Context(), acc, "pw")
	if err != nil || len(folders) != n {
		t.Fatalf("%d folders, err %v", len(folders), err)
	}
	_, err = (&archive.Syncer{}).CheckLogin(t.Context(), acc, "pw")
	if imapsync.CategoryOf(err) != imapsync.CategoryLimit || !strings.Contains(err.Error(), "raise "+config.EnvIMAPMaxFolders) {
		t.Fatalf("default limit: %v", err)
	}
}

// reconcileVanished finds listed folders by name (a map): a
// listed folder that is due but failed to sync is not taken for vanished.
func TestReconcileListedFolderNotVanished(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := sendList(c, "A"); err != nil {
			return err
		}
		tag, err := c.Expect("EXAMINE")
		if err != nil {
			return err
		}
		if err := c.Send(tag + " NO no such folder"); err != nil {
			return err
		}
		return c.Serve()
	})
	f := newFixture(t)
	acc := f.scriptAccount("mail", s)
	if _, err := f.store.GetOrCreateFolder(f.ctx, acc.ID, "A"); err != nil {
		t.Fatal(err)
	}
	r := f.syncer.SyncAccountWith(f.ctx, acc, archive.SyncOptions{Reconcile: true})
	if r.Err == nil || r.Reconcile.Folders != 0 || strings.Contains(f.logs.String(), "folder no longer on the server") {
		t.Fatalf("result %+v; log %s", r, f.logs.String())
	}
}
