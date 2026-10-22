# JSON API

The web UI is a client of this API; scripts can use it too. All responses
are JSON unless noted. Errors look like `{"error": "message"}`.

The [OpenAPI description](./openapi) lists every endpoint with its
parameters and answers. A running server serves it as `/api/openapi.json`,
without a login.

## Authentication

Every endpoint below needs a session cookie or an [API key](#api-keys) and
answers `401` without one. All of them only see the logged-in user's
accounts and the messages found in them; other users' accounts and messages
answer `404`.

### Sessions

| Endpoint | Description |
|---|---|
| `GET /api/session` | The logged-in user: `{"user": {"name": "patrick", "admin": true, "language": "auto", "locale": "auto"}}`. Without a session `401` with `{"error": "login required", "setupRequired": false}`; `setupRequired` is `true` while no user exists, `passkeyOrigin` says where passkeys work (missing when they are off). |
| `POST /api/session` | Log in with `{"username": "...", "password": "..."}`. Answers like `GET` and sets the cookie. For a user with 2FA it answers `200 {"twoFactorRequired": true, "challenge": "..."}` instead and sets no cookie; the challenge is valid for 5 minutes and once. `401` for a wrong name or password, `403` for a locked user, `429` with `Retry-After` (and `retryAfter` in seconds) after too many failures. |
| `POST /api/session/2fa` | Second step for users with 2FA: `{"challenge": "...", "code": "..."}`. `code` is a TOTP code or a recovery code. Answers like `GET` and sets the cookie. `401` for a wrong code or an expired challenge, `429` like the password step. |
| `POST /api/session/passkey/begin` | Start a passkey login: `{"token", "publicKey"}`, where `publicKey` holds the options for `navigator.credentials.get()` (binary values in base64url). `503` without `MAIL_ARCHIVE_PUBLIC_URL`, `429` after 30 starts per minute from one address. |
| `POST /api/session/passkey/finish` | `{"token": "...", "credential": {...}}` with the browser's answer in JSON. Answers like `POST /api/session` and sets the cookie. `401` for a passkey that is not accepted or an expired token; for an unknown passkey also `"unknownCredential": true` with `credentialId` and `rpId`. `403` for a locked user. |
| `DELETE /api/session` | Log out (`204`). |

With curl, keep the cookie in a file:

```sh
curl -c cookies.txt -X POST http://localhost:8080/api/session \
  -H 'Content-Type: application/json' -H 'Origin: http://localhost:8080' \
  -d '{"username": "patrick", "password": "…"}'
curl -b cookies.txt http://localhost:8080/api/status
```

### API keys

Scripts use an API key instead of a session. Admins and users with 2FA or
passkeys can use the API this way too: the second factor is checked when
the key is created, not on every request. A user creates keys in the web
UI's profile (or with `POST /api/profile/api-keys`), with the password and,
with 2FA on, a code. The key is shown once; the server keeps only its
SHA-256 and the last 4 characters.

A key looks like `ma_` followed by 52 characters from `a-z` and `2-7`. Send
it as `Authorization: Bearer ma_…`:

```sh
KEY=ma_…
# read: search, then download a message
curl -H "Authorization: Bearer $KEY" 'http://localhost:8080/api/messages?q=invoice'
curl -H "Authorization: Bearer $KEY" -o mail.eml http://localhost:8080/api/messages/SHA256/raw
# sync: start a sync of all own accounts
curl -X POST -H "Authorization: Bearer $KEY" http://localhost:8080/api/sync
```

A key has one or both scopes:

| Scope | Endpoints |
|---|---|
| `read` | `GET /api/status`, `GET /api/accounts`, `GET /api/messages` (list and search) and every `GET /api/messages/{id}…` endpoint |
| `sync` | `POST /api/sync`, `POST /api/accounts/{name}/sync` |

`sync` does not include `read`: a cron job that only starts syncs needs no
access to mail. Everything else (logins, the profile, users, adding and
changing accounts, listing server folders) is for sessions only. A key does
no more than its owner could in the web UI: an admin's key reaches only the
admin's own accounts, like a session.

| Case | Status |
|---|---|
| Unknown, malformed, revoked or expired key, or its owner is locked or removed | `401` `{"error": "invalid API key"}` with `WWW-Authenticate: Bearer … error="invalid_token"` |
| Endpoint outside the key's scopes | `403` `{"error": "this API key lacks the sync scope", "scope": "sync"}` |
| Endpoint for sessions only | `403` `{"error": "not available with an API key"}` |
| The owner must change their password or set up 2FA | `403` with `passwordChangeRequired` or `twoFactorSetupRequired`, as for a session |
| More than 600 requests per key and minute, or more than 30 invalid keys per client address and minute | `429` with `Retry-After` |

A request with `Authorization: Bearer ma_…` never uses the session cookie,
also when the key is invalid. Other `Authorization` headers, such as Basic
from a reverse proxy, are ignored, and the cookie counts as usual.

Locking a user suspends their keys; unlocking makes them work again. An
admin's password reset revokes them unless asked not to; the user's own
password change and an admin's 2FA reset keep them (after a 2FA reset they
answer `403` until 2FA is set up again). Removing a user deletes their keys.
Revoked and expired keys stay listed for 30 days.

