package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/attest"
	"github.com/pklnx/mail-archive/internal/config"
)

func newAttestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attest",
		Short: "Write a signed attestation over the archived messages now",
		Long: `Write an attestation now: it covers the messages archived since the
previous one, is chained to it and signed with the key in
` + config.EnvAttestKeyFile + ` (default <data dir>/attest/signing.key). It is
stored in the database and appended to <data dir>/attest/attestations.jsonl,
with a leaves file listing what it covers in <data dir>/attest/leaves/.

The web server writes one every ` + config.EnvAttestInterval + ` (default 24h).
Create the key once with "attest keygen". If the attestation file and the
database disagree, nothing is written and the command fails.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			key, err := loadAttestKey(a.cfg)
			if err != nil {
				return err
			}
			res, err := archive.Attest(cmd.Context(), a.store, a.cfg.AttestDir(), key)
			out := cmd.OutOrStdout()
			if res.CaughtUp > 0 {
				fmt.Fprintf(out, "copied %d attestation(s) from the database to the file\n", res.CaughtUp)
			}
			if err != nil {
				return err
			}
			e := res.Entry
			fmt.Fprintf(out, "attestation #%d: %d new message(s), %d deletion(s), %d message(s) in total\n",
				e.Seq, e.MessagesCount, e.DeletionsCount, e.Total)
			fmt.Fprintf(out, "leaves %s\nhash   %s\n", e.Leaves, e.Hash)
			return nil
		},
	}
	cmd.AddCommand(newAttestKeygenCmd(), newAttestPublicKeyCmd())
	return cmd
}

// loadAttestKey loads the signing key, with a hint when there is none.
func loadAttestKey(cfg *config.Config) (ed25519.PrivateKey, error) {
	key, err := attest.LoadSigner(cfg.AttestKeyFile, cfg.AttestDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no attestation key at %s; create one with `%s attest keygen`", cfg.AttestKeyFile, commandName())
	}
	return key, err
}

func newAttestKeygenCmd() *cobra.Command {
	var rotate bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Create the key that signs attestations",
		Long: `Create the Ed25519 key that signs attestations, in ` + config.EnvAttestKeyFile + `
(default <data dir>/attest/signing.key, mode 0600), and its public key in
<data dir>/attest/signing.pub. Refuses if a key exists.

Copy the public key off the host (password manager, printout, your
auditor): an attestation is only worth as much as the copy of the key that
an attacker on the host cannot change.

--rotate replaces the key: the current public key moves to
<data dir>/attest/retired-keys.pem, where it stays trusted for older
attestations, and a new key replaces the old one. Use it when the key was
lost or may be known to others.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var pub ed25519.PublicKey
			if rotate {
				var retired ed25519.PublicKey
				pub, retired, err = attest.Rotate(cfg.AttestKeyFile, cfg.AttestDir())
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "retired key %s (still trusted for older attestations)\n", attest.EncodeKey(retired))
			} else if pub, err = attest.Keygen(cfg.AttestKeyFile, cfg.AttestDir()); err != nil {
				return err
			}
			fmt.Fprintf(out, "created %s\n", cfg.AttestKeyFile)
			fmt.Fprintf(out, "public key %s\n\n", attest.EncodeKey(pub))
			fmt.Fprintf(out, "Copy %s off this host, for example into a password manager.\n",
				filepath.Join(cfg.AttestDir(), attest.PublicKeyFile))
			return nil
		},
	}
	cmd.Flags().BoolVar(&rotate, "rotate", false, "replace an existing key; the old public key stays trusted")
	return cmd
}

func newAttestPublicKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "public-key",
		Short: "Print the public key that attestations are verified with",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			path := filepath.Join(cfg.AttestDir(), attest.PublicKeyFile)
			pub, err := attest.LoadPublicKey(path)
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("no public key at %s; create a key with `%s attest keygen`", path, commandName())
			}
			if err != nil {
				return err
			}
			pemData, err := attest.MarshalPublicKey(pub)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprint(out, string(pemData))
			fmt.Fprintf(out, "raw (base64, as in attestations): %s\n", attest.EncodeKey(pub))
			return nil
		},
	}
}

// startAttester starts the attestation schedule of serve and returns a
// function that stops it. A missing or unusable key turns it off with a log
// line; serve still starts.
func startAttester(ctx context.Context, a *app, log *slog.Logger) (stop func()) {
	key, err := attest.LoadSigner(a.cfg.AttestKeyFile, a.cfg.AttestDir())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		n, cerr := a.store.CountAttestations(ctx)
		if cerr == nil && n > 0 {
			log.Error("attestations off: the signing key is missing, but attestations exist (was the key lost or moved?)",
				"key_file", a.cfg.AttestKeyFile, "attestations", n, "command", commandName()+" attest keygen --rotate")
		} else {
			log.Warn("attestations off", "reason", "no signing key", "key_file", a.cfg.AttestKeyFile,
				"command", commandName()+" attest keygen")
		}
		return func() {}
	case err != nil:
		log.Error("attestations off: the signing key cannot be used", "err", err)
		return func() {}
	case a.cfg.AttestInterval == 0:
		log.Info("attestation schedule off", "reason", config.EnvAttestInterval+" is 0")
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	at := &archive.Attester{Store: a.store, Dir: a.cfg.AttestDir(), Key: key, Interval: a.cfg.AttestInterval, Logger: log}
	go func() {
		at.Run(ctx)
		close(done)
	}()
	log.Info("attestations on", "interval", a.cfg.AttestInterval, "public_key", attest.EncodeKey(key.Public().(ed25519.PublicKey)))
	return func() {
		cancel()
		<-done
	}
}
