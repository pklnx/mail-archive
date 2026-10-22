package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/web"
)

type app struct {
	cfg   *config.Config
	store *store.Store
}

// openApp opens the database for a command. It refuses while the database
// needs a one-time index rebuild (see checkIndexRebuild).
func openApp(ctx context.Context) (*app, error) { return openAppChecked(ctx, true) }

// openAppForMigrate opens the database without the index rebuild check and
// without touching it, for `migrate` (which runs the rebuild before it
// creates the audit log partitions) and the read-only `migrate status`.
func openAppForMigrate(ctx context.Context) (*app, error) { return openAppChecked(ctx, false) }

func openAppChecked(ctx context.Context, normal bool) (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if normal {
		// Before anything else touches the database.
		if err := checkIndexRebuild(ctx, st); err != nil {
			st.Close()
			return nil, err
		}
		ensureAuditPartitions(ctx, st)
	}
	return &app{cfg: cfg, store: st}, nil
}

// checkIndexRebuild refuses while the database was created by the
// postgres:18-alpine image and its indexes were not rebuilt for the
// current collation: lookups and unique checks through text indexes can
// miss rows until then. `migrate` runs the rebuild.
func checkIndexRebuild(ctx context.Context, st *store.Store) error {
	c, err := st.Collation(ctx)
	if err != nil {
		return err
	}
	if !c.NeedsIndexRebuild() {
		return nil
	}
	return fmt.Errorf("the database needs a one-time index rebuild: run %s migrate first (%s; see docs/guide/operations.md)",
		commandName(), c.Reason())
}

// indexRebuildNotice is printed before migrate rebuilds the indexes.
func indexRebuildNotice(c store.CollationState) string {
	return fmt.Sprintf("rebuilding all database indexes once (%s; this is not `reindex`, see docs/guide/operations.md) ...", c.Reason())
}

func (a *app) close() { a.store.Close() }

func newMigrateCmd() *cobra.Command {
	runUp := func(cmd *cobra.Command, _ []string) error {
		a, err := openAppForMigrate(cmd.Context())
		if err != nil {
			return err
		}
		defer a.close()
		// The index rebuild first, before anything writes; Migrate checks
		// again and finds nothing left to do.
		if _, err := a.store.RebuildIndexesIfNeeded(cmd.Context(), func(c store.CollationState) {
			fmt.Println(indexRebuildNotice(c))
		}); err != nil {
			return err
		}
		ensureAuditPartitions(cmd.Context(), a.store)
		applied, err := a.store.Migrate(cmd.Context())
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Println("database schema is up to date")
		}
		for _, m := range applied {
			fmt.Println("applied", m)
		}
		if pending, err := a.store.HasUnindexed(cmd.Context()); err == nil && pending {
			fmt.Println("some messages are not fully searchable yet: run `reindex` once")
		}
		if sealer, err := a.cfg.Sealer(); err == nil {
			n, err := archive.UpgradePasswords(cmd.Context(), a.store, sealer)
			if err != nil {
				return err
			}
			if n > 0 {
				fmt.Printf("stored passwords of %d account(s) bound to the account ID\n", n)
			}
		}
		return nil
	}
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage database schema migrations (default: apply all pending)",
		Args:  cobra.NoArgs,
		RunE:  runUp,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply all pending migrations",
			Args:  cobra.NoArgs,
			RunE:  runUp,
		},
		newMigrateDownCmd(),
		newMigrateStatusCmd(),
	)
	return cmd
}

func newMigrateDownCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Roll back the most recent migration (may delete data)",
		Long: `Roll back the most recently applied migration.

Rolling back usually drops tables or columns and the data in them. Archived
.eml files on disk are not touched. Back up the database first. Intended for
development; in production, prefer restoring a backup.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return errors.New("refusing to roll back without --yes (this may delete data)")
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			rolledBack, err := a.store.MigrateDown(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Println("rolled back", rolledBack)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm that data may be deleted")
	return cmd
}

func newMigrateStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show applied and pending migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openAppForMigrate(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			statuses, err := a.store.MigrationStatus(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "MIGRATION\tSTATE\tAPPLIED AT")
			for _, st := range statuses {
				state, at := "pending", "-"
				if st.Applied {
					state, at = "applied", st.AppliedAt.Local().Format(time.DateTime)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", st.Path, state, at)
			}
			return w.Flush()
		},
	}
}

func newKeygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Generate a random secret key for " + config.EnvSecretKey + " (not the attestation key, see attest keygen)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			key, err := crypto.GenerateKey()
			if err != nil {
				return err
			}
			fmt.Println(key)
			return nil
		},
	}
}

func newAccountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Manage IMAP and smtp accounts",
	}
	cmd.AddCommand(
		newAccountAddCmd(),
		newAccountAddSMTPCmd(),
		newAccountSMTPAddressesCmd(),
		newAccountFillCmd(),
		newAccountListCmd(),
		newAccountFoldersCmd(),
		newAccountSetFoldersCmd(),
		newAccountSetScheduleCmd(),
		newAccountSetPasswordCmd(),
		newAccountRenameCmd(),
		newAccountEnableCmd(true),
		newAccountEnableCmd(false),
		newAccountRemoveCmd(),
		newAccountPurgeCmd(),
		newAccountMoveCmd(),
	)
	cmd.PersistentFlags().String("user", "", userFlagHelp)
	return cmd
}

func newAccountAddCmd() *cobra.Command {
	var (
		acc           store.Account
		tlsMode       string
		passwordStdin bool
		skipCheck     bool
	)
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add an IMAP account",
		Long: `Add an IMAP account. The password is read from the terminal (or from stdin
with --password-stdin) and stored encrypted with ` + config.EnvSecretKey + `.
The login is verified before the account is saved unless --skip-check is set.`,
		Example: `  mail-archive account add private --host imap.mail.de --username me@mail.de
  mail-archive account add gmail --host imap.gmail.com --username me@gmail.com \
      --include "[Gmail]/All Mail" --include "[Gmail]/Sent Mail"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			acc.Name = args[0]
			acc.TLSMode = store.TLSMode(tlsMode)
			acc.Enabled = true
			switch acc.TLSMode {
			case store.TLSModeTLS, store.TLSModeSTARTTLS, store.TLSModeNone:
			default:
				return fmt.Errorf("invalid --tls %q (use tls, starttls or none)", tlsMode)
			}
			if acc.Port == 0 {
				acc.Port = 993
				if acc.TLSMode != store.TLSModeTLS {
					acc.Port = 143
				}
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			sealer, err := a.cfg.Sealer()
			if err != nil {
				return err
			}
			// Fail on an unclear owner before asking for the password.
			if _, err := newOwner(cmd, a); err != nil {
				return err
			}
			password, err := readPassword(passwordStdin, "IMAP password for "+acc.Username+": ")
			if err != nil {
				return err
			}
			var folders []imapsync.Folder
			if !skipCheck {
				fmt.Fprintf(os.Stderr, "checking login at %s:%d ...\n", acc.Host, acc.Port)
				folders, err = checkLogin(cmd.Context(), &acc, password)
				if err != nil {
					return fmt.Errorf("login check failed (use --skip-check to save anyway): %w", err)
				}
				fmt.Fprintf(os.Stderr, "login ok, %d folders found\n", len(folders))
			}
			if acc.OwnerID, err = newOwner(cmd, a); err != nil {
				return err
			}
			seal := func(id int64) ([]byte, error) { return archive.SealPassword(sealer, id, password) }
			e := cliEntry("account.add")
			e.SetTarget(store.TargetAccount, "", acc.Name) // the ID is set when it exists
			e.Detail("kind", string(store.KindIMAP))
			if err := a.store.CreateAccountSealed(withCLIAudit(cmd, e), &acc, seal); err != nil {
				if errors.Is(err, store.ErrConflict) {
					return fmt.Errorf("an account named %q already exists for this user (removed accounts keep their name)", acc.Name)
				}
				return err
			}
			fmt.Printf("account %q added\n", acc.Name)
			printExclusionHint(&acc, folders)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&acc.Host, "host", "", "IMAP server host name (required)")
	f.IntVar(&acc.Port, "port", 0, "IMAP port (default 993 for tls, 143 otherwise)")
	f.StringVar(&tlsMode, "tls", "tls", "connection security: tls, starttls or none")
	f.StringVar(&acc.Username, "username", "", "IMAP user name (required)")
	f.StringArrayVar(&acc.IncludedFolders, "include", nil, "only archive these folders (repeatable; default: all)")
	f.StringArrayVar(&acc.ExcludedFolders, "exclude", nil, "skip these folders (repeatable)")
	f.BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	f.BoolVar(&skipCheck, "skip-check", false, "do not verify the login before saving")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("username")
	return cmd
}

