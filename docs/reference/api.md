# JSON API

The web UI is a client of this API; scripts can use it too. All responses
are JSON unless noted. Errors look like `{"error": "message"}`.

## Write requests

`POST`, `PATCH` and `DELETE` requests are only accepted with:

- `Content-Type: application/json` (also for requests without a body), and
- an `Origin` header that matches the server. Browsers send it on their own;
  with curl, add it yourself.

```sh
curl -X POST http://localhost:8080/api/sync \
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
| `PATCH /api/accounts/{name}` | Change some fields, including `name` to rename the account. A new connection or password is checked with a login first; if that fails, nothing changes. Renaming answers `409` while the account is being synced or when the name is taken. |
| `DELETE /api/accounts/{name}` | Remove the account. Returns `{"result": "removed"}` if archived mail was kept, `{"result": "deleted"}` if the account had none. |
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
