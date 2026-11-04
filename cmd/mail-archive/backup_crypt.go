package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// Encrypted backups and their restore (#392, part 4).

// envKeyringCopy carries a keyring copy, base64, from ./ma into
// seal-stream and unseal-stream: the container cannot read the backup
// directory on the host. A keyring is wrapped under the archive keys, so
// it is no secret.
const envKeyringCopy = "MAIL_ARCHIVE_KEYRING_COPY"

// backupKeyring is the keyring a dump is sealed with: nil while
// encryption at rest is off; otherwise the reconciled keyring the archive
// key opens. Without the key it refuses, so that a dump is never written
// in plaintext while encryption is on (coordinator decision 7).
func backupKeyring(ctx context.Context, a *app) (*blobcrypt.Keyring, error) {
	row, err := a.store.BlobEncryption(ctx)
	if err != nil {
		return nil, err
	}
	if row == nil || row.State == store.BlobOff {
		return nil, nil
	}
	if a.cfg.ArchiveKey == nil {
		return nil, fmt.Errorf("%w: encryption at rest is %s, so the dump is encrypted with the archive key; set %s (or %s). "+
			"A dump is never written in plaintext while encryption is on",
			archive.ErrNoArchiveKey, row.State, config.EnvArchiveKeyFile, config.EnvArchiveKey)
	}
	_, ar, err := openArchiveBlobs(ctx, a, "backup", archive.RoleReader)
	if err != nil {
		return nil, err
	}
	defer ar.Close()
	if ar.Keyring == nil {
		return nil, errors.New("encryption at rest is on, but the keyring did not open")
	}
	return ar.Keyring, nil
}

// newBackupSealer writes the header of an encrypted backup to w, under
// ring's current data key, and returns the writer for the dump.
func newBackupSealer(w io.Writer, ring *blobcrypt.Keyring) (*blobcrypt.Writer, error) {
	id, dk, err := ring.Current()
	if err != nil {
		return nil, err
	}
	return blobcrypt.NewWriter(w, id, dk, blobcrypt.PurposeBackup)
}

// requireArchiveKey is the configured archive key, for the commands that
// work without a database.
func requireArchiveKey() ([]byte, error) {
	kek, _, err := config.ArchiveKeyFromEnv()
	if err != nil {
		return nil, err
	}
	if kek == nil {
		return nil, fmt.Errorf("no archive key: set %s (or %s)", config.EnvArchiveKeyFile, config.EnvArchiveKey)
	}
	return kek, nil
}

// keyringSource is a keyring to try, by where it comes from.
type keyringSource struct {
	name string
	data []byte
}

// keyringSources lists the keyrings a backup stream may be opened with:
// the copy ./ma passes in envKeyringCopy, the file given (--keyring),
// then the data directory's keyring file. Missing files are left out.
func keyringSources(path string) ([]keyringSource, error) {
	var out []keyringSource
	if v := strings.TrimSpace(os.Getenv(envKeyringCopy)); v != "" {
		data, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", envKeyringCopy, err)
		}
		out = append(out, keyringSource{name: "the keyring copy next to the dump", data: data})
	}
	if path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // the operator's keyring copy
		if err != nil {
			return nil, err
		}
		out = append(out, keyringSource{name: path, data: data})
	}
	ringPath := blobcrypt.KeyringPath(config.DataDirFromEnv())
	data, err := os.ReadFile(ringPath) //nolint:gosec // the configured data directory
	switch {
	case err == nil:
		out = append(out, keyringSource{name: ringPath, data: data})
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	return out, nil
}

// openKeyringSource parses and opens one keyring with kek.
func openKeyringSource(s keyringSource, kek []byte) (*blobcrypt.Keyring, error) {
	ring, err := blobcrypt.ParseKeyring(s.data)
	if err == nil {
		err = ring.Open(kek)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.name, err)
	}
	return ring, nil
}

// keyrings looks a data key up in several opened keyrings, in order: a
// dump from before a rekey may need an older copy than the data directory
// holds.
type keyrings []*blobcrypt.Keyring

func (k keyrings) DataKey(id uint32) ([]byte, bool) {
	for _, r := range k {
		if dk, ok := r.DataKey(id); ok {
			return dk, true
		}
	}
	return nil, false
}

