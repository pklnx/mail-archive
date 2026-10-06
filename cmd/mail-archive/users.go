package main

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

func newUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage who can log in to the web UI",
		Long: `Manage who can log in to the web UI.

Create the first admin with:  user add NAME --admin`,
	}
	cmd.AddCommand(
		newUserAddCmd(),
		newUserListCmd(),
		newUserSetPasswordCmd(),
		newUserResetPasswordCmd(),
		newUserReset2FACmd(),
		newUserAdminCmd(true),
		newUserAdminCmd(false),
		newUserLockCmd(true),
		newUserLockCmd(false),
		newUserRemoveCmd(),
	)
	return cmd
}

// readNewPassword asks for a new web password, twice when typed in a
// terminal, and checks its length.
func readNewPassword(fromStdin bool, user string) (string, error) {
	interactive := !fromStdin && term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // fd fits in int
	if interactive {
		fmt.Fprintf(os.Stderr, "Passwords need at least %d characters.\n", auth.MinPasswordLength)
	}
	pw, err := readPassword(fromStdin, "new password for "+user+": ")
	if err != nil {
		return "", err
	}
	if err := auth.ValidatePassword(pw); err != nil {
		return "", err
	}
	if interactive {
		again, err := readPassword(false, "repeat the password: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			return "", errors.New("the passwords do not match")
		}
	}
	return pw, nil
}

func hashPassword(cmd *cobra.Command, pw string) (string, error) {
	return auth.NewHasher(auth.DefaultParams).Hash(cmd.Context(), pw)
}