## Users and profile

The user endpoints are only for admins; other users get `403`. Admins cannot
change their own login with them (`403`); they use the profile endpoint.

| Endpoint | Description |
|---|---|
| `GET /api/users` | All users: `name`, `admin`, `locked`, `mustChangePassword`, `twoFactorEnabled`, `passkeys` (a count), `apiKeys` (active keys), `accounts` (a count), `createdAt`, `lastLoginAt`, `self`. |
| `POST /api/users` | Add a user with `{"name": "...", "admin": false}`. Answers `201 {"name", "password"}` with a generated password, shown only here. |
| `POST /api/users/{name}/password` | Generate a new password (`{"name", "password"}`). The user is logged out and must change it at the next login. Their passkeys are removed and their API keys revoked too, unless the body says `{"removePasskeys": false}` or `{"revokeApiKeys": false}`. |
| `PATCH /api/users/{name}` | Exactly one of `{"admin": true\|false}` or `{"locked": true\|false}`. `409` for the last admin. |
| `POST /api/users/{name}/2fa/reset` | Turn off the user's 2FA and end their sessions (`204`). |
| `DELETE /api/users/{name}/passkeys` | Remove all passkeys of another user and end their sessions (`204`). |
| `DELETE /api/users/{name}` | Remove a user, with their API keys. `409` while they own accounts, or for the last admin. |
| `GET /api/api-keys` | Every user's API keys, like `GET /api/profile/api-keys` plus `user`. Never the keys themselves. |
| `DELETE /api/api-keys/{id}` | Revoke any user's key, also an own one (`204`). |
| `GET /api/profile/2fa` | `{"enabled", "required", "admin", "setupPending"}`. |
| `POST /api/profile/2fa/setup` | Start setup: `{"secret", "otpauthUri", "qrDataUrl"}` (a PNG data URL). `409` while 2FA is on. |
| `POST /api/profile/2fa/confirm` | Finish setup with `{"code": "..."}`. Answers `{"recoveryCodes": [...]}`, shown only here. `401` for a wrong code. |
| `POST /api/profile/2fa/recovery-codes` | New recovery codes with `{"code": "..."}`; the old ones stop working. |
| `DELETE /api/profile/2fa` | Turn 2FA off with `{"currentPassword": "...", "code": "..."}` (`204`). `403` when 2FA is required for this user. |
| `GET /api/profile/passkeys` | Own passkeys: `{"available", "origin", "max", "passkeys": [{"id", "name", "createdAt", "lastUsedAt"}]}`. |
| `POST /api/profile/passkeys/begin` | Start adding a passkey: `{"name": "...", "currentPassword": "...", "code": "..."}`; `code` (TOTP or recovery code) only with 2FA on. Answers `{"token", "publicKey"}` for `navigator.credentials.create()`. `401` for a wrong password or code (counts like a failed login), `409` for a name in use or 10 passkeys. |
| `POST /api/profile/passkeys/finish` | `{"token", "credential"}` → `201 {"name"}`. `409` for a passkey that is already registered or the limit, `400` for an expired token or a passkey that is not accepted. |
| `DELETE /api/profile/passkeys/{id}` | Remove an own passkey; all other sessions end. Answers `{"credentialId", "rpId"}` for `PublicKeyCredential.signalUnknownCredential()`. |
| `GET /api/profile/api-keys` | Own API keys, also revoked and expired ones for 30 days: `{"max", "keys": [{"id", "name", "hint", "scopes", "createdAt", "expiresAt", "lastUsedAt", "revokedAt", "state"}]}`. `hint` is the last 4 characters, `state` is `active`, `expired` or `revoked`. |
| `POST /api/profile/api-keys` | Create a key: `{"name": "...", "scopes": ["read", "sync"], "expiresInDays": 90, "currentPassword": "...", "code": "..."}`. `expiresInDays` from 1 to 3650, or `null` for no expiry; `code` only with 2FA on. Answers `201` with the listed fields and `key`, shown only here. `401` for a wrong password or code (counts like a failed login), `409` for a name in use or 20 active keys, `400` for an unknown scope. |
| `DELETE /api/profile/api-keys/{id}` | Revoke an own key (`204`). No password needed. |
| `PATCH /api/profile` | Change the own web UI settings: `{"language": "de", "locale": "de-AT"}`, one or both. `language` is `auto` or a language code (`de`, `pt-BR`), `locale` is `auto` or a BCP 47 tag for dates and numbers. The UI treats values it has no use for like `auto`. Answers `204`, also when nothing changed; `400` for an empty body, an unknown field or an invalid value. The settings are read from `user` in `GET /api/session`. |
| `PUT /api/profile/password` | Every user: `{"current": "...", "new": "..."}`. `401` for a wrong current password (counts like a failed login, then `429`), `400` for a new password that is too short or unchanged. Other sessions end. |

