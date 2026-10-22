package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/mailbox"
	"github.com/pklnx/mail-archive/internal/store"
)

// importOptions are the flags of the import command.
type importOptions struct {
	from, format, folder, maxSize, label string
	journal, dryRun, labelSet            bool
}

func newImportCmd() *cobra.Command {
	var o importOptions
	cmd := &cobra.Command{
		Use:   "import NAME --from PATH --format mbox|maildir|eml",
		Short: "Import mbox files, a Maildir or .eml files into an import account or an smtp account's JOURNAL",
		Long: `Store the messages of mbox files, a Maildir or .eml files in the archive, like a sync
stores messages from a server, with the same deduplication. They go into the
import account NAME, which is created if needed; an existing import account
gets the new mail added. Import accounts have no server and are never synced.

--from is a single mbox file, an Apple Mail .mbox bundle, a directory of mbox
files (Thunderbird's local folders), a Maildir, or with --format eml a
directory of .eml files, one message each (an export of piler, for example).
Folder names come from the files, or for eml from the directories; with
--folder a single folder gets that name and several get it as a prefix.
.eml files directly in --from need --folder. Only files ending in .eml are
read; hidden files are skipped. Running the same import again adds only what is missing, so an
interrupted import can simply be run again.

With --journal, NAME is an existing smtp account, and the messages go into
its JOURNAL folder like a fill's: without an envelope, and without the
messages whose Message-ID the account received over SMTP. All source folders
are merged into JOURNAL, so --folder is not allowed. --label names the origin
in the message view (default: the --from path). Nothing is created.

Messages are stored as IMAP delivers them: if their first line ends in LF
only, every bare LF becomes CRLF. Nothing else changes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.labelSet = cmd.Flags().Changed("label")
			if o.labelSet && !o.journal {
				return errors.New("--label needs --journal")
			}
			if o.journal {
				return runJournalImport(cmd, args[0], o)
			}
			return runImport(cmd, args[0], o)
		},
	}
	cmd.Flags().StringVar(&o.from, "from", "", "the file or directory to import (required; with Docker Compose under /import)")
	cmd.Flags().StringVar(&o.format, "format", "", "mbox, maildir or eml (required)")
	cmd.Flags().StringVar(&o.folder, "folder", "", "the folder name, or a prefix when the source has several folders")
	cmd.Flags().String("user", "", "the user who owns the account (needed when several users exist)")
	cmd.Flags().StringVar(&o.maxSize, "max-message-size", "256MiB", "skip larger messages")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "only list the folders and message counts; store nothing")
	cmd.Flags().BoolVar(&o.journal, "journal", false, "import into the JOURNAL folder of the existing smtp account NAME")
	cmd.Flags().StringVar(&o.label, "label", "", "with --journal: the origin shown in the message view (default: the --from path)")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("format")
	return cmd
}

// importSource checks the name and size limit and lists the source folders.
func importSource(name string, o importOptions) (int64, []mailbox.SourceFolder, []mailbox.Skipped, error) {
	if err := archive.ValidateAccountName(name); err != nil {
		return 0, nil, nil, err
	}
	limit, err := parseSize(o.maxSize)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("--max-message-size: %w", err)
	}
	var (
		folders []mailbox.SourceFolder
		skipped []mailbox.Skipped
	)
	if o.journal {
		folders, skipped, err = archive.JournalImportFolders(o.format, o.from)
	} else {
		folders, skipped, err = archive.ImportFolders(o.format, o.from, o.folder)
	}
	if errors.Is(err, fs.ErrNotExist) && commandName() == "./ma" && !strings.HasPrefix(o.from, "/import/") {
		return 0, nil, nil, fmt.Errorf("%w\nWith Docker Compose, put the files into ./import (IMPORT_DIR in .env) and use --from /import/<name>", err)
	}
	return limit, folders, skipped, err
}

// logSkipped logs the files the import skips and warns about archives.
func logSkipped(log *slog.Logger, skipped []mailbox.Skipped) {
	archives := 0
	for _, s := range skipped {
		log.Debug("import skips a file", "path", s.Path, "reason", s.Reason)
		if s.Reason == mailbox.ReasonArchive {
			archives++
		}
	}
	if archives > 0 {
		log.Warn("the source holds compressed archives; their messages are not imported, unpack them first", "archives", archives)
	}
}

func runImport(cmd *cobra.Command, name string, o importOptions) error {
	ctx := cmd.Context()
	limit, folders, skipped, err := importSource(name, o)
	if err != nil {
		return err
	}
	if o.dryRun {
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
	blobs, err := openBlobs(a.cfg.DataDir)
	if err != nil {
		return err
	}
	log := newLogger(a.cfg.LogLevel).With("account", acc.Name)
	logSkipped(log, skipped)
	names, err := userNames(cmd, a)
	if err != nil {
		return err
	}
	log.Info("import started", "owner", ownerName(names, acc.OwnerID), "format", o.format, "source", o.from, "folders", len(folders))
	im := &archive.Importer{
		Store: a.store, Blobs: blobs, Logger: log, MaxMessageSize: limit,
		Progress: func(f archive.ImportFolderResult) {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: read %d, added %d\n", f.Folder, f.Read, f.Added)
		},
	}
	e := cliAccountTarget(cliEntry("data.import"), acc)
	e.Detail("format", o.format)
	e.Detail("source", o.from)
	e.Detail("folders", len(folders))
	if err := auditNow(cmd, a, e); err != nil {
		return err
	}
	res, err := im.Import(ctx, acc, folders)
	if errors.Is(err, archive.ErrSyncRunning) {
		return fmt.Errorf("an import into %q is already running", acc.Name)
	}
	if res != nil {
		printImportResult(cmd.OutOrStdout(), res, false)
	}
	if err != nil {
		return err
	}
	if res.Status == "partial" {
		return &exitError{msg: "some messages were skipped", code: 1}
	}
	return nil
}

// runJournalImport imports into the JOURNAL folder of an existing smtp
// account (#152).
func runJournalImport(cmd *cobra.Command, name string, o importOptions) error {
	ctx := cmd.Context()
	if o.folder != "" {
		return errors.New("--folder is not allowed with --journal: everything goes into " + store.SMTPFolder)
	}
	label := o.label
	if !o.labelSet {
		label = o.from
	}
	if err := store.CheckImportLabel(label); err != nil {
		return fmt.Errorf("--label: %w", err)
	}
	limit, folders, skipped, err := importSource(name, o)
	if err != nil {
		return err
	}

	a, err := openApp(ctx)
	if err != nil {
		return err
	}
	defer a.close()
	acc, err := journalAccount(cmd, a, name)
	if err != nil {
		return err
	}
	if o.dryRun {
		if err := printDryRun(cmd.OutOrStdout(), folders, skipped); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "into %s of smtp account %q\n", store.SMTPFolder, acc.Name)
		return nil
	}
	blobs, err := openBlobs(a.cfg.DataDir)
	if err != nil {
		return err
	}
	log := newLogger(a.cfg.LogLevel).With("account", acc.Name)
	logSkipped(log, skipped)
	names, err := userNames(cmd, a)
	if err != nil {
		return err
	}
	e := cliAccountTarget(cliEntry("data.import"), acc)
	e.Detail("format", o.format)
	e.Detail("source", o.from)
	e.Detail("folders", len(folders))
	e.Detail("into", store.SMTPFolder)
	e.Detail("label", label)
	imp := &store.SMTPImport{AccountID: acc.ID, Label: label, Format: o.format, Source: o.from}
	switch err := a.store.CreateSMTPImport(withCLIAudit(cmd, e), imp); {
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("the smtp account %q was removed; nothing was done", acc.Name)
	case err != nil:
		return fmt.Errorf("%w; nothing was done", err)
	}
	log.Info("import started", "owner", ownerName(names, acc.OwnerID), "format", o.format, "source", o.from,
		"folders", len(folders), "into", store.SMTPFolder, "label", label, "import", imp.ID)
	im := &archive.Importer{
		Store: a.store, Blobs: blobs, Logger: log, MaxMessageSize: limit,
		Progress: func(f archive.ImportFolderResult) {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: read %d, added %d, journaled %d\n", f.Folder, f.Read, f.Added, f.Journaled)
		},
	}
	res, err := im.ImportJournal(ctx, acc, imp, folders)
	switch {
	case errors.Is(err, archive.ErrSyncRunning):
		return fmt.Errorf("the account %q is being filled, imported into or removed; try again later", acc.Name)
	case errors.Is(err, archive.ErrAccountRemoved):
		return fmt.Errorf("the smtp account %q was removed", acc.Name)
	}
	if res != nil {
		printImportResult(cmd.OutOrStdout(), res, true)
	}
	if err != nil {
		return err
	}
	if res.Status == "partial" {
		return &exitError{msg: "some messages were skipped", code: 1}
	}
	return nil
}

// journalAccount returns the owner's smtp account with this name for an
// import with --journal. Nothing is created; other kinds of accounts and
// removed ones are refused. A disabled account is fine: disabling stops
// receiving, not storing.
func journalAccount(cmd *cobra.Command, a *app, name string) (*store.Account, error) {
	owner, err := newOwner(cmd, a)
	if err != nil {
		return nil, err
	}
	acc, err := ownedAccount(cmd.Context(), a, owner, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no smtp account %q; --journal imports only into an existing smtp account", name)
	}
	if err != nil {
		return nil, err
	}
	switch {
	case acc.Kind == store.KindImport:
		return nil, fmt.Errorf("%q is an import account; --journal imports only into an smtp account (leave out --journal to import into it)", name)
	case acc.Kind != store.KindSMTP:
		return nil, fmt.Errorf("%q is an IMAP account; --journal imports only into an smtp account", name)
	case acc.RemovedAt != nil:
		return nil, fmt.Errorf("the smtp account %q was removed", name)
	}
	return acc, nil
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
			case acc.Kind == store.KindSMTP:
				return nil, fmt.Errorf("%q is an smtp account; add --journal to import into its %s folder", name, store.SMTPFolder)
			case acc.Kind != store.KindImport:
				return nil, fmt.Errorf("%q is an IMAP account; choose another name for the import", name)
			case acc.RemovedAt != nil:
				return nil, fmt.Errorf("the import account %q was removed; choose another name", name)
			}
			return acc, nil
		}
		acc = &store.Account{Kind: store.KindImport, Name: name, OwnerID: owner}
		e := cliEntry("account.add")
		e.SetTarget(store.TargetAccount, "", name) // the ID is set when it exists
		e.Detail("kind", string(store.KindImport))
		err = a.store.CreateAccount(store.WithAudit(ctx, e), acc)
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
	switch f.Kind {
	case mailbox.KindMaildir:
		files, _, err := mailbox.MaildirFiles(f.Path)
		return len(files), err
	case mailbox.KindEML:
		files, err := mailbox.EMLFiles(f.Path)
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

func printImportResult(out io.Writer, res *archive.ImportResult, journal bool) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if journal {
		fmt.Fprintln(w, "FOLDER\tREAD\tADDED\tNEW\tPRESENT\tJOURNALED\tSKIPPED")
	} else {
		fmt.Fprintln(w, "FOLDER\tREAD\tADDED\tNEW\tPRESENT\tSKIPPED")
	}
	for _, f := range res.Folders {
		if journal {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\n", f.Folder, f.Read, f.Added, f.New, f.Present, f.Journaled, f.Skipped)
		} else {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\n", f.Folder, f.Read, f.Added, f.New, f.Present, f.Skipped)
		}
	}
	_ = w.Flush()
	t := res.Total()
	if journal {
		fmt.Fprintf(out, "\n%s: read %d, added %d (%d new to the archive), %d already there, %d already journaled, %d skipped\n",
			res.Status, t.Read, t.Added, t.New, t.Present, t.Journaled, t.Skipped)
	} else {
		fmt.Fprintf(out, "\n%s: read %d, added %d (%d new to the archive), %d already there, %d skipped\n",
			res.Status, t.Read, t.Added, t.New, t.Present, t.Skipped)
	}
	if t.Deleted > 0 {
		fmt.Fprintf(out, "%d message(s) deleted by retention were not archived again\n", t.Deleted)
	}
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
