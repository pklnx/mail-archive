package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/store"
)

func newRetentionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Manage retention rules (how long mail must be kept)",
		Long: `A retention rule says how many years the mail of an account, or of one
folder of it, must be kept, counted from the end of the year it was received
or sent in (§147 Abs. 4 AO). Without a rule, mail is kept for ever. When
several rules match a message, the longest wins, also across the accounts
of other users that hold the same message.

Rules alone delete nothing: deletion is a separate, reviewed step.`,
	}
	rule := &cobra.Command{Use: "rule", Short: "Add, list and remove retention rules"}
	rule.AddCommand(newRetentionRuleAddCmd(), newRetentionRuleListCmd(), newRetentionRuleRemoveCmd())
	cmd.AddCommand(rule)
	return cmd
}

func newRetentionRuleAddCmd() *cobra.Command {
	var account, folder, timeZone, note string
	var years int
	var force bool
	cmd := &cobra.Command{
		Use:   "add --account NAME --years N",
		Short: "Add a retention rule for an account or one of its folders",
		Long: `Add a rule that keeps the mail of an account (or of --folder, matched
exactly) for --years after the end of the year it was received or sent in,
in --time-zone. An account has at most one rule for all its folders and one
per folder. Rules are not changed; remove one and add another.

Periods under 6 years (business letters, §147 AO) need --force. Which
period applies is a legal question; check with your tax adviser.`,
		Example: `  mail-archive retention rule add --account work --years 8 --note "§147 AO, invoices by mail"
  mail-archive retention rule add --account work --folder Newsletter --years 1 --force`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if years < store.MinBusinessLetterYears && !force {
				return fmt.Errorf("%d years is shorter than the %d years for business letters (§147 AO); add --force if that is intended",
					years, store.MinBusinessLetterYears)
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			acc, err := findAccount(cmd, a, account)
			if err != nil {
				return err
			}
			e := cliAccountTarget(cliEntry("retention.rule_add"), acc)
			e.Detail("years", years)
			e.Detail("time_zone", timeZoneOrDefault(timeZone))
			if folder != "" {
				e.Detail("folder", folder)
			}
			id, err := a.store.AddRetentionRule(withCLIAudit(cmd, e), store.NewRetentionRule{
				AccountID: acc.ID, Folder: folder, Years: years, TimeZone: timeZone, Note: note,
			})
			if err != nil {
				return err
			}
			where := fmt.Sprintf("account %q", acc.Name)
			if folder != "" {
				where = fmt.Sprintf("folder %q of account %q", folder, acc.Name)
				if ok, err := hasFolder(cmd, a, acc, folder); err == nil && !ok {
					fmt.Fprintf(cmd.ErrOrStderr(), "note: account %q has no folder %q yet; the rule applies once it exists\n", acc.Name, folder)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rule %d: %s keeps mail for %d years after the end of its year (%s)\n",
				id, where, years, timeZoneOrDefault(timeZone))
			return nil
		},
	}
	cmd.Flags().StringVar(&account, "account", "", "the account (required)")
	cmd.Flags().String("user", "", userFlagHelp)
	cmd.Flags().StringVar(&folder, "folder", "", "only this folder of the account, matched exactly")
	cmd.Flags().IntVar(&years, "years", 0, "years after the end of the year of receipt (1 to 100, required)")
	cmd.Flags().StringVar(&timeZone, "time-zone", store.DefaultRetentionTimeZone, "time zone of the year end")
	cmd.Flags().StringVar(&note, "note", "", "why, for example the legal basis")
	cmd.Flags().BoolVar(&force, "force", false, "allow a period under 6 years")
	_ = cmd.MarkFlagRequired("account")
	_ = cmd.MarkFlagRequired("years")
	return cmd
}

func timeZoneOrDefault(tz string) string {
	if tz == "" {
		return store.DefaultRetentionTimeZone
	}
	return tz
}

// hasFolder reports whether acc has a folder with this name.
func hasFolder(cmd *cobra.Command, a *app, acc *store.Account, folder string) (bool, error) {
	if acc.OwnerID == nil {
		return true, nil
	}
	accounts, err := a.store.ListAccountFolders(cmd.Context(), *acc.OwnerID)
	if err != nil {
		return false, err
	}
	for _, af := range accounts {
		if af.Name != acc.Name {
			continue
		}
		for _, f := range af.Folders {
			if f.Name == folder {
				return true, nil
			}
		}
	}
	return false, nil
}

func newRetentionRuleListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the retention rules",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			rules, err := a.store.RetentionRules(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(rules) == 0 {
				fmt.Fprintln(out, "no retention rules: all mail is kept for ever")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tOWNER\tACCOUNT\tFOLDER\tYEARS\tTIME ZONE\tCREATED\tNOTE")
			for _, r := range rules {
				owner, folder, account := r.Owner, r.Folder, r.Account
				if owner == "" {
					owner = "-"
				}
				if folder == "" {
					folder = "(all)"
				}
				if r.AccountRemoved {
					account += " (removed)"
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", r.ID, owner, account, folder, r.Years, r.TimeZone,
					r.CreatedAt.Local().Format(time.DateOnly), r.Note)
			}
			return w.Flush()
		},
	}
}

func newRetentionRuleRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove ID",
		Short: "Remove a retention rule",
		Long: `Remove a rule. Mail it covered is then kept for ever, unless another rule
covers it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("rule ID %q is not a number", args[0])
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			e := cliEntry("retention.rule_remove")
			if err := a.store.RemoveRetentionRule(withCLIAudit(cmd, e), id); errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no rule %d", id)
			} else if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rule %d removed\n", id)
			return nil
		},
	}
}

func newHoldCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hold",
		Short: "Manage legal holds (no deletion of what they cover)",
		Long: `A legal hold stops the deletion of the mail it covers while it is active,
whatever the retention rules say: during a tax audit (Ablaufhemmung), a
lawsuit, or an investigation. Holds are released with a note, never
deleted.`,
	}
	cmd.AddCommand(newHoldAddCmd(), newHoldListCmd(), newHoldReleaseCmd())
	return cmd
}

func newHoldAddCmd() *cobra.Command {
	var reason, account, since, until string
	var filter store.HoldFilter
	var all bool
	cmd := &cobra.Command{
		Use:   "add NAME --reason TEXT",
		Short: "Add a legal hold",
		Long: `Add a hold. Every condition that is given must match:

  --user        mail in the user's accounts
  --account     mail in one account (with --user when the name is not unique)
  --since, --until
                mail whose Date header or received date falls in the period
                (YYYY-MM-DD in UTC, or RFC 3339); mail without dates counts
                as inside
  --query, --sender, --recipient, --attachment, --has-attachment
                mail that search finds with this filter, as the user who owns
                it sees it. The matches are stored now and before every
                deletion, and only ever added.

