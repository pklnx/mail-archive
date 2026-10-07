package archive_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/mailbox"
	"github.com/pklnx/mail-archive/internal/store"
)

func (e *archiveEnv) export(opts archive.ExportOptions) (*archive.ExportResult, error) {
	e.t.Helper()
	if opts.Owner == nil {
		opts.Owner = &e.user.ID
	}
	if opts.AccountIDs == nil {
		opts.AccountIDs = []int64{e.acc.ID}
	}
	opts.Logger = quiet
	return archive.Export(e.ctx, e.st, e.blobs, opts)
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// readMbox returns the hashes of the messages in an mbox file.
func readMbox(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	r := mailbox.NewMboxReader(f)
	var out []string
	for {
		m, err := r.Next()
		if errors.Is(err, io.EOF) {
			slices.Sort(out)
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, sum(m.Data))
	}
}

// readMaildir returns the hashes and flags of the messages in a Maildir.
func readMaildir(t *testing.T, dir string) (hashes []string, flags map[string][]string) {
	t.Helper()
	entries, err := mailbox.ReadMaildir(dir)
	if err != nil {
		t.Fatal(err)
	}
	flags = map[string][]string{}
	for _, e := range entries {
		data, err := os.ReadFile(e.Path)
		if err != nil {
			t.Fatal(err)
		}
		h := sum(data)
		hashes = append(hashes, h)
		flags[h] = e.Flags
	}
	slices.Sort(hashes)
	return hashes, flags
}

func TestExportRoundTrip(t *testing.T) {
	e := newArchiveEnv(t)
	archived := e.folderOf(e.acc, "Archive", 5)
	inbox := []string{
		e.save(e.folder, 1, "Subject: one\r\n\r\nFrom the start\r\n>From quoted\r\n", `\Seen`, `\Answered`).SHA256,
		e.save(e.folder, 2, "Subject: two\n\nLF only\n\n").SHA256,
		e.save(e.folder, 3, msg(3), `\Flagged`, `$Label1`).SHA256,
	}
	other := []string{e.save(archived, 1, msg(9)).SHA256}
	slices.Sort(inbox)

	out := filepath.Join(t.TempDir(), "export")
	res, err := e.export(archive.ExportOptions{Format: archive.FormatMbox, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	if res.Folders != 2 || res.Messages != 4 || len(res.Skipped) != 0 {
		t.Fatalf("result %+v", res)
	}
	if got := readMbox(t, filepath.Join(out, "mail", "INBOX.mbox")); !slices.Equal(got, inbox) {
		t.Errorf("INBOX: %v, want %v", got, inbox)
	}
	if got := readMbox(t, filepath.Join(out, "mail", "Archive.mbox")); !slices.Equal(got, other) {
		t.Errorf("Archive: %v, want %v", got, other)
	}
	for _, p := range []string{out, filepath.Join(out, "mail"), filepath.Join(out, "mail", "INBOX.mbox")} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if want := os.FileMode(0o700); fi.IsDir() && fi.Mode().Perm() != want || !fi.IsDir() && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v", p, fi.Mode())
		}
	}

	out = filepath.Join(t.TempDir(), "maildir")
	if _, err := e.export(archive.ExportOptions{Format: archive.FormatMaildir, Out: out, Folder: "INBOX"}); err != nil {
		t.Fatal(err)
	}
	got, flags := readMaildir(t, filepath.Join(out, "mail", "INBOX"))
	if !slices.Equal(got, inbox) {
		t.Errorf("Maildir: %v, want %v", got, inbox)
	}
	if f := flags[inbox[slices.Index(inbox, sum([]byte("Subject: one\r\n\r\nFrom the start\r\n>From quoted\r\n")))]]; !slices.Equal(f, []string{`\Answered`, `\Seen`}) && !slices.Equal(f, []string{`\Seen`, `\Answered`}) {
		t.Errorf("flags %v", f)
	}
	if _, err := os.Stat(filepath.Join(out, "mail", "Archive")); !os.IsNotExist(err) {
		t.Errorf("--folder exported other folders: %v", err)
	}
}

// Only the owner's mail is exported, even if other accounts are asked for.
func TestExportOwnerScoping(t *testing.T) {
	e := newArchiveEnv(t)
	bob, err := e.st.CreateUser(e.ctx, "bob", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	bobAcc := e.account(bob, "mail") // the same account name as Anna's
	bobInbox := e.folderOf(bobAcc, "INBOX", 1)
	e.save(bobInbox, 1, msg(1))
	annas := e.save(e.folder, 1, msg(2)).SHA256

	out := filepath.Join(t.TempDir(), "x")
	res, err := e.export(archive.ExportOptions{Format: archive.FormatMbox, Out: out, AccountIDs: []int64{e.acc.ID, bobAcc.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 1 {
		t.Fatalf("result %+v", res)
	}
	if got := readMbox(t, filepath.Join(out, "mail", "INBOX.mbox")); !slices.Equal(got, []string{annas}) {
		t.Errorf("exported %v", got)
	}
	// Bob's account with Anna as owner: nothing.
	res, err = e.export(archive.ExportOptions{Format: archive.FormatMbox, Out: filepath.Join(t.TempDir(), "y"), AccountIDs: []int64{bobAcc.ID}})
	if err != nil || res.Folders != 0 || res.Messages != 0 {
		t.Errorf("foreign account: %+v %v", res, err)
	}
}

func TestExportFolderNamesStayInside(t *testing.T) {
	e := newArchiveEnv(t)
	names := []string{"../x", "a/b", ".", "..", "%", "%2F", `c\d`, ".hidden"}
	for i, n := range names {
		e.save(e.folderOf(e.acc, n, 1), 1, msg(10+i))
	}
	parent := t.TempDir()
	out := filepath.Join(parent, "out")
	for _, format := range []string{archive.FormatMbox, archive.FormatMaildir} {
		_ = os.RemoveAll(out)
		res, err := e.export(archive.ExportOptions{Format: format, Out: out})
		if err != nil {
			t.Fatal(err)
		}
		if res.Folders != len(names)+1 || res.Messages != len(names) {
			t.Fatalf("%s: %+v", format, res)
		}
		top, _ := os.ReadDir(parent)
		if len(top) != 1 {
			t.Errorf("%s: wrote next to the export: %v", format, top)
		}
		entries, _ := os.ReadDir(filepath.Join(out, "mail"))
		if len(entries) != len(names)+1 {
			t.Errorf("%s: %d entries in the account directory: %v", format, len(entries), entries)
		}
		for _, en := range entries {
			if strings.HasPrefix(en.Name(), ".") || strings.ContainsAny(en.Name(), `/\`) {
				t.Errorf("%s: entry %q", format, en.Name())
			}
		}
	}
	for in, want := range map[string]string{"../x": "%2E.%2Fx", "a/b": "a%2Fb", ".": "%2E", "%": "%25", "INBOX": "INBOX", "Gelöscht": "Gelöscht", "a\x00b\nc": "a%00b%0Ac", "": "%"} {
		if got := archive.PathSegment(in); got != want {
			t.Errorf("PathSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExportRefusals(t *testing.T) {
	e := newArchiveEnv(t)
	e.save(e.folder, 1, msg(1))
	existing := t.TempDir()
	if _, err := e.export(archive.ExportOptions{Format: archive.FormatMbox, Out: existing}); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("existing directory: %v", err)
	}
	for _, inside := range []string{e.path("messages/export"), e.path("tmp/x/y")} {
		if _, err := e.export(archive.ExportOptions{Format: archive.FormatMbox, Out: inside}); err == nil || !strings.Contains(err.Error(), "inside") {
			t.Errorf("%s: %v", inside, err)
		}
	}
	// Through a symlink into the store.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(e.path("messages"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := e.export(archive.ExportOptions{Format: archive.FormatMbox, Out: filepath.Join(link, "x")}); err == nil || !strings.Contains(err.Error(), "inside") {
		t.Errorf("through a symlink: %v", err)
	}
	if _, err := e.export(archive.ExportOptions{Format: "pst", Out: filepath.Join(t.TempDir(), "x")}); err == nil {
		t.Error("unknown format accepted")
	}
	if _, err := e.export(archive.ExportOptions{Format: archive.FormatMbox, Out: filepath.Join(t.TempDir(), "x"), Folder: "Nope"}); err == nil {
		t.Error("unknown folder accepted")
	}
	// The data directory itself is fine, next to messages/.
	out := e.path("export/anna")
	if _, err := e.export(archive.ExportOptions{Format: archive.FormatMbox, Out: out}); err != nil {
		t.Errorf("export below the data directory: %v", err)
	}
}

// A missing or corrupt file is skipped and leaves no partial message.
func TestExportSkipsBrokenFiles(t *testing.T) {
	e := newArchiveEnv(t)
	good := e.save(e.folder, 1, msg(1))
	missing := e.save(e.folder, 2, msg(2))
	corrupt := e.save(e.folder, 3, msg(3))
	last := e.save(e.folder, 4, msg(4))
	_ = os.Remove(e.path(missing.Path))
	data, _ := os.ReadFile(e.path(corrupt.Path))
	data[len(data)-3] ^= 1
	_ = os.WriteFile(e.path(corrupt.Path), data, 0o600)

	for _, format := range []string{archive.FormatMbox, archive.FormatMaildir} {
		out := filepath.Join(t.TempDir(), "out")
		res, err := e.export(archive.ExportOptions{Format: format, Out: out})
		if err != nil {
			t.Fatal(err)
		}
		if res.Messages != 2 || len(res.Skipped) != 2 {
			t.Fatalf("%s: %+v", format, res)
		}
		var got []string
		if format == archive.FormatMbox {
			got = readMbox(t, filepath.Join(out, "mail", "INBOX.mbox"))
		} else {
			got, _ = readMaildir(t, filepath.Join(out, "mail", "INBOX"))
			if tmp, _ := os.ReadDir(filepath.Join(out, "mail", "INBOX", "tmp")); len(tmp) != 0 {
				t.Errorf("tmp not empty: %v", tmp)
			}
		}
		want := []string{good.SHA256, last.SHA256}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: exported %v, want %v", format, got, want)
		}
	}
}

// After a UIDVALIDITY change a message has two locations in the folder;
// it is written once. A location added after the start is left out.
func TestExportDuplicatesAndLateLocations(t *testing.T) {
	e := newArchiveEnv(t)
	b := e.save(e.folder, 1, msg(1))
	if err := e.st.ResetFolder(e.ctx, e.folder.ID, 2); err != nil {
		t.Fatal(err)
	}
	e.folder.UIDValidity = 2
	e.saveRow(e.folder, 1, store.MessageMeta{SHA256: b.SHA256, Size: b.Size, StoredPath: b.Path})

	opts := archive.ExportOptions{Format: archive.FormatMbox, Out: filepath.Join(t.TempDir(), "out")}
	archive.SetAfterStart(&opts, func() { e.save(e.folder, 2, msg(2)) })
	res, err := e.export(opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 1 {
		t.Fatalf("result %+v", res)
	}
	if got := readMbox(t, filepath.Join(opts.Out, "mail", "INBOX.mbox")); !slices.Equal(got, []string{b.SHA256}) {
		t.Errorf("exported %v", got)
	}
}

// After an error nothing is left: no output, no partial directory.
func TestExportCleansUpAfterError(t *testing.T) {
	e := newArchiveEnv(t)
	e.save(e.folder, 1, msg(1))
	parent := t.TempDir()
	opts := archive.ExportOptions{Format: archive.FormatMbox, Out: filepath.Join(parent, "out")}
	archive.SetAfterStart(&opts, func() { e.st.Close() })
	if _, err := e.export(opts); err == nil {
		t.Fatal("no error")
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}
