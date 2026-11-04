package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// keepFlagHelp explains --keep-key-file of rekey.
const keepFlagHelp = "a file with a recovery key whose slot stays (repeatable); every other slot but the configured archive key's is dropped"

const slotsDroppedNote = `A new keyring key can only be wrapped for keys at hand. So rekey keeps the
slot of the configured archive key and of each key given with
--keep-key-file FILE (a file holding the key, readable only by its owner,
outside the data directory; delete it afterwards), adds a new recovery key
(printed once, before the first file is converted; --no-recovery skips it),
and drops every other slot, recovery keys included. It lists the slots it
drops.`

func newEncryptionRotateKeyCmd() *cobra.Command {
	var newKeyFile string
	cmd := &cobra.Command{
		Use:   "rotate-key",
		Short: "Replace the archive key (no file is re-encrypted)",
		Long: `Create a new archive key in --new-key-file (mode 0400, never replacing a
file) and replace the configured archive key's slot with a slot for it.
Every other slot stays, recovery keys included, and so do the keyring key
and the data keys: no message file is re-encrypted, and the web server and
the worker may keep running. Afterwards set ` + config.EnvArchiveKeyFile + ` to
the new file; the old key no longer opens the keyring.

For a routine rotation, or an archive key that is lost or retired.
rotate-key alone does not help after a key leaked: the old key, with an
older copy of the keyring such as a backup, still opens the data keys. After
a leak, run encryption rekey afterwards. Backups made before keep needing
the key of their time. ./ma runs it with the keys directory (ARCHIVE_KEY_DIR,
default ./keys) writable; the new key file must lie in /keys there.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return encryptionRotateKey(cmd.Context(), cmd.OutOrStdout(), newKeyFile, time.Now())
		},
	}
	cmd.Flags().StringVar(&newKeyFile, "new-key-file", "", "where to write the new archive key (required)")
	return cmd
}

// loadKeepKeys reads the keys of --keep-key-file, with the checks of the
// archive key file (owner only, outside the data directory). None may be
// the configured archive key or repeat another.
func loadKeepKeys(paths []string, cfg *config.Config) ([][]byte, error) {
	var keys [][]byte
	seen := map[string]bool{blobcrypt.Fingerprint(cfg.ArchiveKey): true}
	for _, p := range paths {
		k, _, err := config.LoadArchiveKey(p, "", cfg.DataDir, cfg.SecretKey)
		if err != nil {
			return nil, fmt.Errorf("--keep-key-file %s: %w", p, err)
		}
		fp := blobcrypt.Fingerprint(k)
		if seen[fp] {
			return nil, fmt.Errorf("--keep-key-file %s: key %s is the configured archive key or given twice", p, fp)
		}
		seen[fp] = true
		keys = append(keys, k)
	}
	return keys, nil
}

func encryptionRotateKey(ctx context.Context, out io.Writer, newKeyFile string, now time.Time) (err error) {
	if newKeyFile == "" {
		return errors.New("where to write the new archive key: pass --new-key-file")
	}
	dataDir := config.DataDirFromEnv()
	inside, err := config.KeyInDataDir(newKeyFile, dataDir)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("%s is inside the data directory %s; keep the archive key apart from the data it protects", newKeyFile, dataDir)
	}
	nk, err := blobcrypt.GenerateKEK()
	if err != nil {
		return err
	}
	wrote, changed := false, false
	oldFP := ""
	err = changeKeyring(ctx, "rotate-key", func(a *app, ring *blobcrypt.Keyring) error {
		oldFP = blobcrypt.Fingerprint(a.cfg.ArchiveKey)
		// The key file before the keyring: a crash in between leaves a key
		// that opens nothing, never a keyring no key file opens.
		if err := writeKeyFile(newKeyFile, nk, -1, -1); err != nil {
			return err
		}
		wrote = true
		err := ring.ReplaceSlot(oldFP, nk, now)
		if err == nil {
			rotateKeyRewrapped()
		}
		return err
	}, func(ring *blobcrypt.Keyring) {
		// The change took effect: the new key file stays, also when the
		// keyring file could not be written.
		changed = true
		fmt.Fprintf(out, "wrote archive key %s (fingerprint %s); it replaces key %s in the keyring, generation %d\n",
			newKeyFile, blobcrypt.Fingerprint(nk), oldFP, ring.Generation())
		fmt.Fprintln(out, "Every other slot stays, recovery keys included.")
		fmt.Fprintf(out, "\nSet %s=%s in .env now. The running web server and worker keep working;\n", config.EnvArchiveKeyFile, newKeyFile)
		fmt.Fprintln(out, "after the change they start with the new key, and with the old one they would not start.")
		fmt.Fprintln(out, "Back up the new key file apart from the data directory and its backups. Keep the old key as")
		fmt.Fprintln(out, "long as backups made before now exist: they open only with the keys of their time.")
		fmt.Fprintln(out, "This does not help after a key leaked: run `"+commandName()+" encryption rekey` afterwards for that.")
	})
	if err != nil && wrote && !changed {
		_ = os.Remove(newKeyFile)
	}
	return err
}

// rotateKeyRewrapped, for tests, runs after rotate-key replaced the slot
// and before the keyring is written.
var rotateKeyRewrapped = func() {}

func printRecovery(out io.Writer, rk []byte) {
	if rk == nil {
		return
	}
	fmt.Fprintf(out, "\nNew recovery key (fingerprint %s), shown only now:\n\n  %s\n\n", blobcrypt.Fingerprint(rk), blobcrypt.EncodeKEK(rk))
	fmt.Fprintln(out, "Write it down now and keep it offline, apart from this host and from the backups.")
	fmt.Fprintln(out, "It opens the keyring when the archive key file is lost; the recovery keys from")
	fmt.Fprintln(out, "before whose slots were dropped no longer do.")
}

func newEncryptionRekeyCmd() *cobra.Command {
	var keepFiles []string
	var noRecovery bool
	cmd := &cobra.Command{
		Use:   "rekey",
		Short: "Re-encrypt every message file under a new data key (offline)",
		Long: `Create a new data key and a new keyring key, then re-encrypt every message
file in messages/ and orphans/ under the new data key; the old data key is
deleted once a full pass from the start finds no file naming it. Use it after
an archive key or a recovery key leaked: remove its slot (encryption
remove-key, or encryption rotate-key for the configured key) and then run
rekey. A removed key opens neither the new keyring nor the new data key.
Backups made before stay readable with the keys of their time, the leaked
one included.

` + slotsDroppedNote + `

Each file is re-encrypted into tmp/, read back and checked against its
SHA-256, and replaces the old file by an atomic rename. A file that cannot be
decrypted stays as it is and is listed, and its data key stays.` + offlineHelp("rekey"),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return encryptionRekey(cmd.Context(), cmd.OutOrStdout(), keepFiles, !noRecovery)
		},
	}
	cmd.Flags().StringArrayVar(&keepFiles, "keep-key-file", nil, keepFlagHelp)
	cmd.Flags().BoolVar(&noRecovery, "no-recovery", false, "do not add a new recovery key")
	return cmd
}

func encryptionRekey(ctx context.Context, out io.Writer, keepFiles []string, recovery bool) error {
	a, err := openApp(ctx)
	if err != nil {
		return err
	}
	defer a.close()
	keep, err := loadKeepKeys(keepFiles, a.cfg)
	if err != nil {
		return err
	}
	blobs, err := openBlobs(a.cfg.DataDir)
	if err != nil {
		return err
	}
	res, err := archive.Rekey(ctx, a.store, blobs, archive.RekeyOptions{
		ConvertOptions: archive.ConvertOptions{DataDir: a.cfg.DataDir, KEK: a.cfg.ArchiveKey, Out: out, Logger: newLogger(a.cfg.LogLevel)},
		Keep:           keep, Recovery: recovery,
		// Shown as soon as its slot is recorded, before any file is
		// converted (review round 1 of #519).
		ShowRecoveryKey: func(rk []byte) { printRecovery(out, rk) },
	})
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("interrupted; run `%s encryption rekey` again to resume", commandName())
		}
		if res != nil && store.BlobTransitional(res.State) {
			return fmt.Errorf("%w; run `%s encryption rekey` again to resume (it continues where it stopped)", err, commandName())
		}
		return err
	}
	if len(res.Corrupt) > 0 {
		fmt.Fprintf(out, "%d file(s) could not be re-encrypted; run verify:\n", len(res.Corrupt))
		for _, p := range res.Corrupt {
			fmt.Fprintf(out, "  %s\n", p)
		}
	}
	for _, id := range res.Deleted {
		fmt.Fprintf(out, "deleted the old data key %08x: no file names it\n", id)
	}
	for _, id := range res.Retained {
		fmt.Fprintf(out, "kept the old data key %08x: a file that could not be re-encrypted names it\n", id)
	}
	fmt.Fprintf(out, "rekey done: data key %08x; start the web server and the worker again (docker compose up -d)\n", res.NewKey)
	fmt.Fprintln(out, "Backups made before now open only with the keys of their time.")
	return nil
}
