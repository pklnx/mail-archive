package main

import (
	"errors"
	"fmt"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/keepia/archive/internal/store"
)

// Auditor grants on the command line (#393): the operator lists every
// grant and may revoke any; owners create and extend their grants in the
// web UI.

func newAuditorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auditor",
		Short: "List and revoke auditor grants",
		Long: `List and revoke auditor grants. An owner grants an auditor of their
customer group read access to some of their accounts for a limited time,
in the web UI. Create auditor logins with: user add NAME --auditor`,
	}
	cmd.AddCommand(newAuditorListCmd(), newAuditorRevokeCmd())
	return cmd
}

func newAuditorListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every auditor grant, with ended ones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			grants, err := a.store.AllGrants(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(grants) == 0 {
				fmt.Fprintln(out, "No auditor grants.")
				return nil
			}
			now := time.Now()
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tGROUP\tOWNER\tAUDITOR\tLABEL\tSTARTS\tENDS\tSTATE")
			for _, g := range grants {
				auditor := g.Auditor
				if auditor == "" {
					auditor = "-" // removed
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", g.ID, g.Group, g.Owner, auditor, g.Label,
					g.StartsAt.Local().Format(time.DateOnly), g.EndsAt.Local().Format(time.DateOnly), grantState(g, now))
			}
			return w.Flush()
		},
	}
}

// grantState names a grant's state as the web UI does, with the reason of
// a revocation.
func grantState(g store.GrantSummary, now time.Time) string {
	switch {
	case g.RevokedAt != nil:
		return "revoked (" + g.RevokeReason + ")"
	case g.Active:
		return "active"
	case now.Before(g.StartsAt):
		return "scheduled"
	case !now.Before(g.EndsAt):
		return "expired"
	}
	return "suspended (a user is locked)"
}

func newAuditorRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke ID",
		Short: "Revoke an auditor grant at once (ends the auditor's sessions on it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id <= 0 {
				return fmt.Errorf("%q is no grant ID (see: %s auditor list)", args[0], commandName())
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			err = a.store.RevokeGrant(withCLIAudit(cmd, cliTargeted(store.ActionAuditorRevoke)), id, nil, nil)
			switch {
			case errors.Is(err, store.ErrNotFound):
				return fmt.Errorf("no auditor grant %d (see: %s auditor list)", id, commandName())
			case errors.Is(err, store.ErrGrantRevoked):
				return fmt.Errorf("auditor grant %d is revoked already", id)
			case err != nil:
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "auditor grant %d revoked\n", id)
			return nil
		},
	}
}
