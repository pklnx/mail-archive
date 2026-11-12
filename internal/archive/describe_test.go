package archive_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
)

// upstream is text only the IMAP server or the network could have written;
// it must never be stored or shown, only logged.
const upstream = "UPSTREAM-7f3a"

// opaque wraps an error under a text of its own that does not contain the
// wrapped error's text.
type opaque struct{ err error }

func (e opaque) Error() string { return "OPAQUE " + strings.ToUpper(e.err.Error()) }
func (e opaque) Unwrap() error { return e.err }

func TestDescribeError(t *testing.T) {
	login := &imapsync.Error{Category: imapsync.CategoryLogin, Op: "login", LoginSent: true,
		Err: errors.New("NO [AUTHENTICATIONFAILED] " + upstream)}
	examine := &imapsync.Error{Category: imapsync.CategoryServer, Op: `examine "INBOX"`, LoginSent: true,
		Err: errors.New("NO " + upstream)}
	dial := &imapsync.Error{Category: imapsync.CategoryConnect, Op: "connect to 192.0.2.7:993",
		Err: errors.New("dial tcp 192.0.2.7:993: connect: connection refused " + upstream)}
	folders := &imapsync.Error{Category: imapsync.CategoryLimit, Op: "list",
		Err: &imapsync.LimitError{What: imapsync.LimitFolders, Limit: 10000, Configured: true}}
	flags := &imapsync.Error{Category: imapsync.CategoryLimit, Op: "uid fetch",
		Err: &imapsync.LimitError{What: imapsync.LimitFlags, Limit: 1000, UID: 42}}
	idle := &imapsync.Error{Category: imapsync.CategoryTimeout, Op: "list",
		Err: &imapsync.TimeoutError{Command: "LIST", Idle: 2 * time.Minute}}
	deadline := &imapsync.Error{Category: imapsync.CategoryTimeout, Op: "login",
		Err: fmt.Errorf("%w: %w", context.DeadlineExceeded, errors.New(upstream))}
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"local", errors.New("count lost messages: db down"), "count lost messages: db down"},
		{"login", login, "the IMAP server refused the login"},
		{"dial", dial, "cannot connect to the IMAP server"},
		{"deadline", deadline, "the IMAP server did not answer in time"},
		{"folder", fmt.Errorf("%s: %w", "INBOX", examine), "INBOX: the IMAP server refused a command"},
		{"reconcile", fmt.Errorf("Archive: %w", fmt.Errorf("reconcile: %w", examine)),
			"Archive: reconcile: the IMAP server refused a command"},
		{"setting", &archive.SettingError{Setting: config.EnvIMAPMaxFolders, Err: folders},
			"the IMAP server exceeded a limit (folders: 10000); raise " + config.EnvIMAPMaxFolders},
		{"fixed cap", fmt.Errorf("Sent: %w", flags), "Sent: the IMAP server exceeded a limit (flags per message: 1000, message UID 42)"},
		{"idle", &archive.SettingError{Setting: config.EnvIMAPIdleTimeout, Err: idle},
			"the IMAP server did not answer in time (during LIST); raise " + config.EnvIMAPIdleTimeout},
		{"join", errors.Join(fmt.Errorf("INBOX: %w", examine), errors.New("vanished folders: db down"), fmt.Errorf("Sent: %w", login)),
			"INBOX: the IMAP server refused a command\nvanished folders: db down\nSent: the IMAP server refused the login"},
		{"stuck", fmt.Errorf("Big: %w", &archive.StuckError{UID: 9, Err: flags}),
			"Big: a message (UID 9) is larger than 256 MiB; later messages in this folder are not archived until it is removed from the server"},
		{"opaque wrapper", fmt.Errorf("INBOX: %w", opaque{examine}), "INBOX: the IMAP server refused a command"},
		{"hidden wrapper", opaque{examine}, "the IMAP server refused a command"},
		// A folder name may repeat the error's text: the error is the
		// last occurrence.
		{"folder repeats", fmt.Errorf("%s: %w", examine.Error(), examine), examine.Error() + ": the IMAP server refused a command"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := archive.DescribeError(c.err); got != c.want {
				t.Errorf("DescribeError = %q, want %q", got, c.want)
			}
		})
	}
	for _, cat := range []imapsync.Category{imapsync.CategoryConnect, imapsync.CategoryTLS, imapsync.CategoryLogin,
		imapsync.CategoryTimeout, imapsync.CategoryServer, imapsync.CategoryProtocol, imapsync.CategoryLimit, "new"} {
		got := archive.DescribeError(&imapsync.Error{Category: cat, Op: "op " + upstream, Err: errors.New(upstream)})
		if got == "" || strings.Contains(got, upstream) || got != archive.CategoryText(cat) {
			t.Errorf("category %q: %q", cat, got)
		}
	}
}

