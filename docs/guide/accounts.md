# Accounts and providers

An account is one IMAP mailbox. Its password is stored in PostgreSQL,
encrypted with AES-256-GCM using `MAIL_ARCHIVE_SECRET_KEY`.

Every account belongs to one user, who alone sees it and the mail found in
it. Accounts added in the web UI belong to you; names only need to be unique
among your own accounts. On the command line, `--user` picks the owner; see
[CLI](../reference/cli#accounts).

Mail from files (mbox, Maildir) goes into an import account instead; see
[Importing mail](./importing). Import accounts have no server and are never
synced; only renaming, moving and removing apply to them.

An IMAP account is a *snapshot*: it archives what is in the mailbox at each
sync. If you run the mail server yourself, an **smtp account** receives a copy
of every message as it passes through, so nothing deleted between two syncs
is missed; see [Journaling with Postfix](./journaling). smtp accounts have no
server and are never synced; they can be enabled, disabled, renamed, moved
and removed. Admins add them in the web UI (Add account → **SMTP journaling**)
or with `./ma account add-smtp`; only admins change their addresses, because
an address decides who receives the copies of every message. The owner can
bring in the mail from before journaling with **Import existing mail from
IMAP** on the card; see [Journaling](./journaling#bring-in-the-mail-from-before-journaling).

## Adding an account

**Web UI:** Manage accounts → Add account. Fill in:

| Field | Notes |
|---|---|
| Name | Shown in the archive and used in commands (`./ma sync --account NAME`). Suggested from the login's domain (`me@pklnx.space` → `pklnx`) or the server until you type your own. Can be changed later. |
| IMAP server | Host name, see [Providers](#providers). |
| Connection security | `TLS` (port 993) for almost every provider. `STARTTLS` uses port 143. `None` is for local tests only. |
| Port | Leave empty for the default of the chosen security. |
| Username, password | Usually the full email address. Gmail and iCloud need an app password. |

The server logs in before it saves anything. If the login fails, the form
shows the server's error message and nothing is stored. After saving, the
first sync starts right away.

**Command line:**

```sh
./ma account add personal --host imap.mail.de --username me@mail.de
```

Options: `--port`, `--tls tls|starttls|none`, `--include FOLDER`,
`--exclude FOLDER` (both repeatable), `--password-stdin`, `--skip-check`.
See the [command reference](../reference/cli).

## Providers

| Provider | IMAP server | Notes |
|---|---|---|
| mail.de | `imap.mail.de` | Regular password. |
| GMX | `imap.gmx.net` | Enable IMAP in the web settings first. |
| WEB.DE | `imap.web.de` | Enable IMAP in the web settings first. |
| Gmail | `imap.gmail.com` | Needs 2-step verification and an [app password](https://myaccount.google.com/apppasswords). |
| iCloud | `imap.mail.me.com` | Needs an [app-specific password](https://support.apple.com/en-us/102654). The username is the full address. |
| Your own server | your host | Any standard IMAP server works. |

All of these use TLS on port 993, the default.

Microsoft (Outlook.com, Microsoft 365) requires OAuth2 for IMAP and is not
supported.

### Gmail

Gmail shows labels as IMAP folders, so one message appears in many folders.
Deduplication stores it once, but each copy is still downloaded. Archive only
the folders that contain everything:

```sh
./ma account add gmail --host imap.gmail.com --username me@gmail.com \
  --include "[Gmail]/All Mail" --include "[Gmail]/Sent Mail"
```

Folder names are localized, for example `[Gmail]/Alle Nachrichten`. Run
`./ma account folders gmail` to see the exact names.

## Choosing folders

By default every folder is archived, spam and trash included. When you add
an account in the web UI and the server marks folders as trash or spam, the
form asks before saving: **Save without them** or **Save with all folders**.
The question comes before the first sync because mail that has been
archived stays in the archive even if you deselect its folder later.

To change the selection later:

- **Web UI:** Manage accounts → Edit → Choose folders. The list comes live
  from the server; checked folders are archived. Roles reported by the server
  (Trash, Spam, Sent, …) are shown next to the names.
- **Command line:** `./ma account folders NAME` shows which folders will be
  archived and their roles, and suggests a ready-to-run command to skip spam
  and trash. `./ma account set-folders NAME --exclude Spam --exclude Trash`
  replaces the selection.

![Editing an account and choosing folders](/screenshots/account-edit.png)

Mail that was already archived from a folder stays in the archive when you
deselect the folder later.

## Changing an account

In the web UI, **Edit** changes the name, server, port, security, user name
and password. Leave the password empty to keep the current one. Any change to the
connection is checked with a real login before it is saved. On the command
line, use `./ma account set-password NAME`.

### Schedule

Under **Edit**, **Schedule** sets how often the web server syncs the
account: the server's default, every 15 minutes to every day, or **Not on
the schedule**. On the command line:

```sh
./ma account set-schedule work --sync-interval 30m
./ma account set-schedule work --sync-interval manual
./ma account set-schedule work --sync-interval default
```

See [The schedule](./syncing#per-account) for the server's minimum and what
**Not on the schedule** does.

### Renaming

Change the name in the edit form, or on the command line:

```sh
./ma account rename postmaster@pklnx.space pklnx
```

The archived mail moves with the account. The stored password is encrypted
again for the new name, so renaming is refused while the account is being
synced; try again when the sync has finished. Names of removed accounts stay
taken, and removed accounts cannot be renamed.

## Disabling and removing

**Disable** keeps everything and only stops the automatic sync. A sync started
by hand (button or `./ma sync --account NAME`) still runs. To keep only the
web server's schedule away from an account, use **Not on the schedule**
instead; see the [comparison](./syncing#per-account).

**Remove** keeps the archived mail. The mail stays searchable and still shows
the account and folder it came from; in the sidebar the account is marked
*removed*. The stored password is deleted and the account is never synced
again. This cannot be undone, and the name stays taken: a new account needs a
different name, or the removed account is deleted completely (below). An
account without any archived mail is deleted completely right away.

```sh
./ma account disable NAME
./ma account remove NAME
```

### Deleting a removed account completely

A removed account keeps its archived mail for ever unless an admin deletes
it completely. This is meant for accounts that should never have held mail,
for example an import into the wrong account, whose mail is (also) somewhere
else:

```sh
./ma account purge NAME --dry-run      # what would happen
./ma account purge NAME                # asks for the number of messages to delete
./ma account purge NAME --count 42     # the same, for scripts
```

In the web UI, an admin finds **Delete completely…** on each of their own
cards under *Removed accounts* (**Manage accounts**). It opens the same
preview in the card; the button **Delete completely** is enabled only once
the number of messages to delete is typed. When something is in the way,
the card names the rules and holds instead and offers no button: remove the
rules and release the holds on the **Retention** page first. After the
confirmation the web server deletes in the background; the card shows
*Being deleted completely* with the messages deleted so far and disappears
when it is done. A purge confirmed elsewhere (another browser tab, the
command line) shows the same way, without the form. Other users' removed accounts are deleted from the command line with
`./ma account purge NAME --user USER`.

![Deleting the removed account old-provider completely: the preview and the field for the number of messages](/screenshots/account-purge.png)

It is only possible when:

- the account is **removed** (`account remove` first);
- **no retention rule** covers it or one of its folders. Remove the rules
  first with `./ma retention rule remove ID`, deliberately: a protected
  account is never deleted by accident;
- **no legal hold** names it, also a released one (its record keeps the
  account), and no active hold covers any of its mail;
- it is not being synced, filled or imported into, and no other purge is
  open.

The command names every rule and hold that is in the way. Otherwise it
first shows what happens: how many locations are removed, how many messages
are deleted because this account has their last location, and how many
are kept because they are also in other accounts (with the owner and
account). Then it asks for the number of messages to delete.

- A message that is **also in another account**, of any user, stays there
  unchanged, file included.
- A message whose **last location** is in this account is deleted like a
  [retention deletion](./retention): a deletion record (hash, size, dates; no
  subject, sender or recipients), a deletion entry for the next signed
  [attestation](./tamper-evidence), and its file stays for
  `MAIL_ARCHIVE_RETENTION_FILE_GRACE` so that restoring the last dump still
  works.
- Then the folders and the account itself are deleted, with its fills,
  imports, smtp addresses and sync history. The name is free again.

The [audit log](./audit-log) records the confirmation (*Account deletion
confirmed*), each batch and the end (*Finished deleting account*), with the account, owner, locations
removed and messages deleted and kept.

The purge runs in batches of 500 messages. An interrupted purge (Ctrl-C, a
crash) leaves a consistent removed account and resumes with the same
command, without asking again, or when the web server starts. `account
list` shows the account as *removed (being deleted)* meanwhile, the web UI
*Being deleted completely*.

A purge is **cancelled** when a retention rule or a legal hold for the
account appears while it runs, or when it would delete more messages than
confirmed. In the web UI the card then returns to its normal state
(**Delete completely…**); if the page was open meanwhile, the card also
says that the deletion was cancelled. The reason is in the audit log
(*Cancelled account deletion*, with a note such as "retention rule #12
added"), and a new preview names the rule or hold that is now in the way.
`./ma account purge NAME --cancel` cancels an open purge by hand (there is
no cancel button in the web UI), for example when the account was moved to another user after the
confirmation. What a cancelled purge already deleted stays deleted (with its
records); the account stays removed with the rest of its mail, consistent
and searchable. To go on, remove the reason and run `account purge` again
(or **Delete completely…**): it shows a new preview and asks for the new
number.
