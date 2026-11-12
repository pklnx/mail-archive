package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/smtpd"
	"github.com/pklnx/mail-archive/internal/store"
)

// startSMTP starts the SMTP server for smtp accounts. The returned stop
// shuts it down and waits until messages being stored are committed; it
// must run before the store closes.
func startSMTP(ctx context.Context, a *app, blobs *blobstore.Store, log *slog.Logger) (stop func(), err error) {
	c := a.cfg.SMTP
	srv := &smtpd.Server{
		Store: a.store, Blobs: blobs, Logger: log, Allowed: c.AllowedNetworks,
		Hostname: c.Hostname, MaxSize: c.MaxSize,
	}
	if c.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", config.EnvSMTPTLSCert, config.EnvSMTPTLSKey, err)
		}
		srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	l, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", config.EnvSMTPListen, err)
	}
	nets := make([]string, 0, len(c.AllowedNetworks))
	for _, p := range c.AllowedNetworks {
		nets = append(nets, p.String())
	}
	// Postfix drops a journal copy the archive refuses as too large, so its
	// message_size_limit must not be larger.
	log.Info("smtp on", "addr", l.Addr().String(), "allowed_networks", nets, "max_size", c.MaxSize,
		"starttls", srv.TLS != nil, "hint", "Postfix message_size_limit must not exceed max_size")
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ctx, l); err != nil {
			log.Error("smtp server stopped", "err", err)
		}
	}()
	return func() {
		cancel()
		<-done
	}, nil
}

func newAccountAddSMTPCmd() *cobra.Command {
	var addresses []string
	cmd := &cobra.Command{
		Use:   "add-smtp NAME --address ADDRESS",
		Short: "Add an smtp account that receives copies of every message from a mail server",
		Long: `Add an smtp account. It receives every message a mail server sends to one of
its addresses over SMTP (journaling, for example Postfix always_bcc or
recipient_bcc_maps), so unlike an IMAP account it misses nothing that is
deleted between two syncs. The web server receives the mail when
` + config.EnvSMTPListen + ` is set.

An address carries no "+extension": the mail server may encode the original
recipient there (archive+alice=example.com@archive.local), and the archive
stores alice@example.com as the recipient.`,
		Example: `  mail-archive account add-smtp journal --address archive@archive.local`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			acc := store.Account{Name: args[0], Enabled: true}
			if acc.OwnerID, err = newOwner(cmd, a); err != nil {
				return err
			}
			e := cliTargeted("account.add")
			e.SetTarget(store.TargetAccount, "", acc.Name) // the ID is set when it exists
			e.Detail("kind", string(store.KindSMTP))
			e.Detail("addresses", addresses)
			if err := a.store.CreateSMTPAccount(withCLIAudit(cmd, e), &acc, addresses); err != nil {
				if errors.Is(err, store.ErrConflict) {
					return fmt.Errorf("an account named %q already exists for this user (removed accounts keep their name)", acc.Name)
				}
				return err
			}
			fmt.Printf("smtp account %q added for %s\n", acc.Name, strings.Join(addresses, ", "))
			if a.cfg.SMTP == nil {
				fmt.Printf("\nhint: set %s and %s for the web server to receive mail.\n",
					config.EnvSMTPListen, config.EnvSMTPAllowedNetworks)
			}
			fmt.Printf("hint: create the mail server's SMTP login (user name %s) with: %s account smtp-secret %s%s --new\n",
				auth.SMTPUsername(acc.ID), commandName(), shellQuote(acc.Name), userArg(cmd))
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&addresses, "address", nil, "address the account receives for (repeatable; required)")
	_ = cmd.MarkFlagRequired("address")
	return cmd
}

