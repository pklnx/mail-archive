package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// newEncryptionConvertCmd is `encryption enable` (encrypt) or `encryption
// disable` (#392, part 3).
func newEncryptionConvertCmd(encrypt bool) *cobra.Command {
	use, short, long := "disable", "Decrypt every message file and turn encryption at rest off (offline)",
		`Decrypt every message file in messages/ and orphans/ and turn encryption at
rest off. An encrypted file that cannot be decrypted (damaged, or under a data
key the keyring lacks) is moved to orphans/<time>/ and listed. The keyring
file stays.`
	if encrypt {
		use, short, long = "enable", "Encrypt every message file and turn encryption at rest on (offline)",
			`Encrypt every message file in messages/ and orphans/ and turn encryption at
rest on; every new message file is then stored encrypted. Needs the keyring
(encryption keygen) and the archive key. On an empty archive it only turns
encryption on. A plaintext file whose content does not match its name stays
as it is and is listed (verify reports it).`
	}
	long += offlineHelp(use)
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return encryptionConvert(cmd.Context(), cmd.OutOrStdout(), encrypt)
		},
	}
}

// offlineHelp is the help text shared by the offline commands enable,
// disable and rekey.
func offlineHelp(use string) string {
	direction := " Running the other command instead changes direction."
	if use == "rekey" {
		direction = ""
	}
	return `

Runs in the foreground and only while nothing else writes message files:
stop the web server and the worker first (docker compose stop web worker);
sync, import, fill, reindex, verify, retention and purge must not run either.
It refuses and names what is running otherwise. Until it has finished, those
commands refuse to start.

Interrupted (Ctrl-C, a crash, a reboot), it resumes where it stopped: run the
same command again.` + direction + ` It prints the percentage, files per
second and the time left. The state becomes final only after a full pass that
found nothing left to convert.`
}

func encryptionConvert(ctx context.Context, out io.Writer, encrypt bool) error {
	a, err := openApp(ctx)
	if err != nil {
		return err
	}
	defer a.close()
	blobs, err := openBlobs(a.cfg.DataDir)
	if err != nil {
		return err
	}
	opts := archive.ConvertOptions{DataDir: a.cfg.DataDir, KEK: a.cfg.ArchiveKey, Out: out, Logger: newLogger(a.cfg.LogLevel)}
	run, command := archive.Disable, "disable"
	if encrypt {
		run, command = archive.Enable, "enable"
	}
	res, err := run(ctx, a.store, blobs, opts)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("interrupted; run `%s encryption %s` again to resume", commandName(), command)
		}
		if res != nil && store.BlobTransitional(res.State) {
			return fmt.Errorf("%w; run `%s encryption %s` again to resume (it continues where it stopped)", err, commandName(), command)
		}
		return err
	}
	printConvertResult(out, res, encrypt)
	return nil
}

func printConvertResult(out io.Writer, res *archive.ConvertResult, encrypt bool) {
	if res.Unchanged {
		if encrypt {
			fmt.Fprintln(out, "encryption at rest is already on; nothing to do")
		} else {
			fmt.Fprintln(out, "encryption at rest is already off; nothing to do")
		}
		return
	}
	if len(res.Corrupt) > 0 {
		fmt.Fprintf(out, "%d file(s) could not be converted; run verify:\n", len(res.Corrupt))
		for _, p := range res.Corrupt {
			fmt.Fprintf(out, "  %s\n", p)
		}
	}
	if res.MovedTo != "" {
		fmt.Fprintf(out, "encrypted files that could not be decrypted were moved to %s\n", res.MovedTo)
	}
	if encrypt {
		fmt.Fprintln(out, "encryption at rest is on; start the web server and the worker again (docker compose up -d)")
	} else {
		fmt.Fprintln(out, "encryption at rest is off; start the web server and the worker again (docker compose up -d)")
	}
}

// atRestStatus is the line of `status` on encryption at rest; during an
// unfinished `encryption enable` or `disable` it shows the progress and
// the command that finishes it (#392, part 3).
func atRestStatus(ctx context.Context, a *app) (string, error) {
	row, err := a.store.BlobEncryption(ctx)
	if err != nil {
		return "", err
	}
	switch {
	case row == nil || row.State == store.BlobOff:
		return "encryption at rest: off", nil
	case row.State == store.BlobOn:
		return "encryption at rest: on", nil
	}
	command := map[string]string{store.BlobEncrypting: "enable", store.BlobDecrypting: "disable", store.BlobRekeying: "rekey"}[row.State]
	return fmt.Sprintf("encryption at rest: %s, unfinished (%d%% of the current pass, %d file(s) converted, %d corrupt, last progress %s); "+
		"run `%s encryption %s` to finish it; until then the web server, the worker and the commands that write message files do not start",
		row.State, int(archive.ConversionFraction(row.Cursor)*100), row.FilesDone, row.FilesCorrupt,
		row.UpdatedAt.Local().Format(time.DateTime), commandName(), command), nil
}
