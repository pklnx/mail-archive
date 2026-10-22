# Security

## What is protected how

| Asset | Protection |
|---|---|
| IMAP passwords | Encrypted in PostgreSQL with AES-256-GCM, bound to the account's internal ID (so a stored password cannot be moved to another account), using `MAIL_ARCHIVE_SECRET_KEY`. API responses never contain them. |
| Archived mail | Plain `.eml` files and PostgreSQL rows, **not encrypted**. Use an encrypted disk. |
| Exports and backups | `export` and `backup` write unencrypted files with mode 0600 in directories with mode 0700. `BACKUP-NOTE.txt` names the secret key but never contains it, and `pg_dump` gets the database password in its environment, not on the command line. |
| Your mail servers | Only read: `EXAMINE`, `BODY.PEEK[]` and, to [reconcile](../guide/syncing#reconcile), `UID FETCH 1:* (UID FLAGS)`. Never `STORE`, `EXPUNGE` or a read-write `SELECT`. |
| The web UI | Login with user name and password, sessions in PostgreSQL, limits on failed logins, and the browser protections below. |
| User passwords | Only stored as Argon2id hashes (64 MiB, 3 passes). |
| TOTP secrets | Encrypted with AES-256-GCM using `MAIL_ARCHIVE_SECRET_KEY`, bound to the user ID. Recovery codes are stored only as keyed HMAC-SHA-256 hashes. |
| Passkeys | Only the public keys are stored; the private keys never leave the user's devices. |
| API keys | Only their SHA-256 and last 4 characters are stored; the key is shown once. A key reaches only the read and sync endpoints of its owner. See [Login](#login). |
| The audit log | Every search, read, export, login and change is recorded; the trigger refuses changes and deletions for every role. See [Audit log](../guide/audit-log) for what it covers and what it cannot stop. |
| Retention and deletion | Nothing is deleted without a [retention rule](./cli#retention) and a reviewed, confirmed [deletion run](./cli#deletion); rules, legal holds and runs are admin-only and recorded in the audit log. Database triggers refuse to delete a location whose period has not ended or that is on hold, and a message row without its deletion record. A message stays while any location of it, in any user's account, has no rule or an unexpired one, or is on legal hold. |
| Removal of archived mail | Daily signed [attestations](./cli#attest) record which messages were archived; see [The attestation key](#the-attestation-key). Tamper-evident, not tamper-proof. |
| The SMTP port | Off by default. When on, only clients in `MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS` may connect; see [The SMTP server](#the-smtp-server). |

## Login

Every API request needs a login or an API key, except the login itself,
`/healthz` and the API description `/api/openapi.json`. The page code (`/`, `/assets/`) is
public; it contains no data. Which route needs what is set in one table
(`internal/web/routes.go`); a route not listed there, or a new one without
an access level, needs a login.

- **Users** are created by admins in the web UI or with `./ma user add NAME`
  (`--admin` for admins). There is no self-registration. Passwords generated
  by the server (new users, resets) have about 99 bits of entropy, are shown
  once and must be replaced by the user at the first login; until then the
  server refuses every other request. Passwords need at least 12 characters; there are no
  other rules, so a long passphrase is fine.
- **Sessions** end after 7 days without use and after 30 days at the latest.
  Logging out, a new password (`./ma user set-password`), locking
  (`./ma user lock`) and removing a user end their sessions at once. The
  browser only gets a random token in an `HttpOnly`, `SameSite=Strict` cookie;
  the database stores its SHA-256, so a database copy contains no usable
  sessions.
- **Two-factor authentication:** TOTP uses 6-digit RFC 6238 codes with a 30-second period and one-step clock tolerance. Administrators must have TOTP enabled and cannot disable it. A user with mandatory TOTP enabled but without setup is restricted to completing setup. TOTP challenges expire after 5 minutes, are single-use, and include a version check so resets win over in-flight logins. Recovery codes are shown once, are one-use, and are stored only as keyed hashes. The PDF and text downloads are made in the browser from the codes on screen; the server does not send the codes again.
- **Passkeys** (WebAuthn) replace password and TOTP at the login. They are
  bound to the host of `MAIL_ARCHIVE_PUBLIC_URL`, so a look-alike site cannot
  use them. Every passkey login needs user verification (Face ID, Touch ID,
  Windows Hello or the security key's PIN). Any authenticator is accepted
  (attestation `none`). A sign count that does not go up rejects the login,
  as it points to a copied authenticator. Adding a passkey needs the
  password, and a TOTP or recovery code if the user has 2FA on. Removing a
  passkey ends the user's other sessions. Admins with mandatory TOTP must
  still set it up: a passkey session is restricted like a password session
  until then. Starting a passkey login needs no session; it is limited to 30
  per minute per address and 1000 open logins in total. Passwords stay: an
  account never depends on passkeys alone.
- **API keys** let scripts read mail (`read`) or start syncs (`sync`), and
  nothing else: logins, the profile, users and account changes stay with
  the session and the command line. Creating a key needs the password and,
  with 2FA on, a code; using it needs nothing else. A key is 256 random bits
  (`ma_` and 52 base32 characters), shown once; the database stores its
  SHA-256 and the last 4 characters, so a database copy contains no usable
  keys. A key never does more than a session of its owner: the owner's role,
  lock and gates (a password to change, 2FA to set up) are checked on every
  request. Locking a user suspends their keys, an admin's password reset
  revokes them (unless the admin keeps them), removing the user deletes
  them. The user's own password change keeps them: scripts should not break
  on a routine change; the profile lists them for revocation. Each key may
  make 600 requests per minute; more than 30 invalid keys per client address
  and minute answer `429`, without touching the password login limits. Keys
  never appear in logs or in the audit log.
- **Failed logins:** after 5 failures for a user name within 15 minutes, the
  name is blocked for 1 minute, then 2, 4, 8 and at most 15 minutes for every
  further failure. 20 failures from one address within 15 minutes block that
  address for 15 minutes. Failed passkey logins count for the address only.
  Wrong names and wrong passwords get the same answer
  and take the same time. The counters live in memory and reset when the
  server restarts.
- Behind a reverse proxy, the server sees the proxy's address for every
  client, so all clients share the address limit; the limit per user name
  still applies.

Each user sees only their own accounts and the mail found in them. A message
that is in two users' accounts is stored once and shown to both, each with
only their own locations. Other users' accounts and messages answer `404`,
also when someone guesses a message ID. Admins see only their own mail too.

## Network access

Docker Compose publishes the web UI on `127.0.0.1`, so only this computer can
reach it. To use it from your home network or a VPN:

1. **Use HTTPS.** Without it, passwords and session cookies cross the network
   in plain text. Put a reverse proxy or `tailscale serve` in front; the
   server then marks the cookie `Secure` (it recognizes `X-Forwarded-Proto:
   https`).
2. **Allow the host name** you type in the browser in
   `MAIL_ARCHIVE_ALLOWED_HOSTS` (comma-separated, without port). Requests with
   other names are rejected with `host not allowed` and logged.
3. **Publish the port** only as far as needed with `WEB_BIND`. With a proxy
   or `tailscale serve` on the same computer, keep the default `127.0.0.1`.

Do not forward the port from the internet to the web UI.

### Tailscale

`tailscale serve` provides HTTPS with a certificate for the machine's name in
your tailnet and forwards to the local port. In `.env`:

```sh
MAIL_ARCHIVE_ALLOWED_HOSTS=localhost,127.0.0.1,mac.example-tailnet.ts.net
MAIL_ARCHIVE_PUBLIC_URL=https://mac.example-tailnet.ts.net
```

Then:

```sh
docker compose up -d web
tailscale serve --bg 8080
```

Open `https://mac.example-tailnet.ts.net` from any device in your tailnet.

### Reverse proxy in the home network

For example [Caddy](https://caddyserver.com) on the same computer, with a
name that resolves in your network:

```text
archive.home.example {
    reverse_proxy 127.0.0.1:8080
}
```

With `MAIL_ARCHIVE_ALLOWED_HOSTS=localhost,127.0.0.1,archive.home.example`
and, for passkeys, `MAIL_ARCHIVE_PUBLIC_URL=https://archive.home.example`.
The proxy must pass the original `Host` header (Caddy does by default);
otherwise the server's same-origin check rejects changes.

Without more, the audit log and the login limits see every request coming
from the proxy. `MAIL_ARCHIVE_TRUSTED_PROXIES` names the proxies whose
`X-Forwarded-For` header is believed; list only the proxy, and only if all
access goes through it. See [Behind a reverse proxy](../guide/audit-log#behind-a-reverse-proxy).

### Plain LAN access

`WEB_BIND=0.0.0.0` and the computer's LAN address or name in
`MAIL_ARCHIVE_ALLOWED_HOSTS` work without a proxy, but without HTTPS anyone in
the network who can read the traffic can take over a session. Use this only
in a network you fully trust. Browsers offer passkeys only over HTTPS or on
`localhost`, so they do not work this way.

## The SMTP server

With `MAIL_ARCHIVE_SMTP_LISTEN` set, the web server accepts mail for
[smtp accounts](../guide/journaling) from a mail server you run.

- **The allowlist is the only gate.** There is no SMTP login. Clients outside
  `MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS` get `554` before they can send
  anything. Allow the mail server's address, not a whole network the
  internet can reach. Compose publishes the port on `127.0.0.1` unless
  `SMTP_BIND` says otherwise; never forward it from the internet.
- **STARTTLS** is offered when a certificate is configured. Postfix on the same
  host or Docker network does not need it.
- **Limits:** at most 10 connections (more get `421`), 100 recipients per
  message, `MAIL_ARCHIVE_SMTP_MAX_SIZE` per message (`50M`), lines up to 1 MiB,
  5 minutes to send a command or a whole message, 1 minute for each answer
  to be read. A message is acknowledged only after it is stored.
- **The MIME parser sees mail nobody filtered.** Display and search read at
  most 1000 parts, 20 levels of nesting, 1 MiB of top-level header and 256
  MiB of decoded content per message; a message beyond that is stored whole,
  but shown and indexed only up to the limit.
- **Envelopes reveal BCC recipients.** They are visible only to the owner of
  the smtp account, in the message view and to the `to:` filter. Another user
  who has the same message from IMAP sees neither the envelope nor a match
  for a BCC recipient.
- **Every refusal is logged** at error level with client address, sender,
  recipients and reason. Message content is never logged. A message Postfix
  already knows to be too large never reaches the archive; only the Postfix
  log shows it.

## Browser protections

Even with a login, every website open in your browser could try to use your
session. The server therefore adds:

### DNS rebinding

A malicious website can point its own domain at `127.0.0.1` and then read
responses from local servers. Such requests carry the attacker's domain in
the `Host` header. The server only answers requests whose `Host` is in
`MAIL_ARCHIVE_ALLOWED_HOSTS` (default `localhost`, `127.0.0.1`, `::1`) and
rejects everything else with `403`.

### Cross-site requests

A website can send requests to `localhost` (but not read the answers).
State-changing requests (`POST`, `PATCH`, `DELETE`) must have an `Origin`
header matching the server and a JSON content type. Plain HTML forms and
simple cross-site requests can do neither.

Requests with an API key (`Authorization: Bearer ma_…`) are exempt. The
check protects the cookie, which the browser adds on its own to a request
another site starts. A key request carries its credential in the header
itself and never reads the cookie, also when the key is invalid. A page on
another site cannot add an `Authorization` header without a CORS preflight,
which the server never answers with permission, and a page that already
knows a key gains nothing from a visitor's browser. The `Host` check still
applies.

### HTML mail

HTML bodies are served for an iframe with a strict Content-Security-Policy
and the `sandbox` directive: no scripts, no forms, no plugins, no access to
the API. Remote content is blocked unless you click **Load remote images**
for one message. Attachments are served with `sandbox` as well; only common
image types are shown inline, everything else is a download.

### Login checks

Adding or changing an account makes the server connect to the IMAP host and
port given in the request. Every logged-in user can do this, so the server
can be used to probe hosts in your network. Only give logins to people you
trust.

## Sync health and alerts

`/healthz/sync` needs no login, so that monitoring tools can use it. It names
no accounts, users or mail, only account IDs. Still, anyone who can reach the
server with an allowed host name learns how many accounts exist (roughly,
from the IDs) and which ones fail to sync. The answer is cached for 15
seconds, so it cannot be used to load the database.

The alert webhook comes from the environment only, never from the web UI, so
users cannot make the server send requests to addresses of their choice.
There is no block of private addresses: ntfy or Gotify on the LAN is a main
use. The webhook URL and `MAIL_ARCHIVE_NOTIFY_WEBHOOK_AUTHORIZATION` are
secrets (topic names and tokens): they are never logged, and error messages
name only the scheme and host. Redirects are not followed, so the
authorization and the message never reach another host. A request times out
after 10 seconds and at most 4 KiB of the answer is read.

Alerts name the account, its owner, its ID and the last sync error (up to
200 characters). Alerts about mail deleted on the server name the account,
its owner and ID, the number of messages and the names of up to 20 folders,
never subjects, addresses or UIDs. Treat the receiver like `./ma status`, which shows the same:
on the public ntfy.sh server, use a protected topic or a long random one.

## Messages no longer on the server

Identical messages are stored once, even when several users archived them.
Whether a message is gone from a server is recorded per location, and each
user only sees their own accounts' locations: in the message view, in
**Only in archive** (`gone=1`), and in the counts on the account page and in
`GET /api/status`. If Alice deletes a message that Bob also archived, Bob
cannot tell. The log reports counts and folder names, never subjects,
addresses or UIDs.

## The secret key

`MAIL_ARCHIVE_SECRET_KEY` encrypts the stored passwords. Keep it outside the
archive backup's location or protect both equally. If it is lost, archived
mail stays readable, but every account needs its password again. If it
leaks together with the database, the IMAP passwords are exposed: change them
at the providers.

## The attestation key

Attestations are signed with an Ed25519 key, by default
`MAIL_ARCHIVE_DATA_DIR/attest/signing.key` (mode 0600; a key file that the
group or others can read is refused). Its public key is in
`attest/signing.pub`.

The key lies in the data directory, so `backup`, every copy of the data
directory and anyone with access to it has the private key. Someone who can
change the database, the files and has the key can rewrite the chain on the
host. The evidence therefore rests on copies kept elsewhere, not on the key
staying secret: copy `attest/signing.pub` off the host once, and
`attest/attestations.jsonl` (ideally with `attest/leaves/`) regularly, to a
place the archive host cannot write. To keep the key out of the data
directory, point `MAIL_ARCHIVE_ATTEST_KEY_FILE` at a separate mount.

If the key is lost or may be known to others, run
`./ma attest keygen --rotate`: older attestations stay trusted through
`attest/retired-keys.pem`, newer ones are signed with the new key.

Check against the copies with `./ma attest check --file COPY --public-key
COPY-OF-signing.pub`; `--public-key` trusts only the keys given, not the
ones on the host. What this proves and what not, and the operator steps, are
in [Tamper evidence](../guide/tamper-evidence).

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting on the
[repository](https://github.com/pklnx/mail-archive/security) instead of a
public issue.
