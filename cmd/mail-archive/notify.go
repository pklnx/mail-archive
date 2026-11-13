package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/alertmail"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/jobs"
	"github.com/pklnx/mail-archive/internal/notify"
	"github.com/pklnx/mail-archive/internal/store"
)

// syncDeliveryBudget bounds the delivery pass at the end of `sync`. It does
// not wait for retries; the next sync or the web server sends them.
const syncDeliveryBudget = 15 * time.Second

func newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Check the alert webhook and the alert mail relay",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Send a test message to " + config.EnvNotifyWebhookURL,
		Long: `Send one test message to the webhook configured in
` + config.EnvNotifyWebhookURL + `, in the format of
` + config.EnvNotifyWebhookFormat + `. Prints the HTTP status and exits non-zero
if the message was not accepted. The URL is never printed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.Webhook == nil {
				return errors.New(config.EnvNotifyWebhookURL + " is not set")
			}
			w := notify.NewWebhook(cfg.Webhook)
			out := cmd.OutOrStdout()
			if cfg.Webhook.PlainHTTP() {
				fmt.Fprintf(out, "warning: %s uses plain http to another host; alerts and the authorization travel unencrypted\n", w.Target())
			}
			status, err := w.Send(cmd.Context(), notify.TestMessage(time.Now()))
			if err != nil {
				fmt.Fprintf(out, "%s: %v\n", w.Target(), err)
				return &exitError{msg: "the test message was not accepted"}
			}
			fmt.Fprintf(out, "%s: HTTP %d, test message sent\n", w.Target(), status)
			return nil
		},
	})
	var to string
	mailTest := &cobra.Command{
		Use:   "mail-test --to ADDRESS",
		Short: "Send a test mail through " + config.EnvMailURL,
		Long: `Send one test mail to ADDRESS through the relay configured in
` + config.EnvMailURL + ` (TLS required), as alert mail goes. Prints the
SMTP reply code and exits non-zero if the relay did not take the mail.
Neither the password nor the relay's reply text is printed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.Mail == nil {
				return errors.New(config.EnvMailURL + " is not set")
			}
			addr, err := config.ParseMailAddress(to)
			if err != nil || addr.Name != "" {
				return fmt.Errorf("--to: want one mail address, such as admin@example.com")
			}
			now := time.Now()
			subject, body := alertmail.TestTexts(now)
			msg, err := alertmail.Compose(alertmail.Message{
				From: cfg.Mail.FromHeader, To: addr.Address, Subject: subject, Body: body, Date: now,
				MessageID: alertmail.MessageID("test."+now.UTC().Format("20060102150405"), 0, cfg.PublicURL),
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			res, err := newMailSender(cfg).Send(cmd.Context(), addr.Address, msg)
			if err != nil {
				fmt.Fprintf(out, "%s: %v\n", cfg.Mail.Target(), err)
				return &exitError{msg: "the test mail was not accepted"}
			}
			reply := strconv.Itoa(res.Code)
			if res.Enhanced != "" {
				reply += " " + res.Enhanced
			}
			fmt.Fprintf(out, "%s: SMTP %s, test mail sent\n", cfg.Mail.Target(), reply)
			return nil
		},
	}
	mailTest.Flags().StringVar(&to, "to", "", "the address to send the test mail to")
	_ = mailTest.MarkFlagRequired("to")
	cmd.AddCommand(mailTest)
	return cmd
}

// newNotifier returns the notifier for the configured webhook and alert
// mail, or nil without either.
func newNotifier(a *app, log *slog.Logger) *notify.Notifier {
	if a.cfg.Webhook == nil && a.cfg.Mail == nil {
		return nil
	}
	n := &notify.Notifier{
		Store: a.store, AlertAfter: a.cfg.AlertAfterFailures, Logger: log,
		StorageQuota: a.cfg.Storage.Quota, Mail: a.cfg.Mail != nil,
	}
	if a.cfg.Webhook != nil {
		if a.cfg.Webhook.PlainHTTP() {
			log.Warn("the alert webhook uses plain http to another host; alerts and the authorization travel unencrypted",
				"target", a.cfg.Webhook.Target())
		}
		n.Webhook = notify.NewWebhook(a.cfg.Webhook)
	}
	return n
}

// newAlertMail returns the alert mail service for the configured relay,
// or nil without one (#397, part 5).
func newAlertMail(a *app, log *slog.Logger) *alertmail.Service {
	if a.cfg.Mail == nil {
		return nil
	}
	return &alertmail.Service{
		Store: a.store, Mailer: newMailSender(a.cfg), From: a.cfg.Mail.FromHeader, PublicURL: a.cfg.PublicURL, Logger: log,
	}
}

// newMailSender is the sender through the configured relay; tests
// replace it to reach their own SMTP server.
var newMailSender = func(cfg *config.Config) alertmail.Mailer {
	return &alertmail.Sender{Cfg: cfg.Mail, LocalName: publicHost(cfg.PublicURL)}
}

// publicHost is the host of MAIL_ARCHIVE_PUBLIC_URL, the EHLO name; empty
// without one.
func publicHost(publicURL string) string {
	u, err := url.Parse(publicURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// mailPool runs the notify_mail jobs of the process that runs the
// notifier (serve, and `sync` at its end): one at a time, independent of
// MAIL_ARCHIVE_JOB_WORKERS, so that the worker service needs no relay
// password. untilIdle returns once none is due.
func mailPool(a *app, svc *alertmail.Service, log *slog.Logger, untilIdle bool) (*jobs.Pool, error) {
	kinds := &jobs.Registry{}
	if err := kinds.Register(svc.Kind()); err != nil {
		return nil, err
	}
	return &jobs.Pool{Store: a.store, Kinds: kinds, Workers: 1, Logger: log, UntilIdle: untilIdle}, nil
}

// syncMailBudget bounds the alert mail sent at the end of `sync`: one
// delivery may take alertmail.DeliveryTimeout. Mail not sent by then is
// left to the next sync or serve.
const syncMailBudget = alertmail.DeliveryTimeout + 30*time.Second

// deliverAfterSync sends the alerts and recoveries that a `sync` made due,
// so that setups without the web server get them too: the webhook, and
// the alert mail the claims enqueued.
func deliverAfterSync(ctx context.Context, a *app, n *notify.Notifier, log *slog.Logger) {
	if n == nil || ctx.Err() != nil {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, syncDeliveryBudget)
	err := n.Deliver(dctx)
	cancel()
	if err != nil {
		log.Error("deliver notifications", "err", err)
	}
	svc := newAlertMail(a, log)
	if svc == nil || ctx.Err() != nil {
		return
	}
	pool, err := mailPool(a, svc, log, true)
	if err != nil {
		log.Error("alert mail", "err", err)
		return
	}
	mctx, cancel := context.WithTimeout(ctx, syncMailBudget)
	defer cancel()
	if err := pool.Run(mctx); err != nil {
		log.Error("alert mail", "err", err)
	}
}

// failedColumn is the FAILED column of `status`: "-" or "4x since …".
func failedColumn(h *store.SyncHealth) string {
	if h == nil || h.FailureStreak == 0 {
		return "-"
	}
	s := fmt.Sprintf("%dx", h.FailureStreak)
	if h.FailingSince != nil {
		s += " since " + h.FailingSince.Local().Format("2006-01-02 15:04")
	}
	return s
}