After logging in with a generated password, `GET /api/session` reports
`"mustChangePassword": true`, and every other endpoint except
`PUT /api/profile/password` answers `403` with `"passwordChangeRequired": true`.

A user who must use 2FA but has not set it up (admins always, everyone with
`MAIL_ARCHIVE_REQUIRE_2FA=true`) gets `"twoFactorRequired": true` and
`"twoFactorEnabled": false` from `GET /api/session`. Until setup is done,
every endpoint except `GET /api/profile/2fa`, `POST /api/profile/2fa/setup`
and `POST /api/profile/2fa/confirm` answers `403` with
`"twoFactorSetupRequired": true`. The password change comes first.

## Write requests

`POST`, `PATCH` and `DELETE` requests with a session are only accepted with:

- `Content-Type: application/json` (also for requests without a body), and
- an `Origin` header that matches the server. Browsers send it on their own;
  with curl, add it yourself.

```sh
curl -b cookies.txt -X POST http://localhost:8080/api/sync \
  -H 'Content-Type: application/json' -H 'Origin: http://localhost:8080'
```

Requests with an API key need neither: the key is a header that a web page
on another site cannot add, unlike a cookie, which the browser sends on its
own. A body, where an endpoint reads one, must still be JSON.

## Audit log

Searches, opened messages, downloads, logins and changes are recorded in the
[audit log](../guide/audit-log). If an entry cannot be written, the request
answers `503` with `{"error": "the audit log cannot be written; nothing was
done"}` and nothing happens: no message data is sent, no change is saved, no
session is created. A logout always works.

| Endpoint | Description |
|---|---|
| `GET /api/audit` | The log, newest first: `{"entries": [{"id", "at", "actor", "user", "apiKey", "action", "target": {"type", "id", "name", "subject"}, "clientIp", "userAgent", "details"}], "nextCursor"}`. A user gets their own entries; admins every user's. `subject` is the subject of a target message, only if the viewer has that message too. Parameters: `since` (default 30 days ago) and `until` (default now; a plain date includes that day) as `YYYY-MM-DD` or RFC 3339, `action` (repeatable: a group `login`, `search`, `read`, `export`, `accounts`, `users`, `profile`, `api_keys`, `sync`, `retention`, or an action such as `message.open`), `cursor` (the `nextCursor` of the previous page), `limit` (1–500, default 100). Admins only: `user` (a user's name; `404` if there is none) and `actor` (`session`, `api_key` or `cli`); from other users `403`. Sessions only, not with an API key. Reading the log writes no entry. |

Account management and sync need `MAIL_ARCHIVE_SECRET_KEY` on the server;
without it these endpoints answer `503` and `GET /api/accounts` reports
`"manage": false`. See [Security](./security) for why.

## Retention and legal holds

