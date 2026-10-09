# Importing mail

Older mail often no longer lives on an IMAP server: Thunderbird's local
folders, Apple Mail exports, Google Takeout archives, a Maildir from a
previous server, or the `.eml` export of another archive such as
[piler](#from-piler). `import` stores it in the archive like a sync would, with
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
| `eml` | A directory of `.eml` files, one message each, for example a [piler](#from-piler) export | One folder per directory that holds `.eml` files, named by its path: `2024/Q1`. Files directly in `--from` need `--folder`. |

`--folder NAME` names the folder of a single-folder source, or puts the
folders of a larger source under `NAME/`. `--dry-run` lists the folders and
how many messages each has, without storing anything:

```sh
./ma import old-laptop --from /import/thunderbird --format mbox --dry-run
```

Read and other marks are kept where the source has them: the `Status` and
`X-Status` headers in mbox files, the `:2,` part of Maildir file names. The
date shown in the archive is the date of the `From ` line in an mbox file,
the file date in a Maildir, and the `Date` header of an `.eml` file (its file
date if it has none).

With `--format eml`, **only files ending in `.eml`** (in any case) are read.
Other files, hidden files and directories (a leading `.`, which includes the
`._name.eml` files macOS leaves on other disks) and symbolic links are
skipped. `.eml` files carry no marks, so their messages are imported unread.
Within a directory, files are read in byte order of their names, so
`10.eml` comes before `2.eml`; the order only decides the numbering inside
the archive. An empty `.eml` file is skipped with a warning and makes the
import `partial`.

Compressed files (`.gz`, `.zip`) are refused: unpack them first. With
`--format eml`, a ZIP file inside the directory is skipped, and the import
warns how many there were.

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

The piler copy of a mail differs from the IMAP copy as well: piler stores
the message as it arrived over SMTP, while the copy in your mailbox usually
carries one more `Received:` line from your mail server. It is stored a
second time too.

Gmail labels are not turned into folders; a Takeout file becomes one folder.

## From piler

[piler](https://www.mailpiler.org) exports messages only as `.eml` files.
`pilerexport` writes them into the directory it runs in, which the piler
user must be able to write to; `-A` (`--all`) exports the whole archive,
and `--zip FILE` writes a ZIP file instead. Check `pilerexport --help` for
the options of your version.

On the piler server:

```sh
mkdir /var/tmp/piler-export && chown piler /var/tmp/piler-export
cd /var/tmp/piler-export
pilerexport -A
```

Or, as a ZIP file that is easier to copy:

```sh
cd /var/tmp && pilerexport -A --zip piler-export.zip
unzip -d piler-export piler-export.zip
```

Copy the directory into `./import` and import it, with a folder name for
the files:

```sh
./ma import piler --from /import/piler-export --format eml --folder piler --dry-run
./ma import piler --from /import/piler-export --format eml --folder piler
./ma verify
```

`pilerexport` sometimes reports `verification FAILED` and leaves an empty
file; the import skips such files with a warning (`message skipped: empty
file`) and ends `partial`. Export those messages again, or check them in
piler. `verify` checks the stored files afterwards.

piler and IMAP copies of the same mail are usually stored twice; see
[Duplicates](#duplicates). piler keeps no folders or read marks, so all its
mail lands in one folder, unread.

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
