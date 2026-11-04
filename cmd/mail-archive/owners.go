package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// Accounts belong to users, and account names are only unique per user.
// Commands that name an account take --user when several users have an
// account with that name.

const userFlagHelp = "the user who owns the account (needed when several users have one with this name)"

// flagUser resolves --user; nil when it is not set.
func flagUser(cmd *cobra.Command, a *app) (*store.User, error) {
	f := cmd.Flag("user")
	if f == nil {
		return nil, nil
	}
	value := f.Value.String()
	if sv, ok := f.Value.(interface{ GetSlice() []string }); ok {
		// export's --user is repeatable for gobd; mbox and maildir take one.
		value = first(sv.GetSlice())
	}
	if value == "" {
		return nil, nil
	}
	name := auth.NormalizeUserName(value)
	u, err := a.store.GetUserByName(cmd.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("user %q not found", name)
	}
	return u, err
}

// userNames maps user IDs to names.
func userNames(cmd *cobra.Command, a *app) (map[int64]string, error) {
	users, err := a.store.ListUsers(cmd.Context())
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(users))
	for _, u := range users {
		out[u.ID] = u.Name
	}
	return out, nil
}

// ownerName is the name of an account's owner, "-" before the first user
// exists.
func ownerName(names map[int64]string, id *int64) string {
	if id == nil {
		return "-"
	}
	if n, ok := names[*id]; ok {
		return n
	}
	return fmt.Sprint(*id)
}

// findAccount looks up an account by name, also a removed one: the one of
// --user, or the only account with that name.
func findAccount(cmd *cobra.Command, a *app, name string) (*store.Account, error) {
	ctx := cmd.Context()
	u, err := flagUser(cmd, a)
	if err != nil {
		return nil, err
	}
	if u != nil {
		acc, err := a.store.GetOwnedAccount(ctx, u.ID, name)
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("user %q has no account %q", u.Name, name)
		}
		return acc, err
	}
	accounts, err := a.store.ListAccountsByName(ctx, name)
	if err != nil {
		return nil, err
	}
	switch len(accounts) {
	case 0:
		return nil, fmt.Errorf("account %q not found", name)
	case 1:
		return accounts[0], nil
	}
	names, err := userNames(cmd, a)
	if err != nil {
		return nil, err
	}
	owners := make([]string, 0, len(accounts))
	for _, acc := range accounts {
		owners = append(owners, ownerName(names, acc.OwnerID))
	}
	return nil, fmt.Errorf("several users have an account named %q (%s); choose one with --user", name, strings.Join(owners, ", "))
}

// newOwner picks the owner of a new account: --user, or the only user. With
// no user yet the account has no owner until the first user is created.
func newOwner(cmd *cobra.Command, a *app) (*int64, error) {
	u, err := flagUser(cmd, a)
	if err != nil {
		return nil, err
	}
	if u != nil {
		return &u.ID, nil
	}
	users, err := a.store.ListUsers(cmd.Context())
	if err != nil {
		return nil, err
	}
	switch len(users) {
	case 0:
		return nil, nil
	case 1:
		return &users[0].ID, nil
	}
	return nil, errors.New("several users exist; choose the account's owner with --user")
}

func newAccountMoveCmd() *cobra.Command {
	var to string
	var otherGroup bool
	cmd := &cobra.Command{
		Use:   "move NAME --to USER [--other-group]",
		Short: "Hand an account and its archived mail to another user",
		Long: `Hand an account to another user, who then sees it and the mail found in it
instead of the current owner. Also works for removed accounts.

A user of another customer group gets the account only with --other-group:
the account and its mail then belong to the other customer, and every share
of the account ends.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			acc, err := findAccount(cmd, a, args[0])
			if err != nil {
				return err
			}
			target, err := a.store.GetUserByName(cmd.Context(), auth.NormalizeUserName(to))
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("user %q not found", auth.NormalizeUserName(to))
			}
			if err != nil {
				return err
			}
			if acc.OwnerID != nil && *acc.OwnerID == target.ID {
				return fmt.Errorf("account %q already belongs to %q", acc.Name, target.Name)
			}
			names, err := userNames(cmd, a)
			if err != nil {
				return err
			}
			e := cliAccountTarget(cliTargeted("account.move"), acc)
			e.Detail("from_user", ownerName(names, acc.OwnerID))
			e.Detail("to_user", target.Name)
			if err := a.store.SetAccountOwner(withCLIAudit(cmd, e), acc.Ref(), target.ID, otherGroup); err != nil {
				if errors.Is(err, store.ErrConflict) {
					return fmt.Errorf("%q already has an account named %q; rename one of them first", target.Name, acc.Name)
				}
				if errors.Is(err, store.ErrOtherGroup) {
					return fmt.Errorf("%q is in another customer group; add --other-group to move the account and its mail to that group (its shares end)", target.Name)
				}
				return staleError(err)
			}
			fmt.Printf("account %q now belongs to %q\n", acc.Name, target.Name)
			if ended, ok := e.Details["shares_ended"].([]string); ok {
				for _, u := range ended {
					fmt.Printf("share with %q ended\n", u)
				}
			}
			// A recurring fill goes on importing the same mailbox, now for
			// the new owner.
			fills, err := a.store.AccountFills(cmd.Context(), acc.ID)
			if err != nil {
				return err
			}
			for _, f := range fills {
				if f.KeepRunning {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: fill %d keeps importing %s into %q, now for %q; stop it with: %s account fill %s --cancel --fill %d\n",
						f.ID, f.Username, acc.Name, target.Name, commandName(), shellQuote(acc.Name), f.ID)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the new owner (required)")
	cmd.Flags().BoolVar(&otherGroup, "other-group", false, "allow a new owner in another customer group; the account's shares end")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}
