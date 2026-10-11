package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/spf13/cobra"

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
			if err := a.store.CreateSMTPAccount(cmd.Context(), &acc, addresses); err != nil {
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
				if err := a.store.ChangeSMTPAddresses(cmd.Context(), acc.Ref(), add, remove); err != nil {
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
