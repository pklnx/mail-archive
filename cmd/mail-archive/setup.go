package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// setupDoc is where the setup of a new instance is described.
const setupDoc = `docs/guide/getting-started.md, "Start from configuration"`

func newSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set up a new instance from configuration",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "token",
		Short: "Print a new setup token for the first admin",
		Long: `Print a new setup token: 256 random bits, base64url (43 characters). It stores
nothing.

Put it into the file named by ` + config.EnvSetupTokenFile + ` (mode 0400, outside
the data directory) or into ` + config.EnvSetupToken + `, and start the web server.
While no user exists, the link ` + config.EnvPublicURL + `/#setup=<token> creates the
first admin, once, within 7 days of the first start with the token. A start
with another token starts a new window; a start without one revokes it. After
the first user exists (also one created with user add), no token works again.
See ` + setupDoc + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := config.NewSetupToken()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), token)
			return err
		},
	})
	return cmd
}

// encryptionAtSetup is step 3b of the start of serve and worker (#396,
// revision 1): on a new instance (store.FreshInstance) it turns encryption
// at rest on before anything is archived (archive.EnableAtSetup), or
// refuses the start without an archive key. MAIL_ARCHIVE_ENCRYPTION_AT_SETUP
// =off skips it, for test instances only. On an instance that is not new
// it does nothing: the start-up checks of encryption at rest come when the
// process opens its store (archive.OpenAtRest).
func encryptionAtSetup(cfg *config.Config, log *slog.Logger) func(context.Context, *store.Store) error {
	return func(ctx context.Context, st *store.Store) error {
		fresh, err := st.FreshInstance(ctx)
		if err != nil || !fresh {
			return err
		}
		if !cfg.EncryptionAtSetup {
			log.Warn("new instance without encryption at rest: "+config.EnvEncryptionAtSetup+"=off is for test instances only",
				"docs", "docs/reference/configuration.md")
			return nil
		}
		if cfg.ArchiveKey == nil {
			return &codedError{code: exitStartRefused, msg: fmt.Sprintf(
				"new instances start encrypted: set %s (or %s) to the archive key; "+
					"%s=off starts without encryption, for test instances only (%s)",
				config.EnvArchiveKeyFile, config.EnvArchiveKey, config.EnvEncryptionAtSetup, setupDoc)}
		}
		blobs, err := openBlobs(cfg.DataDir)
		if err != nil {
			return err
		}
		enabled, err := archive.EnableAtSetup(ctx, st, blobs, archive.ConvertOptions{
			DataDir: cfg.DataDir, KEK: cfg.ArchiveKey, Out: io.Discard, Logger: log,
		})
		if errors.Is(err, store.ErrWritersRunning) || errors.Is(err, archive.ErrBlobLockHeld) {
			return &codedError{code: exitStartRefused, err: err, msg: fmt.Sprintf(
				"encryption at setup cannot turn encryption at rest on: %v; let that command finish or stop it, then start again", err)}
		}
		if err != nil {
			return fmt.Errorf("encryption at setup: %w", err)
		}
		if enabled {
			log.Info("encryption at rest turned on for the new instance", "key_fingerprint", blobcrypt.Fingerprint(cfg.ArchiveKey))
		}
		return nil
	}
}

// recordSetupToken is step 7 of serve's start (#396): while setup is not
// complete, it stores the hash of the configured setup token, or revokes
// a stored one when none is configured. It logs the state, never the
// token.
func recordSetupToken(ctx context.Context, st *store.Store, cfg *config.Config, log *slog.Logger) error {
	now := time.Now()
	if cfg.SetupToken == "" {
		cleared, err := st.ClearSetupToken(ctx)
		if err != nil {
			return err
		}
		if cleared {
			log.Info("setup token removed: the setup link no longer works")
		}
		return nil
	}
	renewed, setup, err := st.RecordSetupToken(ctx, store.HashSetupToken(cfg.SetupToken), now)
	if err != nil {
		return err
	}
	switch setup.State {
	case store.SetupComplete:
		log.Info("setup is complete: the setup token is not used; remove it from the configuration",
			"setting", config.EnvSetupTokenFile)
	case store.SetupExpired:
		log.Warn("the setup token has expired: create a new one with `"+commandName()+" setup token` and start again",
			"expired_at", setup.TokenExpires.UTC().Format(time.RFC3339))
	case store.SetupToken:
		link := "<" + config.EnvPublicURL + ">/#setup=<token>"
		if cfg.PublicURL != "" {
			link = cfg.PublicURL + "/#setup=<token>"
		}
		log.Info("waiting for the first admin: open the setup link", "link", link, "new_token", renewed,
			"valid_until", setup.TokenExpires.UTC().Format(time.RFC3339))
	}
	return nil
}

// setupStatus is the setup line of `status`.
func setupStatus(is store.InstanceStatus) string {
	var line string
	switch is.SetupState {
	case "":
		return ""
	case store.SetupComplete:
		line = "setup: complete"
		if is.SetupCompletedAt != nil {
			line += " since " + is.SetupCompletedAt.Local().Format(time.DateTime)
		}
	case store.SetupToken:
		line = "setup: waiting for the first admin from the setup link, valid until " + is.SetupTokenExpires.Local().Format(time.DateTime)
	case store.SetupExpired:
		line = fmt.Sprintf("setup: the setup token expired at %s; set a new one (%s setup token)",
			is.SetupTokenExpires.Local().Format(time.DateTime), commandName())
	default:
		line = fmt.Sprintf("setup: no user yet; create the first admin with %s user add NAME --admin", commandName())
	}
	if is.EncryptionAtSetupOff && is.SetupState != store.SetupComplete {
		line += "\nsetup: " + config.EnvEncryptionAtSetup + "=off: a new instance starts without encryption at rest (test instances only)"
	}
	return line
}
