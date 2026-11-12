# Users

Everyone who uses the web UI has their own login. Each user sees only their
own accounts, the accounts other users [shared](#shared-accounts) with them,
and the mail found in them; see [Security](../reference/security#login). In
the hosted instance the admin is the operator: the operator creates your
login and manages users; you manage your own profile, accounts and shares.
In a shared instance every user belongs to one [customer group](#customer-groups),
and shares stay inside it. A tax advisor or tax auditor gets an
[auditor login](#auditor-access), which reads only what an owner grants it.

## The first admin

The operator creates the first user, an admin, on the command line when the
instance is set up:

```sh
./ma user add patrick --admin
```

Administrators must set up two-factor authentication (TOTP) at their first
web login, after choosing their own password. Admins cannot turn it off.
Another admin, or `./ma user reset-2fa NAME` on the command line, can reset
it; the admin then sets it up again at the next login.

This first user also gets all accounts that were added before any user
existed.

## Adding users

Admins (the operator) open **Users** in the navigation and click **Add
user**. In the dialog they enter a name (lowercase letters, digits, `.`,
`-`, `_`) and choose the role: **User**, **Admin** or **Auditor** (see
[Auditor access](#auditor-access)). While there are two or more
[customer groups](#customer-groups), they also choose the user's group, for
every role; with a group filter set, that group is chosen already.

![Users page](/screenshots/users.png)

The server generates a password such as `k7mq2-x9tbr-4hdne-p3wza` and shows
it **once**. Hand it over safely. At the first login the user must choose
their own password before they can do anything else.

On the command line, `./ma user add NAME` asks for a password instead, and
`./ma user reset-password NAME` generates one like the web UI does. While
several [customer groups](#customer-groups) exist, a new user needs one:
`./ma user add NAME --group GROUP`, or `group` in
[`POST /api/users`](../reference/api#users-and-profile).

## Customer groups

One shared instance can serve several customers, for example a tax office
and a coaching business. Every user belongs to exactly one customer group,
and every account to its owner's group. An instance starts with one group,
`Default`, which holds every user; while it is the only one, nothing changes
for anyone. An individual instance stays at that one group.

- Users share accounts only with users of their own group. Asked for a name
  that is not in their group, the server gives one answer, whether the name
  exists in another group or not, so no customer learns another customer's
  user names.
- Users who are not admins never see group names, other members or names of
  other groups. Admins (the operator) see every group's users and their audit
  log.
- User names are unique across the whole instance, because login is by name.
  Prefix them per customer, for example `meier.anna`.
- Moving a user to another group (**Change group** in the user's menu, or
  `./ma user set-group NAME GROUP`) moves their accounts with them and ends
  every share that would cross groups: the user's accounts shared with users
  of the old group and old-group accounts shared with the user. The dialog
  says so before; afterwards the page says how many shares ended, the command
  prints them, and the audit log records them (`user.group_change`). Admins
  change their own group on the command line or through another admin.
- Handing an account to a user of another group needs
  `./ma account move NAME --to USER --other-group`, and ends all of the
  account's shares.
- A group without users can be removed; the last group cannot, nor a group
  that a [legal hold](./retention#holds-per-customer-group) names. A legal
  hold on a group also keeps its users with accounts in it until the hold is
  released.

While there is one group, the users page looks as it always did. The
operator creates the second group on the command line:

```sh
./ma group add "Steuerbüro Meier"
./ma user add meier.anna --group "Steuerbüro Meier"
./ma user set-group jo "Steuerbüro Meier"
./ma group list
```

With two or more groups, the users page has a **Group** column, a group
filter above the table and **Change group** in each user's menu, and a
user's page shows the group. Under the table, **Groups** lists every group
with its users and accounts; **Add group** adds one, and each group's menu
has **Rename** and **Remove**, which stays off while the group has users.

![The customer groups under the users table](/screenshots/user-groups.png)

Groups separate customers logically, inside one database and one archive
directory (see [Security](../reference/security#login)). A customer who needs
physical separation gets an individual instance.

## Managing users

The users page is a table with each user's role, its customer group (with
two or more), state, sign-in (2FA, passkeys, API keys), accounts and last
login. On a phone the table shows
the name, the state and the menu; role and last login are in a second line
under the name.

The name opens the **user's page**: the role and state, a row of tiles
(accounts, last login, two-factor authentication, passkeys, when the user
was created, and the customer group with two or more) and the user's [API keys](#api-keys). The page has its own
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
- **Make admin / Remove admin:** not for auditors, whose role never changes.
- **Change group:** with two or more [customer groups](#customer-groups);
  ends the shares and [auditor grants](#auditor-access) that would cross
  groups. The page then says how many of each ended.
- **Remove:** only for users without accounts. A user who still owns accounts
  (also removed ones) must hand them over first, on the command line:
  `./ma account move NAME --user OLD --to NEW`. Removed from the user's page,
  the list takes its place. The shares of other users' accounts
  with the removed user end; a new user with the same name gets none of them.
  Removing an auditor ends its grants first, as the dialog says; the page
  then names them, and they stay in their owners' lists as history.

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
before they use the archive. Every user sees their own entries there too;
the command-line entries are visible to admins, and to another user only
when an entry concerns them (see [The Log page](./audit-log#the-log-page)).

The web UI shows the operator no other user's mail either, but the operator
runs the server and the database and can technically read the stored mail
(see [What is protected how](../reference/security#what-is-protected-how)).

## Your profile

The profile icon in the header (on phones and touch screens: the person
icon, then **Profile**), or **Profile** in the navigation opens your
profile. It has three tabs:

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
only once and only for about 30 seconds before or after its time. After 3
wrong codes, the login starts again with the password.

Users who are not admins can turn 2FA off in their profile with their
password and a code, unless the operator requires it for everyone
(`MAIL_ARCHIVE_REQUIRE_2FA=true`, see
[Configuration](../reference/configuration)).

If you lose both the app and the recovery codes, an admin resets your 2FA.

## Passkeys

A passkey signs you in with Face ID, Touch ID, Windows Hello or a security
key, without password and code. It works only at the instance's address,
which the operator sets as `MAIL_ARCHIVE_PUBLIC_URL` (see
[Configuration](../reference/configuration)), over HTTPS or on `localhost`.

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
admins, one account with one user at a time, by the user's exact name, and
only with a user of their own [customer group](#customer-groups). smtp
(journal) accounts cannot be shared.

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
  shares it. Moving the account or one of its users into another group ends
  the shares that would cross groups.
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

## Auditor access

For a tax audit or the work of a tax advisor, an owner can give an auditor
read-only access to some of their accounts for a limited time (the
unmittelbarer Datenzugriff of GoBD, see
[Data access in a tax audit](./retention#data-access-in-a-tax-audit-z1-z2-z3)).
The auditor searches, opens and downloads that mail and its attachments, and
nothing else. Every access is in the owner's [Log](./audit-log#auditors).

### Auditor logins

The operator creates the login: on the users page with **Add user** and the
role **Auditor**, or with `./ma user add NAME --auditor` (see
[CLI](../reference/cli#auditors)). The password is generated and shown once.

- An auditor belongs to one [customer group](#customer-groups) and reads only
  grants of owners in that group. A tax advisor who works for two customers
  of a shared instance gets one login per customer group, for example
  `meier.stb-schmidt` and `lindqvist.stb-schmidt`.
- The role never changes: an auditor is never an admin, owns no accounts,
  receives no shares and has no API keys. The users page shows it as
  `auditor`.
- Moving an auditor to another group (**Change group**) ends all its grants;
  the dialog says so. Removing it ends them too; they stay in the owners'
  lists as history.

### The first sign-in

At the first login the auditor chooses their own password, then sets up
[two-factor authentication](#two-factor-authentication), which auditors
cannot turn off (an admin resets it). With more than one active grant, the
auditor then chooses the case to open. Until these steps are done the app
shows only them and sends nothing else. An auditor without an active grant
cannot log in.

The auditor's app has **All mail**, **Log** and **Profile**. A banner names
the case, its owner, the last day of access and the message period, and
says that every access is recorded. With several grants, **Audit case** in
the banner switches to another one; the app then starts again on that
case's mail. The granted accounts are in the folder column as `name · owner`,
with the granted folders only and without counts. The profile has no API
keys tab.

![The auditor's view of a case](/screenshots/auditor-view.png)

### Granting access

Owners open **Auditor access** in the navigation (shown while you own
accounts) and click **Grant access**:

- **Auditor's login name:** the exact name the operator gave the login. A
  name that is not an auditor of your customer group, unknown or not, gets
  one answer: "There is no auditor with this name in your customer group."
- **Case:** a label you and the auditor see, such as *Betriebsprüfung
  2021–2023*.
- **Accounts:** one or more of your own accounts, each with **All folders,
  also later ones** or only the folders you tick. A folder created later is
  covered only with all folders. smtp (journal) accounts cannot be granted.
- **Messages from / to:** optional; only mail of these days, in your
  browser's time zone, the last day included.
- **Access from / until:** 28 days from today unless you choose others. The
  access ends after the last day, and at most 365 days after its start or
  after its latest extension.
- Your password and, with 2FA, a code.

![The Auditor access page](/screenshots/auditor-access.png)

The table lists every grant with its accounts, period, validity, state and
the auditor's last access (their newest login, search or read in your Log):

- **Active**, **Not started** or **Expired**;
- **Suspended** while you or the auditor are locked; unlocking restores it;
- **Ended**, with the reason: revoked by you or by the operator, a move of
  you or the auditor to another customer group (the auditor is then no
  longer named), its accounts moved to another owner or deleted, or the
  auditor's login removed. An ended grant stays as history.

**Extend** in a grant's menu moves its last day later, again with your
password and code. What a grant covers cannot be changed: to change it,
revoke it and grant access anew. **Revoke** ends the access at once, also
the auditor's open sessions; it cannot be undone.

- An account that moves to another owner (`./ma account move`) leaves your
  grants; a grant left without accounts ends.
- A removed account stays in its grants until it is deleted completely: the
  auditor can still read its mail inside the grant's period until the purge.
- Admins revoke any grant with `./ma auditor revoke ID` (`./ma auditor list`
  lists them); your list then says "Revoked by the operator".

Granting is also possible with
[`/api/auditor-grants`](../reference/api#auditors).

For the Datenüberlassung (Z3) of the same case, ask the operator for a
[GoBD export](./gobd-export) of the grant: `./ma export --format gobd
--grant ID` exports exactly its accounts, folders and message period, also
before the access starts or after it ended, but not once it is revoked. The
export is in your Log as the operator's, with the grant.

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
