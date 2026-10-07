# Importing mail

Older mail often no longer lives on an IMAP server: Thunderbird's local
folders, Apple Mail exports, Google Takeout archives, or a Maildir from a
previous server. `import` stores it in the archive like a sync would, with
the same deduplication.

```sh
./ma import old-laptop --from /import/thunderbird --format mbox
```

The mail goes into an **import account**, here `old-laptop`. It is created on
the first import and shows up in the web UI like any account, marked
*imported*. Import accounts have no server or password and are never synced;
to add more mail, run `import` again with the same name. They can be renamed,
handed to another user and removed like other accounts.

Importing runs only on the command line. The web server never reads files
from the disk.

## Where the files go

With Docker Compose, put the files into `./import` next to
`docker-compose.yml` (or the directory `IMPORT_DIR` in `.env` names). The
container sees it read-only as `/import`, so `--from` starts with `/import/`.
On Linux the files must be readable by the container user, UID 65532:

```sh
mkdir -p import
cp -r ~/.thunderbird/xyz.default/Mail/Local\ Folders import/thunderbird
chmod -R a+rX import
```

Nothing is ever written into the source. Once the import is done, you can
delete `./import`.

## Sources

| `--format` | `--from` | Folders |
|---|---|---|
| `mbox` | One file, such as `Sent.mbox` or a Google Takeout `All mail Including Spam and Trash.mbox` | One folder, named after the file without `.mbox`. |
| `mbox` | An Apple Mail export (`Name.mbox` bundles, File, Export Mailbox) | One folder per bundle; nested bundles give `Parent/Child`. |
| `mbox` | Thunderbird's local folders (`Inbox`, `Inbox.sbd/Work`, …) | One folder per file: `Inbox`, `Inbox/Work`. Index files (`.msf`) are skipped. |
| `maildir` | A Maildir (with `cur` and `new`), for example from Dovecot | The Maildir itself is `INBOX`; Maildir++ subfolders `.Sent`, `.Archive.2024` become `Sent` and `Archive/2024`. |

`--folder NAME` names the folder of a single-folder source, or puts the
folders of a larger source under `NAME/`. `--dry-run` lists the folders and
how many messages each has, without storing anything:

```sh
./ma import old-laptop --from /import/thunderbird --format mbox --dry-run
```

Read and other marks are kept where the source has them: the `Status` and
`X-Status` headers in mbox files, the `:2,` part of Maildir file names. The
date shown in the archive is the date of the `From ` line in an mbox file, or
the file date in a Maildir.

Compressed files (`.gz`, `.zip`) are refused: unpack them first.

## Duplicates

A message is stored once for all accounts and users, by its exact bytes.
IMAP servers deliver messages with CRLF line endings, while mbox files and
many Maildirs store LF only. So that the copies match, `import` stores a
message whose first line ends in LF with every bare LF turned into CRLF.
Nothing else changes.

Some copies still do not match the IMAP copy of the same mail, because the
program that wrote them added headers:

- Thunderbird adds `X-Mozilla-Status`, `X-Mozilla-Status2` and
  `X-Mozilla-Keys`.
- Google Takeout adds `X-Gmail-Labels` and `X-GM-THRID`.

These messages are stored a second time. The archive keeps these headers on
purpose: removing them would change the bytes of what you imported.

Gmail labels are not turned into folders; a Takeout file becomes one folder.

## Running an import again

Running the same import again adds only what is not in the folder yet, so an
interrupted import (Ctrl-C, a crash) can simply be run again. Every batch of
100 messages is saved at once; the rerun reads the source from the start,
skips what is stored and continues the numbering. Messages that were already
archived from IMAP get a second location in the import account, not a second
file.

Only one import into an account runs at a time. While it runs, the account
cannot be removed, and the web UI shows it as *Importing…*.

## Limits

- Messages larger than `--max-message-size` (default `256MiB`) are skipped
  with a warning; the import then ends as `partial` with exit code 1.
- At most 10,000 folders per import. Folder names must be valid UTF-8
  without control characters, at most 1,000 bytes.
- Supported mbox variants: mboxrd and mboxo (`>From ` lines are unescaped).
  A line counts as the start of a message if it starts with `From `, follows
  an empty line (or starts the file) and contains a time and a four-digit
  year; this keeps most unescaped `From ` lines in bodies inside their
  message. `Content-Length` based mboxcl2 files are not supported.
