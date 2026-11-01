# Accounts and providers

An account is one IMAP mailbox. Its password is stored in PostgreSQL,
encrypted with AES-256-GCM using `MAIL_ARCHIVE_SECRET_KEY`.

Every account belongs to one user, who alone sees it and its mail. Accounts
added in the web UI belong to you; names are unique per user. Everything on
this page is done in the web UI; where a command is shown, the operator runs
it on request (there `--user` picks the owner; see
[CLI](../reference/cli#accounts)).

Two other kinds of account have no server and are never synced:

- An **import account** holds mail from files (mbox, Maildir, `.eml`); see
  [Importing mail](./importing). Only renaming, moving and removing apply.
- An **smtp account** receives a copy of every message from your own mail
  server, so nothing deleted between two syncs is missed, whereas an IMAP
  account is a *snapshot* of the mailbox at each sync; see [Journaling with
  Postfix](./journaling). It can be enabled, disabled, renamed, moved and
  removed. Admins add it (Add account → **SMTP journaling**, or
  `./ma account add-smtp`) and alone change its addresses, because an
  address decides who receives the copies. The operator opens the archive's
  SMTP port for your mail server first. The owner brings in the mail from
  before journaling with **Import existing mail from IMAP**; see
  [Journaling](./journaling#bring-in-the-mail-from-before-journaling).

## Adding an account

**Web UI:** **Accounts** in the navigation → **Add account**. The form is a
page of its own; **Accounts** at its top, **Cancel** and the browser's Back
lead back.

| Field | Notes |
|---|---|
| Name | Shown in the archive and used in commands (`./ma sync --account NAME`). Suggested from the login's domain (`me@pklnx.space` → `pklnx`) or the server until you type your own. Can be changed later. |
| IMAP server | Host name, see [Providers](#providers). |
| Connection security | `TLS` (port 993) for almost every provider. `STARTTLS` uses port 143. `None` is for local tests only. |
| Port | Leave empty for the default of the chosen security. |
| Username, password | Usually the full email address. Gmail and iCloud need an app password. |

The server logs in before it saves anything; if the login fails, the form
shows the server's error and nothing is stored. After saving, the first sync
starts right away.

**Command line** (operator):

```sh
./ma account add personal --host imap.mail.de --username me@mail.de
```

Options: `--port`, `--tls tls|starttls|none`, `--include FOLDER`,
`--exclude FOLDER` (both repeatable), `--password-stdin`, `--skip-check`.
See the [command reference](../reference/cli#accounts).

## Providers

| Provider | IMAP server | Notes |
|---|---|---|
| mail.de | `imap.mail.de` | Regular password. |
| GMX | `imap.gmx.net` | Enable IMAP in the web settings first. |
| WEB.DE | `imap.web.de` | Enable IMAP in the web settings first. |
| Gmail | `imap.gmail.com` | Needs 2-step verification and an [app password](https://myaccount.google.com/apppasswords). |
| iCloud | `imap.mail.me.com` | Needs an [app-specific password](https://support.apple.com/en-us/102654). The username is the full address. |
| Your own server | your host | Any standard IMAP server works. |

All of these use TLS on port 993, the default. Microsoft (Outlook.com,
Microsoft 365) requires OAuth2 for IMAP and is not supported.

### Gmail

Gmail shows labels as IMAP folders, so one message appears in many folders.
Deduplication stores it once, but each copy is still downloaded. Archive only
the folders that contain everything, in the web UI under *Folders* or, by the
operator:

```sh
./ma account add gmail --host imap.gmail.com --username me@gmail.com \
  --include "[Gmail]/All Mail" --include "[Gmail]/Sent Mail"
```

Folder names are localized (`[Gmail]/Alle Nachrichten`);
`./ma account folders gmail` shows the exact names.

## The Accounts page

**Accounts** in the navigation lists your accounts as a table: name, type
(*snapshot (IMAP)*, *complete (SMTP)* or *imported*), state (*failing*,
*disabled*, *receiving*, or a spinner while a sync runs), number of messages
and the last sync, import or received message. A row's actions are in its
menu (**⋮**):

| Account | Row menu |
|---|---|
| IMAP | **Sync**, **Check server**, **Edit**, **Disable** or **Enable**, **Remove** |
| smtp | **Mail server setup**, **Import existing mail from IMAP**, **Edit**, **Disable** or **Enable**, **Remove** |
| Import | **Remove** |

While an account syncs, **Sync**, **Check server** and **Remove** wait; so
does **Import existing mail from IMAP** while an import from IMAP runs.
**Mail server setup** is there for every smtp account, also when the server
has no `MAIL_ARCHIVE_SECRET_KEY` and nothing else can be changed. On a phone
the table keeps name, state and actions; messages and last activity show
under the name.

![The Accounts page: the accounts as a table, with each account's actions in its row menu](/screenshots/accounts.png)

Accounts other users [shared](./users#shared-accounts) with you follow in
the table **Shared with you**: name, owner, type, messages and the owner's
last sync. They have no menu, because only the owner manages them; the name
opens the account's mail.

![Pat's Accounts page: the own account team, and personal, shared by demo, under Shared with you](/screenshots/accounts-shared.png)

### The account's page

The account's name in the table (or **Edit** in its menu) opens its page;
Back, reload and bookmarks work, and **Accounts** at the top leads back. At
the top: the name with its tags, **Sync** and **Check server** (IMAP
accounts) and a menu with **Disable** or **Enable** and **Remove**. Tiles
show the messages, the messages *Only in archive* (a link, when there are
any), the folders archived and the last sync with its last error (smtp: the
last message received; import: the last import).

Then come the sections. IMAP: **Connection** (name, server, security, port,
user name), **Schedule** and **Folders**. smtp: **Connection** (name and
addresses), **Mail server setup** and *Imports from IMAP*. Import: its
folders. Each section has its own **Edit**, which opens its form in place,
with **Save** and **Cancel**; saving one section sends only its own fields
and keeps what you typed in the others. Without `MAIL_ARCHIVE_SECRET_KEY`
the page only shows the account.

The last section, **Shared with** (not for smtp accounts), lists the users
who may read the account's mail, each with the day it was shared and
**Remove**. To share, type the user's exact name and press **Share**; an
unknown name says so. A removed account keeps its list, so shares can still
be ended, but it cannot be shared again. See
[Shared accounts](./users#shared-accounts) for what a shared user can do.

![The page of the account personal: the tiles, then Connection, Schedule, Folders open for editing, and Shared with, with pat](/screenshots/account-detail.png)

## Choosing folders

By default every folder is archived, spam and trash included. When you add
an account in the web UI and the server marks folders as trash or spam, the
form asks before saving: **Save without them** or **Save with all folders**.
It asks before the first sync, because archived mail stays in the archive
even if you deselect its folder later.

To change the selection:

- **Web UI:** on the account's page, **Edit** under *Folders*, then **Save
  folder selection**. The list comes live from the server; checked folders
  are archived, and roles the server reports (Trash, Spam, Sent, …) show
  next to the names. The section lists the archived folders with their
  messages, and the ones left out under *Not archived*.
- **Command line** (operator): `./ma account folders NAME` shows which folders
  will be archived, with their roles, and suggests a command to skip spam and
  trash. `./ma account set-folders NAME --exclude Spam --exclude Trash`
  replaces the selection.

## Changing an account

**Edit** under *Connection* on the account's page opens the form for name,
server, port, security, user name and password; **Save** stores them. Leave
the password empty to keep it. Any change to the connection is checked with a
real login first. The operator can set a password on the command line with
`./ma account set-password NAME`.

### Schedule

**Edit** under *Schedule* sets how often the web server syncs the account:
the server's default, every 15 minutes to every day, or **Not on the
schedule**; **Save schedule** stores it. The operator, on the command line:

```sh
./ma account set-schedule work --sync-interval 30m
./ma account set-schedule work --sync-interval manual
./ma account set-schedule work --sync-interval default
```

See [The schedule](./syncing#per-account) for the server's minimum and what
**Not on the schedule** does.

### Renaming

Change the name under *Connection*; the page stays open under the new name,
with whatever is open in its other sections. Or, by the operator:

```sh
./ma account rename postmaster@pklnx.space pklnx
```

The archived mail moves with the account. The stored password is encrypted
again for the new name, so renaming is refused while the account syncs; try
again afterwards. Names of removed accounts stay taken, and removed accounts
cannot be renamed.

## Disabling and removing

**Disable** keeps everything and only stops the automatic sync; a sync
started by hand (button or `./ma sync --account NAME`) still runs. To keep
only the web server's schedule away from an account, use **Not on the
schedule**; see the [comparison](./syncing#per-account).

**Disable** and **Remove** are in the row menu and on the account's page;
both ask first.

**Remove** keeps the archived mail: it stays searchable and still shows the
account and folder it came from, with the account marked *removed* in the
folder column. Removed accounts are folded under *Removed accounts* at the
end of the Accounts page, in a table of their own; each has a page with its
tiles. The stored password is deleted and the account is never synced again.
This cannot be undone, and the name stays taken: a new account needs a
different name, or the removed account is deleted completely (below). An
account without archived mail is deleted completely right away.

The operator's commands for both:

```sh
./ma account disable NAME
./ma account remove NAME
```

### Deleting a removed account completely

A removed account keeps its mail for ever unless an admin deletes it
completely. This is for accounts that should never have held mail, for
example an import into the wrong account, whose mail is (also) somewhere
else. The operator, on the command line:

```sh
./ma account purge NAME --dry-run      # what would happen
./ma account purge NAME                # asks for the number of messages to delete
./ma account purge NAME --count 42     # the same, for scripts
```

In the web UI, **Delete completely…** is in the row menu of an admin's own
accounts under *Removed accounts* (a click on the heading unfolds them) and
in the menu of the account's page. It opens the same preview in a dialog;
**Delete completely** is enabled only once the number of messages to delete
is typed, exactly. When something is in the way, the dialog names the rules
and holds and offers no button: remove the rules and release the holds on
the **Retention** page first. The web server then deletes in the background.
The row and the account's page show *Being deleted completely* with the
messages deleted so far, also for a purge confirmed elsewhere (another
browser tab, the command line), and the account disappears when it is done.
Other users' removed accounts are deleted by the operator with
`./ma account purge NAME --user USER`.

![Deleting the removed account old-provider completely: the preview and the field for the number of messages](/screenshots/account-purge.png)

A purge is only possible when:

- the account is **removed** (`account remove` first);
- **no retention rule** covers it or one of its folders. Remove the rules
  first, on the **Retention** page or by the operator with
  `./ma retention rule remove ID`, deliberately: a protected account is never
  deleted by accident;
- **no legal hold** names it, also a released one (its record keeps the
  account), and no active hold covers any of its mail;
- it is not being synced, filled or imported into, and no other purge is
  open.

The command names every rule and hold in the way. Otherwise it shows how
many locations are removed, how many messages are deleted because this
account has their last location, and how many are kept because they are also
in other accounts (with owner and account), then asks for the number of
messages to delete.

- A message **also in another account**, of any user, stays there unchanged,
  file included.
- A message whose **last location** is in this account is deleted like a
  [retention deletion](./retention): a deletion record (hash, size, dates; no
  subject, sender or recipients), a deletion entry for the next signed
  [attestation](./tamper-evidence), and its file stays for
  `MAIL_ARCHIVE_RETENTION_FILE_GRACE` so that restoring the last dump still
  works.
- Then the folders and the account are deleted, with its fills, imports,
  smtp addresses and sync history. The name is free again.

The [audit log](./audit-log) records the confirmation (*Account deletion
confirmed*), each batch and the end (*Finished deleting account*), with the
account, owner, locations removed and messages deleted and kept.

The purge runs in batches of 500 messages. An interrupted purge (Ctrl-C, a
crash) leaves a consistent removed account and resumes with the same
command, without asking again, or when the web server starts (so a purge
confirmed in the web UI needs nobody to resume it). Meanwhile
`account list` shows *removed (being deleted)*, the web UI *Being deleted
completely*.

A purge is **cancelled** when a retention rule or a legal hold for the account
appears while it runs, or when it would delete more messages than confirmed.
The row returns to its normal state (**Delete completely…** in its menu) and,
if the page was open meanwhile, says that the deletion was cancelled. The audit
log has the reason (*Cancelled account deletion*, with a note such as
"retention rule #12 added"), and a new preview names the rule or hold now in
the way. The operator cancels an open purge by hand with
`./ma account purge NAME --cancel` (the web UI has no cancel button), for
example when the account was moved to another user after the confirmation.
What a cancelled purge already deleted stays deleted, with its records; the
account stays removed with the rest of its mail, consistent and searchable.
To go on, remove the reason and run `account purge` again (or **Delete
completely…**): it shows a new preview and asks for the new number.
