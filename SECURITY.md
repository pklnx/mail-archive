# Security Policy

## Reporting a vulnerability

The repository is private, so only its collaborators can read its issues.
Report security problems as an issue with the label `security`.

Include what you found, how to reproduce it and what an attacker could do with it.

## Supported versions

Only the latest state of the `main` branch is supported. There are no
maintained older versions.

## What to expect

This is a hobby project maintained in spare time. Reports are handled on a
best-effort basis, without fixed response times. You will be credited in the
fix unless you prefer otherwise.

CI scans every pull request and every push to `main` for committed
credentials (gitleaks, check `secret-scan`).

This project is entirely AI-written (see the [README](README.md)).
Independent review is welcome.

## Scope

In scope, for example:

- Bypassing login, sessions, TOTP, passkeys or the failed-login limits
- Accessing another user's accounts or mail
- Recovering IMAP passwords or TOTP secrets without `MAIL_ARCHIVE_SECRET_KEY`
- Any write access to the IMAP servers (the archive must stay read-only)

Out of scope:

- Setups that go against the
  [security documentation](https://pklnx.github.io/mail-archive/reference/security),
  for example exposing the web UI to the internet or running it without HTTPS
- Unencrypted archive data on disk (documented: use an encrypted disk)
- Vulnerabilities in dependencies without a demonstrated impact on this project
