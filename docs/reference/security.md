# Security

## What is protected how

| Asset | Protection |
|---|---|
| IMAP passwords | Encrypted in PostgreSQL with AES-256-GCM, bound to the account's internal ID (so a stored password cannot be moved to another account), using `MAIL_ARCHIVE_SECRET_KEY`. API responses never contain them. |
| Archived mail (message files) | With [encryption at rest](#the-archive-key) on, every message file is stored encrypted with AES-256-GCM (`<sha256>.enc`) and written encrypted from the first byte, so no plaintext of it reaches the disk; the key is kept apart from the data. A key file on the same disk as the data protects copies and backups of the data directory, not snapshots of that disk. Without encryption at rest, plain `.eml` files, **not encrypted**: use an encrypted disk. |
| Archived mail (database) | PostgreSQL rows **are not encrypted**, also with encryption at rest: subjects, addresses, body text, the text extracted from attachments for search, attachment names, Message-IDs, envelopes, folder names, account and user names, the audit log and legal holds. Use an encrypted disk; a hosted instance must keep PostgreSQL on an encrypted volume whose key is kept away from its snapshots. |
| Exports and backups | `export` and `backup` write files with mode 0600 in directories with mode 0700. Exports are unencrypted, also with encryption at rest (an export is meant for a mail client: delete it once used); an export inside the data directory gets a warning while encryption at rest is on. Without encryption at rest the database dump is unencrypted; with it, `backup` encrypts the dump while `pg_dump` writes it (`*.dump.enc`, the plaintext only passes through a pipe), next to a copy of the keyring wrapped under the archive keys, and refuses without the archive key. A dump opens only with a key of its time. On an archive with any sign of encryption at rest, `restore-dump` writes only authenticated data into `pg_restore` unless the operator passes `--plaintext`. On an archive that never used encryption, dumps are not authenticated: whoever can write the backup location can run SQL as the database superuser on the next restore, so protect it like the database. `restore-dump` runs `pg_restore` in one transaction, so a damaged dump changes nothing. `BACKUP-NOTE.txt` names the secret key and the fingerprints of the archive keys but never contains a key, and `pg_dump` and `pg_restore` get the database password in their environment, not on the command line. That environment holds nothing else of the app: without Docker Compose, `backup` and `restore-dump` give them only the libpq variables of the app's connection (`PGHOST`, `PGPORT`, `PGUSER`, `PGDATABASE` and `PGPASSWORD` as the app resolves them, which counts `PGHOST`, `PGPORT`, `PGUSER`, `PGDATABASE`, `PGPASSWORD` and `PGPASSFILE` of the process where the URL leaves them out; and, from `MAIL_ARCHIVE_DATABASE_URL` only, `PGSSLMODE`, `PGSSLROOTCERT`, `PGSSLCERT`, `PGSSLKEY`, `PGSSLSNI`, `PGSSLNEGOTIATION`, `PGTARGETSESSIONATTRS`, `PGCHANNELBINDING` and `PGREQUIREAUTH`, each with the value the app takes where the URL leaves it out, the certificate files under `$HOME/.postgresql` included, and an empty `PGREQUIREAUTH` left out; and always `PGGSSENCMODE=disable`) and, when set, `PATH`, `HOME`, `TMPDIR`, `TZ`, `LANG`, `LC_ALL`, `LC_CTYPE`, `LC_MESSAGES`, `LD_LIBRARY_PATH`, `SSL_CERT_FILE`, `SSL_CERT_DIR`, `OPENSSL_CONF`, `KRB5CCNAME` and `KRB5_CONFIG`, never a `MAIL_ARCHIVE_*` variable or another variable of the process. `pg_dump` and `pg_restore` get the TLS and authentication settings of the app's connection, with GSSAPI encryption off (`PGGSSENCMODE=disable`, which the app never uses), or the commands refuse to run; libpq and OpenSSL apply these settings in the tools, Go's TLS in the app. A TLS or authentication `PG*` variable of the process that the URL does not override, a service (`PGSERVICE`, `PGSERVICEFILE`, `service`), a client key password (`PGSSLPASSWORD`, `sslpassword`), settings that do not give the app's connection attempts, or a tool too old for a setting make both commands refuse before `backup` writes its audit entry or the dump, before `restore-dump` decrypts the dump, and before either runs `pg_dump` or `pg_restore` ([Backups without Docker](../guide/operations#backups-without-docker)); `./ma` runs them in the database container with `docker compose exec`, which passes on no variable of the host. A copy of the data directory holds the message files as they are stored, and the keyring only wrapped under the archive keys. A [GoBD export](./cli#export-format-gobd) is plaintext too, outside any encryption at rest: with Docker Compose it goes to `/export` (`EXPORT_DIR`, outside the data directory), and an output inside the data directory gets a warning while encryption at rest is on. Its `STATEMENT.txt` is signed with the attestation key, never with the archive key or `MAIL_ARCHIVE_SECRET_KEY`, under its own header, so it never passes for an attestation nor an attestation for it. |
| Your mail servers | Only read: `EXAMINE`, `BODY.PEEK[]` and, to [reconcile](../guide/syncing#reconcile), `UID FETCH 1:* (UID FLAGS)`. Never `STORE`, `EXPUNGE` or a read-write `SELECT`. |
| The web UI | Login with user name and password, sessions in PostgreSQL, limits on failed logins, and the browser protections below. |
| User passwords | Only stored as Argon2id hashes (64 MiB, 3 passes). |
| TOTP secrets | Encrypted with AES-256-GCM using `MAIL_ARCHIVE_SECRET_KEY`, bound to the user ID. Recovery codes are stored only as keyed HMAC-SHA-256 hashes. |
| Passkeys | Only the public keys are stored; the private keys never leave the user's devices. |
| API keys | Only their SHA-256 and last 4 characters are stored; the key is shown once. A `read` or `sync` key reaches only the read and sync endpoints of its owner and the accounts shared with them. A `metrics` key, which only an admin can create and which has no other scope, reaches only `GET /api/metrics` (IDs and counts, no names). See [Login](#login). |
| The setup token | Only its SHA-256 is stored, with the time of the first start with it. It creates the first admin once, only while no user exists and for 7 days, and is compared in constant time. It is never logged, never in the audit log, and never reaches the server in a URL. See [Login](#login). |
| The audit log | Every search, read, export, login and change is recorded; the trigger refuses changes and deletions for every role. See [Audit log](../guide/audit-log) for what it covers and what it cannot stop. |
| Retention and deletion | Nothing is deleted without a [retention rule](./cli#retention) and a reviewed, confirmed [deletion run](./cli#deletion); rules, legal holds and runs are admin-only and recorded in the audit log. Database triggers refuse to delete a location whose period has not ended or that is on hold, and a message row without its deletion record. A message stays while any location of it, in any user's account, has no rule or an unexpired one, or is on legal hold. |
| Removal of archived mail | Daily signed [attestations](./cli#attest) record which messages were archived; see [The attestation key](#the-attestation-key). Tamper-evident, not tamper-proof. |
| SMTP secrets | Only their SHA-256 and last 4 characters are stored; the secret is shown once, like an API key. A secret lets a mail server hand over mail for its one smtp account, nothing else: it reads nothing. See [The SMTP server](#the-smtp-server). |
| The SMTP port | Off by default. With `MAIL_ARCHIVE_SMTP_AUTH=required` (the default once it is on) a mail server must log in over TLS with a secret of the smtp account it delivers for; the port may then face the internet. With `off`, only clients in `MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS` may connect, and the port must never face the internet. See [The SMTP server](#the-smtp-server). |

## Login

Every API request needs a login or an API key, except the login itself,
the setup of the first admin (`POST /api/setup`, below), `/healthz`,
`/healthz/jobs` and the API description `/api/openapi.json`. The page code (`/`,
`/assets/`, the web app manifest and its icons) is public; it contains no
data. Which route needs what is set in one table (`internal/web/routes.go`);
a route not listed there, or a new one without an access level, needs a
login.

- **Users** are created by admins in the web UI or with `./ma user add NAME`
  (`--admin` for admins). There is no self-registration; the first admin of
  a hosted instance is created once with the operator's setup link.
  Passwords generated
  by the server (new users, resets) have about 99 bits of entropy, are shown
  once and must be replaced by the user at the first login; until then the
  server refuses every other request. Passwords need at least 12 characters; there are no
  other rules, so a long passphrase is fine.
- **The setup link** creates the first admin of a new instance from
  configuration ([Start from configuration](../guide/getting-started#start-from-configuration)).
  The operator sets a setup token (`MAIL_ARCHIVE_SETUP_TOKEN_FILE`: 32 to 256
  printable characters, `./ma setup token` makes one of 256 random bits, not
  one of the instance's keys) and sends the link `<public URL>/#setup=<token>`.
  The token sits in the fragment, which browsers never send: it reaches no
  server log, proxy log or `Referer`. The page removes it from the address
  bar at once and sends it in the body of `POST /api/setup`, the only route
  that takes it. That route creates exactly one admin, and only while no user
  exists, setup was never completed, and the token matches and is under 7
  days old, counted from the first start with it; in one transaction it
  creates the admin, records the setup as complete for good and consumes the
  token. The database keeps only the token's SHA-256, compared in constant
  time. A start with another token starts a new window, a start without one
  revokes it, and the first user created any other way (`./ma user add`)
  completes the setup too, so a token set later never works. A wrong or
  expired token counts as a failed login of the address (below). The answer
  is a login, and the new admin must set up TOTP before anything else. The
  token is never logged and never written to the audit log; the web server
  logs that the link is waiting, never the link. **Remaining risk:** whoever
  opens the link before the customer becomes the admin. The operator sees the
  setup in `./ma status` (`setup: complete since …`) and in the audit log
  (`user.setup`, with the client address), and sends the link only over a
  channel the customer alone reads.
- **Sessions** end after 7 days without use and after 30 days at the latest.
  Logging out, a new password (`./ma user set-password`), locking
  (`./ma user lock`) and removing a user end their sessions at once. The
  browser only gets a random token in an `HttpOnly`, `SameSite=Strict` cookie;
  the database stores its SHA-256, so a database copy contains no usable
  sessions.
- **Two-factor authentication:** TOTP uses 6-digit RFC 6238 codes with a 30-second period and one-step clock tolerance. Administrators must have TOTP enabled and cannot disable it. A user with mandatory TOTP enabled but without setup is restricted to completing setup. TOTP challenges expire after 5 minutes, are single-use, accept at most 3 codes, and include a version check so resets win over in-flight logins. Recovery codes are shown once, are one-use, and are stored only as keyed hashes. The PDF and text downloads are made in the browser from the codes on screen; the server does not send the codes again.
- **Passkeys** (WebAuthn) replace password and TOTP at the login. They are
  bound to the host of `MAIL_ARCHIVE_PUBLIC_URL`, so a look-alike site cannot
  use them. Every passkey login needs user verification (Face ID, Touch ID,
  Windows Hello or the security key's PIN). Any authenticator is accepted
  (attestation `none`). A sign count that does not go up rejects the login,
  as it points to a copied authenticator. Adding a passkey needs the
  password, and a TOTP or recovery code if the user has 2FA on. Removing a
  passkey ends the user's other sessions. Admins with mandatory TOTP must
  still set it up: a passkey session is restricted like a password session
  until then. Starting a passkey login needs no session. It is limited to 30
  per minute per address (IPv6: per `/64`), and to 50 started in 5 minutes
  per address (IPv6: per `/64`) and 200 per IPv6 `/48`. The total of 50,000
  started in 5 minutes only bounds memory and the table. Like the other
  limits, these counters are kept in memory, and a restart resets them.
  Passwords stay: an account never depends on passkeys alone.
- **API keys** let scripts read mail (`read`), start syncs (`sync`) or, for admins, read the instance metrics (`metrics`, alone in its key: only `GET /api/metrics`, with IDs and counts and no names), and nothing else: logins, the profile, users and account changes stay with the session and the command line. Creating a key needs the password and,
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
  (IPv6: per `/64`) and minute answer `429`, without touching the password login limits. A key
  request that waits more than 60 seconds for the key checks ahead of it
  from its address also gets `429`. Keys never appear in logs or in the
  audit log.
- **Failed logins:** after 5 failures for a user name within 15 minutes, the
  name is blocked for 1 minute, then 2, 4, 8 and at most 15 minutes for every
  further failure. 20 failures from one address (IPv6: per `/64`) within 15
  minutes block that address for 15 minutes. Behind a reverse proxy, set
  `MAIL_ARCHIVE_TRUSTED_PROXIES` to it: otherwise every client counts as the
  proxy's address, and 20 failures from anyone block every login through
  the proxy for 15 minutes, and wrong setup tokens block the setup link for
  everyone. Failed passkey logins and wrong setup tokens count for
  the address only (IPv6: per `/64`). An IPv6 client usually gets a whole
  `/64`, so changing addresses within it does not escape the limits. Logs and
  the audit log keep the full address. The server log, like the audit log,
  keeps the name of a failed login only if it is a valid user name, so a
  password typed into the name field is not logged.
  Wrong names and wrong passwords get the same answer
  and take the same time. An attempt holds its place in these limits from
  the moment it starts, so parallel attempts are decided as if they came one
  after another and cannot get past the limits. An attempt waits at most 60
  seconds for the attempts ahead of it; after that it gets `429` with
  `Retry-After: 60`. User names that are not
  valid share one counter. The counters live in memory and reset when the
  server restarts.
- Behind a reverse proxy, the server sees the proxy's address for every
  client, so all clients share the address limit; the limit per user name
  still applies. Close to the address limit, logins through the proxy are
  checked one after another.

Each user sees only their own accounts, the accounts other users shared with
them, and the mail found in them. A message in several accounts is stored once
and shown to each user with only the locations of accounts they may see. Other
accounts and messages answer `404`, also when someone guesses a message ID.
Admins see only their own mail and what was shared with them.

Every user belongs to exactly one customer group, and every account to its
owner's group. An account's owner shares it only with a user of the same
group, one user at a time, and only for reading. A share request answers the
same `404` for a name that does not exist and for a user of another group,
and the store and the database refuse a share across groups. Moving a user or
an account to another group ends every share that would cross groups. An
auditor login belongs to one customer group. An owner grants it read access
only to their own accounts and only if it is in the same group; a grant
request answers the same `404` for a name that does not exist, for a user who
is not an auditor and for a user of another group, and the store and the
database refuse a grant across groups. An account that moves to another owner
leaves the grant. Moving the auditor or the owner to another group revokes
the grant and ends the auditor's sessions on it at once. Users who are not
admins never see names of users of other groups, except in older
Log entries from the time that user was still in the same group. Admins see every
group's users, accounts, audit log, retention rules and holds, but no mail
except their own and what was shared with them: a hold's number of matched
messages is shown only for a hold on the admin themselves or on one of their
own accounts, and the preview of deleting an account completely names only
the admin's own accounts that keep copies of its mail. An admin adds a legal
hold with a search filter in the web UI only over their own mail; a filter over
other users', a customer group's or the whole archive's mail is the operator's,
with `./ma hold add`. Deletion runs depend on every hold: a run's figures can
show an admin which expired messages of other users a filter hold set by the
operator keeps. Filter holds over other users' mail that an admin added in the
web UI before version 1.6.0 stay in force; the operator lists them with
`./ma hold list` and decides whether to keep them. Groups separate customers
inside one instance: they share one database, one archive directory, one
secret key and one backup, and a message that several groups received is
stored once and shown to each only through its own accounts.

Sync, import and fill statistics count per account: a message is *new* when
the account did not hold it before. Since 1.6.0, one more thing does not
depend on what accounts of other customer groups hold or held:

- whether a message deleted by retention or an account purge is archived
  again: it is skipped only in the accounts and groups it was deleted from,
  and only while that group holds no copy of it.

Accepted:

- Within one group, a user who holds the exact bytes of a message can tell
  from this that another account of the group archived it before.
- Messages deleted by a version before 1.6.0 count as deleted in every group
  that holds no copy.
- A purge by a version before 1.6.0 recorded a message only when no other
  account held it. Whether such a message is archived again in the purged
  account's group therefore still depends on whether another account held
  it then.
- While a process of a version before 1.6.0 still runs after the upgrade, it
  behaves as before 1.6.0: its search shows attachment text, and its syncs,
  fills and deliveries skip and store messages as before 1.6.0. Stop web and
  worker before upgrading.

Not covered: a message stays in an account after its retention period while
another account keeps it (see Retention and deletion above). A location's
retention end, and the date of a message without a Date header, count from
when the archive first stored the message.

One GoBD export never spans customer groups: a selection whose users and
accounts are not all in one group is refused, never filtered, and the signed
statement names the group and its owners; places of a message in another
group's accounts are never part of it.

A shared user may search, read and download. Managing, syncing, retention
and removal stay with the owner; the shared user gets `404` (`403` for the admin-only purge
routes) for the account's management routes. smtp (journal) accounts cannot be shared and a legal
hold's filter never searches envelopes, so envelopes (BCC recipients) stay with their owner.
An unshare takes effect on the user's next
request. Every read of shared mail is in the reader's audit log entries, and
the owner sees reads of their shared accounts as described in
[Audit log, Shared accounts](../guide/audit-log#shared-accounts), without the
reader's address, browser or API key name. Two kinds of read do not reach the
owner: a page of a list or search over all of the reader's mail, or a further
page of a named one, whose messages the reader also has through own accounts, and a conversation whose only member
in the shared account is the message that was opened. A legal hold on a shared
account is never shown to the shared user, admins included.

**Auditors** (tax advisors and tax auditors) are logins an admin creates
(on the Users page with the role **Auditor**, `./ma user add NAME --auditor`,
or `POST /api/users` with `"auditor": true`). The role never changes:
an auditor is never an admin, owns no accounts, receives no shares and holds
no API keys (a key of an auditor opens nothing). An auditor reads only
through an auditor grant: one owner's accounts, all or some of their folders,
an optional message period and a validity window that ends at most 365 days
after its start or after its latest extension.

- **Login:** the generated password must be changed at the first login, and
  TOTP is mandatory and cannot be switched off (an admin resets it); passkeys
  work in addition. Without an active grant the login is refused, only after
  a correct password and second factor. A session is bound to one grant (with
  several, the auditor picks one first) and ends with the grant at the latest.
- **Read-only, deny by default:** an auditor's session may call only the
  routes marked `x-auditor` in the [API description](./api): the login, the
  own password, language, 2FA setup and passkeys, the own Log and grants, and
  reading the grant's mail. Every other route answers `403` and is recorded
  as `auditor.denied`. Messages outside the grant's accounts, folders and
  period answer `404`, conversation members included; the account list shows
  only the granted folders, without counts or sync times. Holds and envelopes
  stay hidden.
- **Granting** needs the owner's password and, with 2FA, a code. Only the
  owner grants, and only their own accounts; smtp (journal) accounts cannot
  be granted. The scope cannot be changed later; extending the end needs the
  step-up again.
- **Revocation:** the owner revokes their own grants, any admin and
  `./ma auditor revoke ID` any grant; it ends the auditor's sessions on the
  grant at once and is final. A grant is also revoked when its last account
  moves to another owner or is purged, when the auditor or the owner moves to
  another group, and before its auditor is removed. A removed (not yet
  purged) account stays in its grants until it is purged: the auditor can
  still read its mail inside the grant's period. Locking the auditor or the
  owner suspends the grant; unlocking restores it.
- **Every request of an auditor that reads archive data is recorded**: the
  account list, every folder listing and search page (also empty ones), and
  every opened message, HTML body, `.eml` download and attachment. Such a
  read fails closed (`503`, nothing sent) when its entry cannot be written.
  Every refused request (`auditor.denied`, with the reason: a route outside
  the allowlist, the password change or 2FA setup still pending, no grant
  chosen yet, or a filter only admins may use) and every grant choice is
  recorded too; a refusal is recorded best effort, and the request stays
  refused whether or not its entry is written. Reads of the auditor's own data are not recorded: the session, the
  status, the own Log and the own grant list. The grant's owner sees the auditor's entries in their Log, with
  the search terms, without the auditor's address, browser or key name, and
  sees revocations by others with "operator" instead of the actor's name. See
  [Audit log](../guide/audit-log).

## Operator, admin and customer groups

A hosted instance runs in full-service mode: the operator holds the host
(environment, command line, keys, backups) and is the instance's admin in
the web UI; the first admin from the setup link is the operator. The
customers' people are members of their customer group and are never
admins: an admin sees every group's users, accounts and audit log. An
individual instance is one group, with the operator as its only admin.

- **What admins see.** Every group's storage usage, quota and quota state,
  every account's size and name, and the instance's disk and database, in
  the web UI and with `./ma usage`, but no mail except their own and what
  was shared with them (above). Members see the size of their own accounts
  and, when new mail is not archived, that storage is full, never whose
  usage caused it.
- **Metrics.** `GET /api/metrics` is for admins only: a session, or an API
  key with the scope `metrics` alone. It holds IDs, counts, times and fixed
  values: storage of the instance, of each group and per account by ID,
  the disk, the database by table, jobs, users per role and lock state,
  encryption at rest, the last backup, the version, pending migrations and
  SMTP deferrals for storage. It never holds names, hosts, IMAP user names,
  addresses, folders, subjects or error texts, and no exclusive bytes per
  account, which are computed across groups. A label value that does not
  match its fixed pattern is not written. The answer is cached for 15
  seconds. See [Metrics](../guide/operations#metrics).
- **Webhook alerts** carry IDs only by default
  (`MAIL_ARCHIVE_NOTIFY_WEBHOOK_DETAIL=ids`; see
  [Health endpoints and alerts](#health-endpoints-and-alerts)): webhook
  receivers are often third parties.
- **The shell.** Whoever holds the host, the database or the archive key can
  technically read all mail; no app path is meant for it, and the
  [archive key](#the-archive-key) does not protect against the operator.
  Exports and imports at the command line are audited, but someone with the
  shell can bypass that. Command-line entries in the audit log carry the
  operating system user and `MAIL_ARCHIVE_OPERATOR`, which the app does not
  verify.
- **Support.** The operator has no support access to customers' mail in the
  app. A [GoBD export](../guide/gobd-export) is run by the operator at the
  command line on the customer's request, under a legal hold or an auditor
  grant where there is one; it is recorded in the group's audit log as a
  command-line entry. Anything else that involves mail content goes through
  the customer's own users, for example by screen sharing.
- **Logs** of a hosted instance contain customer data: SMTP senders and
  recipients, account, user and folder names, and the texts of server
  errors. The operator treats logs as customer data and covers who reads
  them and how long they are kept in the data processing agreement.

## Network access

Docker Compose publishes the web UI on `127.0.0.1`, so only this computer can
reach it. To use it from your home network or a VPN:

1. **Use HTTPS.** Without it, passwords and session cookies cross the network
   in plain text, and the browser's protection against HTML mail is weaker
   (see [Plain LAN access](#plain-lan-access)). Put a reverse proxy or
   `tailscale serve` in front. The
   server learns that the browser used HTTPS from the proxy's
   `X-Forwarded-Proto: https`, then sets the session cookie as
   `__Host-mail_archive_session` with `Secure` and sends
   `Strict-Transport-Security: max-age=31536000` (HSTS: the browser uses
   only HTTPS for this host name for a year). The header counts only from a
   loopback address or from an address in `MAIL_ARCHIVE_TRUSTED_PROXIES`;
   from anybody else it is ignored, so a client cannot choose how the cookie
   is set. Under Docker Compose a proxy or `tailscale serve` on the same
   computer does not connect from loopback: Docker's port publishing hands
   its requests to the container from the gateway of the Compose network.
   Set `MAIL_ARCHIVE_TRUSTED_PROXIES` to that address, as in the examples
   below ([Behind a reverse proxy](../guide/audit-log#behind-a-reverse-proxy)
   shows how to find it). Until then logging in works, but the cookie lacks
   `Secure` and the `__Host-` prefix and no HSTS is sent; the server logs a
   warning once, naming the address the header came from.

   When a request counts as HTTPS, only the `__Host-` cookie logs in, never
   the plain one: a plain cookie can be planted over plain HTTP or from
   another host name of the domain, and must not log the browser into
   someone else's session. A `__Host-` cookie on a plain request is one from
   a time the header counted; the server does not set that name there, so it
   does not count either, and the next login expires it.

   This protection works only where requests count as HTTPS: with
   `X-Forwarded-Proto: https` from loopback (a proxy or `tailscale serve` on
   the same computer, with the server run without Docker) or from an address
   in `MAIL_ARCHIVE_TRUSTED_PROXIES`. In Docker Compose, `tailscale serve`
   included, that needs the variable. Without it every request is plain,
   whatever the proxy is, so a planted plain cookie can still log a browser
   in as the planter.

   Setting, removing or changing `MAIL_ARCHIVE_TRUSTED_PROXIES` later, in a
   way that changes whether requests count as HTTPS, makes the cookie each
   browser holds the wrong one, so every user logs in once more. A login and
   a logout end the sessions of both cookies the browser sends.

   Browsers that have seen the HSTS header switch to HTTPS for that host
   name, on all its ports (they do not refuse plain HTTP, they rewrite it to
   HTTPS), and the year counts from the last HTTPS visit, since every answer
   to a request that counts as HTTPS carries the header. So do not serve the
   same name over plain HTTP later (for LAN access, for example): a
   plain-HTTP-only service under that name becomes unreachable in those
   browsers until the year is over.
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
# The gateway of the Compose network, which tailscale serve connects from.
MAIL_ARCHIVE_TRUSTED_PROXIES=172.18.0.1
```

Use your Compose network's gateway address; [Behind a reverse
proxy](../guide/audit-log#behind-a-reverse-proxy) shows how to find it. With
it, the session cookie is `Secure` and HSTS is sent.

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

Caddy sends `X-Forwarded-Proto` and `X-Forwarded-For`. Under Docker Compose
it connects from the gateway of the Compose network, so set
`MAIL_ARCHIVE_TRUSTED_PROXIES` to that address, for example
`MAIL_ARCHIVE_TRUSTED_PROXIES=172.18.0.1`. Then the session cookie is
`Secure`, HSTS is sent, and the audit log and the login limits see the
clients instead of the proxy. `MAIL_ARCHIVE_TRUSTED_PROXIES` names the
proxies whose `X-Forwarded-Proto` and `X-Forwarded-For` headers are
believed; list only the proxy, and only if all access goes through it. See
[Behind a reverse proxy](../guide/audit-log#behind-a-reverse-proxy).

### Plain LAN access

`WEB_BIND=0.0.0.0` and the computer's LAN address or name in
`MAIL_ARCHIVE_ALLOWED_HOSTS` work without a proxy, but without HTTPS anyone in
the network who can read the traffic can take over a session. Use this only
in a network you fully trust. Browsers offer passkeys only over HTTPS or on
`localhost`, so they do not work this way.

Browsers also send the Fetch Metadata headers (`Sec-Fetch-*`) only over HTTPS
and to `localhost` (`127.0.0.1`, `::1`). Over plain HTTP on any other address
(a LAN or VPN address) the server's guard against requests from HTML mail
([HTML mail](#html-mail)) therefore does nothing, in every browser. After
**Load remote images**, an image in a mail that points at the archive's own
address then reaches the API with your session, in every browser. In
Chromium, which sends the session cookie because the navigation began in the
mail frame, a link in a mail to an external page that redirects back to the
archive can also open a mail's HTML (also with its remote images loaded), its
`.eml` or an attachment in a new tab. A page of another service under the
same host name (on another port) or the same domain counts as the same site:
its images and links to the archive carry your session too. Nobody can read
the answers and nothing can be changed, but these requests leave audit
entries in your name, some open IMAP connections, and a mail opened with its
remote images tells its sender that it was opened. The other browser
protections still hold over plain HTTP: the `Host` check, the `Origin` check
on changes, the `SameSite=Strict` session cookie, the mail frame's sandbox
and CSP, the server's link rewrite and, in Chromium, the web UI's link
blocker. HTTPS, as above, or `http://localhost` on the computer itself
restores the guard.

So that this does not go unnoticed, the server logs a warning ("the web UI
is used over plain HTTP under a host name other than localhost or a loopback
address") the first time a browser uses it this way under each allowed host
name, with that name as `host`. It does not warn for `localhost`, names under `.localhost`, loopback
addresses, or HTTPS, direct or through a proxy whose `X-Forwarded-Proto`
counts.

## The SMTP server

With `MAIL_ARCHIVE_SMTP_LISTEN` set, the web server accepts mail for
[smtp accounts](../guide/journaling) from a mail server. How it decides who
may hand over mail depends on `MAIL_ARCHIVE_SMTP_AUTH`.

**`required` (the default).** Port 587 may be public, for customers' mail
servers that connect over the internet from changing addresses. The gate is:

- **Mandatory TLS.** The server does not start without a certificate. It
  offers `AUTH` only after `STARTTLS` and answers `AUTH` before it with
  `523`. `MAIL` without TLS gets `451`. The Postfix setup in the
  [journaling guide](../guide/journaling#log-in-with-an-smtp-secret) verifies
  the certificate and the host name (`smtp_tls_security_level = secure`), so
  a man in the middle cannot strip `STARTTLS` and read the secret.
- **A secret per smtp account.** `AUTH PLAIN` or `AUTH LOGIN` with the user
  name `smtp-<account ID>` and a secret of that account: `mas_` and 256
  random bits, stored only as SHA-256 plus a 4-character hint, shown once
  ([`account smtp-secret`](./cli#smtp-secrets)). A login hands over mail only
  for the addresses of its own account. Every `MAIL` checks the secret again,
  so a revoked or expired secret stops working at the next message, also on
  an open connection. A wrong login answers `535`; a missing one, or one for
  another account's address, answers `451`, so the mail server keeps the copy
  queued instead of dropping it.
- **Failed logins are limited per address**, like the web login: 20 failures
  within 15 minutes block the client address (an IPv6 address counts per
  `/64`) for 15 minutes, with `454` before its credentials are even looked
  at. There is no limit per user name: a 256-bit secret cannot be guessed,
  and such a limit would let anyone who knows a user name stop that
  customer's journaling.
- **Connection caps and a deadline:** at most 16 connections, at most 8 from
  one address (`MAIL_ARCHIVE_SMTP_MAX_CONNS_PER_ADDR`, IPv6 per `/64`), and 60
  seconds to log in; then the connection is closed. More connections get
  `421`.
- **The allowlist is optional:** with `MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS`
  set, a client must be in it **and** log in.
- **Logged without secrets.** A login is logged at info level with the
  client address, the user name and the secret's ID; a failed one at warning
  level with the client address and a reason (`malformed`, `unknown`,
  `wrong_user`, `expired`, `revoked`, `account_removed`, `not_smtp`). A user
  name is logged only in its own format (`smtp-` and digits), otherwise as
  `username_valid=false`, because a customer may type the secret, or a
  mailbox password, into that field. The password, its hash, the SASL bytes
  and any part of a secret are never logged. A password that does not have
  the format of a secret is not even hashed. SMTP logins get no audit entries
  (a public port is scanned all the time); creating and revoking secrets
  does.

**`off`.** For a mail server on a network you control:

- **The allowlist is the only gate.** There is no SMTP login. Clients outside
  `MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS` get `554` before they can send
  anything. Allow the mail server's address, not a whole network the
  internet can reach. Compose publishes the port on `127.0.0.1` unless
  `SMTP_BIND` says otherwise; never forward it from the internet.
- **STARTTLS** is offered when a certificate is configured. Postfix on the same
  host or Docker network does not need it.

In both modes:

- **Limits:** at most 16 connections (more get `421`), 100 recipients per
  message, `MAIL_ARCHIVE_SMTP_MAX_SIZE` per message (`50M`), lines up to 1 MiB,
  5 minutes to send a command or a whole message, 1 minute for each answer
  to be read. A message is acknowledged only after it is stored.
- **Storage limits never drop mail.** When a recipient's customer group or
  the instance is beyond its [storage quota](../guide/operations#storage-and-quotas)
  plus the SMTP overflow share, or the disk is below its reserve, the
  recipient gets `452 4.3.1`, a temporary answer, never a `5xx`: a mail
  server drops a BCC copy that gets a `5xx` without telling anyone. The
  answer is per recipient; with `required` a transaction holds one account,
  so it covers the whole transaction. The check before the message is
  written is repeated at `DATA`; when every account is deferred, nothing is
  written. The answer is the same whichever limit was reached; with `off`,
  a client in the allowed networks can learn from it which addresses'
  groups are full, as it can learn which addresses exist (`550`). The
  check is a lookup in memory, the same for every group. At most once per
  10 seconds per process, the first check after that time reads the figures
  itself (in `serve` the background read usually comes first): that read
  covers every group, so its time grows with the number of groups and of
  their changes not yet folded, instance-wide, and never depends on which
  group is asked or on any one group's state.
- **The MIME parser sees mail nobody filtered.** Display and search read at
  most 1000 parts, 20 levels of nesting, 1 MiB of top-level header and 256
  MiB of decoded content per message; a message beyond that is stored whole,
  but shown and indexed only up to the limit. For search, the web server (or
  a separate worker process, if configured) and `./ma reindex` also extract
  the text of PDF, plain-text, CSV, TSV, Markdown and HTML attachments: in
  the background, one document at a time per worker (one by default), never
  while a message is being received. PDFs are read by PDFium compiled to
  WebAssembly, which runs inside the process in wazero without a file
  system, network, environment variables or output; every PDF gets a new
  instance that is discarded afterwards. A process reads only one PDF at a
  time, also with several workers: the PDF reader keeps the documents it
  reads in a table of the whole process, so a PDF exploiting a bug in it
  could otherwise reach another document read at the same time, possibly
  another user's. Limits: PDFs up to 25 MiB and other text attachments up
  to 4 MiB; 500 pages and 15 seconds per PDF; 20 attachments and 60 seconds
  per message; 256 KiB of text per attachment and 1 MiB per message; 96 MiB
  of WebAssembly memory per document, reserved in advance and returned
  after each document. A hostile PDF can therefore use at most one core for
  15 seconds and about 96 MiB of memory plus its own size. A PDF beyond a
  limit, a damaged one, or one that needs a password to open is not
  searchable by its content. A message whose extraction failed or ended the
  process three times is skipped. Extracted text, like all message content,
  is never logged.
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
(`default-src 'none'; script-src 'none'`, no forms, no plugins) and the
`sandbox` directive. Remote content is blocked unless you click **Load
remote images** for one message. Images embedded in the mail (`cid:`) are
written into the HTML as `data:` URLs, so the frame makes no request to the
server for them; `img-src` allows only `data:` (and `https:`/`http:` after
**Load remote images**). Only PNG, JPEG, GIF and WebP are embedded, up to
10 MiB per message; SVG and every other type become an empty image.
Attachments are served with `sandbox` as well; only common image types are
shown inline, everything else is a download.

The sandbox allows the frame the app's origin (`allow-same-origin`) but
never scripts, in the iframe's attribute and in the response's own CSP,
which also applies when the URL is opened on its own. The web UI needs the
same origin to read the height of the mail and make the frame as tall as
it (up to 50,000 CSS pixels), so the message scrolls as one page. It reads
only the document's geometry, never its content. A mail longer than that
keeps the frame at the height of the visible area and scrolls inside it; a
mail whose own CSS sizes it by the frame's height (`100vh`) gets at least
that height and scrolls inside the frame for the rest. No script runs in the
mail: the sandbox has no `allow-scripts`, and `script-src 'none'` is a second
layer.
The accepted cost: if a browser bug ever ran script in the frame, it would
run with the app's origin and could use the API.

With the same origin, requests the mail itself makes to the server carry
the session cookie: an image on the app's own address (after **Load remote
images**, `https:` matches it) or a link to it. Nobody can read the
answers, but they would leave audit entries in your name, and some open
IMAP connections. These layers stop them:

- The server refuses `/api` requests by their
  [Fetch Metadata](https://developer.mozilla.org/en-US/docs/Glossary/Fetch_metadata_request_header)
  with `403`, before anything is done or logged: every request that is not
  a fetch of the app (an image, style, font, prefetch and so on), and every
  navigation (a link, a frame) except to the routes a browser opens:
  `GET /api/messages/{id}/html`, `…/raw`, `…/parts/{n}` and
  `/api/openapi.json`. A navigation to these routes is refused too when the
  browser marks it as coming from another site (`Sec-Fetch-Site`
  `cross-site` or `same-site`). Requests without these headers (scripts, API
  clients, and browsers over plain HTTP on any address other than
  `localhost`, see [Plain LAN access](#plain-lan-access)) are not affected.
- The server takes every link into the app out of the HTML before it serves
  it. Every `href` and `xlink:href` attribute, on every element, keeps its
  value only if the markup spells out, without character references, an
  `http:` or `https:` URL (or one starting with `//`) to a plain host name
  that is not one of the server's own (`MAIL_ARCHIVE_ALLOWED_HOSTS`, the host
  of `MAIL_ARCHIVE_PUBLIC_URL`, the request's `Host`; any port, either
  scheme), or a `mailto:` or `tel:` URL. Every other one gets `data-` in
  front of its name, so the element is no longer a link: relative links,
  in-page anchors, links to the archive's own address, IP-address forms
  other than a plain IPv4 address, internationalized host names,
  `javascript:`, `data:`, `vbscript:` and every other scheme. `ping`
  attributes and `<meta http-equiv>` get the prefix too. `attributeName`
  gets it as well, so SVG animations cannot set a link (they do not animate
  in mail). So do `rel`, `referrerpolicy`, `crossorigin`, `content`,
  `attributionsrc`, `browsingtopics` and `sharedstoragewritable`, on every
  element, and frame sources (below). A `<link>` in a mail therefore does nothing: no prerender,
  prefetch, preconnect, DNS prefetch or stylesheet. A `<meta>` sets
  nothing; the header's `charset=utf-8` takes precedence over a
  `<meta charset>`. The server's `Referrer-Policy: no-referrer` holds for
  link clicks and, after **Load remote images**, for `<img>` elements,
  which send neither `Referer` nor `Origin`. A mail with inline SVG or
  MathML loads no remote images at all (below). Images that a mail
  would load through CSS are never requested: Chromium sends the archive's
  origin with them whatever the policy says, some of them in `Origin` too.
  So in `style` attributes, in the SVG attributes that hold CSS (such as
  `fill` or `mask`) and in `<style>`, the server puts `data:,` in front of
  every URL in `url()` that is not a `data:` URL or a reference into the
  mail itself (`#…`). It also puts `_` before the parenthesis of every CSS
  function that is not on its list of functions that load nothing
  (colours, gradients, `calc()`, transforms and the like), so the browser
  ignores that declaration or rule. A mail with inline SVG or MathML never
  loads remote images, also after **Load remote images**: there the
  server's HTML parser and the browser can build different documents, so
  the server cannot know which CSS the browser applies. The server finds
  such a mail by the bytes `<svg` or `<math` anywhere in its HTML, in any
  case, without parsing it, and the web UI says why the images stay off.
  For any other mail, before it serves the message with **Load remote
  images**, the server parses the HTML as a browser does. It reads the CSS
  that applies there with a tokenizer that follows the CSS Syntax
  specification, escapes included. If it still finds such a URL or an
  unlisted function, no remote image is loaded for that message either.
  This holds as long as the server's HTML parser and CSS tokenizer read
  such mail as the browser does. This needs nothing
  to run in the browser, so it holds in every engine. Only the HTML sent to
  the browser for display is changed: the stored message file, `…/raw`,
  `…/parts/{n}` and exports stay byte for byte as archived. What you see:
  links into the archive's own address are not clickable, SVG `<use>` and
  SVG images whose `href` the rewrite renames (relative, into the archive,
  `data:` and so on) show nothing, and a mail's own `color-scheme` or
  `theme-color` `<meta>` has no effect. Images a mail sets through CSS
  (backgrounds, list bullets, border images) stay empty, also after
  **Load remote images**, and an element with a CSS mask from another
  address is not shown. A mail with inline SVG or MathML shows no remote
  images at all. The colours and the rest of the mail's CSS apply,
  except a declaration or rule that uses a CSS function the server does
  not know. External links work as before.
- In Chromium the web UI also cancels clicks, middle clicks and drags of such
  links (also inside open shadow roots) and of `javascript:`, `data:` and
  `vbscript:` links. It cancels every click, middle click and drag through an SVG `<use>` element, whatever the link
  points to: links through `<use>` do not work at all. The frame takes no
  clicks and no focus (it is `inert`) until this blocker is on the mail's
  document. The web UI installs it as soon as that document exists, before
  its `load`, so a remote image that never answers does not leave the mail
  unusable. The app's CSP (`frame-src 'self'`) lets no external page into
  the frame: a link that would open in the frame itself (`target="_self"`,
  or an SVG link, which ignores `<base target>`) shows the browser's error
  page there, except `about:blank`, which the CSP lets through and which
  shows an empty page. Whenever the frame leaves the mail's document, it is
  `inert` again until the blocker is on the next mail document, the error
  page or the empty page included. So Back (or Forward) to the mail is
  guarded like its first load, also while a remote image holds it. WebKit
  (Safari) does not run listeners that the web UI adds to the sandboxed
  frame's document (#459): there, this layer does nothing, and the server's
  rewrite is what holds.

The server's rewrite and the web UI's layer have to see the links. A mail
can hide them in three ways: behind SVG `<use>`, which shows a copy of the
link in a tree the web UI cannot see into (cancelled as above), in a
declarative shadow root and in a `srcdoc` frame. Before it serves the HTML, the server neutralizes the last
two: declarative shadow roots (attributes whose names start with
`shadowroot` on `<template>`, such as `shadowrootmode="closed"`) and
frames (attributes whose names start with `src` on `<iframe>` and
`<frame>`, so `srcdoc` and `src`). It puts `data-` in front of these
attribute names, so the template stays an unrendered `<template>` and the
frame an empty page; their content is not shown. A frame's `src` would load
nothing under the CSP, but Chromium connects to its host before the CSP
blocks the navigation, which would tell the sender that the mail was
opened; renamed, the frame connects nowhere. The rename runs last, on the bytes as served (after the images are
inlined), and changes no other byte. It does not depend on where the tag
is, so the same text in a comment, a style or an attribute value gets the
prefix too. The link rewrite runs in the same pass and the same way. Other
ways to nest a document (an object with a `data` URL, `embed`, a `<meta>`
refresh) load nothing under the CSP. This was measured in Chromium and
WebKit; that they open no connection either (and that a renamed frame opens
none) was measured in Chromium only.

What remains:

- The context menu's "Open link in new tab" cannot be intercepted, but it
  only offers links the server kept, none of which points into the app. An
  external page can still send the browser on to the archive, as any
  website can. The server refuses navigations to the routes above that the
  browser marks as coming from another site (`Sec-Fetch-Site` `cross-site`
  or `same-site`), so a link in a mail that redirects back to the archive
  cannot open them, in every browser with Fetch Metadata (Chromium, Firefox,
  Safari 16.4 and later) when you open the archive over HTTPS or on
  `localhost`; Chromium would send the session cookie
  along, because the navigation began in the mail frame. Opening such an
  address directly (typed, a bookmark, the app's own links) still works.
- A link or redirect can still open the web UI itself (`/`, for example
  `/?m=<sha>`), which is not an `/api` route. Opening a message there writes
  the usual `message.open` and `message.html` entries in your name, as with
  any website that links to the archive; the sender has to know the stored
  message's SHA-256.
- Browsers send the Fetch Metadata headers only over HTTPS and to `localhost`
  (`127.0.0.1`, `::1`); browsers without Fetch Metadata (Safari before 16.4)
  send none. Without the headers the server cannot tell these requests apart.
  Over [plain LAN access](#plain-lan-access) (plain HTTP on any address other
  than `localhost`), after **Load remote images** an image in the mail can
  make such requests in every browser, and in Chromium a link in a mail that
  redirects back to the archive can open the routes above, with remote images
  loaded. In Safari before 16.4, after **Load remote images**, an image in
  the mail can make such requests over HTTPS too. Nobody can read the
  answers; they leave audit entries in your name.
- For a mail without inline SVG or MathML, whether its CSS can load an
  image is decided by the server's HTML parser and CSS tokenizer, which
  read such mail as Chromium does today, and by its list of CSS functions
  that load nothing. Inside SVG and MathML they are known to differ from
  Chromium, which is why such mail gets no remote images. If a browser
  later parses HTML or CSS outside SVG and MathML differently from the
  server, or lets a function on the server's list load an image, a mail
  written for that could again tell the sender the archive's origin after
  **Load remote images**, until the server follows.
- The accepted cost above: if a browser bug ever ran script in the frame,
  it would run with the app's origin.

What holds per engine: in Chromium and WebKit, the sandbox, the CSP, the
Fetch Metadata guard (over HTTPS or on `localhost` only; Safari 16.4 and
later) and the server's link rewrite;
in Chromium also the web UI's blocker and `inert` gate. In WebKit, a link
that would open in the frame itself leaves the frame on the mail instead of
showing an error page. Measured in Playwright's Chromium and Linux WebKit
(Playwright 1.64.1, as CI runs them: Chromium headless shell 156, WebKit
27.2). In both, `inert` on the
frame stops clicks into it, a `<meta>` refresh does not navigate the
sandboxed frame, and the frame's own `load` and the web UI's
`ResizeObserver` still size it. In WebKit, the web UI's `click`,
`auxclick` and `dragstart` listeners on the mail's document do not run, a
MathML `href` is a link, and an SVG link without `target` opens in a new
tab; in Chromium, MathML `href` is not a link and an SVG link without
`target` opens in the frame itself. Safari on macOS and iOS uses the same
engine but was not tested. Resource hints were measured in Playwright's
full Chromium 156.0.8078.4 with background networking on: the headless
shell never sends them, and the full Chromium sends a prerender in any
profile but a preconnect or DNS prefetch only in a persistent one. CSS
images were measured in Playwright's headless shell and full Chromium
156.0.8078.4.

### Login checks

Adding or changing an account makes the server connect to the IMAP host and
port given in the request. Every logged-in user can do this, so the server
can be used to probe hosts in your network. Only give logins to people you
trust.

The same holds for a fill's folders, starting, switching on and resuming a
fill, and an account's folder list from the server. All these checks are
limited before they connect; a refused check answers `429` with
`Retry-After` and makes no connection:

- 20 checks per user and 60 per client address (IPv6: per `/64`) in 10
  minutes;
- 10 failed logins per IMAP server, port and user name in 10 minutes,
  whoever makes them, so the archive cannot be used to guess the password of
  someone else's mailbox. Every check that fails after the login was sent
  counts, also when the server drops the connection instead of answering.
  The folder list of a saved account, which uses its saved password, does
  not count, so nobody can block another user's folder list. The limit is
  kept per spelling of the host, port and user name: case, a trailing dot
  and the ways of writing one IP address count as one, but another name for
  the same server (a DNS alias, `localhost` for `127.0.0.1`, another address
  of the provider) or a user-name alias the server accepts gets its own 10
  failures. The limits per user and per address still apply to all of them;
- 2 checks at a time in all and 1 per user; a check waits for its turn at
  most 15 seconds (`Retry-After: 15`).

Each check takes at most a minute and reads at most 16 MiB (more with a
raised folder limit, see [IMAP limits](../guide/operations#imap-limits)). A
failed check answers with a fixed sentence and a code naming its category
(`imap_connect`, `imap_tls`, `imap_login`, `imap_timeout`, `imap_server`,
`imap_protocol`, `imap_limit`). The IMAP server's own answer and the
network's error text (addresses, certificate names) go only to the server
log, never to the user, the web UI or alerts. Folder names, which the IMAP
server chooses (up to 1,024 bytes), still appear in stored run errors and
alerts, so a hostile server can put text there through a folder name.

## Health endpoints and alerts

While the web server starts (it waits for the migration lock, migrates and
checks the secret key), it already listens: `/healthz` answers `503` with
`{"status":"starting"}`, and every other request `503` with `Retry-After: 5`,
after the same Host check and headers as later. Nothing else is served
before the start is complete.

`/healthz/sync` needs no login, so that monitoring tools can use it. It names
no accounts, users or mail, only account IDs. Still, anyone who can reach the
server with an allowed host name learns how many accounts exist (roughly,
from the IDs), which ones fail to sync, and which ones are `paused` because
their customer group or the instance reached a
[storage limit](../guide/operations#storage-and-quotas). It does not say
which limit, nor any figure or group. The answer is cached for 15
seconds, so it cannot be used to load the database.

`/healthz/jobs` needs no login either. It gives only a status: `ok`, or
`degraded` with the names of the job kinds (for example `attachment_text`)
whose work for new mail has waited more than 30 minutes. It gives no counts
and names no messages, accounts or users. The answer is cached for 15
seconds. Counts are shown only to admins (`GET /api/status`) and in
`./ma status`. Failed jobs record a short error code, never message content.

The alert webhook comes from the environment only, never from the web UI, so
users cannot make the server send requests to addresses of their choice.
There is no block of private addresses: ntfy or Gotify on the LAN is a main
use. The webhook URL and `MAIL_ARCHIVE_NOTIFY_WEBHOOK_AUTHORIZATION` are
secrets (topic names and tokens): they are never logged, and error messages
name only the scheme and host. Redirects are not followed, so the
authorization and the message never reach another host. A request times out
after 10 seconds and at most 4 KiB of the answer is read.

By default (`MAIL_ARCHIVE_NOTIFY_WEBHOOK_DETAIL=ids`) alerts name accounts,
recurring fills and customer groups by their ID only: no account, owner or
group names, no IMAP user names, no folder names and no error texts, and
their title and text are fixed text with IDs. The JSON entries have no field
that could hold such a value. Storage alerts carry the level, the group ID,
the bytes used, the quota and the percentage, in every mode.

With `MAIL_ARCHIVE_NOTIFY_WEBHOOK_DETAIL=full`, which sends customer data to
the receiver and is meant for receivers the operator runs, alerts name the
account, its owner, its ID and the last sync error (up to
200 characters). For an error from the IMAP server or the network, that is a
fixed sentence of its category, never the server's own text. Alerts about mail deleted on the server then name the account,
its owner and ID, the number of messages and the names of up to 20 folders,
never subjects, addresses or UIDs. Treat the receiver like `./ma status`, which shows the same;
in either mode, on the public ntfy.sh server, use a protected topic or a long random one.

With `full`, names, mailboxes, errors and folder names in an alert are shown as literal
text in every receiver, so whoever controls them (a user, a mail server)
cannot add formatting, links or mentions to the alert, or forge a line.
In the `text` (Slack, Mattermost) and `content` (Discord) fields each value
is inline code, with backticks replaced and, in `text`, `&`, `<` and `>`
escaped; both fields are shortened by whole accounts, so every code span is
closed, and Discord is told to ping no one (`allowed_mentions`). Gotify is
told to show plain text (`extras`), and ntfy gets plain text with an always
encoded `Title` header. In every field, control characters, line and
paragraph separators and bidirectional controls in a value become spaces.
`accounts` holds the exact values.

## Messages no longer on the server

Identical messages are stored once, even when several users archived them.
Whether a message is gone from a server is recorded per location, and each
user only sees the locations of their own and shared accounts: in the message view, in
**Only in archive** (`gone=1`), in the tag on list rows (`gone` in the
list JSON), and in the counts on the account page and in
`GET /api/status`. If Alice deletes a message that Bob also archived, Bob
cannot tell. The log reports counts and folder names, never subjects,
addresses or UIDs.

## The secret key

`MAIL_ARCHIVE_SECRET_KEY` encrypts the stored IMAP passwords and TOTP
secrets. Keep it outside the archive backup's location or protect both
equally. If it is lost, archived mail stays readable, but every account
needs its password again and every user with TOTP a new setup. If it leaks
together with the database, the IMAP passwords are exposed: change them at
the providers.

- **From a file.** `MAIL_ARCHIVE_SECRET_KEY_FILE` names a file that holds the
  key, preferred over the variable; both set is refused. The file has the
  rules of the archive key file: refused when group or others have any
  permission on it, when it is not a regular file, and when it lies inside
  the data directory. The error names the reason, never the key. Under
  Docker Compose it lies in `SECRET_KEY_DIR`, mounted read-only into the
  web server and the command-line container only.
- **Required.** The web server refuses to start without a key (exit code 4),
  before it listens or migrates.
- **Checked at start.** The database keeps a check value of the key,
  HMAC-SHA256 with the key over a fixed text, never the key. The web server
  refuses to start with another key (exit code 4), so a wrong key is noticed
  before anything is sealed with it, instead of later as passwords that no
  longer open. The message names two fingerprints, the first 8 bytes of the
  check values of the configured and of the expected key, never a key. On
  the first start of 1.6.0 there is no check value yet: if stored IMAP
  passwords or TOTP secrets exist, the key must open at least one of up to 5
  of each, otherwise the start is refused; then the check value is recorded.
  After an intended change of the key, `./ma secret-key accept --yes` records
  the new one (audit log `instance.secret_key_accepted`, with both
  fingerprints); the passwords and TOTP secrets sealed with the old key must
  then be set again.
- **Not in other processes.** The worker gets no secret key under Docker
  Compose: it seals and opens nothing. The processes the app starts get
  none either: `pg_dump` and `pg_restore` only what the table above lists
  ([Backups without Docker](../guide/operations#backups-without-docker)),
  and the write check of the data directory that `./ma migrate` runs as the
  container user (`prepare-data --check`) only the data directory and
  `PATH`, `TZ`, `LANG` and `LC_ALL`. No `MAIL_ARCHIVE_*` or `PG*` variable of
  the app reaches a child process, apart from what it is given on purpose;
  that includes the setup token.

## The archive key

Encryption at rest protects the message files on disks, volume snapshots
and copies of the data directory, while the archive key is kept apart from
them. It does not protect against anyone who controls the running
instance: root on the host, the process memory, a modified binary, and the
operator of a hosted instance, who holds the key. The format and the key
hierarchy are in [Architecture](../development/architecture#encryption-at-rest).

- **Hierarchy.** The archive key (`MAIL_ARCHIVE_ARCHIVE_KEY_FILE`, 32 random
  bytes) and every recovery key open the keyring; the keyring holds the data
  keys, and each file is encrypted under a key derived from a data key and a
  random salt. The keyring is kept twice, as `data/keys/keyring` and in the
  database, both copies wrapped under the archive and recovery keys, so
  either one is safe in a backup and enough, with a key, to recover.
- **Custody.** The operator holds one archive key per instance. The app
  never stores it and refuses it inside the data directory, when the group
  or others can read its file, and when it equals `MAIL_ARCHIVE_SECRET_KEY`.
  Keep the key file and the recovery key that `encryption keygen` prints
  apart from the data directory, its snapshots and its backups.
- **New instances.** A new instance (no user, no message, no state of
  encryption at rest recorded yet) turns encryption at rest on at its first
  start, before anything is archived, and refuses to start without an
  archive key (exit code 4). Without a keyring file it creates one with a
  slot for the configured archive key alone; add a recovery key with
  `./ma encryption add-key`. `MAIL_ARCHIVE_ENCRYPTION_AT_SETUP=off` starts a
  new instance without encryption: for test instances only, never for a
  customer's; `./ma status` says so.
- **Loss.** Without the archive key and every recovery key, no encrypted
  message file can be read, backups included.
- **Start.** While encryption is on, every command that reads or writes
  message files refuses to start without a key that opens the keyring;
  `verify` starts, checks the attestations and then stops with the reason.
  There is no locked mode. The key stays in the process's memory while it
  runs.
- **What stays readable (accepted).** The SHA-256 of each message's
  plaintext is its identity in the database, the file names, the URLs and
  the attestation leaves: someone who holds an exact copy of a message can
  confirm that the archive holds it too. File sizes and dates stay visible.
  The database is not encrypted (see the table above); its audit entries
  stay readable also later, because the log is append-only. Encrypting the
  database content is planned for 1.6.0.
- **Corrupt files.** A plaintext message file whose content no longer
  matches its name is never encrypted by `encryption enable`: it stays
  plaintext, in `messages/` or in `orphans/`, until it is replaced from a
  backup or deleted.
- **Old plaintext.** After an existing archive is switched to encryption,
  its earlier plaintext can survive in unlinked files and free space of the
  disk, in older snapshots and in backups made before. For a clean state,
  restore an encrypted backup onto a new volume and destroy the old one.

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