func newAccountSMTPAddressesCmd() *cobra.Command {
	var add, remove []string
	cmd := &cobra.Command{
		Use:   "smtp-addresses NAME [--add ADDRESS] [--remove ADDRESS]",
		Short: "Show or change the addresses of an smtp account",
		Long: `Show the addresses an smtp account receives for, or add and remove some.
An address belongs to one account; the account keeps at least one. Mail to
an address no account receives for is refused with 550, and the mail
server drops it without telling anyone.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, acc, err := loadAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if acc.Kind != store.KindSMTP {
				return fmt.Errorf("%q is not an smtp account", acc.Name)
			}
			if len(add)+len(remove) > 0 {
				e := cliAccountTarget(cliTargeted("account.change"), acc)
				e.Detail("fields", []string{"addresses"})
				e.Detail("added", add)
				e.Detail("removed", remove)
				if err := a.store.ChangeSMTPAddresses(withCLIAudit(cmd, e), acc.Ref(), add, remove); err != nil {
					return staleError(err)
				}
			}
			all, err := a.store.SMTPAddresses(cmd.Context())
			if err != nil {
				return err
			}
			for _, addr := range all[acc.ID] {
				fmt.Println(addr)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&add, "add", nil, "address to add (repeatable)")
	cmd.Flags().StringArrayVar(&remove, "remove", nil, "address to remove (repeatable)")
	return cmd
}

var graceDays = regexp.MustCompile(`^([0-9]{1,2})d$`)

// parseGrace parses --grace: whole days such as "7d", 0 to 30.
func parseGrace(v string) (time.Duration, error) {
	m := graceDays.FindStringSubmatch(v)
	if v == "0" {
		m = []string{v, v}
	}
	if m == nil {
		return 0, fmt.Errorf("--grace %q: give whole days, for example 7d", v)
	}
	n, _ := strconv.Atoi(m[1])
	d := time.Duration(n) * 24 * time.Hour
	if d > store.MaxSMTPGrace {
		return 0, fmt.Errorf("--grace %q: at most 30d", v)
	}
	return d, nil
}

func newAccountSMTPSecretCmd() *cobra.Command {
	var create bool
	var grace string
	var revoke int64
	cmd := &cobra.Command{
		Use:   "smtp-secret NAME [--new [--grace 7d] | --revoke ID]",
		Short: "List, create or revoke the SMTP secrets of an smtp account",
		Long: `List the SMTP login of an smtp account: its user name, smtp-<account id>,
and its secrets, by the last characters, with their state. Revoked and
expired secrets stay listed for 30 days.

--new creates a secret and prints it once, with the line for the mail
server's SASL password map; only its hash is stored. The current secret
stays usable for --grace (default 7d, at most 30d) so the mail server can
switch without losing mail; --grace 0 revokes it at once. A secret that was
already expiring is revoked: an account has at most two usable secrets.

--revoke ID revokes a secret at once.

The user who owns the account and the admins manage the secrets in the web
UI too. Locking the owner does not stop them: rotate or revoke them when the
owner should no longer know a usable one.`,
		Example: `  mail-archive account smtp-secret journal
  mail-archive account smtp-secret journal --new
  mail-archive account smtp-secret journal --new --grace 0
  mail-archive account smtp-secret journal --revoke 12`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			changed := cmd.Flags().Changed
			switch {
			case create && changed("revoke"):
				return errors.New("give --new or --revoke, not both")
			case changed("grace") && !create:
				return errors.New("--grace needs --new")
			}
			var graceD time.Duration
			if create {
				var err error
				if graceD, err = parseGrace(grace); err != nil {
					return err
				}
			}
			a, acc, err := loadAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if acc.Kind != store.KindSMTP {
				return fmt.Errorf("%q is not an smtp account; only smtp accounts have SMTP secrets", acc.Name)
			}
			username := auth.SMTPUsername(acc.ID)
			switch {
			case create:
				secret, hash, hint, err := auth.NewSMTPSecret()
				if err != nil {
					return err
				}
				days := int(graceD / (24 * time.Hour))
				e := cliAccountTarget(cliTargeted("smtp_credential.create"), acc)
				e.Detail("hint", hint)
				e.Detail("grace_days", days)
				c, previous, err := a.store.CreateSMTPCredential(withCLIAudit(cmd, e), acc.Ref(), store.NewSMTPCredential{
					Hash: hash, Hint: hint, Grace: graceD, Now: time.Now(),
				})
				if err != nil {
					return staleError(err)
				}
				for _, p := range previous {
					if p.RevokedAt != nil {
						fmt.Printf("secret %d revoked\n", p.ID)
					} else {
						fmt.Printf("secret %d stays usable until %s\n", p.ID, p.ExpiresAt.Local().Format(time.DateTime))
					}
				}
				fmt.Printf("secret %d created for %q. It is shown only now:\n\n", c.ID, acc.Name)
				fmt.Printf("user name: %s\nsecret:    %s\n\n", username, secret)
				fmt.Println("Postfix SASL password map (use the host and port the mail server connects to):")
				fmt.Printf("[archive.example]:587 %s:%s\n", username, secret)
				return nil
			case changed("revoke"):
				e := cliAccountTarget(cliTargeted("smtp_credential.revoke"), acc)
				c, err := a.store.RevokeSMTPCredential(withCLIAudit(cmd, e), &acc.ID, revoke, nil, time.Now())
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("%q has no secret %d that is not revoked", acc.Name, revoke)
				}
				if err != nil {
					return err
				}
				fmt.Printf("secret %d (%s…%s) of %q revoked\n", c.ID, auth.SMTPSecretPrefix, c.Hint, acc.Name)
				return nil
			}
			list, err := a.store.SMTPCredentials(cmd.Context(), acc.ID)
			if err != nil {
				return err
			}
			fmt.Printf("user name: %s\n", username)
			if len(list) == 0 {
				fmt.Printf("%q has no SMTP secrets; create one with: %s account smtp-secret %s%s --new\n",
					acc.Name, commandName(), shellQuote(acc.Name), userArg(cmd))
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSECRET\tCREATED\tEXPIRES\tLAST USED\tSTATE")
			now := time.Now()
			for _, c := range list {
				expires, used := "never", "never"
				if c.ExpiresAt != nil {
					expires = c.ExpiresAt.Local().Format(time.DateTime)
				}
				if c.LastUsedAt != nil {
					used = c.LastUsedAt.Local().Format(time.DateTime)
				}
				fmt.Fprintf(w, "%d\t%s…%s\t%s\t%s\t%s\t%s\n", c.ID, auth.SMTPSecretPrefix, c.Hint,
					c.CreatedAt.Local().Format(time.DateTime), expires, used, c.State(now))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&create, "new", false, "create a secret and print it once")
	cmd.Flags().StringVar(&grace, "grace", "7d", "with --new: how long the current secret stays usable, 0 to 30d")
	cmd.Flags().Int64Var(&revoke, "revoke", 0, "revoke the secret with this ID")
	return cmd
}

// smtpSecretHint prints, for each smtp account in counts, that its secrets
// stay usable, which matters when who knows them should no longer deliver
// (#395 decision 9). who names whose knowledge it is about.
func smtpSecretHint(counts []store.SMTPCredentialCount, owner, who string) {
	for _, c := range counts {
		fmt.Printf("hint: smtp account %q has %d usable SMTP secret(s), which %s may know and which keep working; rotate them with: %s account smtp-secret %s --user %s --new --grace 0\n",
			c.AccountName, c.N, who, commandName(), shellQuote(c.AccountName), shellQuote(owner))
	}
}
