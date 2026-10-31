# Users

Everyone who uses the web UI has their own login. Each user sees only their
own accounts, the accounts other users [shared](#shared-accounts) with them,
and the mail found in them; see [Security](../reference/security#login).

## The first admin

The first user is created on the command line, usually right after
installing:

```sh
./ma user add patrick --admin
```

Administrators must set up two-factor authentication (TOTP) at their first
web login, after choosing their own password. Admins cannot turn it off.
Another admin, or `./ma user reset-2fa NAME`, can reset it; the admin then
sets it up again at the next login.

This first user also gets all accounts that were added before any user
existed.

## Adding users

Admins open **Users** in the navigation and click **Add user**. In the
dialog they enter a name (lowercase letters, digits, `.`, `-`, `_`) and
choose whether the new user is an admin.

![Users page](/screenshots/users.png)

The server generates a password such as `k7mq2-x9tbr-4hdne-p3wza` and shows
it **once**. Hand it over safely. At the first login the user must choose
their own password before they can do anything else.

On the command line, `./ma user add NAME` asks for a password instead, and
`./ma user reset-password NAME` generates one like the web UI does.

## Managing users

The users page is a table with each user's role, state, sign-in (2FA,
passkeys, API keys), accounts and last login. On a phone the table shows
the name, the state and the menu; role and last login are in a second line
under the name.

The name opens the **user's page**: the role and state, a row of tiles
(accounts, last login, two-factor authentication, passkeys, when the user
was created) and the user's [API keys](#api-keys). The page has its own
address, so Back, reload and bookmarks work.

![A user's page](/screenshots/user-detail.png)

For every other user, the menu (⋮) at the end of the row and the menu on
the user's page offer the actions below; the user's page also has **Reset
password** as a button. Actions that delete or lock out ask in a dialog
first. Admins can:

- **Reset 2FA:** only shown while the user has 2FA on. Turns it off and logs
  the user out everywhere. Admins, and everyone when 2FA is required, set it
  up again at the next login.
- **Reset password:** generates a new password, shown once. The user is
  logged out everywhere and must change it at the next login. Their passkeys
  are removed and their [API keys](#api-keys) revoked too, unless you untick
  the boxes: after a break-in, a new password alone would not lock the
  intruder out.
- **Remove passkeys:** removes all passkeys of the user and logs them out
  everywhere, for example after a lost phone.
- **API keys:** the user's page lists their keys; **Revoke** stops one at
  once, after asking. This also works for your own keys, on your own page.
- **Lock / Unlock:** a locked user is logged out at once and cannot log in;
  their API keys stop working until they are unlocked.
- **Make admin / Remove admin.**
- **Remove:** only for users without accounts. A user who still owns accounts
  (also removed ones) must hand them over first, on the command line:
  `./ma account move NAME --user OLD --to NEW`. Removed from the user's page,
  the list takes its place. The shares of other users' accounts with the
  removed user end; a new user with the same name gets none of them.

A user whose accounts keep failing to sync is marked with the number of
those accounts, so you can tell them.

The last admin who can log in can be neither locked, removed nor demoted.
Admins cannot change their own login on this page: their own row has no
menu, and their own user's page has no actions but **Revoke** for their API
keys and a link to their profile. They use their profile, or another admin
does it.

Admins manage logins and never see other users' mail, unless an owner
shared an account with them. They do see the [audit log](./audit-log) of
every user on the **Log** page: searches with their text, which messages
were opened, account names, IP addresses and browsers. Tell your users
before they use the archive. Every user sees their own entries there too.

## Your profile

The person icon in the header, then **Profile**, or **Profile** in the
navigation opens your profile. It has three tabs:

- **Display:** the [language and formats](./web-ui#appearance-and-language)
  of the web UI.
- **Sign-in & security:** your password, [2FA](#two-factor-authentication)
  and [passkeys](#passkeys).
- **API keys:** [keys for scripts](#api-keys).

To change your password you need the current one. Other devices are logged
out; the one you use stays logged in.

![Profile page, tab Display](/screenshots/profile.png)

![Profile page, tab Sign-in & security](/screenshots/profile-security.png)

Wrong current passwords count like failed logins: after 5 within 15 minutes,
you have to wait.

## Two-factor authentication

In your profile, on the **Sign-in & security** tab, a tag shows whether 2FA
is on. **Set up authenticator app** shows a QR code. Scan it with an
authenticator app (for example Aegis, 2FAS or Google Authenticator) or type
the key below it (the copy button copies it), then enter the 6-digit code
from the app and click **Enable two-factor authentication**.

You then get 10 recovery codes, shown **once**, in a dialog that only
**I have saved the codes** closes. Keep them somewhere safe: each one
replaces a code from the app a single time, for example when the phone is
lost. **Download PDF** and **Download .txt** save them with your user name,
the server address and the date, to print or to keep offline or in a
password manager. The browser makes the file from the codes on screen; the
server does not send them again.

Users who are not admins can generate new codes in their profile with a
code from the app (**Generate new recovery codes**), and download them the
same way. The old codes stop working, so throw away the old sheet.

From then on, the login asks for a code after the password. A code works
only once and only for about 30 seconds before or after its time.

Users who are not admins can turn 2FA off in their profile with their
password and a code, unless the server requires it for everyone
(`MAIL_ARCHIVE_REQUIRE_2FA=true`, see
[Configuration](../reference/configuration)).

If you lose both the app and the recovery codes, an admin resets your 2FA.

## Passkeys

A passkey signs you in with Face ID, Touch ID, Windows Hello or a security
key, without password and code. It works only at the address in
`MAIL_ARCHIVE_PUBLIC_URL` (see [Configuration](../reference/configuration)),
over HTTPS or on `localhost`.

On the **Sign-in & security** tab, click **Add passkey**. In the dialog,
enter a name for the passkey (like *iPhone*), your password and, if you use
2FA, a code, then click **Add passkey** and confirm on your device. You can
have up to 10 passkeys; the table lists them with their last use.

At the login, click **Sign in with a passkey**, or pick the passkey in the
user name field when the browser offers it.

**Remove** in your profile deletes a passkey, after asking, and logs out
your other devices. Your password keeps working; admins must still keep TOTP
set up.

The passkey itself stays on your device or in your password manager. Chrome
and Safari are told to forget it; Firefox and some password managers keep
offering it, and signing in with it then says it is not registered. Delete
it there too (in Firefox: Settings, Privacy & Security, Passkeys; on a Mac:
the Passwords app).

## Shared accounts

The owner of an account can let another user read it: search, open and
download its mail. Managing, syncing and removing the account, retention
and legal holds stay with the owner. Only the owner shares, also among
admins, one account with one user at a time, by the user's exact name.
smtp (journal) accounts cannot be shared.

- The shared user sees the account as `owner/name` (for example
  `anna/info`), with its folders, their counts and when each was last
  synced and reconciled, but not its server, login, sync errors, sync
  health or the other users it is shared with.
- A message in several accounts shows the shared user only the locations
  of accounts they may see. A message the owner has only in an account that
  is not shared stays hidden.
- Ending the share takes effect on the user's next request. A removed
  account keeps its shares (they can be ended, not added); deleting it
  completely ends them. `./ma account move` keeps the shares; the new owner's
  own share ends, and the old owner keeps no access unless the new owner
  shares it.
- Every read of shared mail is in the [audit log](./audit-log#shared-accounts),
  and the owner sees it in their Log with the reader's name and whether a
  session or an API key read it, without the reader's IP address, browser
  or key name.

The owner shares on the account's page, in its section **Shared with**: the
user's exact name, then **Share**; **Remove** ends a share (see
[The account's page](./accounts#the-account-s-page)). The shared user finds
the account under **Shared with you** on the Accounts page and, with its
owner's name (`info · anna`), in the folder column, in search and in
conversations. The owner's Log names the reader in the **Who** column and
says, for example, "jo's search showed 2 messages of info", without the
reader's search terms unless the search named the account.

Sharing is done with `PUT` and `DELETE /api/accounts/{name}/shares/{user}`
(see the [JSON API](../reference/api#shared-accounts)).

## API keys

Scripts, cron jobs and other tools use an API key instead of your login.
This also works for admins and everyone with 2FA or passkeys: the second
factor is checked when the key is created, not on every use.

In your profile, on the **API keys** tab, enter a name (like *backup
script*), choose the scopes and when the key expires, confirm with your
password and, if you use 2FA, a code, then click **Create API key**. The key
is shown once: copy it right away. It cannot be shown again; create a new
one if it is lost.

- **read** lets the key search and read your mail, also in the accounts
  shared with you, download messages and attachments, and see your
  accounts and their status.
- **sync** lets it start syncs of your accounts.

A key can do nothing else: no logins, no profile or account changes, no
user management, also for an admin. How to use a key is described in the
[JSON API](../reference/api#api-keys).

The table shows each key with its last 4 characters, its scopes, its state
(active, expired or revoked), when it expires and when it was last used.
**Revoke** stops a key at once, after asking. You can have up to 20 active
keys; revoked and expired keys stay listed for 30 days.

![Profile page, tab API keys](/screenshots/profile-api-keys.png)

Changing your password keeps your keys, so scripts do not break. Revoke a
key if it may be known to others.

Admins see the number of keys per user on the users page and can list and
revoke any user's keys on the user's page. A password reset by an admin
revokes the user's keys unless the admin unticks the box. Locking a user
suspends their keys until the user is unlocked; removing a user deletes
them. On the command line, `./ma user api-keys NAME` lists and
`./ma user revoke-api-keys NAME` revokes a user's keys.
