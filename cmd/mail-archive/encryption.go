package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/config"
)

// Slot labels written by the commands.
const (
	labelArchiveKey  = "archive key"
	labelRecoveryKey = "recovery key"
)

const keyLossWarning = "Without the archive key and every recovery key, archived mail cannot be recovered, backups included."

func newEncryptionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "encryption",
		Short: "Manage the archive key and the keyring of encryption at rest",
		Long: `Manage the archive key (KEK) and the keyring of encryption at rest.

The keyring (<data dir>/keys/keyring) holds the data keys, wrapped under one
slot per archive key or recovery key. keygen and status need no database;
add-key, remove-key and rotate-key first reconcile the keyring file with its
copy in the database; enable, disable and rekey convert the archive offline.
See docs/guide/operations.md, "Encryption at rest", and
docs/development/architecture.md, "Encryption at rest".`,
	}
	cmd.AddCommand(newEncryptionKeygenCmd(), newEncryptionAddKeyCmd(), newEncryptionRemoveKeyCmd(), newEncryptionStatusCmd(),
		newEncryptionConvertCmd(true), newEncryptionConvertCmd(false), newEncryptionRotateKeyCmd(), newEncryptionRekeyCmd())
	return cmd
}

func newEncryptionKeygenCmd() *cobra.Command {
	var keyFile, owner string
	var noRecovery bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Create the archive key file and the keyring, with a recovery key",
		Long: `Create a new archive key and write it to --key-file (default
` + config.EnvArchiveKeyFile + `, mode 0400), create the keyring in the data
directory with a new data key, and add a recovery key, printed once.

The key file must lie outside the data directory. keygen never overwrites a
key file or a keyring. ./ma runs it in a one-off container with the keys
directory (ARCHIVE_KEY_DIR, default ./keys) mounted read-write.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if keyFile == "" {
				keyFile = strings.TrimSpace(config.Getenv(config.EnvArchiveKeyFile))
			}
			if keyFile == "" {
				return fmt.Errorf("where to write the key: pass --key-file or set %s", config.EnvArchiveKeyFile)
			}
			if strings.TrimSpace(config.Getenv(config.EnvArchiveKey)) != "" {
				return fmt.Errorf("%s is set; keygen writes a key file, so unset it first", config.EnvArchiveKey)
			}
			uid, gid, err := parseOwner(owner)
			if err != nil {
				return err
			}
			return encryptionKeygen(cmd.OutOrStdout(), config.DataDirFromEnv(), keyFile, !noRecovery, uid, gid, time.Now())
		},
	}
	cmd.Flags().StringVar(&keyFile, "key-file", "", "where to write the archive key (default "+config.EnvArchiveKeyFile+")")
	cmd.Flags().BoolVar(&noRecovery, "no-recovery", false, "do not add a recovery key (add one later with encryption add-key)")
	cmd.Flags().StringVar(&owner, "chown", "", "UID:GID to own the written files (./ma passes the container user)")
	_ = cmd.Flags().MarkHidden("chown")
	return cmd
}

// parseOwner reads UID:GID; empty means "do not chown" (-1, -1).
func parseOwner(s string) (int, int, error) {
	if s == "" {
		return -1, -1, nil
	}
	u, g, ok := strings.Cut(s, ":")
	uid, err1 := strconv.Atoi(u)
	gid, err2 := strconv.Atoi(g)
	if !ok || err1 != nil || err2 != nil || uid < 0 || gid < 0 {
		return 0, 0, fmt.Errorf("--chown: want UID:GID, got %q", s)
	}
	return uid, gid, nil
}

func encryptionKeygen(out io.Writer, dataDir, keyFile string, recovery bool, uid, gid int, now time.Time) (err error) {
	inside, err := config.KeyInDataDir(keyFile, dataDir)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("%s is inside the data directory %s; keep the archive key apart from the data it protects", keyFile, dataDir)
	}
	ringPath := blobcrypt.KeyringPath(dataDir)
	if err := refuseExistingKeyring(ringPath); err != nil {
		return err
	}
	unlock, err := blobcrypt.LockKeyring(dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	keygenLocked()
	// Again under the lock: another keygen may have written one meanwhile.
	if err := refuseExistingKeyring(ringPath); err != nil {
		return err
	}

	kek, err := blobcrypt.GenerateKEK()
	if err != nil {
		return err
	}
	ring, err := blobcrypt.NewKeyring(kek, labelArchiveKey, now)
	if err != nil {
		return err
	}
	var rk []byte
	if recovery {
		if rk, err = blobcrypt.GenerateKEK(); err != nil {
			return err
		}
		if err := ring.AddSlot(rk, labelRecoveryKey, now); err != nil {
			return err
		}
	}
	if err := writeKeyFile(keyFile, kek, uid, gid); err != nil {
		return err
	}
	wroteRing := false
	defer func() {
		// Nothing half-made stays: a rerun starts from scratch.
		if err != nil {
			_ = os.Remove(keyFile)
			if wroteRing {
				_ = os.Remove(ringPath)
			}
		}
	}()
	if err := blobcrypt.WriteKeyringFile(ringPath, ring); err != nil {
		return err
	}
	wroteRing = true
	if uid >= 0 {
		for _, p := range []string{filepath.Dir(ringPath), ringPath, filepath.Join(filepath.Dir(ringPath), "keyring.lock")} {
			if err := lchown(p, uid, gid); err != nil {
				return fmt.Errorf("chown %s: %w", p, err)
			}
		}
	}
	id, _, _ := ring.Current()
	fmt.Fprintf(out, "wrote archive key %s (fingerprint %s)\n", keyFile, blobcrypt.Fingerprint(kek))
	fmt.Fprintf(out, "wrote keyring %s (archive id %s, data key %08x)\n", ringPath, ring.ArchiveID(), id)
	if rk != nil {
		fmt.Fprintf(out, "\nRecovery key (fingerprint %s), shown only now:\n\n  %s\n\n", blobcrypt.Fingerprint(rk), blobcrypt.EncodeKEK(rk))
		fmt.Fprintln(out, "Keep it offline, apart from this host and from the backups (a password manager")
		fmt.Fprintln(out, "or paper in a safe). It opens the keyring when the archive key file is lost.")
	} else {
		fmt.Fprintf(out, "\nNo recovery key. Add one with `%s encryption add-key`.\n", commandName())
	}
	fmt.Fprintln(out, "Back up the archive key file too, apart from the data directory and its backups.")
	fmt.Fprintln(out, keyLossWarning)
	fmt.Fprintf(out, "\nSet %s=%s in .env (it is read by web, worker and ./ma).\n", config.EnvArchiveKeyFile, keyFile)
	return nil
}

// Test hooks: keygenLocked runs right after keygen took the keyring lock;
// lchown changes the owner of the keyring's files.
var (
	keygenLocked = func() {}
	lchown       = os.Lchown
)

func refuseExistingKeyring(ringPath string) error {
	if _, err := os.Lstat(ringPath); err == nil {
		return fmt.Errorf("a keyring exists at %s; keygen never replaces it (its archive key opens it; add keys with encryption add-key)", ringPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// writeKeyFile creates path (never replacing it) with mode 0400 and the
// base64 key, synchronized. With uid >= 0 the file belongs to uid:gid and
// its directory to uid:gid with mode 0700.
func writeKeyFile(path string, kek []byte, uid, gid int) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400) //nolint:gosec // the operator's key file
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s exists; keygen never replaces a key file", path)
		}
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(path)
		}
	}()
	if _, err := f.WriteString(blobcrypt.EncodeKEK(kek) + "\n"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if uid >= 0 {
		if err := f.Chown(uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", path, err)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if uid >= 0 {
		if err := os.Chown(dir, uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory needs the x bit
			return err
		}
	}
	ok = true
	if d, err := os.Open(dir); err == nil { //nolint:gosec // the key file's directory
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// changeKeyring runs a keyring change of command (add-key, remove-key,
// rotate-key) through archive.ChangeKeyring: the file is reconciled with
// the database copy first (#392, part 5). After the change took effect,
// the keyring is returned, also with an error wrapping
// archive.ErrKeyringFileNotWritten; done then reports what was changed
// (new keys must be shown) before the error is returned.
func changeKeyring(ctx context.Context, command string, change func(a *app, ring *blobcrypt.Keyring) error, done func(*blobcrypt.Keyring)) error {
	a, err := openApp(ctx)
	if err != nil {
		return err
	}
	defer a.close()
	ring, err := archive.ChangeKeyring(ctx, a.store, archive.KeyringChangeOptions{
		DataDir: a.cfg.DataDir, KEK: a.cfg.ArchiveKey, Command: command, Logger: newLogger(a.cfg.LogLevel),
	}, func(ring *blobcrypt.Keyring) error { return change(a, ring) })
	if ring != nil {
		done(ring)
	}
	if errors.Is(err, blobcrypt.ErrNoKeyring) {
		return fmt.Errorf("%w (create one with `%s encryption keygen`)", err, commandName())
	}
	return err
}

func newEncryptionAddKeyCmd() *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:   "add-key",
		Short: "Add a new recovery key to the keyring and print it once",
		Long: `Create a new key, add a slot for it to the keyring and print it once. It
opens the keyring like the archive key does: keep it offline, apart from the
host and the backups. The configured archive key must open the keyring.
No file is re-encrypted. Needs the database (the keyring file is reconciled
with its copy there first); the web server and the worker may keep running.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return encryptionAddKey(cmd.Context(), cmd.OutOrStdout(), label, time.Now())
		},
	}
	cmd.Flags().StringVar(&label, "label", labelRecoveryKey, "a name for the slot, shown by encryption status")
	return cmd
}

