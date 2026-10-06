# Security

## What is protected how

| Asset | Protection |
|---|---|
| IMAP passwords | Encrypted in PostgreSQL with AES-256-GCM, bound to the account name, using `MAIL_ARCHIVE_SECRET_KEY`. API responses never contain them. |
| Archived mail | Plain `.eml` files and PostgreSQL rows, **not encrypted**. Use an encrypted disk. |
| Your mail servers | Only read: `EXAMINE` and `BODY.PEEK[]`. |
| The web UI | No login yet. Reachable from this computer only, with the protections below. |

## No login yet

The web server has no user accounts. Docker Compose publishes it on
`127.0.0.1`, so only programs on the same computer can reach it. **Do not
make it reachable from a network** (port forwarding, a reverse proxy, binding
to `0.0.0.0`) until login through OpenID Connect is available.

"Only localhost" is not enough on its own, because every website open in your
browser runs on your computer too. The server therefore adds:

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

### HTML mail

HTML bodies are served for an iframe with a strict Content-Security-Policy
and the `sandbox` directive: no scripts, no forms, no plugins, no access to
the API. Remote content is blocked unless you click **Load remote images**
for one message. Attachments are served with `sandbox` as well; only common
image types are shown inline, everything else is a download.

### Login checks

Adding or changing an account makes the server connect to the IMAP host and
port given in the request. While the server is reachable only from your own
computer this is harmless; it will be revisited together with login.

## The secret key

`MAIL_ARCHIVE_SECRET_KEY` encrypts the stored passwords. Keep it outside the
archive backup's location or protect both equally. If it is lost, archived
mail stays readable, but every account needs its password again. If it
leaks together with the database, the IMAP passwords are exposed: change them
at the providers.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting on the
[repository](https://github.com/pklnx/mail-archive/security) instead of a
public issue.