func checkLogin(ctx context.Context, acc *store.Account, password string) ([]imapsync.Folder, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := imapsync.Dial(ctx, imapConfig(acc, password))
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	return conn.ListFolders()
}

// printExclusionHint suggests excluding junk and trash folders that the
// account's filters would still archive.
func printExclusionHint(acc *store.Account, folders []imapsync.Folder) {
	fmt.Print(exclusionHint(acc, folders))
}

func exclusionHint(acc *store.Account, folders []imapsync.Folder) string {
	suggest := archive.SuggestExclusions(folders, acc.IncludedFolders, acc.ExcludedFolders)
	if len(suggest) == 0 {
		return ""
	}
	args := []string{commandName(), "account", "set-folders", shellQuote(acc.Name)}
	for _, f := range acc.IncludedFolders {
		args = append(args, "--include", shellQuote(f))
	}
	for _, f := range append(slices.Clone(acc.ExcludedFolders), suggest...) {
		args = append(args, "--exclude", shellQuote(f))
	}
	return fmt.Sprintf("\nhint: %s marked as junk/trash by the server and will be archived.\n"+
		"      To skip them: %s\n", strings.Join(suggest, ", "), strings.Join(args, " "))
}

// commandName is how the user invokes this tool, for copy-paste hints.
// The ./ma wrapper sets MAIL_ARCHIVE_COMMAND.
func commandName() string {
	if c := os.Getenv("MAIL_ARCHIVE_COMMAND"); c != "" {
		return c
	}
	return "mail-archive"
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9._/@+-]+$`)

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func imapConfig(acc *store.Account, password string) imapsync.Config {
	return imapsync.Config{
		Host: acc.Host, Port: acc.Port, TLSMode: string(acc.TLSMode),
		Username: acc.Username, Password: password,
	}
}

func readPassword(fromStdin bool, prompt string) (string, error) {
	if fromStdin || !term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // fd fits in int
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		pw := strings.TrimRight(line, "\r\n")
		if pw == "" {
			return "", errors.New("empty password")
		}
		return pw, nil
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd())) //nolint:gosec // fd fits in int
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errors.New("empty password")
	}
	return string(b), nil
}

func newAccountListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured accounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			accounts, err := a.store.ListAccounts(cmd.Context())
			if err != nil {
				return err
			}
			u, err := flagUser(cmd, a)
			if err != nil {
				return err
			}
			names, err := userNames(cmd, a)
			if err != nil {
				return err
			}
			addresses, err := a.store.SMTPAddresses(cmd.Context())
			if err != nil {
				return err
			}
			purge, err := a.store.OpenAccountPurge(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tOWNER\tSERVER\tLOGIN\tSTATE\tINCLUDE\tEXCLUDE")
			for _, acc := range accounts {
				if u != nil && (acc.OwnerID == nil || *acc.OwnerID != u.ID) {
					continue
				}
				state := "enabled"
				switch {
				case acc.RemovedAt != nil && purge != nil && purge.AccountID == acc.ID:
					state = "removed (being deleted)"
				case acc.RemovedAt != nil:
					state = "removed"
				case acc.Kind == store.KindImport:
					state = "import"
				case !acc.Enabled:
					state = "disabled"
				}
				switch acc.Kind {
				case store.KindImport:
					fmt.Fprintf(w, "%s\t%s\timport\t-\t%s\t-\t-\n", acc.Name, ownerName(names, acc.OwnerID), state)
					continue
				case store.KindSMTP:
					// LOGIN lists the addresses it receives for.
					fmt.Fprintf(w, "%s\t%s\tsmtp\t%s\t%s\t-\t-\n", acc.Name, ownerName(names, acc.OwnerID),
						listOrDash(addresses[acc.ID], "-"), state)
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t%s:%d (%s)\t%s\t%s\t%s\t%s\n", acc.Name, ownerName(names, acc.OwnerID), acc.Host, acc.Port, acc.TLSMode,
					acc.Username, state, listOrDash(acc.IncludedFolders, "all"), listOrDash(acc.ExcludedFolders, "-"))
			}
			return w.Flush()
		},
	}
}

func listOrDash(l []string, empty string) string {
	if len(l) == 0 {
		return empty
	}
	return strings.Join(l, ", ")
}

// updateAccount saves a change to an account loaded with loadAccount, only
// if nobody changed it in between.
func updateAccount(cmd *cobra.Command, a *app, acc *store.Account, change store.AccountChange) error {
	e := cliAccountTarget(cliEntry("account.change"), acc)
	e.Detail("fields", accountChangeFields(change))
	if change.Name != nil {
		e.Detail("name", map[string]any{"from": acc.Name, "to": *change.Name})
	}
	if change.Enabled != nil {
		e.Detail("enabled", *change.Enabled)
	}
	if change.SyncInterval != nil {
		e.Detail("sync_interval", archive.FormatOwnInterval(change.SyncInterval.D))
	}
	return staleError(a.store.UpdateAccount(withCLIAudit(cmd, e), acc.Ref(), change))
}

func staleError(err error) error {
	if errors.Is(err, store.ErrStale) {
		return errors.New("the account was changed meanwhile (web UI or another command); run the command again")
	}
	return err
}

func loadAccount(cmd *cobra.Command, name string) (*app, *store.Account, error) {
	a, err := openApp(cmd.Context())
	if err != nil {
		return nil, nil, err
	}
	acc, err := findAccount(cmd, a, name)
	if err != nil {
		a.close()
		return nil, nil, err
	}
	if acc.RemovedAt != nil {
		a.close()
		return nil, nil, fmt.Errorf("account %q was removed; its archived mail is kept, but it cannot be changed or synced", name)
	}
	return a, acc, nil
}

// loadIMAPAccount is loadAccount for commands that only make sense for
// accounts synced from a server.
func loadIMAPAccount(cmd *cobra.Command, name string) (*app, *store.Account, error) {
	a, acc, err := loadAccount(cmd, name)
	if err != nil {
		return nil, nil, err
	}
	switch acc.Kind {
	case store.KindImport:
		a.close()
		return nil, nil, fmt.Errorf("%q is an import account: it has no server and is never synced; run import again to add mail", acc.Name)
	case store.KindSMTP:
		a.close()
		return nil, nil, fmt.Errorf("%q is an smtp account: it has no server and is never synced; it receives mail from a mail server", acc.Name)
	}
	return a, acc, nil
}

// loadEnableableAccount is loadAccount for enable and disable: IMAP
// accounts are synced only when enabled, smtp accounts receive only when
// enabled. Import accounts are never enabled.
func loadEnableableAccount(cmd *cobra.Command, name string) (*app, *store.Account, error) {
	a, acc, err := loadAccount(cmd, name)
	if err != nil {
		return nil, nil, err
	}
	if acc.Kind == store.KindImport {
		a.close()
		return nil, nil, fmt.Errorf("%q is an import account: it is never synced, so it cannot be enabled or disabled", acc.Name)
	}
	return a, acc, nil
}

func newAccountFoldersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "folders NAME",
		Short: "Connect to the server and show which folders would be archived",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, acc, err := loadIMAPAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			sealer, err := a.cfg.Sealer()
			if err != nil {
				return err
			}
			pw, err := archive.OpenPassword(sealer, acc)
			if err != nil {
				return err
			}
			conn, err := imapsync.Dial(cmd.Context(), imapConfig(acc, pw))
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			folders, err := conn.ListFolders()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ARCHIVE\tFOLDER\tROLE\tATTRIBUTES")
			for _, f := range folders {
				mark := "no"
				if archive.FolderSelected(f.Name, acc.IncludedFolders, acc.ExcludedFolders) {
					mark = "yes"
				}
				role := f.SpecialUse()
				if role == "" {
					role = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", mark, f.Name, role, strings.Join(f.Attrs, " "))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			printExclusionHint(acc, folders)
			return nil
		},
	}
}

func newAccountSetFoldersCmd() *cobra.Command {
	var included, excluded []string
	cmd := &cobra.Command{
		Use:   "set-folders NAME",
		Short: "Replace the include/exclude folder lists (no flags = archive all folders)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, acc, err := loadIMAPAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			change := store.AccountChange{Folders: &store.FolderFilters{Included: included, Excluded: excluded}}
			if err := updateAccount(cmd, a, acc, change); err != nil {
				return err
			}
			fmt.Printf("folder filters of %q updated\n", acc.Name)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&included, "include", nil, "only archive these folders (repeatable)")
	cmd.Flags().StringArrayVar(&excluded, "exclude", nil, "skip these folders (repeatable)")
	return cmd
}

func newAccountSetPasswordCmd() *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "set-password NAME",
		Short: "Replace the stored IMAP password",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, acc, err := loadIMAPAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			sealer, err := a.cfg.Sealer()
			if err != nil {
				return err
			}
			pw, err := readPassword(passwordStdin, "new IMAP password for "+acc.Username+": ")
			if err != nil {
				return err
			}
			enc, err := archive.SealPassword(sealer, acc.ID, pw)
			if err != nil {
				return err
			}
			if err := updateAccount(cmd, a, acc, store.AccountChange{PasswordEnc: enc}); err != nil {
				return err
			}
			fmt.Printf("password of %q updated\n", acc.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	return cmd
}

func newAccountRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename NAME NEW-NAME",
		Short: "Rename an account (its archived mail moves with it)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := archive.ValidateAccountName(args[1]); err != nil {
				return err
			}
			// Loaded first for its checks (exists, not removed, --user).
			a, _, err := loadAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			// A password still bound to the old name would no longer decrypt
			// after renaming: bind all passwords to the account ID first.
			sealer, err := a.cfg.Sealer()
			if err != nil {
				return err
			}
			if _, err := archive.UpgradePasswords(cmd.Context(), a.store, sealer); err != nil {
				return err
			}
			acc, err := findAccount(cmd, a, args[0]) // after the upgrade: new version
			if err != nil {
				return err
			}
			if err := updateAccount(cmd, a, acc, store.AccountChange{Name: &args[1]}); err != nil {
				if errors.Is(err, store.ErrConflict) {
					return fmt.Errorf("an account named %q already exists (removed accounts keep their name)", args[1])
				}
				return err
			}
			fmt.Printf("account %q renamed to %q\n", args[0], args[1])
			return nil
		},
	}
}

func newAccountEnableCmd(enable bool) *cobra.Command {
	use, short := "disable NAME", "Exclude an account from sync, or stop an smtp account receiving (archived data is kept)"
	if enable {
		use, short = "enable NAME", "Include an account in sync, or let an smtp account receive"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, acc, err := loadEnableableAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			return updateAccount(cmd, a, acc, store.AccountChange{Enabled: &enable})
		},
	}
}

func newAccountRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove an account (archived mail is kept)",
		Long: `Remove an account. If mail from it is already archived, the mail stays