func encryptionAddKey(ctx context.Context, out io.Writer, label string, now time.Time) error {
	nk, err := blobcrypt.GenerateKEK()
	if err != nil {
		return err
	}
	return changeKeyring(ctx, "add-key", func(_ *app, ring *blobcrypt.Keyring) error { return ring.AddSlot(nk, label, now) },
		func(ring *blobcrypt.Keyring) {
			fmt.Fprintf(out, "added key %s (%s) to the keyring, generation %d; shown only now:\n\n  %s\n\n",
				blobcrypt.Fingerprint(nk), label, ring.Generation(), blobcrypt.EncodeKEK(nk))
			fmt.Fprintln(out, "Keep it offline, apart from this host and from the backups.")
			fmt.Fprintln(out, "Backups made before now do not know this key; they open with the keys of their time.")
		})
}

var fingerprintRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

func newEncryptionRemoveKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove-key FINGERPRINT",
		Short: "Remove a key's slot from the keyring",
		Long: `Remove the slot of the key with this fingerprint (see encryption status).
The configured archive key and the last slot cannot be removed. A removed key
still opens older copies of the keyring, such as backups, and through their
keyring key also the current one; after a key leaked, run encryption rekey
afterwards, which replaces the keyring key and the data key. Needs the
database, like add-key.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return encryptionRemoveKey(cmd.Context(), cmd.OutOrStdout(), strings.ToLower(strings.TrimSpace(args[0])))
		},
	}
}

func encryptionRemoveKey(ctx context.Context, out io.Writer, fp string) error {
	if !fingerprintRe.MatchString(fp) {
		return fmt.Errorf("want a fingerprint of 16 hex digits as shown by encryption status, got %q", fp)
	}
	return changeKeyring(ctx, "remove-key", func(a *app, ring *blobcrypt.Keyring) error {
		if fp == blobcrypt.Fingerprint(a.cfg.ArchiveKey) {
			return fmt.Errorf("refusing to remove the configured archive key's slot: the next start could not open the keyring; replace it with `%s encryption rotate-key` instead", commandName())
		}
		return ring.RemoveSlot(fp)
	}, func(ring *blobcrypt.Keyring) {
		fmt.Fprintf(out, "removed key %s from the keyring, generation %d\n", fp, ring.Generation())
		fmt.Fprintf(out, "If the key leaked, run `%s encryption rekey` now: the removed key still opens older copies of the keyring (backups), and through them the current data keys.\n", commandName())
	})
}

func newEncryptionStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the keyring and whether the configured archive key opens it",
		Long: `Show the keyring's archive id, generation, data keys and slots, and
