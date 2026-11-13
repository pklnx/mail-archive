package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// Exit codes of verify.
const (
	exitFindings   = 1
	exitIncomplete = 2
)

func newVerifyCmd() *cobra.Command {
	var opts archive.VerifyOptions
	var publicKeys []string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check that every archived message file is present and intact",
		Long: `Check the whole archive. Every message in the database is read from disk
and hashed again, which reports missing and corrupt files with the users,
accounts and folders they belong to. Then every file in the data directory's
messages/ and tmp/ is checked against the database, which reports orphans
(files without a database row), files left by an interrupted sync (only
after an hour: younger ones may belong to a delivery in progress), and
anything that does not belong there.

Before that, it checks the attestations (see attest check): the chain and its
signatures, the attestation file, and every attestation against the
database, which reports attested messages that were removed, rows inserted
into attested ranges, and an attestation job that stopped. --public-key
trusts only the keys in the given PEM files, such as an off-host copy of
signing.pub. Without a key and without attestations it prints "tamper
evidence off" and checks the rest as before.

verify never changes a database row or an archived file. With --fix-orphans
it moves orphans and leftover temporary files to orphans/<time>/ in the data
directory; delete that directory yourself once you are sure.

The orphan check waits up to --lock-timeout while a sync writes files, then
is skipped. While it runs, SMTP deliveries wait to commit; a delivery whose
new file it moved is answered with 451, and the mail server delivers again. Exit codes: 0 nothing found, 1 findings, 2 the check is
incomplete (orphan check skipped, unreadable files, attestations without a
trusted key, or another error).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			code, err := runVerify(cmd.Context(), opts, publicKeys, cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "error:", err)
				return &exitError{msg: err.Error(), code: exitIncomplete}
			}
			if code != 0 {
				return &exitError{msg: "verify found problems", code: code}
			}
			return nil
		},
	}
	// Usage errors mean the check did not run either.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintln(c.ErrOrStderr(), "error:", err)
		return &exitError{msg: err.Error(), code: exitIncomplete}
	})
	cmd.Flags().IntVar(&opts.Jobs, "jobs", 4, "files hashed in parallel (1 to 16)")
	cmd.Flags().DurationVar(&opts.LockTimeout, "lock-timeout", time.Minute, "how long to wait for running syncs before the orphan check")
	cmd.Flags().BoolVar(&opts.FixOrphans, "fix-orphans", false, "move orphans and leftover temporary files to orphans/<time>/ in the data directory")
	cmd.Flags().StringArrayVar(&publicKeys, "public-key", nil, "trust only the attestation public keys in this PEM file (repeatable)")
	return cmd
}

func runVerify(ctx context.Context, opts archive.VerifyOptions, publicKeys []string, stdout, stderr io.Writer) (int, error) {
	if opts.Jobs < 1 || opts.Jobs > 16 {
		return 0, fmt.Errorf("--jobs must be between 1 and 16, got %d", opts.Jobs)
	}
	if opts.LockTimeout <= 0 {
		return 0, errors.New("--lock-timeout must be positive")
	}
	a, err := openApp(ctx)
	if err != nil {
		return 0, err
	}
	defer a.close()
	blobs, ar, err := openArchiveBlobs(ctx, a, "verify", archive.RoleVerify)
	if err != nil {
		return 0, err
	}
	defer ar.Close()
	opts.Encryption, opts.KeyMissing = ar.State, ar.KeyMissing
	if opts.Attest, err = attestCheckOptions(a.cfg, publicKeys, ""); err != nil {
		return 0, err
	}
	opts.Logger = newLogger(a.cfg.LogLevel)
	opts.OnFinding = func(f archive.Finding) { fmt.Fprintln(stdout, formatFinding(f)) }
	opts.Progress = func(n int) { fmt.Fprintf(stderr, "checked %d messages\n", n) }
	res, err := archive.Verify(ctx, a.store, blobs, opts)
	if err != nil {
		if res != nil && opts.KeyMissing != nil {
			// The attestation check ran; its result is still shown.
			for _, n := range res.Notes {
				fmt.Fprintln(stdout, "note:", n)
			}
			fmt.Fprintf(stdout, "attestations checked: %d\n", res.Attestations)
		}
		return 0, err
	}
	for _, n := range res.Notes {
		fmt.Fprintln(stdout, "note:", n)
	}
	fmt.Fprintln(stdout, verifySummary(res))
	if res.OrphansSkipped {
		fmt.Fprintf(stdout, "orphan check skipped: a sync was writing files for longer than %s; run verify again later\n", opts.LockTimeout)
		if opts.FixOrphans {
			fmt.Fprintln(stdout, "--fix-orphans moved nothing")
		}
	}
	if res.Moved > 0 {
		fmt.Fprintf(stdout, "moved %d file(s), %d bytes, to %s in the data directory\n", res.Moved, res.MovedBytes, res.MovedTo)
	}
	if res.PendingRemovals > 0 {
		fmt.Fprintf(stdout, "note: %d file(s) of deleted messages (retention or account purge) wait for their removal after the grace period\n", res.PendingRemovals)
	}
	if res.Scheduled > 0 {
		fmt.Fprintf(stdout, "scheduled %d file(s) of deleted messages for removal by the next sweep\n", res.Scheduled)
	}
	for _, d := range res.OldOrphanDirs {
		fmt.Fprintf(stdout, "note: %s holds files moved by an earlier --fix-orphans; delete it once you are sure\n", d)
	}
	switch {
	case !res.Complete():
		return exitIncomplete, nil
	case res.Problems()-res.Moved-res.Scheduled > 0:
		return exitFindings, nil
	}
	return 0, nil
}

const maxPlaces = 5

func formatFinding(f archive.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-10s %s", f.Kind, f.Path)
	if f.Detail != "" {
		fmt.Fprintf(&b, " (%s)", f.Detail)
	}
	for i, p := range f.Places {
		if i == maxPlaces {
			fmt.Fprintf(&b, "\n           and %d more place(s)", len(f.Places)-maxPlaces)
			break
		}
		owner := p.Owner
		if owner == "" {
			owner = "-"
		}
		fmt.Fprintf(&b, "\n           in %s/%s/%s UID %d", owner, p.Account, p.Folder, p.UID)
	}
	return b.String()
}

func verifySummary(res *archive.VerifyResult) string {
	what := fmt.Sprintf("%d message(s)", res.Checked)
	if res.Attestations > 0 {
		what += fmt.Sprintf(" and %d attestation(s)", res.Attestations)
	}
	return fmt.Sprintf("checked %s: %s", what, findingCounts(res.Findings))
}

// findingKinds is the order findings are counted in summaries.
var findingKinds = []string{
	archive.FindingMissing, archive.FindingCorrupt, archive.FindingBadPath, archive.FindingOrphan,
	archive.FindingStaleTemp, archive.FindingUnexpected, archive.FindingDeleted,
	archive.FindingRemoved, archive.FindingUnattested, archive.FindingAttestation, archive.FindingAttestFile,
	archive.FindingChain, archive.FindingAttestStale,
	archive.FindingIOError,
}

// findingCounts lists the number of findings per kind, or "no problems".
func findingCounts(findings map[string]int) string {
	parts := []string{}
	for _, k := range findingKinds {
		if n := findings[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	if len(parts) == 0 {
		return "no problems"
	}
	return strings.Join(parts, ", ")
}

func newExportCmd() *cobra.Command {
	var format string
	var g gobdFlags
	cmd := &cobra.Command{
		Use:   "export --format mbox|maildir|gobd --out PATH (--user USER | --account NAME [--folder NAME] | ...)",
		Short: "Write archived mail as mbox files or Maildirs, or a GoBD export",
		Long: `Write the archived mail of one user, one account or one folder to DIR,
as one mbox file (mboxrd) or one Maildir per folder:
DIR/<account>/<folder>.mbox or DIR/<account>/<folder>/{cur,new,tmp}.

Every message is written once per folder, byte for byte. DIR must not exist;
it appears when the export is complete. The files are not encrypted and are
readable only by the user running the command.

One export never mixes users: give --user, or --account (with --user when
several users have an account with that name). Removed accounts are included.

` + gobdHelp,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format == "gobd" {
				return runExportGoBD(cmd, &g)
			}
			if g.out == "" {
				return errors.New("--out is required")
			}
			for _, name := range gobdOnlyFlags {
				if cmd.Flags().Changed(name) {
					return fmt.Errorf("--%s is only for --format gobd", name)
				}
			}
			if len(g.users) > 1 || len(g.accounts) > 1 || len(g.folders) > 1 {
				return errors.New("--user, --account and --folder take one value with mbox and maildir")
			}
			account, folder := first(g.accounts), first(g.folders)
			if account == "" && len(g.users) == 0 {
				return errors.New("choose what to export with --user or --account")
			}
			if folder != "" && account == "" {
				return errors.New("--folder needs --account")
			}
			out := g.out
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			opts, label, err := exportSelection(cmd, a, account)
			if err != nil {
				return err
			}
			e := cliTargeted("data.export")
			e.Detail("format", format)
			e.Detail("out", out)
			if folder != "" {
				e.Detail("folder", folder)
			}
			if err := exportTarget(cmd, a, e, account, opts); err != nil {
				return err
			}
			blobs, ar, err := openArchiveBlobs(cmd.Context(), a, "export", archive.RoleReader)
			if err != nil {
				return err
			}
			defer ar.Close()
			log := newLogger(a.cfg.LogLevel)
			opts.Format, opts.Out, opts.Folder, opts.Logger = format, out, folder, log
			warnPlaintext(cmd.ErrOrStderr(), a.cfg.DataDir, out)
			if err := auditNow(cmd, a, e); err != nil {
				return err
			}
			res, err := archive.Export(cmd.Context(), a.store, blobs, opts)
			if err != nil {
				return err
			}
			log.Info("export finished", "user", label, "account", account, "folder", folder, "format", format,
				"folders", res.Folders, "messages", res.Messages, "bytes", res.Bytes, "skipped", len(res.Skipped))
			fmt.Printf("exported %d message(s) in %d folder(s), %d bytes, to %s\n", res.Messages, res.Folders, res.Bytes, out)
			if len(res.Skipped) > 0 {
				for _, s := range res.Skipped {
					fmt.Fprintf(os.Stderr, "skipped %s in %s/%s: %s\n", s.SHA256, s.Account, s.Folder, s.Reason)
				}
				return &exitError{msg: "some messages were skipped", code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "", "mbox, maildir or gobd (required)")
	cmd.Flags().StringVar(&g.out, "out", "", "the directory to create, for gobd also a .zip (required; with Docker Compose under /data, for gobd /export)")
	cmd.Flags().StringArrayVar(&g.users, "user", nil, "export this user's accounts, or pick the owner of --account (gobd: repeatable)")
	cmd.Flags().StringArrayVar(&g.accounts, "account", nil, "only this account (gobd: [OWNER/]NAME, repeatable)")
	cmd.Flags().StringArrayVar(&g.folders, "folder", nil, "only this folder of --account (gobd: repeatable)")
	addGoBDFlags(cmd, &g)
	_ = cmd.MarkFlagRequired("format")
	cmd.AddCommand(newExportVerifyCmd())
	return cmd
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// exportSelection resolves --user and --account to an owner and accounts.
// It returns the owner's name for the log.
func exportSelection(cmd *cobra.Command, a *app, account string) (archive.ExportOptions, string, error) {
	var opts archive.ExportOptions
	if account != "" {
		acc, err := findAccount(cmd, a, account)
		if err != nil {
			return opts, "", err
		}
		opts.Owner, opts.AccountIDs = acc.OwnerID, []int64{acc.ID}
		names, err := userNames(cmd, a)
		if err != nil {
			return opts, "", err
		}
		return opts, ownerName(names, acc.OwnerID), nil
	}
	u, err := flagUser(cmd, a)
	if err != nil {
		return opts, "", err
	}
	accounts, err := a.store.ListOwnedAccounts(cmd.Context(), u.ID)
	if err != nil {
		return opts, "", err
	}
	if len(accounts) == 0 {
		return opts, "", fmt.Errorf("user %q has no accounts", u.Name)
	}
	opts.Owner = &u.ID
	for _, acc := range accounts {
		opts.AccountIDs = append(opts.AccountIDs, acc.ID)
	}
	return opts, u.Name, nil
}

// exportTarget records in e what an export selected: the account, or the
// user whose accounts are exported.
func exportTarget(cmd *cobra.Command, a *app, e *store.AuditEntry, account string, opts archive.ExportOptions) error {
	if account != "" {
		acc, err := findAccount(cmd, a, account)
		if err != nil {
			return err
		}
		cliAccountTarget(e, acc)
		return nil
	}
	u, err := flagUser(cmd, a)
	if err != nil {
		return err
	}
	cliUserTarget(e, u)
	e.Detail("accounts", len(opts.AccountIDs))
	return nil
}

func newBackupCmd() *cobra.Command {
	var noteOnly, keyringCopy bool
	cmd := &cobra.Command{
		Use:   "backup DIR",
		Short: "Dump the database into DIR, next to a note on what else to back up",
		Long: `Dump the database with pg_dump (custom format) into
DIR/mailarchive-<time>.dump and write DIR/BACKUP-NOTE.txt, which says what
else belongs to a complete backup and how to restore it.

With encryption at rest on (or being changed), the dump is encrypted while
pg_dump writes it, into DIR/mailarchive-<time>.dump.enc, under the current
data key; the plaintext only passes through a pipe. Next to it,
DIR/mailarchive-<time>.keyring is a copy of the keyring, wrapped under the
archive keys like the one in the data directory. Without the archive key
the backup refuses: a dump is never written in plaintext while encryption
is on. The note names the fingerprints of the keys that open the keyring,
never a key.

A complete backup is the dump, then a copy of the data directory, in this
order, and ` + config.EnvSecretKey + ` (and the archive key), kept
separately. The note never contains a key. With an attestation key, an
attestation is written first, so that the dump holds the newest one.
Restore with restore-dump.

Needs pg_dump in PATH, at least the server's major version. The Docker image
has none: with Docker Compose use "./ma backup DIR", which runs pg_dump in
the database container and writes DIR on the host. pg_dump takes its TLS
settings from ` + config.EnvDatabaseURL + ` only: a PGSSLMODE or other TLS
variable it would not get makes backup refuse (see "Backups without Docker"
in the operations guide).`,
		Args: func(cmd *cobra.Command, args []string) error {
			if noteOnly {
				return cobra.MaximumNArgs(1)(cmd, args)
			}
			if keyringCopy {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// A schema that does not match is only a warning: the backup
			// before an upgrade must work (#396).
			a, err := openAppSchemaWarn(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			if keyringCopy {
				// For ./ma backup: the keyring the dump is sealed with,
				// written by ./ma next to the dump.
				ring, err := backupKeyring(cmd.Context(), a)
				if err != nil {
					return err
				}
				if ring == nil {
					return errors.New("encryption at rest is off: the dump is not encrypted and needs no keyring copy")
				}
				data, err := ring.Marshal()
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			now := time.Now().UTC()
			name := "mailarchive-" + now.Format("20060102T150405Z") + ".dump"
			// Before the audit entry: a backup that cannot be sealed
			// stops here, without a dump.
			ring, err := backupKeyring(cmd.Context(), a)
			if err != nil {
				return err
			}
			// Before the audit entry too: pg_dump's connection, its place
			// in PATH and its version (#540), so a refused backup leaves no
			// data.backup entry. ./ma backup runs pg_dump itself, in the
			// database container: --note-only checks none of it.
			var pgDump *pgTool
			if !noteOnly {
				if pgDump, err = findPgTool(cmd.Context(), a, "pg_dump", "./ma backup DIR"); err != nil {
					return err
				}
			}
			// A dump is an export of everything. ./ma backup runs pg_dump
			// itself, after this command with --note-only.
			e := cliInstance("data.backup")
			if noteOnly {
				if len(args) == 1 {
					name = args[0]
				}
				e.Detail("dump", sealedName(name, ring))
			} else {
				e.Detail("dir", args[0])
				e.Detail("dump", sealedName(name, ring))
			}
			if err := auditNow(cmd, a, e); err != nil {
				return err
			}
			if noteOnly {
				// For ./ma backup: the dump is written by the database
				// container, the note by this command. The note names the
				// dump .dump.enc when it is to be sealed; ./ma reads that.
				attestBeforeBackup(cmd.Context(), a, cmd.ErrOrStderr())
				note, err := backupNote(cmd.Context(), a, name, ring, now)
				if err != nil {
					return err
				}
				fmt.Print(note)
				return nil
			}
			attestBeforeBackup(cmd.Context(), a, cmd.OutOrStdout())
			note, err := backupNote(cmd.Context(), a, name, ring, now)
			if err != nil {
				return err
			}
			path, err := runBackup(cmd.Context(), backupJob{
				PgDump: pgDump, Dir: args[0], Name: name, Note: note, Seal: ring,
			})
			if err != nil {
				return err
			}
			if ring != nil {
				fmt.Printf("wrote %s (encrypted), %s and %s\n", path, filepath.Join(args[0], keyringCopyName(name)), filepath.Join(args[0], backupNoteName))
				fmt.Printf("now copy the data directory (%s), and keep %s and the archive key separately\n", a.cfg.DataDir, config.EnvSecretKey)
				return nil
			}
			fmt.Printf("wrote %s and %s\n", path, filepath.Join(args[0], backupNoteName))
			fmt.Printf("now copy the data directory (%s), and keep %s separately\n", a.cfg.DataDir, config.EnvSecretKey)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noteOnly, "note-only", false, "only print the backup note for the given dump name (used by ./ma backup)")
	_ = cmd.Flags().MarkHidden("note-only")
	cmd.Flags().BoolVar(&keyringCopy, "keyring-copy", false, "only print the keyring the dump is encrypted with (used by ./ma backup)")
	_ = cmd.Flags().MarkHidden("keyring-copy")
	cmd.MarkFlagsMutuallyExclusive("note-only", "keyring-copy")
	return cmd
}

const backupNoteName = "BACKUP-NOTE.txt"

type backupJob struct {
	PgDump *pgTool // checked by checkPgTool
	Dir    string
	Name   string // file name of the dump
	Note   string
	// Seal, when set, is the opened keyring the dump is encrypted with
	// (Name gets .enc); a copy of it is written next to the dump.
	Seal *blobcrypt.Keyring
}

// runBackup dumps into Dir/Name.partial with the pg_dump checkPgTool
// checked, and renames it when pg_dump succeeds, then writes the note. The
// connection is passed in the environment, so the password is not in
// pg_dump's arguments.
// With Seal, pg_dump's output is encrypted on its way to the file, which
// is finished only after pg_dump succeeded: a failed pg_dump never leaves
// a complete encrypted dump. The keyring copy is written before the dump
// gets its name, so a dump never exists without it.
func runBackup(ctx context.Context, job backupJob) (string, error) {
	if err := os.MkdirAll(job.Dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(job.Dir, sealedName(job.Name, job.Seal))
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the backup directory given by the operator
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(partial)
		}
	}()
	var stderr bytes.Buffer
	var out io.Writer = f
	var sealer *blobcrypt.Writer
	if job.Seal != nil {
		if sealer, err = newBackupSealer(f, job.Seal); err != nil {
			return "", err
		}
		out = sealer
	}
	cmd := pgCommand(ctx, job.PgDump.Path, job.PgDump.Env, "--format=custom", "--no-password")
	cmd.Stdout, cmd.Stderr = out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pg_dump: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if sealer != nil {
		if err := sealer.Finish(nil); err != nil {
			return "", err
		}
		data, err := job.Seal.Marshal()
		if err != nil {
			return "", err
		}
		if err := writeFileAtomic(filepath.Join(job.Dir, keyringCopyName(job.Name)), data); err != nil {
			return "", err
		}
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(partial, path); err != nil {
		return "", err
	}
	ok = true
	return path, writeFileAtomic(filepath.Join(job.Dir, backupNoteName), []byte(job.Note))
}

// pgToolEnvNames are the variables of this process that pg_dump and
// pg_restore get besides the libpq variables of the database URL: none
// holds a secret, and libpq, OpenSSL, Kerberos or the client install may
// need them (HOME for ~/.pgpass and ~/.postgresql/root.crt, SSL_CERT_* for
// sslrootcert=system, KRB5* for GSSAPI). Nothing else is passed on, so no
// KEEPIA_ARCHIVE_* or MAIL_ARCHIVE_* (old name) variable and no PG* variable
// of this process reaches the tools.
var pgToolEnvNames = []string{
	"PATH", "HOME", "TMPDIR", "TZ", "LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "LD_LIBRARY_PATH",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "OPENSSL_CONF", "KRB5CCNAME", "KRB5_CONFIG",
}

// pgCommand runs pg_dump or pg_restore with env, the environment
// pgToolConn.env built, and nothing inherited from this process.
func pgCommand(ctx context.Context, tool string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, tool, args...) //nolint:gosec // pg_dump or pg_restore from PATH
	cmd.Env = env
	return cmd
}

var pgDumpVersion = regexp.MustCompile(`\(PostgreSQL\) (\d+)`)

func pgDumpMajor(version string) (int, error) {
	m := pgDumpVersion.FindStringSubmatch(version)
	if m == nil {
		return 0, fmt.Errorf("cannot read the pg_dump version from %q", strings.TrimSpace(version))
	}
	return strconv.Atoi(m[1])
}

// backupNote is the text of BACKUP-NOTE.txt. It never contains a key.
// ring is the keyring the dump is sealed with, nil for a plaintext dump.
func backupNote(ctx context.Context, a *app, dumpName string, ring *blobcrypt.Keyring, now time.Time) (string, error) {
	statuses, err := a.store.MigrationStatus(ctx)
	if err != nil {
		return "", err
	}
	migration := "none"
	for _, s := range statuses {
		if s.Applied {
			migration = filepath.Base(s.Path)
		}
	}
	dataDir := a.cfg.DataDir
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	compose := commandName() == "./ma"
	if compose {
		dataDir = "the directory mounted at /data (ARCHIVE_DIR in .env, by default ./data next to docker-compose.yml)"
	}
	d := backupNoteData{
		Time: now, Version: version, Migration: migration, Dump: sealedName(dumpName, ring), DataDir: dataDir, Compose: compose,
		Attest: attestKeyExists(a.cfg),
	}
	if ring != nil {
		d.Keyring = keyringCopyName(dumpName)
		d.Slots = ring.Slots()
		d.ArchiveID, d.DataKey = ring.ArchiveID(), ring.CurrentKey()
		if a.cfg.ArchiveKey != nil {
			d.Configured = blobcrypt.Fingerprint(a.cfg.ArchiveKey)
		}
	} else {
		// A dump without encryption on an archive with a sign of
		// encryption at rest (after encryption disable, the key and the
		// keyring stay): restore-dump refuses it without --plaintext.
		d.Plaintext = a.cfg.ArchiveKey != nil || fileExists(blobcrypt.KeyringPath(a.cfg.DataDir))
	}
	return formatBackupNote(d), nil
}

// attestBeforeBackup writes an attestation before the dump when a key
// exists, so that the dump holds the newest one. A failure is reported on
// w and does not stop the backup.
func attestBeforeBackup(ctx context.Context, a *app, w io.Writer) {
	if !attestKeyExists(a.cfg) {
		return
	}
	key, err := loadAttestKey(a.cfg)
	if err == nil {
		var res archive.AttestResult
		if res, err = archive.Attest(ctx, a.store, a.cfg.AttestDir(), key); err == nil {
			fmt.Fprintf(w, "attestation #%d written before the dump\n", res.Entry.Seq)
			return
		}
	}
	fmt.Fprintf(w, "warning: no attestation before the dump: %v\n", err)
}

type backupNoteData struct {
	Time      time.Time
	Version   string
	Migration string
	Dump      string
	DataDir   string
	Compose   bool
	Attest    bool // attestations are on
	// Plaintext: the dump is not encrypted, but the archive shows a sign
	// of encryption at rest, so restore-dump needs --plaintext.
	Plaintext bool

	// Set for an encrypted dump.
	Keyring    string // file name of the keyring copy
	ArchiveID  string
	DataKey    uint32 // the data key the dump is encrypted with
	Slots      []blobcrypt.Slot
	Configured string // fingerprint of the configured archive key
}

func formatBackupNote(d backupNoteData) string {
	var b strings.Builder
	fmt.Fprintf(&b, `mail-archive backup

Database dump: %s
`, d.Dump)
	if d.Keyring != "" {
		fmt.Fprintf(&b, `Keyring copy:  %s
`, d.Keyring)
	}
	fmt.Fprintf(&b, `Written:       %s
App version:   %s
Last migration: %s

`, d.Time.UTC().Format(time.RFC3339), d.Version, d.Migration)
	keyPart := ""
	if d.Keyring != "" {
		fmt.Fprintf(&b, `The dump is encrypted (encryption at rest is on): archive %s,
data key %08x. It opens with the keyring copy next to it and one of the
keys of these slots, the archive key or a recovery key:
`, d.ArchiveID, d.DataKey)
		for _, s := range d.Slots {
			mark := ""
			if s.Fingerprint == d.Configured {
				mark = " (the configured archive key)"
			}
			fmt.Fprintf(&b, "  %s  %s%s\n", s.Fingerprint, s.Label, mark)
		}
		b.WriteString(`No key is in this note, in the dump or in the keyring copy. Without one of
them, neither the dump nor the encrypted mail in the data directory can be
read by anybody.

`)
		keyPart = `
4. the archive key file (` + config.EnvArchiveKeyFile + `), or a recovery
   key, kept apart from this backup and from the data directory.`
	}
	parts := "three"
	dumpPart := "this database dump"
	if d.Keyring != "" {
		parts, dumpPart = "four", "this database dump and the keyring copy next to it"
	}
	fmt.Fprintf(&b, `A complete backup has %s parts:
1. %s,
2. a copy of the data directory, made after the dump:
   %s
3. %s, kept separately (password manager). It is not
   in this note or in the dump. Without it the stored IMAP passwords and
   two-factor secrets cannot be read.%s

Restore:
`, parts, dumpPart, d.DataDir, config.EnvSecretKey, keyPart)
	keys := "the key into .env"
	if d.Keyring != "" {
		keys = "the key into .env and the archive key file into the keys directory"
	}
	plaintextFlag := ""
	if d.Plaintext {
		plaintextFlag = "--plaintext "
	}
	if d.Compose {
		fmt.Fprintf(&b, `1. Put %s, copy the data directory back, stop the web server and
   the worker (docker compose stop web worker), and create an empty database:
   docker compose exec -T postgres dropdb -U mailarchive --force mailarchive
   docker compose exec -T postgres createdb -U mailarchive mailarchive
2. Load the dump into it, in one transaction:
   ./ma restore-dump %s<this directory>/%s
`, keys, plaintextFlag, d.Dump)
	} else {
		keys = config.EnvSecretKey
		if d.Keyring != "" {
			keys += " and " + config.EnvArchiveKeyFile
		}
		fmt.Fprintf(&b, `1. Set %s, copy the data directory back, create an empty database,
   and stop the web server and the worker.
2. Load the dump into it, in one transaction (needs pg_restore in PATH):
   mail-archive restore-dump %s%s
`, keys, plaintextFlag, d.Dump)
		if d.Keyring != "" {
			fmt.Fprintf(&b, `   or by hand:
   mail-archive unseal-stream --keyring %s < %s |
     pg_restore --no-owner --single-transaction --exit-on-error -d "$DATABASE_URL"
`, d.Keyring, d.Dump)
		} else {
			fmt.Fprintf(&b, `   or by hand:
   pg_restore --no-owner --single-transaction --exit-on-error -d "$DATABASE_URL" %s
`, d.Dump)
		}
	}
	if d.Plaintext {
		b.WriteString(`   --plaintext: the dump is not encrypted, but this archive has an archive
   key or a keyring (encryption at rest was turned off), so restore-dump
   refuses it without the flag. A dump without encryption is not
   authenticated: restore only one you know comes from this backup.
`)
	}
	b.WriteString(`   restore-dump then reconciles the keyring and the state of encryption at
   rest with the data directory and prints what it changed.
`)
	if d.Attest {
		b.WriteString(`3. Run "migrate", then "attest" before anything archives: it imports the
   attestations written after the dump from the data directory. Then
   "sync", then "verify". verify lists every attested message the restore
   lost as "removed"; files newer than the dump show up as orphans; sync
   adopts those still on the server, "import --format eml" the others. Move
   the rest aside with "verify --fix-orphans" only after checking them.

Attestations (tamper evidence): the data directory holds attest/, with the
private signing key unless ` + config.EnvAttestKeyFile + ` points elsewhere.
Whoever has this backup can sign attestations. The evidence rests on the
copies kept off this host: attest/attestations.jsonl with attest/leaves/,
and signing.pub. They are not part of this backup; keep updating them.
`)
		return b.String()
	}
	b.WriteString(`3. Run "migrate", then "sync", then "verify". Files newer than the dump
   show up as orphans; sync adopts those still on the server. Move the rest
   aside with "verify --fix-orphans" only after checking them.
`)
	return b.String()
}

// sealedName is the file name of a dump: name, with .enc when it is
// encrypted with ring.
func sealedName(name string, ring *blobcrypt.Keyring) string {
	if ring == nil {
		return name
	}
	return name + ".enc"
}

// keyringCopyName is the name of the keyring copy next to the dump name
// (mailarchive-<time>.dump → mailarchive-<time>.keyring).
func keyringCopyName(dumpName string) string {
	return strings.TrimSuffix(strings.TrimSuffix(dumpName, ".enc"), ".dump") + ".keyring"
}

// writeFileAtomic writes a file with mode 0600 through a temporary file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
