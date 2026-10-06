# JSON API

The web UI is a client of this API; scripts can use it too. All responses
are JSON unless noted. Errors look like `{"error": "message"}`.

## Login

Every endpoint below needs a session cookie and answers `401` without one.
All of them only see the logged-in user's accounts and the messages found in
them; other users' accounts and messages answer `404`.

| Endpoint | Description |
|---|---|
| `GET /api/session` | The logged-in user: `{"user": {"name": "patrick", "admin": true}}`. Without a session `401` with `{"error": "login required", "setupRequired": false}`; `setupRequired` is `true` while no user exists. |
| `POST /api/session` | Log in with `{"username": "...", "password": "..."}`. Answers like `GET` and sets the cookie. For a user with 2FA it answers `200 {"twoFactorRequired": true, "challenge": "..."}` instead and sets no cookie; the challenge is valid for 5 minutes and once. `401` for a wrong name or password, `403` for a locked user, `429` with `Retry-After` (and `retryAfter` in seconds) after too many failures. |
| `POST /api/session/2fa` | Second step for users with 2FA: `{"challenge": "...", "code": "..."}`. `code` is a TOTP code or a recovery code. Answers like `GET` and sets the cookie. `401` for a wrong code or an expired challenge, `429` like the password step. |
| `DELETE /api/session` | Log out (`204`). |

With curl, keep the cookie in a file:

```sh
curl -c cookies.txt -X POST http://localhost:8080/api/session \
  -H 'Content-Type: application/json' -H 'Origin: http://localhost:8080' \
  -d '{"username": "patrick", "password": "…"}'
curl -b cookies.txt http://localhost:8080/api/status
```

## Users and profile

The user endpoints are only for admins; other users get `403`. Admins cannot
change their own login with them (`403`); they use the profile endpoint.

| Endpoint | Description |
|---|---|
| `GET /api/users` | All users: `name`, `admin`, `locked`, `mustChangePassword`, `twoFactorEnabled`, `accounts` (a count), `createdAt`, `lastLoginAt`, `self`. |
| `POST /api/users` | Add a user with `{"name": "...", "admin": false}`. Answers `201 {"name", "password"}` with a generated password, shown only here. |
| `POST /api/users/{name}/password` | Generate a new password (`{"name", "password"}`). The user is logged out and must change it at the next login. |
| `PATCH /api/users/{name}` | Exactly one of `{"admin": true\|false}` or `{"locked": true\|false}`. `409` for the last admin. |
| `POST /api/users/{name}/2fa/reset` | Turn off the user's 2FA and end their sessions (`204`). |
| `DELETE /api/users/{name}` | Remove a user. `409` while they own accounts, or for the last admin. |
| `GET /api/profile/2fa` | `{"enabled", "required", "admin", "setupPending"}`. |
| `POST /api/profile/2fa/setup` | Start setup: `{"secret", "otpauthUri", "qrDataUrl"}` (a PNG data URL). `409` while 2FA is on. |
| `POST /api/profile/2fa/confirm` | Finish setup with `{"code": "..."}`. Answers `{"recoveryCodes": [...]}`, shown only here. `401` for a wrong code. |
| `POST /api/profile/2fa/recovery-codes` | New recovery codes with `{"code": "..."}`; the old ones stop working. |
| `DELETE /api/profile/2fa` | Turn 2FA off with `{"currentPassword": "...", "code": "..."}` (`204`). `403` when 2FA is required for this user. |
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

`POST`, `PATCH` and `DELETE` requests are only accepted with:

- `Content-Type: application/json` (also for requests without a body), and
- an `Origin` header that matches the server. Browsers send it on their own;
  with curl, add it yourself.

```sh
curl -b cookies.txt -X POST http://localhost:8080/api/sync \
  -H 'Content-Type: application/json' -H 'Origin: http://localhost:8080'
```

Account management and sync need `MAIL_ARCHIVE_SECRET_KEY` on the server;
without it these endpoints answer `503` and `GET /api/accounts` reports
`"manage": false`. See [Security](./security) for why.

## Messages

| Endpoint | Description |
|---|---|
| `GET /api/messages` | List or search messages, newest first. |
| `GET /api/messages/{id}` | Headers, plain text, attachment list and every location (account, folder, UID, flags). |
| `GET /api/messages/{id}/html[?images=1]` | The HTML body for a sandboxed iframe. Scripts are blocked; remote images only with `images=1`. |
| `GET /api/messages/{id}/raw` | The original `.eml`. |
| `GET /api/messages/{id}/parts/{n}` | One attachment. PNG, JPEG, GIF and WebP are shown inline; everything else is a download. |

`{id}` is the SHA-256 of the message file.

Query parameters of `GET /api/messages`:

| Parameter | Description |
|---|---|
| `q` | Search query; see [Search](../guide/web-ui#search). |
| `account`, `folder` | Only messages found in this account or folder. |
| `after`, `before` | Date range, `YYYY-MM-DD` (UTC). |
| `limit` | Page size, 1 to 200, default 50. |
| `cursor` | The `nextCursor` of the previous page. |

The response is `{"messages": [...], "nextCursor": "..." | null}`. Each
message has `id`, `size`, `subject`, `from`, `sentAt`, `sortAt` and, for
searches, a `snippet` in which matches are marked with U+E000 (start) and
U+E001 (end).

## Accounts

| Endpoint | Description |
|---|---|
| `GET /api/accounts` | All accounts with server settings (never the password), folders with message counts, and the sync state. |
| `POST /api/accounts` | Add an account. The login is checked first; the first sync starts right away. |
| `PATCH /api/accounts/{name}` | Change some fields, including `name` to rename the account. A new connection or password is checked with a login first; if that fails, nothing changes. All fields are saved together or not at all. `409` when the name is taken or when the account was changed since it was loaded (reload and try again). Works while the account is being synced; the change applies from the next sync. |
| `DELETE /api/accounts/{name}` | Remove the account. Returns `{"result": "removed"}` if archived mail was kept, `{"result": "deleted"}` if the account had none. `409` while the account is being synced. |
| `GET /api/accounts/{name}/server-folders` | The account's folders, live from the IMAP server: `name`, `specialUse` (role) and `selected`. |

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

Each account in `GET /api/accounts` has a `sync` object:

```json
{
  "state": "running",
  "lastRun": {
    "startedAt": "2026-10-06T08:29:16Z",
    "finishedAt": null,
    "status": "running",
    "fetched": 120,
    "new": 87
  }
}
```

`state` is `idle`, `queued` or `running` (also when the command line syncs
the account). `status` of the last run is `running`, `ok`, `partial` or
`failed`, with `error` set for the latter two. The response also has
`manage` (see above) and `syncInterval`, a Go duration such as `6h0m0s`, or
empty when the schedule is off.

## Sync

| Endpoint | Description |
|---|---|
| `POST /api/accounts/{name}/sync` | Queue a sync of one account, also a disabled one. Answers `202`. |
| `POST /api/sync` | Queue all enabled accounts. Answers `202` with `{"queued": n}`. |
| `GET /api/status` | Messages per account and the last sync, like `./ma status`. |
