package main

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/store"
)

// Customer groups (#470): one instance serves several customers. Every
// user belongs to one group, every account to its owner's; shares stay
// inside a group.

func newGroupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "group",
		Short: "Manage customer groups",
		Long: `Manage customer groups. Every user belongs to exactly one group and every
account to its owner's group. Users share accounts only with users of their
own group and never see names of other groups' users.

An instance starts with one group, "Default". While it is the only one,
nothing changes for anyone. Move users with: user set-group NAME GROUP`,
	}
	cmd.AddCommand(newGroupAddCmd(), newGroupListCmd(), newGroupRenameCmd(), newGroupRemoveCmd(), newGroupSetQuotaCmd())
	return cmd
}

func newGroupAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add NAME",
		Short: "Create a customer group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if err := store.ValidateGroupName(name); err != nil {
				return err
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			if _, err := a.store.CreateGroup(withCLIAudit(cmd, cliTargeted("group.add")), name); err != nil {
				if errors.Is(err, store.ErrConflict) {
					return fmt.Errorf("a group named %q already exists", name)
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "group %q created\n", name)
			return nil
		},
	}
}

func newGroupListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List customer groups",
		Long: `List customer groups with their users, accounts, storage quota (QUOTA, "-"
without one), storage used (USED: message bytes deduplicated within the
group, as in usage; "-" before the first snapshot) and quota state (STATE:
ok, warn, full or overflow).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			groups, err := a.store.ListGroups(cmd.Context())
			if err != nil {
				return err
			}
			usage, err := groupStorage(cmd.Context(), a)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tUSERS\tACCOUNTS\tQUOTA\tUSED\tSTATE\tCREATED")
			for _, g := range groups {
				u := usage[g.ID]
				quota, used, state := "-", "-", u.State
				if u.QuotaBytes != nil {
					quota = humanBytes(*u.QuotaBytes)
				}
				if u.SnapshotAt != nil {
					used = humanBytes(u.Bytes)
				}
				if state == "" {
					state = "ok"
				}
				fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\t%s\t%s\n", g.Name, g.Users, g.Accounts, quota, used, state,
					g.CreatedAt.Local().Format(time.DateOnly))
			}
			return w.Flush()
		},
	}
}

func newGroupRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename NAME NEW-NAME",
		Short: "Rename a customer group",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[1])
			if err := store.ValidateGroupName(name); err != nil {
				return err
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			g, err := findGroup(cmd, a, args[0])
			if err != nil {
				return err
			}
			if err := a.store.RenameGroup(withCLIAudit(cmd, cliTargeted("group.rename")), g.ID, name); err != nil {
				if errors.Is(err, store.ErrConflict) {
					return fmt.Errorf("a group named %q already exists", name)
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "group %q is now %q\n", g.Name, name)
			return nil
		},
	}
}

func newGroupRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a customer group without users",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			g, err := findGroup(cmd, a, args[0])
			if err != nil {
				return err
			}
			err = a.store.DeleteGroup(withCLIAudit(cmd, cliTargeted("group.remove")), g.ID)
			switch {
			case errors.Is(err, store.ErrGroupHeld):
				return fmt.Errorf("group %q stays: a legal hold names it (holds are released, never deleted)", g.Name)
			case errors.Is(err, store.ErrGroupInUse):
				return fmt.Errorf("group %q still has users; move them first with: %s user set-group NAME GROUP", g.Name, commandName())
			case errors.Is(err, store.ErrLastGroup):
				return fmt.Errorf("group %q is the last group; every user needs one", g.Name)
			case err != nil:
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "group %q removed\n", g.Name)
			return nil
		},
	}
}

func newUserSetGroupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-group NAME GROUP",
		Short: "Move a user into another customer group",
		Long: `Move a user into another customer group, with the accounts they own. Every
share that would then cross groups ends: the user's accounts shared with
users of the old group, and old-group accounts shared with the user. Every
auditor grant of the user, as auditor or as owner, that would cross groups
is revoked, and the auditor's sessions on it end. The ended shares and
revoked grants are printed and recorded in the audit log.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			g, err := findGroup(cmd, a, args[1])
			if err != nil {
				return err
			}
			c, err := a.store.SetUserGroup(withCLIAudit(cmd, cliUserTarget(cliTargeted("user.group_change"), u)), u.ID, g.ID)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !c.Changed {
				fmt.Fprintf(out, "%q already is in group %q\n", u.Name, g.Name)
				return nil
			}
			fmt.Fprintf(out, "%q moved from group %q to %q\n", u.Name, c.From, c.To)
			for _, s := range c.Ended {
				fmt.Fprintf(out, "share ended: %s/%s with %s\n", s.Owner, s.Account, s.User)
			}
			for _, g := range c.GrantsRevoked {
				fmt.Fprintf(out, "auditor grant revoked: %d %q of %s to %s\n", g.ID, g.Label, g.Owner, g.Auditor)
			}
			return nil
		},
	}
}

// findGroup looks up a group by name, regardless of case.
func findGroup(cmd *cobra.Command, a *app, name string) (*store.Group, error) {
	g, err := a.store.GetGroupByName(cmd.Context(), name)
	if errors.Is(err, store.ErrUnknownGroup) {
		return nil, fmt.Errorf("group %q not found (see: %s group list)", strings.TrimSpace(name), commandName())
	}
	return g, err
}

// groupNames maps group IDs to names.
func groupNames(cmd *cobra.Command, a *app) (map[int64]string, error) {
	groups, err := a.store.ListGroups(cmd.Context())
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(groups))
	for _, g := range groups {
		out[g.ID] = g.Name
	}
	return out, nil
}

// userGroupNames maps user IDs to the names of their groups.
func userGroupNames(cmd *cobra.Command, a *app) (map[int64]string, error) {
	groups, err := groupNames(cmd, a)
	if err != nil {
		return nil, err
	}
	users, err := a.store.ListUsers(cmd.Context())
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(users))
	for _, u := range users {
		out[u.ID] = groups[u.GroupID]
	}
	return out, nil
}

// ownerGroup is the group of an account's owner, "-" without an owner.
func ownerGroup(groups map[int64]string, owner *int64) string {
	if owner == nil {
		return "-"
	}
	if g, ok := groups[*owner]; ok {
		return g
	}
	return "-"
}