// A sync run's stored error, which the web UI shows and the alert sends as
// lastError, holds the category's sentence, never the server's text; the
// server log has it.
func TestSyncRunErrorHidesServerText(t *testing.T) {
	var refuse atomic.Bool
	refuse.Store(true)
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Send("* OK [CAPABILITY IMAP4rev1] ready"); err != nil {
			return err
		}
		tag, err := c.Expect("LOGIN")
		if err != nil {
			return err
		}
		if refuse.Load() {
			return c.Send(tag + " NO [AUTHENTICATIONFAILED] " + upstream)
		}
		if err := c.Send(tag + " OK [CAPABILITY IMAP4rev1] logged in"); err != nil {
			return err
		}
		if err := sendList(c, "INBOX"); err != nil {
			return err
		}
		tag, err = c.Expect("EXAMINE")
		if err != nil {
			return err
		}
		if err := c.Send(tag + " NO [SERVERBUG] " + upstream); err != nil {
			return err
		}
		return c.Serve()
	})
	f := newFixture(t)
	acc := f.scriptAccount("work", s)

	// The login fails: the run fails, and the alert is due at once.
	r := f.syncer.SyncAccountWith(f.ctx, acc, archive.SyncOptions{})
	if r.Err == nil || !strings.Contains(r.Err.Error(), upstream) {
		t.Fatalf("result %+v: the caller (and the runner's log) gets the raw error", r)
	}
	if err := s.Wait(t); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRunError(acc.Name); got != "the IMAP server refused the login" {
		t.Errorf("stored run error %q", got)
	}
	pending, err := f.store.ClaimNotifications(f.ctx, []byte("lease"), time.Minute, 1, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending alerts %+v, %v", pending, err)
	}
	if got := pending[0].LastError; got != "the IMAP server refused the login" {
		t.Errorf("alert lastError %q", got)
	}

	// A folder fails: the run is partial, and its error names the folder.
	refuse.Store(false)
	r = f.syncer.SyncAccountWith(f.ctx, acc, archive.SyncOptions{})
	if err := s.Wait(t); err != nil {
		t.Fatal(err)
	}
	if r.Err == nil {
		t.Fatalf("result %+v", r)
	}
	if got := f.lastRunError(acc.Name); got != "INBOX: the IMAP server refused a command" {
		t.Errorf("stored run error %q", got)
	}
	if !strings.Contains(f.logs.String(), upstream) {
		t.Errorf("the server log lacks the server's text:\n%s", f.logs)
	}
}

// lastRunError returns the error of the account's latest sync run.
func (f *fixture) lastRunError(name string) string {
	f.t.Helper()
	stats, _, err := f.store.Stats(f.ctx, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, s := range stats {
		if s.Account == name && s.LastRunError != nil {
			return *s.LastRunError
		}
	}
	f.t.Fatalf("no run of %q", name)
	return ""
}

// A fill's error and its folders' errors, which the web UI shows and the
// alert of a recurring fill sends, hold no server text either.
func TestFillErrorHidesServerText(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := sendList(c, "INBOX"); err != nil {
			return err
		}
		tag, err := c.Expect("EXAMINE")
		if err != nil {
			return err
		}
		if err := c.Send(tag + " NO [SERVERBUG] " + upstream); err != nil {
			return err
		}
		return c.Serve()
	})
	f := newFixture(t)
	acc := f.smtpAccount()
	fill := &store.Fill{AccountID: acc.ID, Host: s.Host, Port: s.Port, TLSMode: store.TLSModeNone, Username: "u", Cutoff: &fillCutoff}
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(f.sealer, id, "pw") }
	if err := f.store.CreateFill(f.ctx, fill, seal); err != nil {
		t.Fatal(err)
	}
	got, err := f.filler().Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := "INBOX: the IMAP server refused a command"; got.Error != want {
		t.Errorf("fill error %q, want %q", got.Error, want)
	}
	if want := "the IMAP server refused a command"; folderOf(got, "INBOX").Error != want {
		t.Errorf("folder error %q, want %q", folderOf(got, "INBOX").Error, want)
	}
	if !strings.Contains(f.logs.String(), upstream) {
		t.Errorf("the server log lacks the server's text:\n%s", f.logs)
	}
}