Without any condition the hold covers the whole archive; that needs --all.`,
		Example: `  mail-archive hold add "Tax audit 2026" --reason "Betriebsprüfung, letter of 2026-09-01" --account work --until 2024-01-01
  mail-archive hold add "Falcon dispute" --reason "lawsuit 12 O 345/26" --query falcon`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h := store.NewLegalHold{Name: args[0], Reason: reason}
			if !filter.IsZero() {
				h.Filter = &filter
			}
			if since != "" {
				t, err := parseAuditDate(since, false)
				if err != nil {
					return fmt.Errorf("--since: %w", err)
				}
				h.PeriodStart = &t
			}
			if until != "" {
				t, err := parseAuditDate(until, true)
				if err != nil {
					return fmt.Errorf("--until: %w", err)
				}
				h.PeriodEnd = &t
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			e := cliEntry("hold.add")
			u, err := flagUser(cmd, a)
			if err != nil {
				return err
			}
			if u != nil {
				h.OwnerID = &u.ID
				cliUserTarget(e, u)
			}
			if account != "" {
				acc, err := findAccount(cmd, a, account)
				if err != nil {
					return err
				}
				h.AccountID = &acc.ID
				cliAccountTarget(e, acc)
			}
			if h.OwnerID == nil && h.AccountID == nil && h.Filter == nil && h.PeriodStart == nil && h.PeriodEnd == nil && !all {
				return errors.New("a hold without --user, --account, a period or a filter covers the whole archive; add --all if that is intended")
			}
			id, err := a.store.AddLegalHold(withCLIAudit(cmd, e), h)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "legal hold %d added\n", id)
			if h.Filter != nil {
				n, err := a.store.MatchLegalHolds(cmd.Context())
				if err != nil {
					return fmt.Errorf("the hold covers everything in its scope until its filter is matched: %w", err)
				}
				fmt.Fprintf(out, "the filter matched %d message(s)\n", n)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the mail is held (required)")
	cmd.Flags().String("user", "", "only mail in this user's accounts; also picks the account of --account")
	cmd.Flags().StringVar(&account, "account", "", "only mail in this account")
	cmd.Flags().StringVar(&since, "since", "", "only mail from this date on")
	cmd.Flags().StringVar(&until, "until", "", "only mail before the end of this date")
	cmd.Flags().StringVar(&filter.Query, "query", "", "only mail that search finds with this text")
	cmd.Flags().StringVar(&filter.From, "sender", "", "only mail whose sender contains this")
	cmd.Flags().StringVar(&filter.To, "recipient", "", "only mail whose To or Cc contains this")
	cmd.Flags().StringVar(&filter.Attachment, "attachment", "", "only mail with an attachment whose name contains this")
	cmd.Flags().BoolVar(&filter.HasAttachment, "has-attachment", false, "only mail with an attachment")
	cmd.Flags().BoolVar(&all, "all", false, "allow a hold over the whole archive")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func newHoldListCmd() *cobra.Command {
	var released bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the active legal holds",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			holds, err := a.store.LegalHolds(cmd.Context(), released)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(holds) == 0 {
				fmt.Fprintln(out, "no legal holds")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tSCOPE\tCREATED\tSTATE\tREASON")
			for _, h := range holds {
				state := "active"
				if h.ReleasedAt != nil {
					state = "released " + h.ReleasedAt.Local().Format(time.DateOnly) + ": " + h.ReleaseNote
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", h.ID, h.Name, holdScope(&h), h.CreatedAt.Local().Format(time.DateOnly), state, h.Reason)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&released, "released", false, "also list released holds")
	return cmd
}

// holdScope describes what a hold covers.
func holdScope(h *store.LegalHold) string {
	var parts []string
	if h.Owner != "" {
		parts = append(parts, "user "+h.Owner)
	}
	if h.Account != "" {
		parts = append(parts, fmt.Sprintf("account %s (%s)", h.Account, h.AccountOwner))
	}
	if h.PeriodStart != nil {
		parts = append(parts, "from "+h.PeriodStart.UTC().Format(time.DateOnly))
	}
	if h.PeriodEnd != nil {
		parts = append(parts, "before "+h.PeriodEnd.UTC().Format(time.DateOnly))
	}
	if f := h.Filter; f != nil {
		var terms []string
		for _, t := range []struct{ k, v string }{{"", f.Query}, {"from:", f.From}, {"to:", f.To}, {"attachment:", f.Attachment}} {
			if t.v != "" {
				terms = append(terms, t.k+strconv.Quote(t.v))
			}
		}
		if f.HasAttachment {
			terms = append(terms, "has:attachment")
		}
		match := "not matched yet"
		if h.MatchedAt != nil {
			match = fmt.Sprintf("%d matched", h.Matched)
		}
		parts = append(parts, fmt.Sprintf("filter %s (%s)", strings.Join(terms, " "), match))
	}
	if len(parts) == 0 {
		return "whole archive"
	}
	return strings.Join(parts, ", ")
}

func newHoldReleaseCmd() *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   "release ID --note TEXT",
		Short: "Release a legal hold",
		Long: `Release a hold: the mail it covered may be deleted again once its
retention period has ended. The hold stays listed with --released.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("hold ID %q is not a number", args[0])
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			err = a.store.ReleaseLegalHold(withCLIAudit(cmd, cliEntry("hold.release")), id, nil, note)
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no legal hold %d", id)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "legal hold %d released\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "why it is released (required)")
	_ = cmd.MarkFlagRequired("note")
	return cmd
}

// retentionStatus is the retention line of status.
func retentionStatus(cmd *cobra.Command, a *app) (string, error) {
	c, err := a.store.RetentionCounts(cmd.Context())
	if err != nil {
		return "", err
	}
	if c.Rules == 0 && c.ActiveHolds == 0 {
		return "retention: no rules (all mail is kept for ever)", nil
	}
	return fmt.Sprintf("retention: %d rule(s), %d active legal hold(s)", c.Rules, c.ActiveHolds), nil
}
