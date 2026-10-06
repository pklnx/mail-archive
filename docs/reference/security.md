# Security

## What is protected how

| Asset | Protection |
|---|---|
| IMAP passwords | Encrypted in PostgreSQL with AES-256-GCM, bound to the account name, using `MAIL_ARCHIVE_SECRET_KEY`. API responses never contain them. |
| Archived mail | Plain `.eml` files and PostgreSQL rows, **not encrypted**. Use an encrypted disk. |
| Your mail servers | Only read: `EXAMINE` and `BODY.PEEK[]`. |
| The web UI | Login with user name and password, sessions in PostgreSQL, limits on failed logins, and the browser protections below. |
| User passwords | Only stored as Argon2id hashes (64 MiB, 3 passes). |

## Login

Every API request needs a login, except the login itself and `/healthz`. The
page code (`/`, `/assets/`) is public; it contains no data.

- **Users** are created with `./ma user add NAME` (`--admin` for admins). There
  is no self-registration. Passwords need at least 12 characters; there are no
  other rules, so a long passphrase is fine.
- **Sessions** end after 7 days without use and after 30 days at the latest.
  Logging out, a new password (`./ma user set-password`), locking
  (`./ma user lock`) and removing a user end their sessions at once. The
  browser only gets a random token in an `HttpOnly`, `SameSite=Strict` cookie;
  the database stores its SHA-256, so a database copy contains no usable
  sessions.
- **Failed logins:** after 5 failures for a user name within 15 minutes, the
  name is blocked for 1 minute, then 2, 4, 8 and at most 15 minutes for every
  further failure. 20 failures from one address within 15 minutes block that
  address for 15 minutes. Wrong names and wrong passwords get the same answer
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

With `MAIL_ARCHIVE_ALLOWED_HOSTS=localhost,127.0.0.1,archive.home.example`.
The proxy must pass the original `Host` header (Caddy does by default);
otherwise the server's same-origin check rejects changes.

### Plain LAN access

`WEB_BIND=0.0.0.0` and the computer's LAN address or name in
`MAIL_ARCHIVE_ALLOWED_HOSTS` work without a proxy, but without HTTPS anyone in
the network who can read the traffic can take over a session. Use this only
in a network you fully trust.

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
