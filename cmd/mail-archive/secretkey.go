package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/crypto"
	"github.com/keepia/archive/internal/store"
)

// The secret key at start (#396, step 5). serve refuses to start without a
// key and with a key that is not the instance's: the database keeps the
// key's check value (crypto.KeyCheck). On the first start of a key, when
// no check value is recorded yet, the key must open at least one of the
// sealed values it can try (sealedSample), if there are any; then its
// check value is recorded. `secret-key accept` records a new key after an
// intended change.

// sealedSample is how many stored IMAP passwords and how many TOTP secrets
// the first start of a key tries to open.
const sealedSample = 5

// secretKeyDoc is where the messages about the secret key point.
const secretKeyDoc = `docs/reference/security.md, "The secret key"`

// missingSecretKeyError is the refusal of serve without a secret key.
func missingSecretKeyError() error {
	return config.NewSecretKeyError(fmt.Sprintf(
		"serve needs the secret key: set %s (preferred) or %s; create a key with `%s keygen` (%s)",
		config.EnvSecretKeyFile, config.EnvSecretKey, commandName(), secretKeyDoc))
}

// checkSecretKey is step 5 of serve's start: it runs under the migration
// lock on a current schema, so instance_state exists and two starts do
// not both record a check value.
func checkSecretKey(ctx context.Context, st *store.Store, cfg *config.Config, log *slog.Logger) error {
	if cfg.SecretKey == nil {
		return missingSecretKeyError()
	}
	given := crypto.KeyCheck(cfg.SecretKey)
	stored, err := st.SecretKeyCheck(ctx)
	if err != nil {
		return err
	}
	if stored != nil {
		if !crypto.SameKeyCheck(stored, given) {
			return config.NewSecretKeyError(fmt.Sprintf(
				"the secret key (%s, fingerprint %s) is not the key of this instance (fingerprint %s): "+
					"the stored IMAP passwords and TOTP secrets do not open with it; configure the instance's key, "+
					"or, after an intended change of the key, run `%s secret-key accept --yes` (%s)",
				cfg.SecretKeySource, crypto.KeyCheckFingerprint(given), crypto.KeyCheckFingerprint(stored), commandName(), secretKeyDoc))
		}
		log.Info("secret key checked", "fingerprint", crypto.KeyCheckFingerprint(given))
		return nil
	}
	// The first start with a check: the sealed values decide.
	sealer, err := crypto.NewSealer(cfg.SecretKey)
	if err != nil {
		return err
	}
	tried, opened, err := sealedValuesOpen(ctx, st, sealer)
	if err != nil {
		return err
	}
	if tried > 0 && opened == 0 {
		return config.NewSecretKeyError(fmt.Sprintf(
			"the secret key (%s, fingerprint %s) opens none of the %d stored IMAP passwords and TOTP secrets tried: "+
				"configure the key this instance used so far, or, after an intended change of the key, run `%s secret-key accept --yes` "+
				"and set those passwords and TOTP again (%s)",
			cfg.SecretKeySource, crypto.KeyCheckFingerprint(given), tried, commandName(), secretKeyDoc))
	}
	recorded, err := st.RecordSecretKeyCheck(ctx, given)
	if err != nil {
		return err
	}
	if recorded {
		log.Info("secret key check recorded", "fingerprint", crypto.KeyCheckFingerprint(given), "sealed_values_opened", opened)
	}
	return nil
}

// sealedValuesOpen tries to open up to sealedSample stored IMAP passwords
// and up to sealedSample TOTP secrets with sealer, and reports how many it
// tried and how many opened.
func sealedValuesOpen(ctx context.Context, st *store.Store, sealer *crypto.Sealer) (tried, opened int, err error) {
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		return 0, 0, err
	}
	n := 0
	for _, a := range accounts {
		if len(a.PasswordEnc) == 0 {
			continue // a removed account
		}
		if n == sealedSample {
			break
		}
		n++
		if _, err := archive.OpenPassword(sealer, a); err == nil {
			opened++
		}
	}
	totpTried, totpOpened, err := st.TwoFactorSecretsOpen(ctx, sealer, sealedSample)
	if err != nil {
		return 0, 0, err
	}
	return n + totpTried, opened + totpOpened, nil
}

func newSecretKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret-key",
		Short: "The check of the secret key at start (accept a new key)",
	}
	var yes bool
	accept := &cobra.Command{
		Use:   "accept --yes",
		Short: "Make the configured secret key the instance's key, after an intended change",
		Long: `Record the configured secret key (` + config.EnvSecretKeyFile + ` or
` + config.EnvSecretKey + `) as the key of this instance. serve refuses to start
with any other key, because the stored IMAP passwords and TOTP secrets open
only with the key that sealed them. After an intended change of the key,
this command makes serve start with the new one.

It first tells how many of the stored IMAP passwords and TOTP secrets it
tried open with the key. Those that do not must be set again: the
passwords of the accounts (account set-password, or in the web UI), and
TOTP for the users (user reset-2fa). Without --yes it changes nothing.
Written to the audit log as instance.secret_key_accepted, with the
fingerprints of the old and the new key's check value, never a key.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			if a.cfg.SecretKey == nil {
				return config.NewSecretKeyError(fmt.Sprintf("no secret key is configured: set %s (preferred) or %s",
					config.EnvSecretKeyFile, config.EnvSecretKey))
			}
			sealer, err := a.cfg.Sealer()
			if err != nil {
				return err
			}
			check := crypto.KeyCheck(a.cfg.SecretKey)
			tried, opened, err := sealedValuesOpen(cmd.Context(), a.store, sealer)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "secret key from %s, fingerprint %s: %d of %d stored IMAP passwords and TOTP secrets tried open with it\n",
				a.cfg.SecretKeySource, crypto.KeyCheckFingerprint(check), opened, tried)
			if !yes {
				return errors.New("nothing changed: run it again with --yes to make this key the instance's key")
			}
			previous, err := a.store.AcceptSecretKey(withCLIAudit(cmd, cliInstance("instance.secret_key_accepted")), check)
			if err != nil {
				return err
			}
			if previous != nil && crypto.SameKeyCheck(previous, check) {
				fmt.Fprintln(out, "this key is the instance's key already; nothing changed")
				return nil
			}
			fmt.Fprintln(out, "accepted: serve starts with this key from now on")
			if opened < tried {
				fmt.Fprintln(out, "set the IMAP passwords and TOTP that do not open with it again (account set-password, user reset-2fa)")
			}
			return nil
		},
	}
	accept.Flags().BoolVar(&yes, "yes", false, "confirm the change of the key")
	cmd.AddCommand(accept)
	return cmd
}