// openStreamKeys opens every keyring of sources that kek opens. It fails
// only when none opens; why the others did not is kept for the error of a
// missing data key.
func openStreamKeys(sources []keyringSource, kek []byte) (keyrings, []error, error) {
	var rings keyrings
	var failed []error
	for _, s := range sources {
		ring, err := openKeyringSource(s, kek)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		rings = append(rings, ring)
	}
	if len(rings) == 0 {
		if len(failed) == 0 {
			return nil, nil, fmt.Errorf("%w: no keyring found; pass the keyring copy next to the dump with --keyring", blobcrypt.ErrNoKeyring)
		}
		return nil, nil, fmt.Errorf("the archive key opens none of the keyrings: %w", errors.Join(failed...))
	}
	return rings, failed, nil
}

// openBackupStream checks the header of an encrypted backup read from in
// and returns its plaintext reader.
func openBackupStream(in io.Reader, keyringPath string) (*blobcrypt.StreamReader, error) {
	kek, err := requireArchiveKey()
	if err != nil {
		return nil, err
	}
	sources, err := keyringSources(keyringPath)
	if err != nil {
		return nil, err
	}
	keys, failed, err := openStreamKeys(sources, kek)
	if err != nil {
		return nil, err
	}
	r, err := blobcrypt.NewStreamReader(in, keys, blobcrypt.PurposeBackup, nil)
	if errors.Is(err, blobcrypt.ErrUnknownKey) {
		if len(failed) > 0 {
			err = fmt.Errorf("%w; these keyrings did not open: %w", err, errors.Join(failed...))
		}
		return nil, fmt.Errorf("%w: pass the keyring copy that was written next to the dump (--keyring)", err)
	}
	return r, err
}

func newSealStreamCmd() *cobra.Command {
	var keyringPath string
	cmd := &cobra.Command{
		Use:   "seal-stream",
		Short: "Encrypt standard input as a backup (offline, no database)",
		Long: `Encrypt standard input to standard output in the format of encrypted
backups, under the current data key of the keyring (` + "--keyring" + `, by default
the data directory's), opened with the configured archive key (` + config.EnvArchiveKeyFile + `
or ` + config.EnvArchiveKey + `). Needs no database. ./ma backup pipes pg_dump
through it; for example:

  pg_dump --format=custom | mail-archive seal-stream > mailarchive.dump.enc

The output is complete only when the command succeeds: a stream cut short
fails to decrypt. If the producer fails, discard the output, since seal-stream
cannot tell its end from a crash. Empty input is refused.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sealStream(cmd.InOrStdin(), cmd.OutOrStdout(), keyringPath)
		},
	}
	cmd.Flags().StringVar(&keyringPath, "keyring", "", "the keyring file (default <data dir>/keys/keyring)")
	return cmd
}

// sealStream encrypts in to out under the current data key of the first
// keyring source: the copy from ./ma, --keyring, or the data directory's.
func sealStream(in io.Reader, out io.Writer, keyringPath string) error {
	kek, err := requireArchiveKey()
	if err != nil {
		return err
	}
	sources, err := keyringSources(keyringPath)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return fmt.Errorf("%w at %s (create it with `%s encryption keygen`)", blobcrypt.ErrNoKeyring,
			blobcrypt.KeyringPath(config.DataDirFromEnv()), commandName())
	}
	ring, err := openKeyringSource(sources[0], kek)
	if err != nil {
		return err
	}
	w, err := newBackupSealer(out, ring)
	if err != nil {
		return err
	}
	n, err := io.Copy(w, in)
	if err != nil {
		return fmt.Errorf("read standard input: %w", err)
	}
	if n == 0 {
		return errors.New("nothing on standard input; refusing to write an empty encrypted backup")
	}
	return w.Finish(nil)
}

func newUnsealStreamCmd() *cobra.Command {
	var keyringPath string
	cmd := &cobra.Command{
		Use:   "unseal-stream",
		Short: "Decrypt an encrypted backup from standard input (offline, no database)",
		Long: `Decrypt an encrypted backup (*.dump.enc) from standard input to standard
output, with the configured archive key (` + config.EnvArchiveKeyFile + ` or
` + config.EnvArchiveKey + `). The data key is looked up in the keyring copy
given with --keyring (the mailarchive-<time>.keyring next to the dump), then
in the data directory's keyring. Needs no database. restore-dump uses it; by
hand:

  mail-archive unseal-stream --keyring mailarchive-<time>.keyring < mailarchive-<time>.dump.enc |
    pg_restore --no-owner --single-transaction --exit-on-error -d "$DATABASE_URL"

Only authenticated data is written. A damaged or cut backup makes it stop
with an error and a non-zero exit before the end of its output, so that
pg_restore with --single-transaction --exit-on-error fails and changes
nothing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := openBackupStream(cmd.InOrStdin(), keyringPath)
			if err != nil {
				return err
			}
			_, err = io.Copy(cmd.OutOrStdout(), r)
			return err
		},
	}
	cmd.Flags().StringVar(&keyringPath, "keyring", "", "the keyring copy next to the dump")
	return cmd
}

