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

// keepFlagHelp explains --keep-key-file of rotate-key and rekey.
const keepFlagHelp = "a file with a recovery key whose slot stays (repeatable); every slot not kept is dropped"

const slotsDroppedNote = `A new keyring key can only be wrapped for keys at hand, so every slot whose
key is not given is dropped: keep a recovery key with --keep-key-file FILE
(a file holding the key, readable only by its owner, outside the data
directory; delete it afterwards), or let the command add a new recovery key
(the default; --no-recovery skips it).`

func newEncryptionRotateKeyCmd() *cobra.Command {
	var newKeyFile string
	var keepFiles []string
	var noRecovery bool
	cmd := &cobra.Command{
		Use:   "rotate-key",
		Short: "Replace the archive key and the keyring key (no file is re-encrypted)",
		Long: `Create a new archive key in --new-key-file (mode 0400, never replacing a
file) and a new keyring key, wrapped for the new archive key and the kept
keys; the slot of the configured archive key is dropped. The data keys stay,
so no message file is re-encrypted and the web server and the worker may
keep running. Afterwards set ` + config.EnvArchiveKeyFile + ` to the new file;
the old key no longer opens the keyring.

` + slotsDroppedNote + `

rotate-key does not help after a key leaked: the old key, with an older copy
of the keyring such as a backup, still opens the data keys. Use encryption
rekey for that. Backups made before keep needing the key of their time.
./ma runs it with the keys directory (ARCHIVE_KEY_DIR, default ./keys)
writable; the new key file must lie in /keys there.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return encryptionRotateKey(cmd.Context(), cmd.OutOrStdout(), newKeyFile, keepFiles, !noRecovery, time.Now())
		},
	}
	cmd.Flags().StringVar(&newKeyFile, "new-key-file", "", "where to write the new archive key (required)")
	cmd.Flags().StringArrayVar(&keepFiles, "keep-key-file", nil, keepFlagHelp)
	cmd.Flags().BoolVar(&noRecovery, "no-recovery", false, "do not add a new recovery key")
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

func encryptionRotateKey(ctx context.Context, out io.Writer, newKeyFile string, keepFiles []string, recovery bool, now time.Time) (err error) {
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
	var rk []byte
	if recovery {
		if rk, err = blobcrypt.GenerateKEK(); err != nil {
			return err
		}
	}
	wrote, changed := false, false
	var dropped []blobcrypt.Slot
	err = changeKeyring(ctx, "rotate-key", func(a *app, ring *blobcrypt.Keyring) error {
		keep, err := loadKeepKeys(keepFiles, a.cfg)
		if err != nil {
			return err
		}
		add := []blobcrypt.NewSlot{{KEK: nk, Label: labelArchiveKey, Created: now}}
		if rk != nil {
			add = append(add, blobcrypt.NewSlot{KEK: rk, Label: labelRecoveryKey, Created: now})
		}
		// The key file before the keyring: a crash in between leaves a key
		// that opens nothing, never a keyring no key file opens.
		if err := writeKeyFile(newKeyFile, nk, -1, -1); err != nil {
			return err
		}
		wrote = true
		if dropped, err = ring.Rewrap(keep, add); err == nil {
			rotateKeyRewrapped()
		}
		return err
	}, func(ring *blobcrypt.Keyring) {
		// The change took effect: the new key file stays and the keys are
		// shown, also when the keyring file could not be written.
		changed = true
		fmt.Fprintf(out, "wrote archive key %s (fingerprint %s); new keyring key, generation %d\n",
			newKeyFile, blobcrypt.Fingerprint(nk), ring.Generation())
		printDropped(out, dropped)
		printRecovery(out, rk)
		fmt.Fprintf(out, "\nSet %s=%s in .env now. The running web server and worker keep working;\n", config.EnvArchiveKeyFile, newKeyFile)
		fmt.Fprintln(out, "after the change they start with the new key, and with the old one they would not start.")
		fmt.Fprintln(out, "Back up the new key file apart from the data directory and its backups. Keep the old key as")
		fmt.Fprintln(out, "long as backups made before now exist: they open only with the keys of their time.")
		fmt.Fprintln(out, "This does not help after a key leaked: run `"+commandName()+" encryption rekey` for that.")
	})
	if err != nil && wrote && !changed {
		_ = os.Remove(newKeyFile)
	}
	return err
}

// rotateKeyRewrapped, for tests, runs after rotate-key rewrapped the
// keyring and before it is written.
var rotateKeyRewrapped = func() {}

func printDropped(out io.Writer, dropped []blobcrypt.Slot) {
	for _, s := range dropped {
		fmt.Fprintf(out, "dropped the slot of key %s (%s); that key no longer opens the keyring\n", s.Fingerprint, s.Label)
	}
}

func printRecovery(out io.Writer, rk []byte) {
	if rk == nil {
		return
	}
	fmt.Fprintf(out, "\nNew recovery key (fingerprint %s), shown only now:\n\n  %s\n\n", blobcrypt.Fingerprint(rk), blobcrypt.EncodeKEK(rk))
	fmt.Fprintln(out, "Keep it offline, apart from this host and from the backups. It opens the keyring")
	fmt.Fprintln(out, "when the archive key file is lost.")
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
	})
	if res != nil {
		// Shown even when the run then failed: its slot is recorded.
		printRecovery(out, res.RecoveryKey)
	}
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
