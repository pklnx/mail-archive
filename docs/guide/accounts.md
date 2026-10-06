# Accounts and providers

An account is one IMAP mailbox. Its password is stored in PostgreSQL,
encrypted with AES-256-GCM using `MAIL_ARCHIVE_SECRET_KEY`.

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

By default every folder is archived, spam and trash included. To skip some:

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
by hand (button or `./ma sync --account NAME`) still runs.

**Remove** keeps the archived mail. The mail stays searchable and still shows
the account and folder it came from; in the sidebar the account is marked
*removed*. The stored password is deleted and the account is never synced
again. This cannot be undone, and the name stays taken: a new account needs a
different name. An account without any archived mail is deleted completely.

```sh
./ma account disable NAME
./ma account remove NAME
```