whether the configured archive key opens it. Without a key the slots are
shown unverified. Exits non-zero when a configured key does not open the
keyring. Never prints a key.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return encryptionStatus(cmd.OutOrStdout(), config.DataDirFromEnv())
		},
	}
}

func encryptionStatus(out io.Writer, dataDir string) error {
	path := blobcrypt.KeyringPath(dataDir)
	ring, err := blobcrypt.ReadKeyringFile(path)
	if errors.Is(err, blobcrypt.ErrNoKeyring) {
		fmt.Fprintf(out, "keyring: none at %s (create it with `%s encryption keygen`)\n", path, commandName())
		return nil
	}
	if err != nil {
		return err
	}
	kek, source, kerr := config.ArchiveKeyFromEnv()
	var openErr error
	fp := ""
	if kerr == nil && kek != nil {
		fp = blobcrypt.Fingerprint(kek)
		openErr = ring.Open(kek)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "keyring:\t%s\n", path)
	fmt.Fprintf(w, "archive id:\t%s\n", ring.ArchiveID())
	fmt.Fprintf(w, "generation:\t%d\n", ring.Generation())
	fmt.Fprintf(w, "current data key:\t%08x\n", ring.CurrentKey())
	if ids := ring.DataKeyIDs(); ids != nil {
		s := make([]string, len(ids))
		for i, id := range ids {
			s[i] = fmt.Sprintf("%08x", id)
		}
		fmt.Fprintf(w, "data keys:\t%s\n", strings.Join(s, ", "))
	}
	_ = w.Flush()
	verified := ""
	if !ring.Opened() {
		verified = " (unverified: no archive key opened the keyring)"
	}
	fmt.Fprintf(out, "slots%s:\n", verified)
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  FINGERPRINT\tCREATED\tLABEL")
	for _, s := range ring.Slots() {
		mark := ""
		if s.Fingerprint == fp {
			mark = " (configured)"
		}
		fmt.Fprintf(w, "  %s\t%s\t%s%s\n", s.Fingerprint, s.Created.UTC().Format(time.RFC3339), s.Label, mark)
	}
	_ = w.Flush()
	switch {
	case kerr != nil:
		fmt.Fprintf(out, "archive key: %v\n", kerr)
		return &exitError{msg: kerr.Error()}
	case kek == nil:
		fmt.Fprintf(out, "archive key: not configured (%s)\n", config.EnvArchiveKeyFile)
	case openErr != nil:
		fmt.Fprintf(out, "archive key: %s, fingerprint %s: does not open the keyring: %v\n", source, fp, openErr)
		return &exitError{msg: openErr.Error()}
	default:
		fmt.Fprintf(out, "archive key: %s, fingerprint %s: opens the keyring\n", source, fp)
	}
	return nil
}

