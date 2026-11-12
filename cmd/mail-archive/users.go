package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
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
		newUserRemovePasskeysCmd(),
		newUserAPIKeysCmd(),
		newUserRevokeAPIKeysCmd(),
		newUserAdminCmd(true),
		newUserAdminCmd(false),
		newUserLockCmd(true),
		newUserLockCmd(false),
		newUserRemoveCmd(),
		newUserSetGroupCmd(),
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
	var admin, auditor, passwordStdin bool
	var group string
	cmd := &cobra.Command{
		Use:   "add NAME [--group GROUP]",
		Short: "Create a user (the password is prompted)",
		Long: `Create a user who can log in to the web UI. Names are lowercase: a-z, 0-9,
dot, hyphen and underscore. Passwords need at least 12 characters.

Every user belongs to one customer group. --group names it; it is needed
while several groups exist, otherwise the user joins the only one. Names
are unique across all groups, so prefix them per customer (meier.anna).

Admins will manage users in the web UI. They do not see other users' mail,
but they see everyone's audit log: searches, opened messages, logins.

--auditor creates an auditor (a tax advisor or tax auditor): a login that
reads only what an owner of its group grants it, read-only and for a
limited time, and must set up two-factor authentication. Its password is
generated and printed once; it must be changed at the first login. The
role never changes; a tax advisor gets one auditor login per customer
group.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := auth.NormalizeUserName(args[0])
			if err := auth.ValidateUserName(name); err != nil {
				return err
			}
			if admin && auditor {
				return errors.New("an auditor cannot be an admin")
			}
			if auditor && passwordStdin {
				return errors.New("an auditor's password is generated; leave out --password-stdin")
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			if _, err := a.store.GetUserByName(cmd.Context(), name); err == nil {
				return fmt.Errorf("user %q already exists", name)
			}
			var groupID int64
			if group != "" {
				g, err := findGroup(cmd, a, group)
				if err != nil {
					return err
				}
				groupID = g.ID
			} else if n, err := a.store.ListGroups(cmd.Context()); err != nil {
				return err
			} else if len(n) > 1 {
				return errors.New("several customer groups exist; choose one with --group (see: " + commandName() + " group list)")
			}
			// An auditor's password is generated (#393): the admin hands it
			// over, and the auditor chooses their own at the first login.
			var pw string
			if auditor {
				pw, err = auth.GeneratePassword()
			} else {
				pw, err = readNewPassword(passwordStdin, name)
			}
			if err != nil {
				return err
			}
			hash, err := hashPassword(cmd, pw)
			if err != nil {
				return err
			}
			e := cliTargeted("user.add")
			e.SetTarget(store.TargetUser, "", name) // the ID is set when it exists
			e.Detail("admin", admin)
			u, err := a.store.CreateUserInGroup(withCLIAudit(cmd, e), store.NewUser{
				Name: name, PasswordHash: hash, Admin: admin, GroupID: groupID, Auditor: auditor, Generated: auditor,
			})
			if err != nil {
				switch {
				case errors.Is(err, store.ErrConflict):
					return fmt.Errorf("user %q already exists", name)
				case errors.Is(err, store.ErrGroupRequired):
					return errors.New("several customer groups exist; choose one with --group (see: " + commandName() + " group list)")
				case errors.Is(err, store.ErrFirstUserAuditor):
					return errors.New("the first user cannot be an auditor; create the first admin with: " + commandName() + " user add NAME --admin")
				}
				return err
			}
			role := "user"
			switch {
			case admin:
				role = "admin"
			case auditor:
				role = "auditor"
			}
			fmt.Printf("%s %q created\n", role, name)
			if auditor {
				fmt.Printf("password for %q (shown only now; to be changed at the first login):\n%s\n", name, pw)
				return nil
			}
			// The first user gets the accounts added before any user existed.
			if counts, err := a.store.CountOwnedAccounts(cmd.Context()); err == nil && counts[u.ID] > 0 {
				fmt.Printf("%q owns the %d existing account(s)\n", name, counts[u.ID])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&admin, "admin", false, "allow managing users")
	cmd.Flags().BoolVar(&auditor, "auditor", false, "create an auditor, who reads only through grants (password generated)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	cmd.Flags().StringVar(&group, "group", "", "the user's customer group (needed while several exist)")
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
			passkeys, err := a.store.PasskeyCounts(cmd.Context())
			if err != nil {
				return err
			}
			apiKeys, err := a.store.APIKeyCounts(cmd.Context())
			if err != nil {
				return err
			}
			groups, err := groupNames(cmd, a)
			if err != nil {
				return err
			}
			if len(users) == 0 {
				fmt.Printf("No users yet. Create the first admin with: %s user add NAME --admin\n", commandName())
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tGROUP\tROLE\tSTATE\tACCOUNTS\tPASSKEYS\tAPI KEYS\tCREATED\tLAST LOGIN")
			// STATE says "must change password" for generated passwords.
			for _, u := range users {
				role, state, last := "user", "active", "never"
				switch {
				case u.IsAdmin:
					role = "admin"
				case u.IsAuditor:
					role = "auditor"
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
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\n", u.Name, groups[u.GroupID], role, state, owned[u.ID], passkeys[u.ID], apiKeys[u.ID], u.CreatedAt.Local().Format(time.DateOnly), last)
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
			if err := a.store.SetUserPassword(withCLIAudit(cmd, cliUserTarget(cliTargeted("user.password_set"), u)), u.ID, hash, store.PasswordReset{}); err != nil {
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
			e := cliUserTarget(cliTargeted("user.change"), u)
			e.Detail("locked", lock)
			if err := a.store.SetUserLocked(withCLIAudit(cmd, e), u.ID, lock); err != nil {
				return lastAdminError(err, u.Name)
			}
			fmt.Printf("user %q %s\n", u.Name, done)
			if lock {
				// The lock does not stop the SMTP secrets of the user's smtp
				// accounts (#395 decision 9).
				counts, err := a.store.UsableSMTPCredentialsByOwner(cmd.Context(), u.ID, time.Now())
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: the user is locked, but their SMTP secrets could not be counted: %v\n", err)
					return nil
				}
				smtpSecretHint(counts, u.Name, fmt.Sprintf("%q", u.Name))
			}
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
			e := cliUserTarget(cliTargeted("user.remove"), u)
			if err := a.store.DeleteUser(withCLIAudit(cmd, e), u.ID); err != nil {
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
			// An auditor's grants were revoked first and stay as history.
			if revoked, ok := e.Details["grants_revoked"].([]store.RevokedGrant); ok {
				for _, g := range revoked {
					fmt.Printf("auditor grant %d (%q of %s) revoked\n", g.ID, g.Label, g.Owner)
				}
			}
			return nil
		},
	}
}

func newUserResetPasswordCmd() *cobra.Command {
	var keepPasskeys, keepAPIKeys bool
	cmd := &cobra.Command{
		Use:   "reset-password NAME",
		Short: "Generate a new password the user must change at the next login",
		Long: `Generate a random password and print it once. Hand it to the user: at the
next login they must choose their own password. The user is logged out
everywhere.

The user's passkeys are removed and their API keys revoked too, unless
--keep-passkeys or --keep-api-keys is given: after a compromise, a new
password alone would not lock the attacker out.`,
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
			e := cliUserTarget(cliTargeted("user.password_reset"), u)
			e.Detail("passkeys_removed", !keepPasskeys)
			opts := store.PasswordReset{MustChange: true, DropPasskeys: !keepPasskeys, RevokeAPIKeys: !keepAPIKeys}
			if err := a.store.SetUserPassword(withCLIAudit(cmd, e), u.ID, hash, opts); err != nil {
				return err
			}
			if !keepPasskeys {
				fmt.Printf("passkeys of %q removed\n", u.Name)
			}
			if !keepAPIKeys {
				fmt.Printf("%v API key(s) of %q revoked\n", e.Details["api_keys_revoked"], u.Name)
			}
			fmt.Printf("new password for %q (shown only now; to be changed at the next login):\n%s\n", u.Name, pw)
			return nil
		},
	}
	cmd.Flags().BoolVar(&keepPasskeys, "keep-passkeys", false, "do not remove the user's passkeys")
	cmd.Flags().BoolVar(&keepAPIKeys, "keep-api-keys", false, "do not revoke the user's API keys")
	return cmd
}

func newUserRemovePasskeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove-passkeys NAME",
		Short: "Remove all passkeys of a user and end their sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			n, err := a.store.RemovePasskeys(withCLIAudit(cmd, cliUserTarget(cliTargeted("user.passkeys_removed"), u)), u.ID)
			if err != nil {
				return err
			}
			fmt.Printf("%d passkey(s) of %q removed; existing logins ended\n", n, u.Name)
			return nil
		},
	}
}

func newUserAPIKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "api-keys NAME",
		Short: "List a user's API keys",
		Long: `List a user's API keys: name, the last characters of the key, scopes,
when it was created, when it expires, when it was last used, and its state.
Revoked and expired keys stay listed for 30 days. Keys are created in the
web UI's profile, with the password and the second factor.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			keys, err := a.store.ListAPIKeys(cmd.Context(), u.ID)
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				fmt.Printf("%q has no API keys\n", u.Name)
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tKEY\tSCOPES\tCREATED\tEXPIRES\tLAST USED\tSTATE")
			now := time.Now()
			for _, k := range keys {
				expires, used := "never", "never"
				if k.ExpiresAt != nil {
					expires = k.ExpiresAt.Local().Format(time.DateOnly)
				}
				if k.LastUsedAt != nil {
					used = k.LastUsedAt.Local().Format(time.DateTime)
				}
				fmt.Fprintf(w, "%s\t%s…%s\t%s\t%s\t%s\t%s\t%s\n", k.Name, auth.APIKeyPrefix, k.Hint, strings.Join(k.Scopes, ","),
					k.CreatedAt.Local().Format(time.DateOnly), expires, used, k.State(now))
			}
			return w.Flush()
		},
	}
}

func newUserRevokeAPIKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke-api-keys NAME",
		Short: "Revoke all API keys of a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			e := cliUserTarget(cliTargeted("api_key.revoke"), u)
			e.Detail("reason", "cli")
			n, err := a.store.RevokeUserAPIKeys(withCLIAudit(cmd, e), u.ID, nil)
			if err != nil {
				return err
			}
			fmt.Printf("%d API key(s) of %q revoked\n", n, u.Name)
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
			e := cliUserTarget(cliTargeted("user.change"), u)
			e.Detail("admin", admin)
			if err := a.store.SetUserAdmin(withCLIAudit(cmd, e), u.ID, admin); err != nil {
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
			if err := a.store.ResetTwoFactor(withCLIAudit(cmd, cliUserTarget(cliTargeted("user.2fa_reset"), u)), u.ID); err != nil {
				return err
			}
			fmt.Printf("2FA of %q reset; existing logins ended\n", u.Name)
			return nil
		},
	}
}
