package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/mailbox"
	"github.com/pklnx/mail-archive/internal/store"
)

func newImportCmd() *cobra.Command {
	var (
		from, format, folder, maxSize string
		dryRun                        bool
	)
	cmd := &cobra.Command{
		Use:   "import NAME --from PATH --format mbox|maildir",
		Short: "Import mbox files or a Maildir into an import account",
		Long: `Store the messages of mbox files or a Maildir in the archive, like a sync
stores messages from a server, with the same deduplication. They go into the
import account NAME, which is created if needed; an existing import account
gets the new mail added. Import accounts have no server and are never synced.

--from is a single mbox file, an Apple Mail .mbox bundle, a directory of mbox
files (Thunderbird's local folders), or a Maildir. Folder names come from the
files; with --folder a single folder gets that name and several get it as a
prefix. Running the same import again adds only what is missing, so an
interrupted import can simply be run again.

Messages are stored as IMAP delivers them: if their first line ends in LF
only, every bare LF becomes CRLF. Nothing else changes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd, args[0], from, format, folder, maxSize, dryRun)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "the file or directory to import (required; with Docker Compose under /import)")
	cmd.Flags().StringVar(&format, "format", "", "mbox or maildir (required)")
	cmd.Flags().StringVar(&folder, "folder", "", "the folder name, or a prefix when the source has several folders")
	cmd.Flags().String("user", "", "the user who owns the import account (needed when several users exist)")
	cmd.Flags().StringVar(&maxSize, "max-message-size", "256MiB", "skip larger messages")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only list the folders and message counts; store nothing")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("format")
	return cmd
}

func runImport(cmd *cobra.Command, name, from, format, folder, maxSize string, dryRun bool) error {
	ctx := cmd.Context()
	if err := archive.ValidateAccountName(name); err != nil {
		return err
	}
	limit, err := parseSize(maxSize)
	if err != nil {
		return fmt.Errorf("--max-message-size: %w", err)
	}
	folders, skipped, err := archive.ImportFolders(format, from, folder)
	if errors.Is(err, fs.ErrNotExist) && commandName() == "./ma" && !strings.HasPrefix(from, "/import/") {
		return fmt.Errorf("%w\nWith Docker Compose, put the files into ./import (IMPORT_DIR in .env) and use --from /import/<name>", err)
	}
	if err != nil {
		return err
	}
	if dryRun {
		return printDryRun(cmd.OutOrStdout(), folders, skipped)
	}

	a, err := openApp(ctx)
	if err != nil {
		return err
	}
	defer a.close()
	acc, err := importAccount(cmd, a, name)
	if err != nil {
		return err
	}
	blobs, err := blobstore.New(a.cfg.DataDir)
	if err != nil {
		return err
	}
	log := newLogger(a.cfg.LogLevel).With("account", acc.Name)
	for _, s := range skipped {
		log.Debug("import skips a file", "path", s.Path, "reason", s.Reason)
	}
	names, err := userNames(cmd, a)
	if err != nil {
		return err
	}
	log.Info("import started", "owner", ownerName(names, acc.OwnerID), "format", format, "source", from, "folders", len(folders))
	im := &archive.Importer{
		Store: a.store, Blobs: blobs, Logger: log, MaxMessageSize: limit,
		Progress: func(f archive.ImportFolderResult) {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: read %d, added %d\n", f.Folder, f.Read, f.Added)
		},
	}
	res, err := im.Import(ctx, acc, folders)
	if errors.Is(err, archive.ErrSyncRunning) {
		return fmt.Errorf("an import into %q is already running", acc.Name)
	}
	if res != nil {
		printImportResult(cmd.OutOrStdout(), res)
	}
	if err != nil {
		return err
	}
	if res.Status == "partial" {
		return &exitError{msg: "some messages were skipped", code: 1}
	}
	return nil
}

// importAccount returns the owner's import account with this name, creating
// it if needed. IMAP and removed accounts are refused.
func importAccount(cmd *cobra.Command, a *app, name string) (*store.Account, error) {
	ctx := cmd.Context()
	owner, err := newOwner(cmd, a)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		acc, err := ownedAccount(ctx, a, owner, name)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err == nil {
			switch {
			case acc.Kind != store.KindImport:
				return nil, fmt.Errorf("%q is an IMAP account; choose another name for the import", name)
			case acc.RemovedAt != nil:
				return nil, fmt.Errorf("the import account %q was removed; choose another name", name)
			}
			return acc, nil
		}
		acc = &store.Account{Kind: store.KindImport, Name: name, OwnerID: owner}
		err = a.store.CreateAccount(ctx, acc)
		if errors.Is(err, store.ErrConflict) && attempt == 0 {
			continue // created meanwhile: read it again
		}
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "import account %q created\n", name)
		return acc, nil
	}
}

func ownedAccount(ctx context.Context, a *app, owner *int64, name string) (*store.Account, error) {
	if owner != nil {
		return a.store.GetOwnedAccount(ctx, *owner, name)
	}
	accounts, err := a.store.ListAccountsByName(ctx, name)
	if err != nil {
		return nil, err
	}
	for _, acc := range accounts {
		if acc.OwnerID == nil {
			return acc, nil
		}
	}
	return nil, store.ErrNotFound
}

func printDryRun(out io.Writer, folders []mailbox.SourceFolder, skipped []mailbox.Skipped) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FOLDER\tMESSAGES\tSOURCE")
	total := 0
	for _, f := range folders {
		n, err := countMessages(f)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		total += n
		fmt.Fprintf(w, "%s\t%d\t%s\n", f.Name, n, f.Path)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%d message(s) in %d folder(s); nothing was stored\n", total, len(folders))
	for _, s := range skipped {
		fmt.Fprintf(out, "skipped %s: %s\n", s.Path, s.Reason)
	}
	return nil
}

func countMessages(f mailbox.SourceFolder) (int, error) {
	if f.Kind == mailbox.KindMaildir {
		files, _, err := mailbox.MaildirFiles(f.Path)
		return len(files), err
	}
	file, err := os.Open(f.Path) //nolint:gosec // the import source given by the operator
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	s := mailbox.NewMboxScanner(file)
	n := 0
	for {
		_, err := s.Next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		n++
	}
}

func printImportResult(out io.Writer, res *archive.ImportResult) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FOLDER\tREAD\tADDED\tNEW\tPRESENT\tSKIPPED")
	for _, f := range res.Folders {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\n", f.Folder, f.Read, f.Added, f.New, f.Present, f.Skipped)
	}
	_ = w.Flush()
	t := res.Total()
	fmt.Fprintf(out, "\n%s: read %d, added %d (%d new to the archive), %d already there, %d skipped\n",
		res.Status, t.Read, t.Added, t.New, t.Present, t.Skipped)
}

// parseSize reads a size like 256MiB, 100MB, 10M or 1048576.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		factor int64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3},
		{"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1},
	}
	factor := int64(1)
	for _, u := range units {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			s, factor = strings.TrimSpace(n), u.factor
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 || n > (1<<40)/factor {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * factor, nil
}