Admins only (`403` otherwise), sessions only. Rules alone delete nothing.
See [`retention` and `hold`](./cli#retention) on the command line.

| Endpoint | Description |
|---|---|
| `GET /api/retention/rules` | `{"rules": [{"id", "accountId", "account", "accountKind", "accountRemoved", "owner", "folder", "years", "timeZone", "note", "createdAt", "createdBy"}], "accounts": [{"id", "name", "kind", "removed", "owner", "folders"}], "minYears", "defaultTimeZone"}`. `folder` is empty for every folder; `createdBy` is empty for the command line. `accounts` lists every account to choose from. |
| `POST /api/retention/rules` | `{"accountId", "folder", "years", "timeZone", "note", "force"}`: `201 {"id"}`. `years` from 1 to 100; under `minYears` (6) only with `force: true`. `timeZone` defaults to `Europe/Berlin`. `409` if the account (or folder) has a rule, `400` for an unknown account or time zone. |
| `DELETE /api/retention/rules/{id}` | Remove a rule (`204`). |
| `GET /api/legal-holds[?released=1]` | `{"holds": [{"id", "name", "reason", "user", "accountId", "account", "accountOwner", "filter", "since", "until", "matchedAt", "matched", "createdAt", "createdBy", "releasedAt", "releasedBy", "releaseNote"}]}`, active ones, and released ones too with `released=1`. |
| `POST /api/legal-holds` | `{"name", "reason", "user", "accountId", "filter": {"q", "from", "to", "attachment", "hasAttachment"}, "since", "until", "all"}`: `201 {"id", "matched"}`. Every condition that is set must match; `until` is exclusive. A filter is matched at once (`matched`); until then the hold covers its whole scope. Without any condition the hold covers the whole archive and needs `all: true`. |
| `POST /api/legal-holds/{id}/release` | `{"note"}` (required): `204`. `409` if it is released already. |
| `GET /api/retention/runs` | The last 50 [deletion runs](./cli#deletion), newest first: `{"runs": [{"id", "state", "createdAt", "createdBy", "candidates", "bytes", "confirmedAt", "confirmedBy", "finishedAt", "deleted", "kept"}]}`. `state` is `proposed`, `confirmed` (deleting), `done` or `cancelled`. |
| `POST /api/retention/runs` | Propose a run (`201`, the run). Deletes nothing. `409` while another run is open. |
| `GET /api/retention/runs/{id}` | `{"run", "groups": [{"owner", "account", "folder", "year", "messages"}], "candidates": [{"sha256", "size", "retainUntil", "state"}], "listed"}`: the pending candidates by place and the year their period ended, and up to 1000 candidates by hash. No subjects or senders; `retention show` on the command line has them. |
| `POST /api/retention/runs/{id}/confirm` | `{"count"}`, the run's number of candidates: `202`, and the web server deletes in the background; poll the run. `400` for another number, `409` if the run is not proposed. |
| `POST /api/retention/runs/{id}/cancel` | Drop a proposed run (`204`). |

## Messages

| Endpoint | Description |
|---|---|
| `GET /api/messages` | List or search messages, newest first. |
| `GET /api/messages/{id}` | Headers, plain text, attachment list and every location (account, folder, UID, flags, `superseded`, `goneAt`, `lastSeenAt`). A superseded location is from before the folder's `UIDVALIDITY` changed (the server renumbered it); current locations come first. `goneAt` is when a [reconcile](../guide/syncing#reconcile) found the location missing on the server (`null` while it is there or before the folder was reconciled); `lastSeenAt` is when the server last listed it. Also `thread` (the thread key) and, for a reply, `inReplyTo` (the Message-ID it answers, without angle brackets). A location in an smtp account has an `envelope`: `mailFrom`, `rcptTo` (the original recipients, BCC included), `receivedAt`, `clientIp` and `helo`; other locations have `null`. `filled` is true for a location in an smtp account without an envelope: a [fill](#fills) copied it from IMAP or [`import --journal`](../guide/importing#into-an-smtp-account) imported it from files, so BCC recipients are unknown. `fill` names that fill and its mailbox, `{"id", "username", "host"}`; `import` names that import of files and its label, `{"id", "label"}`. Both are `null` for other locations, and when unknown. `retainUntil` is when the [retention period](./cli#retention) ends, per location and for the message (the latest over the user's locations); `null` when no rule covers a location, so it is kept for ever. Admins also get `onHold`. |
| `GET /api/messages/{id}/conversation` | The conversation of the message; see below. `404` like `GET /api/messages/{id}`. |
| `GET /api/messages/{id}/html[?images=1]` | The HTML body for a sandboxed iframe. Scripts are blocked; remote images only with `images=1`. |
| `GET /api/messages/{id}/raw` | The original `.eml`. |
| `GET /api/messages/{id}/parts/{n}` | One attachment. PNG, JPEG, GIF and WebP are shown inline; everything else is a download. |

`{id}` is the SHA-256 of the message file.

Query parameters of `GET /api/messages`:

| Parameter | Description |
|---|---|
| `q` | Full-text query over subject, sender and body; see [Search](../guide/web-ui#search). Prefixes such as `to:` are parsed by the web UI, not here. |
| `account`, `folder` | Only messages found in this account or folder. |
| `from` | Part of the sender's name or address, in any case. |
| `to` | Part of a name or address in To or Cc, in any case, or of an envelope recipient of the user's smtp accounts (which includes BCC recipients). Bcc headers are not stored. |
| `attachment` | Part of an attachment's file name, in any case. Implies `has=attachment`. |
| `has` | Only `attachment`: messages with at least one attachment. |
| `after`, `before` | Date range. `after` is inclusive, `before` exclusive. `YYYY-MM-DD` means midnight UTC; RFC 3339 with an offset (`2024-01-01T00:00:00+01:00`) sets another midnight. |
| `group` | `1`: one row per conversation, the newest message of it that matches all other parameters. `0` or empty: one row per message. |
| `thread` | Only the messages of this conversation: a `thread` value from a grouped row or a message. At most 2000 bytes. |
| `gone` | `1` or `true`: only messages no longer on any server. They have locations in the user's IMAP accounts (with `account`, in that account), and all of those are gone. Messages only in import accounts are never listed. |
| `limit` | Page size, 1 to 200, default 50. |
| `cursor` | The `nextCursor` of the previous page. |

`from`, `to` and `attachment` take at most 200 characters each. All filters
combine. `400` for a longer value, a `has` other than `attachment`, a
`group` other than `0` or `1`, a `gone` other than `1`, `true`, `0` or
`false`, a longer `thread`, an invalid date, or
`after` later than `before`.

The response is `{"messages": [...], "nextCursor": "..." | null}`. Each
message has `id`, `size`, `subject`, `from`, `sentAt`, `sortAt`,
`hasAttachment` and, for searches, a `snippet` in which matches are marked
with U+E000 (start) and U+E001 (end). With `group=1` each row also has
`thread` and `count`, the number of the conversation's messages that match;
`thread=` with the same other parameters lists exactly those. The cursor
works the same in both modes.

Recipients, attachment names, `hasAttachment` and conversations of messages
archived before they were recorded are empty until `reindex` has run; see
[Upgrades](../guide/operations#one-time-steps). Until then, each such
message is a conversation of its own.

### Conversations

A message's conversation (its thread key) is the first ID in its
`References` header, else its `In-Reply-To`, else its own `Message-ID`, else
the message alone. Subjects are not used.

`GET /api/messages/{id}/conversation` answers
`{"messages": [...], "total": N, "truncated": false}`, oldest first: the
messages of the conversation, plus the message it answers and its replies
even when they belong to another conversation (a reply with only an
`In-Reply-To` to a message in the middle of a conversation). Each entry has
`id`, `subject`, `from`, `sentAt`, `sortAt` and `relation`: `self`, `parent`
(the message it answers), `reply` (answers it or names it in `References`)
or `thread`. At most the newest 200 are listed; `total` counts all, and
`truncated` says that older ones were left out.

Only messages found in the user's own accounts are listed or counted, in
conversations, grouped rows and `thread=` alike.

## Accounts

| Endpoint | Description |
|---|---|
| `GET /api/accounts` | All accounts with `kind` (`imap`, `import` for mail imported from files, or `smtp` for mail a mail server delivers over SMTP), server settings (never the password), folders with message counts and the hierarchy `delimiter` (`/` or `.` as the IMAP server reports it, `""` if it has none, `null` until the folder's first sync after the upgrade; always `/` for import and smtp accounts), and the sync state. Import and smtp accounts have an empty `host`, `tls` and `username` and `port` 0; import accounts have `enabled` false. smtp accounts list their `addresses` and `lastReceivedAt`, when they last accepted a message (`null` before the first); other kinds have `[]` and `null`. The response's `smtp` is `{}` when the SMTP server is on and `null` when `MAIL_ARCHIVE_SMTP_LISTEN` is empty. smtp accounts also have `fill`, their latest [fill](#fills) (`null` without one), `fills`, their recurring fills and the latest one, and `firstReceived`, the first message they received that is still stored (`{"at", "subject"}`, `null` without one); other kinds have `null`, `[]` and `null`. `purging` is `true` while a removed account is being [deleted completely](../guide/accounts#deleting-a-removed-account-completely). |
| `POST /api/accounts` | Add an account. The login is checked first; the first sync starts right away. With `{"kind": "smtp", "name": "journal", "addresses": ["archive@archive.local"]}` (admins only, otherwise `403`) it adds an smtp account: no server fields, no login check; `409` names an address another account receives for. |
| `PATCH /api/accounts/{name}` | Change some fields, including `name` to rename the account. `addresses` replaces an smtp account's list (admins only; `409` on other kinds). A new connection or password is checked with a login first; if that fails, nothing changes. All fields are saved together or not at all. `409` when the name is taken or when the account was changed since it was loaded (reload and try again). Works while the account is being synced; the change applies from the next sync. |
| `DELETE /api/accounts/{name}` | Remove the account. Returns `{"result": "removed"}` if archived mail was kept, `{"result": "deleted"}` if the account had none. `409` while the account is being synced or filled. |
| `GET /api/accounts/{name}/purge` | Admins only (`403`), own accounts only (other users' are `404`). What [deleting the removed account completely](../guide/accounts#deleting-a-removed-account-completely) would do, without changing anything: `locations` removed, `messages`, `deleted` (only in this account; the number to type) with `deletedBytes`, `kept` (also in other accounts) with `keptIn` (`[{"owner", "account", "messages"}]`), `grace` (`MAIL_ARCHIVE_RETENTION_FILE_GRACE`, `"168h0m0s"`), `blockers` (`notRemoved`, `syncing`, `purgeOpen`, `rules` `[{"id", "folder", "years"}]`, `holds` `[{"id", "name", "released", "namesAccount"}]`, and `messages` naming them) and `purge`, the account's confirmed purge while it is being executed (`{"id", "state", "expected", "locations", "deleted", "kept"}`, otherwise `null`). |
| `POST /api/accounts/{name}/purge` | Confirm it with `{"count": N}`, the preview's `deleted`. Answers `202` with `{"purge": id}`; the web server's retention job deletes in the background, and `purging` in `GET /api/accounts` is `true` until it is done. `400` without `count`; `409` names the blocking rules and holds, an open purge or a changed account, or says the number changed (with `deleted`, the current number: review again); `503` without the retention job. |
| `GET /api/accounts/{name}/server-folders` | The account's folders, live from the IMAP server: `name`, `specialUse` (role) and `selected`. |

Import accounts only take a rename and a `DELETE`; smtp accounts also take
`enabled`. These answer `409` `import and smtp accounts have no server: …`
for them: sync, `server-folders`, and a `PATCH` with other fields (for import
accounts, `enabled` too). The addresses of an smtp account are changed with
`./ma account smtp-addresses`.

Body of `POST` and `PATCH` (in a `PATCH`, missing fields keep their value):

```json
{
  "name": "personal",
  "host": "imap.mail.de",
  "port": 993,
  "tls": "tls",
  "username": "me@mail.de",
  "password": "…",
  "enabled": true,
  "excludedFolders": ["Spam", "Trash"]
}
```

`name` renames the account in a `PATCH`. In a `POST`, if the server marks
folders as trash or spam and they would be archived, the account is not
saved: the answer is `409` with
`{"error": "…", "suggestedExclusions": ["Trash", "Spam"]}`. Send the request
again with `"confirmFolders": true`, with or without those folders in
`excludedFolders`. `excludedFolders` replaces the folder
selection: everything except these folders is archived.

`syncInterval` sets an IMAP account's own sync interval: `default`, `manual`
or a Go duration of whole minutes such as `30m` or `1h30m`, from `5m` (or
`syncIntervalMin`, if higher) to `720h`. Other values answer `400`; import and
smtp accounts `409`. A value equal to the stored one is accepted and changes
nothing, so a value stored before `MAIL_ARCHIVE_SYNC_INTERVAL_MIN` was raised
can be sent back. A change needs no login check and applies at the next
minute's check of the schedule.

### Fills

A fill copies an IMAP mailbox into an smtp account's folder `JOURNAL`: once,
the mail from before
[journaling](../guide/journaling#bring-in-the-mail-from-before-journaling)
started, or on a schedule, what arrives in the mailbox later and never
passed the journal ([recurring fill](../guide/journaling#keep-importing-recurring-fill)).
These endpoints need a session (not an API key), only work on the
user's own smtp accounts (`409` on other kinds) and answer `503` without
`MAIL_ARCHIVE_SECRET_KEY`.

| Endpoint | Description |
|---|---|
| `POST /api/accounts/{name}/fills/folders` | The folders of the IMAP mailbox, before a fill: body `{"host", "port", "tls", "username", "password"}`. Answers `{"folders": [{"name", "specialUse", "selected"}], "suggestedExclusions": [...]}` with the folders the server marks as trash or spam. `422` when the login fails. |
| `POST /api/accounts/{name}/fills` | Start a fill. The login is checked first (`422`). Answers `202` with `{"id"}`; the web server runs it in the background, one fill at a time. `409` when a fill of the account runs or folders need confirming (as for `POST /api/accounts`: `suggestedExclusions`, then again with `"confirmFolders": true`). Also for a disabled account. |
| `PATCH /api/accounts/{name}/fills/{id}` | Change a fill. `{"keepRunning": true, "password", "interval", "confirmFolders"}` switches a fill that ended on (its first run scans its IMAP folders again and copies what the cut-off left out): the password is required and checked first (`422`), trash and spam folders it has not copied need `confirmFolders` (`409` with `suggestedExclusions`), and it answers `202` with `{"id"}` and queues the first run. `{"keepRunning": false}` stops a recurring fill, like `cancel`. `{"interval": "12h"}` alone changes the interval of a fill that keeps running (`204`; `400` below `MAIL_ARCHIVE_SYNC_INTERVAL_MIN`). `409` while the fill runs, when it already keeps running (or, to stop it or change the interval, does not). A password only goes with `"keepRunning": true`: to give a recurring fill a new one, stop it and resume it. |
| `POST /api/accounts/{name}/fills/{id}/resume` | Continue a fill that failed, partly failed or was cancelled, with `{"password"}` (checked first, `422`). A stopped recurring fill keeps running again. `202`; `409` when it runs, ended well or keeps running. |
| `POST /api/accounts/{name}/fills/{id}/cancel` | Stop a running fill after its current batch, or a recurring fill, and wipe its password. `204`; `409` when it neither runs nor keeps running. What it copied stays. |

Body of `POST /api/accounts/{name}/fills`:

```json
{
  "host": "imap.example.com",
  "port": 993,
  "tls": "tls",
  "username": "alice@example.com",
  "password": "…",
  "excludedFolders": ["Spam"],
  "before": "2026-10-01T00:00:00Z",
  "confirmFolders": true
}
```

or, for a recurring fill, `"keepRunning": true` and `"interval": "12h"`
instead of `before`.

`port` defaults to 993 with `tls`, else 143; `tls` to `tls`. `includedFolders`
limits the fill to these folders. Every IMAP folder goes into `JOURNAL`; a
message filed in several folders is stored once. `folderPrefix` is no longer
supported: a non-empty value is answered `400`. `before` is the cut-off,
by default now: a message is copied when its INTERNALDATE is before the
cut-off plus one hour, unless the account received a message with the same
Message-ID over SMTP. The password is kept, encrypted, only while the fill
runs.

With `"keepRunning": true` the fill is recurring and has no cut-off (`before`
together with it is `400`): a message is copied once its INTERNALDATE is two
hours old, unless the account received it over SMTP. `interval` is
`default` (the server's `MAIL_ARCHIVE_SYNC_INTERVAL`) or a duration of whole
minutes from `5m` to `720h`, not below `MAIL_ARCHIVE_SYNC_INTERVAL_MIN`. The
web server runs it every interval, counted from the start of its last run,
but not in the first 70 minutes after the server started. Its password is
kept until it is stopped.

While a fill waits or runs, the account's `sync.state` is `queued` or
`running`. `fills` in `GET /api/accounts` lists the account's recurring fills
(also stopped ones) and its latest fill, newest first; `fill` is the latest
one. Neither carries a password:

```json
{
  "id": 3,
  "status": "running",
  "host": "imap.example.com",
  "port": 993,
  "tls": "tls",
  "username": "alice@example.com",
  "includedFolders": [],
  "excludedFolders": ["Spam"],
  "folderPrefix": "",
  "before": "2026-10-13T08:00:00Z",
  "keepRunning": false,
  "recurringSince": null,
  "runStartedAt": null,
  "interval": null,
  "every": null,
  "nextAt": null,
  "health": null,
  "unconfirmedFolders": [],
  "error": "",
  "createdAt": "2026-10-13T08:00:00Z",
  "finishedAt": null,
  "folders": [
    {"source": "INBOX", "folder": "JOURNAL", "done": true, "error": "",
     "scanned": 1200, "imported": 1150, "new": 1150, "afterCutoff": 12, "journaled": 38, "present": 0, "deleted": 0,
     "waiting": 0}
  ]
}
```

`status` is `running`, `ok`, `partial` (some folders failed; see their
`error`), `failed` or `cancelled`. `folders` lists the folders started so far:
`source` is the IMAP folder, `folder` the archive folder, `JOURNAL`. Fills
started before fills wrote into `JOURNAL` have a `folderPrefix` and write
into `PREFIX/<IMAP folder>`; for newer fills it is empty.
The counters: `scanned` headers read, `imported` messages copied (`new` of
them new to the archive), `afterCutoff` left to journaling, `journaled`
skipped because the account received them over SMTP, `present` already in
`JOURNAL` (a resumed fill, or a message in several IMAP folders), `deleted`
deleted by retention before.

A recurring fill has `keepRunning` true until it is stopped,
`recurringSince` (when it became recurring), `runStartedAt` (the start of the
current or last run), `interval` (its own: `default` or a duration such as
`12h0m0s`), `every` (the effective interval) and `nextAt` (the next run; in
the past when due, the end of the 70 minutes while a run waits after the
server's start). `every` and `nextAt` are `null` once it is stopped or when
the schedule is off. Its `status`, `error` and counters are those of the
current or last run, and `waiting` counts messages younger than two hours
that the next run copies. `health` is
`{"state", "failureStreak", "failingSince", "lastSuccessAt"}` like an
account's [sync state](#sync-state), `null` for other fills.
`unconfirmedFolders` are trash and spam folders that appeared after the fill
started; its runs skip them. A stopped recurring fill is `cancelled`.

### Sync state

Each account in `GET /api/accounts` has a `sync` object:

```json
{
  "state": "running",
  "lastRun": {
    "startedAt": "2026-10-06T08:29:16Z",
    "finishedAt": null,
    "status": "running",
    "fetched": 120,
    "new": 87,
    "reconciledFolders": 0,
    "gone": 0,
    "back": 0,
    "flagsChanged": 0
  },
  "failureStreak": 0,
  "failingSince": null,
  "health": "ok"
}
```

`state` is `idle`, `queued` or `running` (also when the command line syncs
the account). `status` of the last run is `running`, `ok`, `partial` or
`failed`, with `error` set for the latter two. `failureStreak` counts the
failed syncs in a row and `failingSince` is when the first of them started
(`null` without a streak). `health` is `failing` when the last `alertAfter`
syncs failed, `stale` when no sync succeeded within two of the account's
sync intervals (at least one plus an hour; never while it is running or
queued, a running sync counts from its start), else `ok`; disabled accounts
and accounts the schedule does not sync are always `ok`. See
[Syncing](../guide/syncing#alerts). `syncEvery` is how often the schedule
syncs the account after the default and the floor (`1h0m0s`), `null` when it
does not (not on the schedule, schedule off, removed, import or smtp);
`nextAt` is when it syncs next, in the past while it waits for its turn, and
`null` without `syncEvery` or when the account is disabled.
`reconciledFolders` counts the folders the run compared with the server;
`gone`, `back` and `flagsChanged` count the locations it marked as gone,
found again, and gave new flags.

Each account also has `syncInterval`, its own sync interval: `default`
(the server's), `manual` (not on the schedule) or a Go duration such as
`30m0s`; always `default` for import and smtp accounts. Each account also
has `goneMessages`, the number of its messages whose
locations in this account are all gone from the server, and
`lastReconciledAt`, the latest reconcile of any of its folders (`null` if
never). Each folder has `lastReconciledAt` too.

The response also has `manage` (see above), `syncInterval`, the server's
`MAIL_ARCHIVE_SYNC_INTERVAL` (the default for accounts without their own) as
a Go duration such as `6h0m0s`, or empty when the schedule is off,
`syncIntervalMin`, the floor for every account (`5m0s`; empty when the
server cannot sync), `reconcileInterval`
(`24h0m0s`, or empty when folders are only reconciled on request), and
`alertAfter`
(`MAIL_ARCHIVE_ALERT_AFTER_FAILURES`). For admins it also has
`otherFailing`: how many accounts of other users are failing, as a number
only.

## Sync

| Endpoint | Description |
|---|---|
| `POST /api/accounts/{name}/sync` | Queue a sync of one account, also a disabled one. Answers `202`. With the body `{"reconcile": true}` the sync also reconciles every folder not reconciled in the last 5 minutes. A sync already waiting is upgraded, not queued twice. `409` for import and removed accounts. |
| `POST /api/sync` | Queue all enabled IMAP accounts. Answers `202` with `{"queued": n}`. |
| `GET /api/status` | Messages per account with its `kind`, the last sync or import, `failureStreak`, `health` and `goneMessages`, like `./ma status`. |

## Health checks

These need no login and no `/api/` prefix. Like every request, they need an
allowed `Host`.

| Endpoint | Description |
|---|---|
| `GET /healthz` | `200 ok` while the server runs. |
| `GET /healthz/sync` | `200 {"status":"ok"}`, or `503` with `{"status":"degraded","failing":[…],"stale":[…]}` (account IDs only; a failing or stale recurring fill lists its smtp account) or `{"status":"unavailable"}`. Cached for 15 seconds. See [Monitoring and alerts](../guide/operations#healthz-sync). |