var encName = regexp.MustCompile(`^([0-9a-f]{64})\.enc$`)

func newDecryptCmd() *cobra.Command {
	var outPath, sha, keyringPath string
	cmd := &cobra.Command{
		Use:   "decrypt FILE",
		Short: "Decrypt an encrypted message file or backup offline (no database)",
		Long: `Decrypt FILE with the configured archive key (` + config.EnvArchiveKeyFile + ` or
` + config.EnvArchiveKey + `) and the keyring, and write the plaintext to
standard output or --out. Needs no database and no running instance.

A message file is bound to the SHA-256 of its content: it is taken from a
file name <sha256>.enc, or from --sha for a renamed file. Only authenticated
data is written; on an error the output ends early, --out is not created, and
the command exits non-zero.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if keyringPath == "" {
				keyringPath = blobcrypt.KeyringPath(config.DataDirFromEnv())
			}
			return decryptFile(cmd.OutOrStdout(), args[0], outPath, strings.ToLower(sha), keyringPath)
		},
	}
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "write the plaintext to this new file instead of standard output")
	cmd.Flags().StringVar(&sha, "sha", "", "the message's SHA-256, when the file name does not carry it")
	cmd.Flags().StringVar(&keyringPath, "keyring", "", "the keyring file (default <data dir>/keys/keyring)")
	return cmd
}

func decryptFile(stdout io.Writer, path, outPath, sha, keyringPath string) (err error) {
	kek, _, err := config.ArchiveKeyFromEnv()
	if err != nil {
		return err
	}
	if kek == nil {
		return fmt.Errorf("no archive key: set %s (or %s)", config.EnvArchiveKeyFile, config.EnvArchiveKey)
	}
	ring, err := blobcrypt.ReadKeyringFile(keyringPath)
	if err != nil {
		return err
	}
	if err := ring.Open(kek); err != nil {
		return err
	}
	f, err := os.Open(path) //nolint:gosec // the operator's file
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only
	info, err := f.Stat()
	if err != nil {
		return err
	}
	var raw [blobcrypt.HeaderSize]byte
	n, _ := f.ReadAt(raw[:], 0)
	h, err := blobcrypt.ParseHeader(raw[:n])
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var binding []byte
	if h.Purpose == blobcrypt.PurposeMessage {
		if sha == "" {
			m := encName.FindStringSubmatch(filepath.Base(path))
			if m == nil {
				return fmt.Errorf("%s: a message file is bound to its SHA-256; pass --sha when the name is not <sha256>.enc", path)
			}
			sha = m[1]
		}
		if binding, err = hex.DecodeString(sha); err != nil || len(binding) != sha256.Size {
			return fmt.Errorf("--sha: want 64 hex digits, got %q", sha)
		}
	}
	r, err := blobcrypt.Open(f, info.Size(), ring, 0, binding)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	dst := stdout
	if outPath != "" {
		of, oerr := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the operator's file
		if oerr != nil {
			return oerr
		}
		defer func() {
			if cerr := of.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				_ = os.Remove(outPath)
			}
		}()
		dst = of
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, hash), r); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if binding != nil && hex.EncodeToString(hash.Sum(nil)) != sha {
		return fmt.Errorf("%s: %w: the plaintext's SHA-256 does not match its binding", path, blobcrypt.ErrCorrupt)
	}
	return nil
}