searchable and keeps showing where it came from; the account's password is
deleted and it is never synced again. An account without archived mail is
deleted completely; a removed account with archived mail can be deleted
completely afterwards with "account purge NAME".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, acc, err := loadAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			// Not while a sync writes folders and messages for the account.
			unlock, ok, err := a.store.TryLockSync(cmd.Context(), acc.ID)
			if err != nil {
				return err
			}
			if !ok {
				return archive.ErrSyncRunning
			}
			defer unlock()
			res, err := a.store.DeleteOrRemoveAccount(withCLIAudit(cmd, cliAccountTarget(cliEntry("account.remove"), acc)), acc.Ref())
			if err != nil {
				return staleError(err)
			}
			if res == store.AccountDeleted {
				fmt.Printf("account %q deleted\n", acc.Name)
			} else {
				fmt.Printf("account %q removed; its archived mail is kept\n", acc.Name)
				fmt.Printf("delete it completely with: account purge %s%s\n", shellQuote(acc.Name), userArg(cmd))
			}
			return nil
		},
	}
}

func newSyncCmd() *cobra.Command {
	var (
		only      []string
		reconcile bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Copy new messages from all enabled accounts",
		Long: `Copy new messages from all enabled accounts into the archive.

The server is never modified: folders are opened read-only and messages are
fetched without setting the \Seen flag. Messages deleted on the server stay in
the archive. Run this periodically (cron, systemd timer).

After a folder's sync, it is reconciled when due: the server lists every UID
and its flags (no message bodies), and messages it no longer lists are marked
as gone, while flags are updated. A folder is due once a day
(` + config.EnvReconcileInterval + `, "0" for only with --reconcile).
--reconcile compares every selected folder now.

With ` + config.EnvNotifyWebhookURL + ` set, alerts for accounts whose syncs keep
failing, and their recovery, are sent at the end.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			sealer, err := a.cfg.Sealer()
			if err != nil {
				return err
			}
			blobs, err := openBlobs(a.cfg.DataDir)
			if err != nil {
				return err
			}
			log := newLogger(a.cfg.LogLevel)
			syncer := &archive.Syncer{
				Store: a.store, Blobs: blobs, Sealer: sealer, Logger: log,
				ReconcileInterval: a.cfg.ReconcileInterval,
			}
			var owner *int64
			if u, err := flagUser(cmd, a); err != nil {
				return err
			} else if u != nil {
				owner = &u.ID
			}
			names, err := userNames(cmd, a)
			if err != nil {
				return err
			}
			if err := refuseImportAccounts(cmd, a, only, owner); err != nil {
				return err
			}
			results, err := syncer.SyncAll(cmd.Context(), only, owner, archive.SyncOptions{Reconcile: reconcile})
			deliverAfterSync(cmd.Context(), newNotifier(a, log), log)
			failed := 0
			for _, r := range results {
				status := "ok"
				switch {
				case errors.Is(r.Err, archive.ErrSyncRunning):
					status = "skipped: already syncing (web server or another run)"
				case r.Err != nil:
					status = "error: " + r.Err.Error()
					failed++
				}
				label := r.Account
				if len(names) > 1 {
					label = ownerName(names, r.OwnerID) + "/" + r.Account
				}
				var rec string
				if rc := r.Reconcile; rc.Folders > 0 {
					rec = fmt.Sprintf("gone=%-4d back=%-4d flags=%-4d ", rc.Gone, rc.Back, rc.FlagsChanged)
				}
				if r.Skipped > 0 {
					// Deleted by retention and expired here: not archived again.
					rec += fmt.Sprintf("deleted=%d ", r.Skipped)
				}
				fmt.Printf("%-20s fetched=%-6d new=%-6d %s%s\n", label, r.Fetched, r.New, rec, status)
			}
			if err != nil {
				return err
			}
			if len(only) > 0 && len(results) == 0 {
				return fmt.Errorf("no account matches %v", only)
			}
			if failed > 0 {
				return &exitError{msg: fmt.Sprintf("%d account(s) failed", failed)}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&only, "account", nil, "only sync these accounts (repeatable; also syncs disabled ones)")
	cmd.Flags().String("user", "", "only sync this user's accounts")
	cmd.Flags().BoolVar(&reconcile, "reconcile", false, "compare every selected folder with the server now, not only due ones")
	return cmd
}

// refuseImportAccounts fails if a name given to sync --account is an import
// or smtp account (of the owner, if set).
func refuseImportAccounts(cmd *cobra.Command, a *app, only []string, owner *int64) error {
	if len(only) == 0 {
		return nil
	}
	accounts, err := a.store.ListAccounts(cmd.Context())
	if err != nil {
		return err
	}
	for _, acc := range accounts {
		if acc.Kind.HasServer() || acc.RemovedAt != nil || owner != nil && (acc.OwnerID == nil || *acc.OwnerID != *owner) {
			continue
		}
		for _, name := range only {
			if !strings.EqualFold(name, acc.Name) {
				continue
			}
			if acc.Kind == store.KindSMTP {
				return fmt.Errorf("%s is an smtp account; it receives mail from a mail server and is never synced", acc.Name)
			}
			return fmt.Errorf("%s is an import account; run import again instead", acc.Name)
		}
	}
	return nil
}

func newServeCmd() *cobra.Command {
	var listen string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the web server (UI, JSON API, sync schedule)",
		Long: `Run the web server: the UI, the JSON API and the sync schedule
(` + config.EnvSyncInterval + `, default 6h).

Everything except the login page needs a login; create users with
"user add". Requests are only accepted with a Host header listed in
` + config.EnvAllowedHosts + ` (default: localhost, 127.0.0.1, ::1), which blocks DNS
rebinding; state-changing requests must come from the same origin. Use HTTPS
(a reverse proxy or tailscale serve) when the server is reachable from a
network.

With ` + config.EnvSMTPListen + ` set, it also receives mail for smtp accounts
(journaling from a mail server) from the networks in
` + config.EnvSMTPAllowedNetworks + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			blobs, err := openBlobs(a.cfg.DataDir)
			if err != nil {
				return err
			}
			log := newLogger(a.cfg.LogLevel)
			opts := web.Options{
				AllowedHosts: a.cfg.AllowedHosts, Require2FA: a.cfg.Require2FA, SecretKey: a.cfg.SecretKey, PublicURL: a.cfg.PublicURL,
				AlertAfterFailures: a.cfg.AlertAfterFailures, TrustedProxies: a.cfg.TrustedProxies,
			}
			// After a restore, attestations only the file has are imported
			// before anything archives, so new messages are numbered past
			// them.
			archive.CatchUpAtStart(cmd.Context(), a.store, a.cfg.AttestDir(), log)
			var afterSync func()
			if n := newNotifier(a, log); n != nil {
				ctx, cancel := context.WithCancel(cmd.Context())
				notifierDone := make(chan struct{})
				go func() {
					n.Run(ctx)
					close(notifierDone)
				}()
				// Stop it also when serve fails, and before the store closes.
				defer func() {
					cancel()
					<-notifierDone
				}()
				log.Info("alerts on", "target", a.cfg.Webhook.Target(), "after_failures", a.cfg.AlertAfterFailures)
				afterSync = n.Wake
			} else {
				log.Info("alerts off", "reason", config.EnvNotifyWebhookURL+" is not set")
			}
			if sealer, err := a.cfg.Sealer(); err != nil {
				log.Warn("account management and sync are off", "reason", err)
			} else {
				opts.Sealer = sealer
				if n, err := archive.UpgradePasswords(cmd.Context(), a.store, sealer); err != nil {
					return err
				} else if n > 0 {
					log.Info("stored passwords bound to the account ID", "accounts", n)
				}
				opts.Syncer = &archive.Syncer{
					Store: a.store, Blobs: blobs, Sealer: sealer, Logger: log,
					ReconcileInterval: a.cfg.ReconcileInterval,
				}
				opts.Runner = &archive.Runner{Syncer: opts.Syncer, Schedule: scheduleOf(a.cfg), AfterSync: afterSync}
				runnerDone := make(chan struct{})
				go func() {
					opts.Runner.Run(cmd.Context())
					close(runnerDone)
				}()
				// Let a running sync record its outcome before the store closes.
				defer func() { <-runnerDone }()
				// Recurring fills wait after the start, so that journal copies
				// the mail server queued during an outage arrive first.
				opts.Fills = &archive.FillRunner{
					Filler:   &archive.Filler{Store: a.store, Blobs: blobs, Sealer: sealer, Logger: log},
					Schedule: opts.Runner.Schedule, HoldUntil: time.Now().Add(archive.RecurringStartHold), AfterRun: afterSync,
				}
				fillsDone := make(chan struct{})
				go func() {
					opts.Fills.Run(cmd.Context())
					close(fillsDone)
				}()
				// A fill records its last batch before the store closes.
				defer func() { <-fillsDone }()
				if a.cfg.SyncInterval > 0 {
					log.Info("sync schedule on", "interval", a.cfg.SyncInterval, "min", a.cfg.SyncIntervalMin, "reconcile_interval", a.cfg.ReconcileInterval)
				} else {
					log.Info("sync schedule off")
				}
			}
			if pending, err := a.store.HasUnindexed(cmd.Context()); err == nil && pending {
				log.Warn("some messages are not fully searchable yet", "command", commandName()+" reindex")
			}
			if n, err := a.store.CountUsers(cmd.Context()); err != nil {
				return err
			} else if n == 0 {
				log.Warn("no users yet; the web UI shows how to create the first admin", "command", commandName()+" user add NAME --admin")
			}
			// The first attestation of a large archive takes minutes; it runs
			// in the background. Stopped before the store closes.
			defer startAttester(cmd.Context(), a, log)()
			retention, stopRetention := startRetention(cmd.Context(), a, blobs, log)
			defer stopRetention()
			opts.Retention = retention
			if a.cfg.SMTP != nil {
				stop, err := startSMTP(cmd.Context(), a, blobs, log)
				if err != nil {
					return err
				}
				defer stop()
				opts.SMTPListen = a.cfg.SMTP.Listen
			}
			srv := web.New(a.store, blobs, log, opts)
			log.Info("listening", "addr", listen)
			if err := srv.ListenAndServe(cmd.Context(), listen); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8080", "address to listen on")
	return cmd
}

func newReindexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reindex",
		Short: "Extract search data from messages archived by an older version",
		Long: `Extract the body text, the To and Cc recipients and the attachment names of
messages archived by an older version, so search and its filters find them.
Needed once after an upgrade that says so; sync and import index new messages
themselves. Reads every message stored by an older version. Safe to
interrupt and rerun, and to run while the web server and syncs run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			blobs, err := openBlobs(a.cfg.DataDir)
			if err != nil {
				return err
			}
			n, err := archive.Reindex(cmd.Context(), a.store, blobs, newLogger(a.cfg.LogLevel))
			if errors.Is(err, archive.ErrReindexRunning) {
				return err
			}
			fmt.Printf("updated %d message(s)\n", n)
			return err
		},
	}
}

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show archive statistics and the last sync per account",
		Long: `Show per account its ID (as listed by /healthz/sync and in alerts), its
SOURCE, the number of folders and messages, in GONE how many of its messages
are no longer on the server (found by reconcile; "-" for import and smtp
accounts), the last sync, and in FAILED how many syncs in a row failed and
since when.

SOURCE tells what the account guarantees: "snapshot (IMAP)" archives what is
in the mailbox at each sync, so mail deleted between two syncs is missed;
"complete (SMTP)" receives a copy of every message from the mail server; for
it, LAST SYNC is when it last received a message. "import" holds mail
imported from files.

For IMAP accounts, EVERY is how often the web server's schedule syncs it:
"(default)" for MAIL_ARCHIVE_SYNC_INTERVAL, "(min)" when the account's own
interval is below MAIL_ARCHIVE_SYNC_INTERVAL_MIN, "manual" when it is not on
the schedule, "off" when the schedule is off. NEXT SYNC is when the
schedule syncs it next ("due" when that has passed). HEALTH is "ok",
"failing" or "stale", as in /healthz/sync; this command cannot see the web
server's queue, so an account waiting there may show "stale" here while the
web UI shows it as queued.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			var owner *int64
			if u, err := flagUser(cmd, a); err != nil {
				return err
			} else if u != nil {
				owner = &u.ID
			}
			names, err := userNames(cmd, a)
			if err != nil {
				return err
			}
			stats, unique, err := a.store.Stats(cmd.Context(), owner)
			if err != nil {
				return err
			}
			health, err := a.store.SyncHealth(cmd.Context(), owner)
			if err != nil {
				return err
			}
			received, err := a.store.LastReceived(cmd.Context())
			if err != nil {
				return err
			}
			running, err := a.store.SyncingAccounts(cmd.Context())
			if err != nil {
				return err
			}
			runs, err := a.store.LastRuns(cmd.Context())
			if err != nil {
				return err
			}
			sched, now := scheduleOf(a.cfg), time.Now()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tACCOUNT\tSOURCE\tOWNER\tENABLED\tFOLDERS\tMESSAGES\tGONE\tLAST SYNC\tEVERY\tNEXT SYNC\tHEALTH\tSTATUS\tFAILED")
			for _, s := range stats {
				last, status := "never", "-"
				if s.LastRunAt != nil {
					last = s.LastRunAt.Local().Format(time.DateTime)
				}
				if s.LastStatus != nil {
					status = *s.LastStatus
				}
				enabled, gone := fmt.Sprint(s.Enabled), fmt.Sprint(s.Gone)
				switch s.Kind {
				case store.KindImport:
					enabled, gone = "import", "-"
				case store.KindSMTP:
					gone, last, status = "-", "never", "-"
					if t, ok := received[s.AccountID]; ok {
						last, status = t.Local().Format(time.DateTime), "received"
					}
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.AccountID, s.Account, sourceLabel(s.Kind),
					ownerName(names, s.OwnerID), enabled, s.Folders, s.Messages, gone, last,
					everyColumn(sched, s), nextColumn(sched, s, now), healthColumn(a.cfg, sched, s, health[s.AccountID], running, runs, now),
					status, failedColumn(health[s.AccountID]))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if owner != nil {
				fmt.Printf("\nunique messages of this user: %d\n", unique)
			} else {
				fmt.Printf("\nunique messages in archive: %d\n", unique)
			}
			line, err := attestStatus(cmd.Context(), a, time.Now())
			if err != nil {
				return err
			}
			fmt.Println(line)
			if line, err = retentionStatus(cmd, a); err != nil {
				return err
			}
			fmt.Println(line)
			return nil
		},
	}
	cmd.Flags().String("user", "", "only this user's accounts")
	return cmd
}

// sourceLabel names where an account's mail comes from and what that
// guarantees.
func sourceLabel(k store.AccountKind) string {
	switch k {
	case store.KindSMTP:
		return "complete (SMTP)"
	case store.KindImport:
		return "import"
	default:
		return "snapshot (IMAP)"
	}
}
