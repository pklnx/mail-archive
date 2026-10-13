# Journaling with Postfix

An IMAP account is a **snapshot**: a sync archives what is in the mailbox at
that moment. A message that arrives and is deleted between two syncs never
reaches the archive, and whoever controls the mailbox decides what the archive
sees.

An **smtp account** is **complete**: your mail server sends the archive a copy
of every message as it passes through, over SMTP. This is how
[piler](https://www.mailpiler.org) works, and it needs control over the mail
server. Hosted mailboxes (GMX, mail.de, Gmail) cannot do it.

```text
 internet ──► Postfix ──► mailboxes (IMAP)
                │
                └── copy of every message ──► mail-archive :2525 ──► smtp account
```

The archive stores each copy with its **envelope**: the sender, the recipients
the server delivered to (BCC recipients included), the time, the client
address and its HELO name. The message view shows it, and the `to:` filter
finds envelope recipients, so a search for a BCC recipient finds the message.
Only the owner of the smtp account sees its envelopes.

![A message received over SMTP: the envelope lists counsel@lawfirm.example, a BCC recipient the headers do not show](/screenshots/envelope.png)

The accounts page marks each account *snapshot (IMAP)* or *complete (SMTP)*,
and shows for an smtp account its addresses and when it last received a
message.

## Set up the archive

1. Turn the SMTP server on in `.env` and restart the web server:

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

2. Create the account and the address it receives for. In the web UI, as
   an admin: **Manage accounts → Add account → SMTP journaling**, with a name
   and the address. On the command line:

   ```sh
   ./ma account add-smtp journal --address archive@archive.local
   ```

   After creating the account, the web UI shows the Postfix lines below,
   filled in with its address; **Mail server setup** on the account's card
   shows them again. It only routes a domain that looks like it belongs to
   the archive alone (`archive.local`, or a name ending in `.local`,
   `.internal`, `.test` or `.invalid`): for an address like
   `journal@example.com` it warns and uses `archive@archive.local`, because a
   transport for `example.com` would send all of that domain's mail to the
   archive. **Edit** on the card changes the addresses (admins only).

   The address is only a name for the archive: `archive.local` needs no DNS
   and no MX record, because Postfix delivers to it with a transport (below).
   An address carries no `+`; the part after `+` is reserved for the original
   recipient. `account smtp-addresses journal --add ADDRESS` adds more.

### Who may connect

The archive accepts connections only from `MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS`
and answers everyone else with `554`. With Docker Compose, a Postfix **on the
host** reaches the published port from the gateway of the Compose network, an
address like `172.18.0.1`, not from `127.0.0.1` or its own address. Find it
with:

```sh
docker network inspect "$(basename "$PWD")_default" --format '{{(index .IPAM.Config 0).Gateway}}'
```

and allow it, or allow `172.16.0.0/12`, Docker's default range. A Postfix
on another machine connects from its own address; allow exactly that one.

STARTTLS is on when `MAIL_ARCHIVE_SMTP_TLS_CERT` and `MAIL_ARCHIVE_SMTP_TLS_KEY`
name a certificate and key. There is no SMTP login: keep the port off the
internet. The allowlist is the only gate.

## Set up Postfix

Postfix delivers the copies to the archive over a transport, so the archive's
domain needs no DNS:

```text
# /etc/postfix/transport
archive.local   smtp:[127.0.0.1]:2525
```

```sh
postmap /etc/postfix/transport
postconf -e 'transport_maps = hash:/etc/postfix/transport'
```

Then pick one of the two ways to copy every message.

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

Postfix sends one copy with one recipient per original recipient. Sent mail is
covered too: its recipients are envelope recipients when it enters Postfix.

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

A copy for many recipients may arrive in several transactions: Postfix sends at
most `smtp_destination_recipient_limit` (50) recipients at a time, and retries
only the recipients that failed. The archive merges them into one stored
message with all recipients.

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
  addressed to. A message that was acknowledged is in the archive.

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

The archive cannot tell a broken setup from a quiet mailbox, so watch the mail
server. Two checks cover both kinds of failure:

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

On the archive's side, `./ma status` shows for each smtp account when it last
received a message, and the web server logs every refusal at error level.

## Two copies of one message

A message that is also in an IMAP account is usually stored twice: the copy
in the mailbox carries one more `Received:` line, added by the server that
delivered it there, so its bytes differ. Search finds both. The same is true
for mail imported from piler.
