# Journaling with Postfix

An IMAP account is a **snapshot**: a sync archives what is in the mailbox at
that moment. A message that arrives and is deleted between two syncs never
reaches the archive, and whoever controls the mailbox decides what the
archive sees.

An **smtp account** is **complete**: your own mail server sends the archive a
copy of every message as it passes through, over SMTP. This is how
[piler](https://www.mailpiler.org) works, and it needs control over the mail
server. Hosted mailboxes (GMX, mail.de, Gmail) cannot do it.

Journaling has two sides. The operator turns the archive's SMTP server on,
allows your mail server's address and creates the smtp account
([Set up the archive](#set-up-the-archive)); you, or whoever runs your mail
server, configure Postfix ([Set up Postfix](#set-up-postfix)). Agree the
archive's SMTP address and port with your operator first.

```text
 internet ──► Postfix ──► mailboxes (IMAP)
                │
                └── copy of every message ──► mail-archive :2525 ──► smtp account
```

The archive stores each copy with its **envelope**: the sender, the
recipients the server delivered to (BCC recipients included), the time, the
client address and its HELO name. The message view shows it, and the `to:`
filter finds envelope recipients, so a search for a BCC recipient finds the
message. Only the owner of the smtp account sees its envelopes.

![A message received over SMTP: the envelope lists counsel@lawfirm.example, a BCC recipient the headers do not show](/screenshots/envelope.png)

The accounts page marks each account *snapshot (IMAP)* or *complete (SMTP)*,
and shows for an smtp account its addresses and when it last received a
message.

## Set up the archive

1. **The operator** turns the SMTP server on in the instance's `.env` and
   restarts the web server:

   ```sh
   MAIL_ARCHIVE_SMTP_LISTEN=:2525
   MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS=172.16.0.0/12
   MAIL_ARCHIVE_SMTP_MAX_SIZE=50M
   ```

   ```sh
   docker compose up -d web
   ```

   The log shows `smtp on` with the address, the allowed networks and the
   size limit. Compose publishes the port on `127.0.0.1:2525`; `SMTP_BIND` and
   `SMTP_PORT` change that. See [Configuration](../reference/configuration#smtp).

2. **The operator**, as an admin, creates the account and the address it
   receives for. In the web UI: **Accounts → Add account → SMTP
   journaling**, with a name and the address. On the command line:

   ```sh
   ./ma account add-smtp journal --address archive@archive.local
   ```

   The web UI then shows the Postfix lines below, filled in with the
   address; the account's page repeats them under **Mail server setup**
   (also in its row menu on the Accounts page). It only routes a domain that
   looks like the archive's alone (`archive.local`, or a name ending in
   `.local`, `.internal`, `.test` or `.invalid`). For an address like
   `journal@example.com` it warns and uses `archive@archive.local`, because a
   transport for `example.com` would send all of that domain's mail to the
   archive. **Edit** under *Connection* on the account's page changes the
   addresses (admins only).

   The address is only a name for the archive: `archive.local` needs no DNS
   and no MX record, because Postfix delivers to it with a transport (below).
   An address carries no `+`; the part after `+` is reserved for the original
   recipient. More addresses: **Edit** under *Connection*, or
   `account smtp-addresses journal --add ADDRESS`.

### Who may connect

The archive accepts connections only from `MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS`
and answers everyone else with `554`. Your mail server connects from its own
address: tell the operator exactly that one, and tell them when it changes.

For the operator: a Postfix **on the archive's host** reaches the published
port from the gateway of the Compose network, an address like `172.18.0.1`,
not from `127.0.0.1` or its own address. Find it with:

```sh
docker network inspect "$(basename "$PWD")_default" --format '{{(index .IPAM.Config 0).Gateway}}'
```

and allow it, or allow `172.16.0.0/12`, Docker's default range. A Postfix on
another machine connects from its own address; allow exactly that one
(`SMTP_BIND` publishes the port for it).

STARTTLS is on when `MAIL_ARCHIVE_SMTP_TLS_CERT` and `MAIL_ARCHIVE_SMTP_TLS_KEY`
name a certificate and key. There is no SMTP login: keep the port off the
internet. The allowlist is the only gate.

## Set up Postfix

This part is yours, on your mail server. Postfix delivers the copies over a
transport, so the archive's domain needs no DNS. `[127.0.0.1]:2525` is the
address when Postfix runs on the archive's host; otherwise put the address
and port your operator gave you:

```text
# /etc/postfix/transport
archive.local   smtp:[127.0.0.1]:2525
```

```sh
postmap /etc/postfix/transport
postconf -e 'transport_maps = hash:/etc/postfix/transport'
```

Then pick one of two ways to copy every message.

### Variant A: always_bcc

```sh
postconf -e 'always_bcc = archive@archive.local'
postfix reload
```

Simple, but the copy carries only `archive@archive.local` as its envelope
recipient: the archive does not learn who the message was delivered to, so
**BCC recipients are lost**. The `To` and `Cc` headers are in the message as
always.

### Variant B: recipient_bcc_maps (recommended)

Postfix looks up `recipient_bcc_maps` for every envelope recipient of every
message it accepts, incoming and outgoing. A regular expression encodes the
recipient in the copy's address, and the archive decodes it again:
`archive+alice=example.com@archive.local` is stored as the recipient
`alice@example.com`.

```text
# /etc/postfix/archive_bcc (regexp)
if !/@archive\.local$/
/^(.+)@(.+)$/   archive+${1}=${2}@archive.local
endif
```

```sh
postconf -e 'recipient_bcc_maps = regexp:/etc/postfix/archive_bcc'
postfix reload
```

Postfix sends one copy with one recipient per original recipient. Sent mail
is covered too: its recipients are envelope recipients when it enters
Postfix.

- **No `sender_bcc_maps`** with the same expression: it would add a second
  copy whose address encodes the *sender*, and the archive would store the
  sender as a recipient.
- The `if !/@archive\.local$/` line keeps Postfix from copying the copies.
- With a content filter (Amavis as `content_filter`), address mappings must
  run on one side of the filter only. Postfix's FILTER_README sets
  `receive_override_options = no_address_mappings` in `main.cf`, before the
  filter, and leaves it off on the reinjection port. Otherwise the map runs
  twice and the archive gets two copies with different bytes, stored as two
  messages.

A copy for many recipients may arrive in several transactions: Postfix sends
at most `smtp_destination_recipient_limit` (50) recipients at a time, and
retries only the recipients that failed. The archive merges them into one
stored message with all recipients.

## When a copy is refused

Postfix adds BCC copies with `NOTIFY=NONE`. If the archive answers a copy with
a **permanent error (`5xx`)**, Postfix drops it: no bounce, no notice, only a
line in the Postfix log. The archive therefore refuses permanently only what
cannot work, and logs every refusal at error level with client address,
sender, recipients and reason (never the content):

| Answer | When | Result |
|---|---|---|
| `554` | The client is not in the allowlist. | Postfix drops the copy. |
| `550` | No smtp account receives for the address. | Postfix drops the copy. |
| `552` | The message is larger than `MAIL_ARCHIVE_SMTP_MAX_SIZE`. | Postfix drops the copy. Postfix usually checks the size the archive announces itself and drops the copy without connecting: then only the Postfix log has it (`exceeds size limit`). |
| `451` | The account is disabled, or storing failed. | Postfix keeps the copy queued and retries. |
| `421` | More than 10 connections at once. | Postfix retries later. |

So:

- `MAIL_ARCHIVE_SMTP_MAX_SIZE` must be at least Postfix's
  `message_size_limit` (`postconf message_size_limit`; 10 MB by default).
- Disabling an smtp account keeps its copies in the Postfix queue for
  `maximal_queue_lifetime`, 5 days by default. Then Postfix drops them:
  **disabling an account for longer loses mail.**
- A message is acknowledged only after it is stored for every account it is
  addressed to. An acknowledged message is in the archive.

Postfix opens up to `smtp_destination_concurrency_limit` (20) connections to
one destination; the archive takes 10 and answers more with `421`. Set a
lower limit for the archive's transport to keep the log clean:

```text
# /etc/postfix/master.cf
archive   unix  -  -  n  -  10  smtp
```

and use `archive:[127.0.0.1]:2525` instead of `smtp:[127.0.0.1]:2525` in the
transport map.

## Monitoring

The archive cannot tell a broken setup from a quiet mailbox, so watch your
mail server. Two checks cover both kinds of failure:

- **The queue.** Copies for the archive that wait in the queue mean `4xx`
  answers, an archive that is down or a disabled account:

  ```sh
  mailq | grep -c archive.local
  qshape deferred | head
  ```

- **The log.** A refused copy (`5xx`) never reaches the queue. It shows up
  once in the log, as `status=bounced` for the archive's address:

  ```sh
  grep 'to=<archive' /var/log/mail.log | grep 'status=bounced'
  ```

On the archive's side, the Accounts page shows for each smtp account when it
last received a message (the operator sees the same in `./ma status`), and
the web server logs every refusal at error level, in the log the operator
reads.

## Bring in the mail from before journaling

Journaling covers mail from the moment it is switched on. What the mailbox
already holds comes into the same account with a one-time **fill** from IMAP.
A fill can also [keep importing](#keep-importing-recurring-fill) what shows
up in the mailbox later.

In the web UI, **Import existing mail from IMAP** is in the smtp account's
row menu on the Accounts page and under *Imports from IMAP* on the account's
page (also offered right after creating the account). It opens the import
form as a page of its own. The form takes the IMAP server and login, checks
the login and lists the folders; trash and spam folders start unticked. The
cut-off is optional. The form shows the first message the account received
over SMTP, as a check that journaling works.

![The import form of the smtp account "journal": IMAP server, login and cut-off](/screenshots/fill-form.png)

On the command line:

```sh
./ma account fill journal --host imap.example.com --username alice@example.com --exclude Junk
```

The password is asked for (or read with `--password-stdin`) and kept,
encrypted, only while the fill runs. The command prints the first message the
account received over SMTP and then the progress per folder. It is not a
sync: nothing is fetched again afterwards, unless the fill
[keeps running](#keep-importing-recurring-fill), and the account stays
*complete (SMTP)*.

**Where the mail goes.** An smtp account has one folder, `JOURNAL`. The fill
writes into it too, whatever the IMAP folder: a message filed in `INBOX` and
in `Clients/Miller` is stored once, and the archive's folders do not follow
the mailbox's. What arrived over SMTP has an envelope; imported messages have
none, so BCC recipients are unknown for them. The message view says so in an
**Origin** section, with the mailbox the message was imported from, such as
"Imported from IMAP: alice@example.com".

![A message the fill copied: found in the journal's JOURNAL folder and in the work account; the origin section names the mailbox it was imported from](/screenshots/fill-origin.png)

Fills started before this layout wrote into folders of their own,
`PREFIX/<IMAP folder>` (such as `alice@example.com/INBOX`). Those folders
stay, and such a fill, resumed, finishes there. `--prefix` (`folderPrefix` in
the API) is no longer accepted. An old fill with the prefix `JOURNAL` has
folders such as `JOURNAL/INBOX`, which the folder column shows below the
journal folder; that is only how they are displayed.

**Which messages.** The journal copy and the mailbox copy of a message differ
in their `Received:` lines, so the archive cannot match them by content. The
fill decides by the time the mailbox received a message (its INTERNALDATE)
and by its Message-ID. `C` is the cut-off, now by default; `--before` sets an
earlier one (`2026-06-01` for midnight UTC, or RFC 3339 with an offset).

| INTERNALDATE | Fill |
|---|---|
| before `C` plus one hour | imported, unless the account received a message with the same Message-ID over SMTP |
| from `C` plus one hour on | left to journaling |

- The hour covers clocks that differ and mail that was delivered late.
- The Message-ID check runs on every message, so a test message, copies the
  archive refused or a late start of journaling cause neither gaps nor two
  copies.
- **Still stored twice:** a message without a `Message-ID` header that was
  journaled too. It is imported anyway; a duplicate is better than a gap.
- **Not stored:** mail that arrives after an earlier `--before` while
  journaling is not set up yet. With the default cut-off, nothing.
- Trash and spam folders are refused unless excluded, or confirmed with
  `--confirm-folders`.

**Several mailboxes** journaled into one account get one fill each, one after
another, all into `JOURNAL`; the origin section tells which mailbox a message
came from.

**Interrupted or failed.** In the web UI, **Resume import** on the account's
page continues a failed, partly failed or cancelled fill (see below). On the
command line, Ctrl-C stops the fill; `./ma account fill journal --resume`
continues it where it stopped (the password is asked again), and so does a
fill whose folders partly failed. `--cancel` stops a fill that another
process runs. `./ma account fill journal` lists the fills with their counts:
scanned, imported, after the cut-off, journaled, already there and deleted by
retention; the account's page shows the same.

**From the web server.** A fill started in the web UI (or the
[API](../reference/api#fills)) runs in the web server's background, one at a
time and apart from the scheduled syncs. The account's page shows its
progress per folder under *Imports from IMAP*, with **Cancel import**; after
a failed, partly failed or cancelled fill it shows the error and **Resume
import**, which asks for the password again. The folder column marks the
account as importing meanwhile. A fill the web server was running when it
stopped keeps its encrypted password and continues at the next start. So
does a fill whose command-line process died without Ctrl-C: the web server
picks it up within a minute.

**From files (piler, mbox, Maildir).** Mail that is no longer in an IMAP
mailbox, such as a [piler](./importing#piler-into-a-journaled-mailbox)
export, goes into the same account with `import --journal`, which the
operator runs with the files handed over, with the rules of a fill: what
the account received over SMTP is left out by its Message-ID, and the origin
section says "Imported from files: LABEL". See
[Into an smtp account](./importing#into-an-smtp-account).

```sh
./ma import journal --journal --format eml --from /import/piler-alice --label piler
```

**Retention.** A rule on the folder `JOURNAL`, or an account-wide one, covers
journaled and imported mail alike. Folders of older fills are covered by an
account-wide rule or by rules of their own. See
[Retention](./retention#smtp-accounts-with-a-fill).

### Keep importing (recurring fill)

Some mail never passes the journal: mail of an old address at another
provider that the user later moves into the journaled mailbox, mail sent
from another account and filed there, or copies lost during an outage. A
one-time fill has run by then. A **recurring fill** goes on: tick **Keep
importing new mail** in the import form (unticked by default) and choose how
often, or on the command line:

```sh
./ma account fill journal --host imap.example.com --username alice@example.com --exclude Junk \
  --keep-running --interval 12h
```

The first run happens right away (on the command line in the foreground);
afterwards the web server runs it on its schedule.

A recurring fill has no cut-off: a message is imported once it is two hours
old and the account did not receive its Message-ID over SMTP. A younger
message waits for the next run, because its journal copy may still be in the
mail server's queue. How the fill decides in detail, what it does after a
restart of the web server, with moved, deleted and new folders, its interval
and counts, health and alerts, and retention are in the reference under
[`account fill`](../reference/cli#account-fill).

**Stop importing** on the account's page, or
`./ma account fill journal --cancel`, stops the fill and deletes its password;
what it imported stays. **Resume import**, or `--resume`, asks for the
password and makes it keep running again; a fill that fails at login gets its
new password the same way. An ended one-time fill has **Keep importing new
mail** on the account's page, or `./ma account fill journal --keep-running`,
which turns it into a recurring one. Change how often it runs on the
account's page, or with `./ma account fill journal --interval 24h`.

## Two copies of one message

A message that is also in an IMAP account is usually stored twice: the copy
in the mailbox carries one more `Received:` line, added by the server that
delivered it there, so its bytes differ. Search finds both. The same is true
for mail imported from piler into an import account. A
[fill](#bring-in-the-mail-from-before-journaling) and
[`import --journal`](./importing#into-an-smtp-account) avoid this inside an
smtp account by checking the Message-ID.
