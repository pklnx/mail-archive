package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

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
	cmd.AddCommand(newAttestKeygenCmd(), newAttestPublicKeyCmd(), newAttestCheckCmd())
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

// loadPublicKeys reads the keys of --public-key: PEM files with one or more
// public keys each. None given returns nil (signing.pub and the retired
// keys are trusted then).
func loadPublicKeys(files []string) (attest.Keys, error) {
	if len(files) == 0 {
		return nil, nil
	}
	keys := attest.Keys{}
	for _, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // given by the operator
		if err != nil {
			return nil, err
		}
		pubs, err := attest.ParsePublicKeys(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for _, p := range pubs {
			keys[attest.EncodeKey(p)] = true
		}
	}
	return keys, nil
}

// attestKeyExists reports whether the signing key file exists. Reading it
// is not needed, so verify and status work without access to it.
func attestKeyExists(cfg *config.Config) bool {
	_, err := os.Stat(cfg.AttestKeyFile)
	return err == nil
}

// attestCheckOptions are the options of the attestation check in verify
// and attest check.
func attestCheckOptions(cfg *config.Config, publicKeys []string, file string) (*archive.AttestCheckOptions, error) {
	keys, err := loadPublicKeys(publicKeys)
	if err != nil {
		return nil, err
	}
	return &archive.AttestCheckOptions{
		Dir: cfg.AttestDir(), PublicKeys: keys, File: file,
		KeyExists: attestKeyExists(cfg), Interval: cfg.AttestInterval,
	}, nil
}

// attestStatus is the attestation line of status.
func attestStatus(ctx context.Context, a *app, now time.Time) (string, error) {
	last, err := a.store.LastAttestation(ctx)
	if err != nil {
		return "", err
	}
	key := attestKeyExists(a.cfg)
	switch {
	case last == nil && !key:
		return fmt.Sprintf("attestations: off (no key at %s; run %s attest keygen)", a.cfg.AttestKeyFile, commandName()), nil
	case last == nil:
		return "attestations: none yet (the web server writes the first one; or run " + commandName() + " attest)", nil
	}
	at := last.Time.Local().Format("2006-01-02 15:04")
	switch {
	case !key:
		return fmt.Sprintf("attestations: off since #%d at %s (the key at %s is missing; run %s attest keygen --rotate)",
			last.Seq, at, a.cfg.AttestKeyFile, commandName()), nil
	case a.cfg.AttestInterval > 0 && now.Sub(last.Time) > 2*a.cfg.AttestInterval:
		return fmt.Sprintf("attestations: overdue (last #%d at %s; one is due every %s)", last.Seq, at, a.cfg.AttestInterval), nil
	}
	return fmt.Sprintf("attestations: #%d at %s, %d messages", last.Seq, at, last.Total), nil
}

func newAttestCheckCmd() *cobra.Command {
	var publicKeys []string
	var file string
	var offline bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check the attestations only, without reading message files",
		Long: `Check the tamper-evidence chain: the attestations in the database, their
signatures and keys, the attestation file against the database, and every
attestation against today's database. This is the attestation part of
verify; it reads no message files and is fast.

--public-key trusts only the keys in the given PEM files (repeatable), for
example the copy of signing.pub kept off this host; pass the retired keys
too if the chain was rotated. Without it, <data dir>/attest/signing.pub and
retired-keys.pem are trusted.

--file compares another copy of attestations.jsonl, for example the one kept
off this host, with the database: it must be a prefix of the database chain.

--offline needs no database: it checks the chain, signatures and totals of
--file (default <data dir>/attest/attestations.jsonl), and every leaves file
in the leaves/ directory next to it against its signed digest.

Exit codes: 0 nothing found, 1 findings, 2 the check is incomplete (no
trusted key, or another error).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			var code int
			var err error
			if offline {
				code, err = runAttestCheckOffline(publicKeys, file, out)
			} else {
				code, err = runAttestCheck(cmd.Context(), publicKeys, file, out)
			}
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "error:", err)
				return &exitError{msg: err.Error(), code: exitIncomplete}
			}
			if code != 0 {
				return &exitError{msg: "attest check found problems", code: code}
			}
			return nil
		},
	}
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintln(c.ErrOrStderr(), "error:", err)
		return &exitError{msg: err.Error(), code: exitIncomplete}
	})
	cmd.Flags().StringArrayVar(&publicKeys, "public-key", nil, "trust only the public keys in this PEM file (repeatable)")
	cmd.Flags().StringVar(&file, "file", "", "an attestation file to compare with the database, such as an off-host copy")
	cmd.Flags().BoolVar(&offline, "offline", false, "check --file and its leaves alone, without the database")
	return cmd
}

func runAttestCheck(ctx context.Context, publicKeys []string, file string, out io.Writer) (int, error) {
	a, err := openApp(ctx)
	if err != nil {
		return 0, err
	}
	defer a.close()
	opts, err := attestCheckOptions(a.cfg, publicKeys, file)
	if err != nil {
		return 0, err
	}
	opts.Copy = file != ""
	opts.OnFinding = func(f archive.Finding) { fmt.Fprintln(out, formatFinding(f)) }
	res, err := archive.CheckAttestations(ctx, a.store, *opts)
	if err != nil {
		return 0, err
	}
	for _, n := range res.Notes {
		fmt.Fprintln(out, "note:", n)
	}
	fmt.Fprintf(out, "checked %d attestation(s): %s\n", res.Attestations, findingCounts(res.Findings))
	switch {
	case res.Unchecked:
		return exitIncomplete, nil
	case res.Problems() > 0:
		return exitFindings, nil
	}
	return 0, nil
}

func runAttestCheckOffline(publicKeys []string, file string, out io.Writer) (int, error) {
	if file == "" {
		cfg, err := config.Load()
		if err != nil {
			return 0, err
		}
		file = filepath.Join(cfg.AttestDir(), attest.ChainFile)
	}
	dir := filepath.Dir(file)
	trusted, err := loadPublicKeys(publicKeys)
	if err != nil {
		return 0, err
	}
	if trusted == nil {
		if trusted, err = attest.LoadTrusted(dir); err != nil {
			return 0, err
		}
	}
	if len(trusted) == 0 {
		return 0, fmt.Errorf("no trusted public key: %s is missing; pass --public-key", filepath.Join(dir, attest.PublicKeyFile))
	}
	res, err := archive.CheckAttestFile(file, dir, trusted)
	if err != nil {
		return 0, err
	}
	for _, p := range res.Problems {
		fmt.Fprintf(out, "%-10s %s (%s)\n", archive.FindingChain, file, p)
	}
	for _, seq := range res.KeyChanges {
		fmt.Fprintf(out, "note: attestation #%d is signed with a new key (key rotation)\n", seq)
	}
	if len(res.Problems) > 0 {
		fmt.Fprintf(out, "checked %d attestation(s) offline: %d problem(s)\n", res.Attestations, len(res.Problems))
		return exitFindings, nil
	}
	fmt.Fprintf(out, "checked %d attestation(s) offline: no problems\n", res.Attestations)
	return 0, nil
}