func newUserAddCmd() *cobra.Command {
	var admin, passwordStdin bool
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Create a user (the password is prompted)",
		Long: `Create a user who can log in to the web UI. Names are lowercase: a-z, 0-9,
dot, hyphen and underscore. Passwords need at least 12 characters.

Admins will manage users in the web UI. They do not see other users' mail.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := auth.NormalizeUserName(args[0])
			if err := auth.ValidateUserName(name); err != nil {
				return err
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			if _, err := a.store.GetUserByName(cmd.Context(), name); err == nil {
				return fmt.Errorf("user %q already exists", name)
			}
			pw, err := readNewPassword(passwordStdin, name)
			if err != nil {
				return err
			}
			hash, err := hashPassword(cmd, pw)
			if err != nil {
				return err
			}
			u, err := a.store.CreateUser(cmd.Context(), name, hash, admin)
			if err != nil {
				if errors.Is(err, store.ErrConflict) {
					return fmt.Errorf("user %q already exists", name)
				}
				return err
			}
			role := "user"
			if admin {
				role = "admin"
			}
			fmt.Printf("%s %q created\n", role, name)
			// The first user gets the accounts added before any user existed.
			if counts, err := a.store.CountOwnedAccounts(cmd.Context()); err == nil && counts[u.ID] > 0 {
				fmt.Printf("%q owns the %d existing account(s)\n", name, counts[u.ID])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&admin, "admin", false, "allow managing users")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	return cmd
}

func newUserListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			users, err := a.store.ListUsers(cmd.Context())
			if err != nil {
				return err
			}
			owned, err := a.store.CountOwnedAccounts(cmd.Context())
			if err != nil {
				return err
			}
			if len(users) == 0 {
				fmt.Printf("No users yet. Create the first admin with: %s user add NAME --admin\n", commandName())
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tROLE\tSTATE\tACCOUNTS\tCREATED\tLAST LOGIN")
			// STATE says "must change password" for generated passwords.
			for _, u := range users {
				role, state, last := "user", "active", "never"
				if u.IsAdmin {
					role = "admin"
				}
				switch {
				case u.LockedAt != nil:
					state = "locked"
				case u.MustChangePassword:
					state = "must change password"
				}
				if u.LastLoginAt != nil {
					last = u.LastLoginAt.Local().Format(time.DateTime)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", u.Name, role, state, owned[u.ID], u.CreatedAt.Local().Format(time.DateOnly), last)
			}
			return w.Flush()
		},
	}
}

func loadUser(cmd *cobra.Command, name string) (*app, *store.User, error) {
	a, err := openApp(cmd.Context())
	if err != nil {
		return nil, nil, err
	}
	name = auth.NormalizeUserName(name)
	u, err := a.store.GetUserByName(cmd.Context(), name)
	if err != nil {
		a.close()
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, fmt.Errorf("user %q not found", name)
		}
		return nil, nil, err
	}
	return a, u, nil
}

func lastAdminError(err error, name string) error {
	if errors.Is(err, store.ErrLastAdmin) {
		return fmt.Errorf("%q is the last admin who can log in; create or unlock another admin first", name)
	}
	return err
}

func newUserSetPasswordCmd() *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "set-password NAME",
		Short: "Set a new password (logs the user out everywhere)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			pw, err := readNewPassword(passwordStdin, u.Name)
			if err != nil {
				return err
			}
			hash, err := hashPassword(cmd, pw)
			if err != nil {
				return err
			}
			if err := a.store.SetUserPassword(cmd.Context(), u.ID, hash, false); err != nil {
				return err
			}
			fmt.Printf("password of %q changed; existing logins ended\n", u.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	return cmd
}

func newUserLockCmd(lock bool) *cobra.Command {
	use, short, done := "unlock NAME", "Allow a locked user to log in again", "unlocked"
	if lock {
		use, short, done = "lock NAME", "Stop a user from logging in (logs them out everywhere)", "locked"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if err := a.store.SetUserLocked(cmd.Context(), u.ID, lock); err != nil {
				return lastAdminError(err, u.Name)
			}
			fmt.Printf("user %q %s\n", u.Name, done)
			return nil
		},
	}
}

func newUserRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Delete a user (only without accounts)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if err := a.store.DeleteUser(cmd.Context(), u.ID); err != nil {
				if errors.Is(err, store.ErrOwnsAccounts) {
					n := int64(0)
					if counts, cerr := a.store.CountOwnedAccounts(cmd.Context()); cerr == nil {
						n = counts[u.ID]
					}
					return fmt.Errorf("%q still owns %d account(s), also removed ones; hand them to another user first with: %s account move NAME --user %s --to USER",
						u.Name, n, commandName(), u.Name)
				}
				return lastAdminError(err, u.Name)
			}
			fmt.Printf("user %q removed\n", u.Name)
			return nil
		},
	}
}

func newUserResetPasswordCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-password NAME",
		Short: "Generate a new password the user must change at the next login",
		Long: `Generate a random password and print it once. Hand it to the user: at the
next login they must choose their own password. The user is logged out
everywhere.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			pw, err := auth.GeneratePassword()
			if err != nil {
				return err
			}
			hash, err := hashPassword(cmd, pw)
			if err != nil {
				return err
			}
			if err := a.store.SetUserPassword(cmd.Context(), u.ID, hash, true); err != nil {
				return err
			}
			fmt.Printf("new password for %q (shown only now; to be changed at the next login):\n%s\n", u.Name, pw)
			return nil
		},
	}
}

func newUserAdminCmd(admin bool) *cobra.Command {
	use, short, done := "demote NAME", "Take the admin role away", "is no longer an admin"
	if admin {
		use, short, done = "promote NAME", "Make a user an admin", "is now an admin"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if err := a.store.SetUserAdmin(cmd.Context(), u.ID, admin); err != nil {
				return lastAdminError(err, u.Name)
			}
			fmt.Printf("%q %s\n", u.Name, done)
			return nil
		},
	}
}

func newUserReset2FACmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-2fa NAME",
		Short: "Reset a user's TOTP and end their sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if err := a.store.ResetTwoFactor(cmd.Context(), u.ID); err != nil {
				return err
			}
			fmt.Printf("2FA of %q reset; existing logins ended\n", u.Name)
			return nil
		},
	}
}
