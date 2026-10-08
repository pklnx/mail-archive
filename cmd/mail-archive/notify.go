package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/notify"
	"github.com/pklnx/mail-archive/internal/store"
)

// syncDeliveryBudget bounds the delivery pass at the end of `sync`. It does
// not wait for retries; the next sync or the web server sends them.
const syncDeliveryBudget = 15 * time.Second

func newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Check the alert webhook",
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
	return cmd
}

// newNotifier returns the notifier for the configured webhook, or nil
// without one.
func newNotifier(a *app, log *slog.Logger) *notify.Notifier {
	if a.cfg.Webhook == nil {
		return nil
	}
	if a.cfg.Webhook.PlainHTTP() {
		log.Warn("the alert webhook uses plain http to another host; alerts and the authorization travel unencrypted",
			"target", a.cfg.Webhook.Target())
	}
	return &notify.Notifier{
		Store: a.store, Webhook: notify.NewWebhook(a.cfg.Webhook), AlertAfter: a.cfg.AlertAfterFailures, Logger: log,
	}
}

// deliverAfterSync sends the alerts and recoveries that a `sync` made due,
// so that setups without the web server get them too.
func deliverAfterSync(ctx context.Context, n *notify.Notifier, log *slog.Logger) {
	if n == nil || ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, syncDeliveryBudget)
	defer cancel()
	if err := n.Deliver(ctx); err != nil {
		log.Error("deliver notifications", "err", err)
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