func newRestoreDumpCmd() *cobra.Command {
	var keyringPath string
	var reconcileOnly, hold bool
	cmd := &cobra.Command{
		Use:   "restore-dump FILE",
		Short: "Load a backup's database dump, encrypted or not, and reconcile encryption at rest",
		Long: `Load the dump FILE written by backup into the database, which must be
new and empty, in one transaction: pg_restore --no-owner
--single-transaction --exit-on-error. An encrypted dump (*.dump.enc) is decrypted on its way into
pg_restore with the archive key and the keyring copy next to it
(mailarchive-<time>.keyring, or --keyring), else the data directory's. A
keyring copy of another archive than the data directory's is refused before
anything is restored. If the dump is damaged or cut, pg_restore is stopped
and the database stays as it was. The writers lock is held from the checks
to the end of the reconciliation.

Then the keyring and the state of encryption at rest are reconciled with
the data directory, and what changed is printed: the newer of the keyring
in the dump and the keyring file wins (a recovery key added after the
backup stays), the data keys of both are kept, and encrypted files in a
data directory next to a dump that says off make the state encrypting, to
be finished with encryption enable. --reconcile-only does only this, for
example after migrate on a dump older than the schema.

Copy the data directory back first, stop the web server and the worker (it
refuses while they run), and create an empty database: it refuses one that
holds anything, since pg_restore --clean cannot replace this schema (the
partitions of the audit log). Needs pg_restore in PATH, at least the server's
major version. The Docker image has none: with Docker Compose use
"./ma restore-dump FILE", which runs pg_restore in the database container.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if reconcileOnly || hold {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// Neither the index rebuild check nor the audit partitions: the
			// database may be empty, or about to be replaced.
			a, err := openAppForMigrate(ctx)
			if err != nil {
				return err
			}
			defer a.close()
			lock, err := a.store.TryLockWritersExclusive(ctx, "restore-dump")
			if err != nil {
				return err
			}
			defer lock.Release()
			// The blob lock on the writers lock's connection, as encryption
			// enable takes it: verify --fix-orphans, a sweep, a purge or a
			// retention run must not move or remove files while the
			// database is replaced and the files are walked.
			if ok, err := lock.TryBlobLock(ctx); err != nil {
				return err
			} else if !ok {
				return archive.ErrBlobLockHeld
			}
			out := cmd.OutOrStdout()
			if hold {
				return holdForRestore(ctx, a, cmd.InOrStdin(), out)
			}
			if !reconcileOnly {
				if err := requireEmptyDatabase(ctx, a); err != nil {
					return err
				}
				if err := restoreDump(ctx, a, out, args[0], keyringPath); err != nil {
					return err
				}
			}
			return reconcileRestored(ctx, a, out)
		},
	}
	cmd.Flags().StringVar(&keyringPath, "keyring", "", "the keyring copy of an encrypted dump (default: the .keyring next to it)")
	cmd.Flags().BoolVar(&reconcileOnly, "reconcile-only", false, "only reconcile the keyring and the state with the data directory")
	cmd.Flags().BoolVar(&hold, "hold", false, "hold the writers lock while ./ma restore-dump runs pg_restore, then reconcile (used by ./ma)")
	_ = cmd.Flags().MarkHidden("hold")
	cmd.MarkFlagsMutuallyExclusive("reconcile-only", "hold")
	return cmd
}

// requireEmptyDatabase refuses a database that holds anything: a dump is
// restored only into a new, empty database. pg_restore --clean cannot
// replace an existing one, because it cannot drop the partitions of
// audit_log (their constraints are inherited), and a restore over one that
// went on despite errors would mix the two.
func requireEmptyDatabase(ctx context.Context, a *app) error {
	n, err := a.store.UserRelations(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	cfg, err := pgconn.ParseConfig(a.cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("%s: %w", config.EnvDatabaseURL, err)
	}
	if commandName() == "./ma" {
		return fmt.Errorf("the database %q is not empty (%d tables and other relations); a dump is restored only into a new, empty database. "+
			"With the web server and the worker stopped: docker compose exec -T postgres dropdb -U mailarchive --force mailarchive && "+
			"docker compose exec -T postgres createdb -U mailarchive mailarchive", cfg.Database, n)
	}
	return fmt.Errorf("the database %q is not empty (%d tables and other relations); a dump is restored only into a new, empty database: "+
		"drop and create it (dropdb --force, createdb) or point %s at a new one", cfg.Database, n, config.EnvDatabaseURL)
}

// Lines of the --hold protocol between ./ma and restore-dump.
const (
	holdReady    = "ready"
	holdRestored = "restored"
)

// holdForRestore is restore-dump --hold, for ./ma restore-dump, which runs
// pg_restore in the database container: with the writers lock held by the
// caller, it runs the checks of a restore (an empty database, and a keyring
// copy of this archive), prints "ready", and keeps the lock while ./ma
// restores. When ./ma then writes "restored", it reconciles under the same
// lock and returns; at the end of its input without that line (the restore
// failed, or ./ma was interrupted) it returns an error without
// reconciling. So no writer can start from the checks to the end of the
// reconciliation.
func holdForRestore(ctx context.Context, a *app, in io.Reader, out io.Writer) error {
	if err := requireEmptyDatabase(ctx, a); err != nil {
		return err
	}
	if err := refuseForeignKeyring(a.cfg.DataDir, ""); err != nil {
		return err
	}
	fmt.Fprintln(out, holdReady)
	line, err := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(line) != holdRestored {
		if err == nil || errors.Is(err, io.EOF) {
			err = errors.New("the restore did not finish; nothing was reconciled")
		}
		return err
	}
	return reconcileRestored(ctx, a, out)
}

// refuseForeignKeyring refuses, before anything is restored, a keyring
// copy of another archive than the data directory's keyring: the copy ./ma
// passes in envKeyringCopy, and the file at path. The ids are read without
// a key. A plaintext dump has no copy next to it; its keyring is only in
// the dump and is checked by the reconciliation after the restore.
func refuseForeignKeyring(dataDir, path string) error {
	fileData, err := os.ReadFile(blobcrypt.KeyringPath(dataDir)) //nolint:gosec // the configured data directory
	if errors.Is(err, os.ErrNotExist) {
		return nil // rebuilt from the dump by the reconciliation
	}
	if err != nil {
		return err
	}
	file, err := blobcrypt.ParseKeyring(fileData)
	if err != nil {
		return fmt.Errorf("%s: %w", blobcrypt.KeyringPath(dataDir), err)
	}
	var copies []keyringSource
	if v := strings.TrimSpace(os.Getenv(envKeyringCopy)); v != "" {
		data, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return fmt.Errorf("%s: %w", envKeyringCopy, err)
		}
		copies = append(copies, keyringSource{name: "the keyring copy next to the dump", data: data})
	}
	if path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // the operator's keyring copy
		if err != nil {
			return err
		}
		copies = append(copies, keyringSource{name: path, data: data})
	}
	for _, c := range copies {
		ring, err := blobcrypt.ParseKeyring(c.data)
		if err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
		if ring.ArchiveID() != file.ArchiveID() {
			return fmt.Errorf("%w: %s has archive id %s, the data directory's keyring %s: the dump and the data directory are of different archives; "+
				"nothing was restored", blobcrypt.ErrOtherArchive, c.name, ring.ArchiveID(), file.ArchiveID())
		}
	}
	return nil
}

// isSealed reports whether the file starts like an encrypted file.
func isSealed(f *os.File) (bool, error) {
	var magic [len(blobcrypt.Magic)]byte
	n, err := f.ReadAt(magic[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return string(magic[:n]) == blobcrypt.Magic, nil
}

// readErrs remembers the error of its reader, so that a failed copy into
// pg_restore can tell a damaged dump from a pg_restore that stopped.
type readErrs struct {
	r   io.Reader
	err error
}

func (r *readErrs) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = err
	}
	return n, err
}

// restoreDump pipes the dump, decrypted when it is encrypted, into
// pg_restore. On a damaged dump pg_restore is killed before it sees the
// end of its input, so its single transaction rolls back.
func restoreDump(ctx context.Context, a *app, out io.Writer, file, keyringPath string) error {
	pgRestore, err := exec.LookPath("pg_restore")
	if err != nil {
		return errors.New("pg_restore not found; install the PostgreSQL client tools, or with Docker Compose use ./ma restore-dump FILE")
	}
	major, err := a.store.ServerMajorVersion(ctx)
	if err != nil {
		return err
	}
	f, err := os.Open(file) //nolint:gosec // the operator's dump
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only
	sealed, err := isSealed(f)
	if err != nil {
		return err
	}
	var in io.Reader = f
	if sealed {
		if keyringPath == "" {
			if sibling := filepath.Join(filepath.Dir(file), keyringCopyName(filepath.Base(file))); fileExists(sibling) {
				keyringPath = sibling
			}
		}
		if err := refuseForeignKeyring(a.cfg.DataDir, keyringPath); err != nil {
			return err
		}
		if in, err = openBackupStream(f, keyringPath); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	return runRestore(ctx, restoreJob{PgRestore: pgRestore, DatabaseURL: a.cfg.DatabaseURL, ServerMajor: major, In: in, Name: file, Sealed: sealed}, out)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

type restoreJob struct {
	PgRestore   string
	DatabaseURL string
	ServerMajor int
	In          io.Reader // the plaintext dump
	Name        string    // for messages
	Sealed      bool
}

// runRestore checks the pg_restore version and runs it on job.In.
func runRestore(ctx context.Context, job restoreJob, out io.Writer) error {
	env, err := pgEnv(job.DatabaseURL)
	if err != nil {
		return err
	}
	cfg, err := pgconn.ParseConfig(job.DatabaseURL)
	if err != nil {
		return fmt.Errorf("%s: %w", config.EnvDatabaseURL, err)
	}
	version, err := exec.CommandContext(ctx, job.PgRestore, "--version").Output() //nolint:gosec // pg_restore from PATH
	if err != nil {
		return fmt.Errorf("pg_restore --version: %w", err)
	}
	major, err := pgDumpMajor(string(version))
	if err != nil {
		return err
	}
	if major < job.ServerMajor {
		return fmt.Errorf("pg_restore is version %d, the server %d: install pg_restore %d or newer", major, job.ServerMajor, job.ServerMajor)
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, job.PgRestore, //nolint:gosec // pg_restore from PATH
		"--no-owner", "--single-transaction", "--exit-on-error", "--no-password",
		"--dbname="+cfg.Database)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, out, &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("pg_restore: %w", err)
	}
	src := &readErrs{r: job.In}
	_, copyErr := io.Copy(stdin, src)
	if src.err != nil {
		// Damaged or unreadable: pg_restore must not see a clean end of
		// its input. It is killed before its input is closed (kill(2)
		// returns once the signal is pending, so it runs no further
		// instruction), and with it its connection: the transaction rolls
		// back.
		_ = cmd.Process.Kill()
		_ = stdin.Close()
		_ = cmd.Wait()
		return fmt.Errorf("%s: %w; pg_restore was stopped, the database is unchanged", job.Name, src.err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("pg_restore: %w: %s (in one transaction: the database is unchanged)", err, strings.TrimSpace(stderr.String()))
	}
	if copyErr != nil {
		return fmt.Errorf("pg_restore: %w", copyErr)
	}
	kind := "the dump"
	if job.Sealed {
		kind = "the encrypted dump"
	}
	fmt.Fprintf(out, "restored %s %s\n", kind, job.Name)
	return nil
}

// reconcileRestored reconciles the restored database with the data
// directory (archive.ReconcileRestored) and prints what it found and did.
func reconcileRestored(ctx context.Context, a *app, out io.Writer) error {
	statuses, err := a.store.MigrationStatus(ctx)
	if err != nil {
		return err
	}
	for _, s := range statuses {
		if !s.Applied {
			fmt.Fprintf(out, "the restored database is older than this version (%s is not applied): run `%s migrate`, then `%s restore-dump --reconcile-only`\n",
				filepath.Base(s.Path), commandName(), commandName())
			return nil
		}
	}
	blobs, err := openBlobs(a.cfg.DataDir)
	if err != nil {
		return err
	}
	rep, err := archive.ReconcileRestored(ctx, a.store, blobs, archive.RestoreOptions{
		DataDir: a.cfg.DataDir, KEK: a.cfg.ArchiveKey, Logger: newLogger(a.cfg.LogLevel),
	})
	if rep != nil {
		for _, line := range rep.Lines(commandName()) {
			fmt.Fprintln(out, line)
		}
	}
	if err != nil {
		return fmt.Errorf("reconcile encryption at rest after the restore: %w", err)
	}
	if !rep.Derived {
		fmt.Fprintf(out, "next: `%s migrate`, then `%s attest` if attestations are on, then `%s sync` and `%s verify`\n",
			commandName(), commandName(), commandName(), commandName())
	}
	return nil
}
