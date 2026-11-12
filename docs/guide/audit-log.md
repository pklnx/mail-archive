# Audit log

The archive records who searched, read, exported and changed what: in the
web UI, with API keys and on the command line, where the operator works. The
application cannot change or shorten the log. The **Log** page in the web UI
shows it, and the operator writes it out for a period with `./ma audit`.

## Privacy

The audit log records **reading behaviour**: searches with their text,
opened messages, downloads, the IP address and the browser of each request.

- **Admins can see all of it**, including other users' search queries,
  account names and which messages (by their SHA-256) they opened. Messages
  are stored once for all users, so an admin can tell that a user opened a
  message the admin has too.
- **Tell your users** before they use the archive. In a company, a works
  council may have a say in such recording.
- **Owners see the reads of their shared accounts**: who opened, downloaded
  or listed their mail and when, never the reader's IP address, browser or
  API key, nor the terms of searches that did not name the account. See
  [Shared accounts](#shared-accounts).
- The log is personal data itself. Nothing in the archive deletes its rows;
  how long it is kept is part of the retention rules, not of the log.
- **The operator's commands are in it too.** What the operator runs on the
  command line that changes or exports something (imports, exports, backups,
  user and account changes; see the [command line](../reference/cli)) is an
  entry without a user; see [The fields](#the-fields). Admins see these
  entries on the **Log** page; another user only when an entry concerns
  them.

## What is recorded

Each event writes exactly one entry.

| Action | When |
|---|---|
| `auth.login` | A login succeeded; `method` is `password`, `2fa` or `passkey`. With 2FA, the entry is written after the second step. |
| `auth.login_failed` | A login was refused; `method`, and `reason`: `wrong_password`, `unknown_user`, `locked`, `second_factor`, `unknown_passkey`, `passkey_invalid` or `no_access` (an [auditor](#auditors) without an active grant, after a correct password and second factor). For an unknown user, the name is kept only if it is a valid user name, so a password typed into the name field is not stored. A refused [API key](../reference/api#api-keys) has `method: api_key` and `reason` `invalid` (no such key), `revoked`, `expired` or `locked`, with the key's `key_id`, `key_name` and owner when the key exists. The key itself is never stored. |
| `auth.logout` | A valid session logged out. |
| `message.search` | A list with a search or filter: `q`, `from`, `to`, `attachment`, `after`, `before` or `has`. The details hold every parameter that was set. |
| `message.open` | A message was opened. |
| `message.html` | Its HTML body was loaded; `images: true` with remote images. `images_refused` when remote images were asked for but not allowed: `"svg"` or `"math"` for a mail with inline SVG or MathML, `"css"` when the mail's CSS could still load an image in a way the server cannot make inert (see [HTML mail](../reference/security#html-mail)); the mail is then shown without remote images. `inline_images` counts the embedded images (`cid:` references) shown, `inline_omitted` those shown empty (unknown, not an image type that is shown, past the 10 MiB limit, or unreadable). |
| `message.raw` | The `.eml` file was downloaded. |
| `message.part` | An attachment was loaded; `index`, `content_type` and `inline`. In older entries also an inline image of an HTML message. |
| `message.list`, `message.page`, `message.conversation` | Only for users with [shared accounts](#shared-accounts), when the request reaches one, and for every request of an [auditor](#auditors): a list without search parameters (folder or all mail, also an expanded conversation with `thread`), a further page of any list (`cursor`), and a message's conversation (`members`, the number of messages shown). |
| `sync.start` | A sync was started by hand, for one account (`reconcile`) or all (`accounts`, the number queued). |
| `account.share`, `account.unshare` | The owner shared an account with a user, or ended the share; `user` and `user_id`. Sharing an account again records nothing. |
| `account.add`, `account.change`, `account.remove`, `account.move` | Account management in the web UI or on the command line. `account.change` names the changed fields, the old and new name, the new state and the new `sync_interval` (`default`, `manual` or a duration such as `30m0s`), never the server, login or password. `account.add` of an smtp account records its `addresses`; a change of them records `added` and `removed`. The upgrade that renamed the journal folder of smtp accounts wrote one `account.change` per smtp account with `"by": "migration 00027"`, the `folder` (`INBOX` to `JOURNAL`) and the renamed `retention_rules`, and a `retention.rule_remove` for each rule on `JOURNAL` it removed. `account.remove` says whether the account was `deleted` or `removed`. `account.move` counts the shares that stay (`shares_kept`); the new owner's own share ends. A move into another [customer group](./users#customer-groups) also has `from_group` and `to_group` (`id`, `name`) and the users whose shares ended (`shares_ended`). A move to another owner takes the account out of the old owner's [auditor grants](#auditors): `grants_changed` lists them (`id`, `label`, `revoked` for a grant left without accounts). |
| `account.purge`, `account.purge_batch`, `account.purge_finish`, `account.purge_cancel` | A removed account [deleted completely](./accounts#deleting-a-removed-account-completely): confirmed (`purge_id`, `owner`, `kind`, `locations`, `messages`, `deleted`, `kept`), each batch (`locations`, `deleted`, `kept`), its end with the totals, and a cancellation with its `note`. The deleted messages themselves are recorded per message in the deletion records. Purges resumed by the web server are written with actor `cli` and `by: web server`. |
| `user.add`, `user.change`, `user.remove`, `user.password_reset`, `user.password_set`, `user.2fa_reset`, `user.passkeys_removed` | User administration. |
| `user.group_change` | A user moved into another [customer group](./users#customer-groups): `from_group` and `to_group` (`id`, `name`), and `shares_ended`, each with `account`, `owner` and `user`; `grants_revoked` lists the [auditor grants](#auditors) the move revoked (`id`, `label`, `owner`, `auditor`). |
| `auditor.grant`, `auditor.extend`, `auditor.revoke`, `auditor.grant_change` | An [auditor grant](#auditors) was created (`label`, the `accounts` with `all_folders` or `folders`, `period_from`, `period_until`, `starts_at`, `ends_at`), extended (`from`, `to`), revoked (`reason`: `owner`, `operator`, `group_change`, `account_move`, `account_purge` or `user_removed`) or lost an account to a move (`reason: account_move`, `account`). The target is the auditor; every entry has `auditor_grant` and is recorded under the grant's group. Group `auditor`. |
| `auditor.select`, `auditor.denied`, `account.list` | An auditor picked the grant their session reads through (`auditor_grant`, `label`), had a request refused (`method`, `route`, `reason`: `not_allowed` outside the allowlist, `password_change_required`, `two_factor_required`, `grant_selection_required` before the setup or the grant choice is done, `admin_only` for a Log filter only admins may use; nothing was done), or listed the granted accounts (`accounts`, the number). |
| `group.add`, `group.rename`, `group.remove` | A customer group was created, renamed (`from`) or removed; `group_id` and `group`. Group `users`, with the `user.*` actions. |
| `profile.password_change`, `profile.2fa_enable`, `profile.2fa_disable`, `profile.recovery_codes`, `profile.passkey_add`, `profile.passkey_remove` | Changes a user makes to their own login: password, 2FA and passkeys. |
| `profile.change` | A user changed their own web UI settings. `fields` names them (`language`, `locale`), each with its old and new value (`from`, `to`). Saving an unchanged value records nothing. |
| `retention.rule_add`, `retention.rule_remove`, `hold.add`, `hold.release` | [Retention rules and legal holds](../reference/cli#retention), with the account, folder and years, or the hold's name, scope, filter and release note. A hold's entries have the account or the user it names as target (`hold.release` from 1.4.0 on); a hold that names only a [customer group](./retention#holds-per-customer-group) has `group_id` and `group` in its details and is recorded under that group. Group `retention`. |
| `retention.preview`, `retention.confirm`, `retention.cancel`, `retention.batch`, `retention.finish`, `retention.sweep` | A [deletion run](../reference/cli#deletion): proposed (candidates, bytes), confirmed, cancelled, each batch (`deleted`, `kept`), its end with the totals, and removed files. The deleted messages themselves are recorded per message in the deletion records, not here. Runs resumed by the web server are written with actor `cli` and `by: web server`. |
| `api_key.create`, `api_key.revoke` | An API key was created (`key_name`, `scopes`, `expires_at`) or revoked (`key_id`, `key_name`, `reason`: `user`, `admin` or `cli`). The target is the key's owner. `user.password_reset` and `user.remove` say how many keys went with them (`api_keys_revoked`, `api_keys_removed`); `user.remove` also counts the shares of other users' accounts with the user (`shares_removed`), and for an auditor lists the grants revoked before the removal (`grants_revoked`). |
| `data.import`, `data.export`, `data.backup` | `import`, `export` and `backup` on the command line, before they read or write mail. `data.import` has `format`, `source` and `folders`; with [`--journal`](./importing#into-an-smtp-account) also `into: "JOURNAL"`, `label` and `import`, the ID of the run the imported locations name. A [GoBD export](./gobd-export) (`format: gobd`) has `selection` (users, accounts, folders, hold, grant, period and time zone as given, never a group name), `out`, `resume`, `requested_by` when given, `auditor_grant` and `via_owners` (the grant's owner) with `--grant`, and `owners` when it covers several users. |
| `data.export_done` | The end of a GoBD export, also of one that stopped: `id`, the counts (`messages`, `locations`, `missing`, `bytes`), `complete`, `manifest_sha256` (the SHA-256 of `SHA256SUMS`) and `statement_sha256` (of `STATEMENT.txt`), with which a customer's copy can be checked later; `aborted` with the reason when it stopped, and `group_moved_after_snapshot` with the names of the selected users and accounts that changed group while it ran. With `--grant` also `auditor_grant` and `via_owners`. |
| `data.fill`, `data.fill_change`, `data.fill_resume`, `data.fill_cancel`, `data.fill_done` | A [fill](./journaling#bring-in-the-mail-from-before-journaling) of an smtp account from IMAP: started (`fill`, `host`, `username`, `folders`, and `before`, or for a [recurring fill](./journaling#keep-importing-recurring-fill) `keep_running` and `interval`; `prefix` for fills from before fills wrote into `JOURNAL`), changed (`fill_change`: switched to recurring with `keep_running` and `interval` as `{"from", "to"}`, or a new `interval`), resumed, cancelled or stopped, and the end of a one-time fill with `status` and the counts. The runs of a recurring fill are not logged, like scheduled syncs. A fill the web server ran ends without a user, with `"by": "web server"`. The password is never recorded. The Log page lists them under the Export filter, with the other `data.*` actions. |
| `jobs.retry` | `./ma jobs retry KIND` queued failed [background jobs](./operations#background-jobs) again: `kind` and the number of `jobs`. |
| `instance.secret_key_accepted` | `./ma secret-key accept --yes` made the configured secret key the instance's key: `fingerprint` of the new key's check value and `previous` of the old one, never a key. See [The secret key](../reference/security#the-secret-key). |
| `audit.export` | `./ma audit` itself. |

Not recorded:

- automatic syncs, on schedule or with `sync` on the command line (the sync
  history has them);
- page views over the user's own accounts: the account list, the status, a
  folder listed without a search, further pages of a list (the first page
  was recorded), expanding a conversation (the search was recorded), the
  conversation of a message. Over shared accounts these are recorded (see
  above), and for auditors all of them except the status;
- reading the log;
- requests with an API key that the web UI would not record either, such as
  `GET /api/status`; the key's last use is in the profile instead;
- logins refused because of too many failures (`429`), so a flood cannot
  grow the log. The login limits bound the failures that are recorded.

Some reading writes several entries. Opening a message with an HTML body
writes `message.open` and `message.html`; its inline images are counted on
`message.html`. The search box searches as you type: a pause of 300 ms in
"invoice" can write "inv" and "invoice". Each download request is recorded,
also a repeated or resumed one. The entries record what the browser asked
for; whether it was shown cannot be known.

## Shared accounts

An owner can let another user read an account (see
[Users](./users#shared-accounts)). The reader's own Log and the admins' view
have every entry of that reader, as for any user. Entries that read the
owner's mail also go into the owner's Log, marked as another user's read:

| Reader's request | In the owner's Log |
|---|---|
| A message opened, its HTML, the `.eml` file or an attachment, when the message is in the shared account | The action and the message, also when the reader has the message too. |
| The first page of a search or list that names the shared account (`account=owner/name`) | `message.search` with its terms, or `message.list`, and `n`, the messages on the page the reader sees only there. |
| A further page of that search or list (`message.page`), or any page of a search or list over all of the reader's mail, that shows messages the reader sees only through the share | The action, the account and `n` on that page, and `search: true` or `false`. No terms, folder or thread: on further pages the owner has them from the first page, and over all mail they may be about the reader's own mail. |
| A conversation with messages the reader sees only through the share, other than the message opened | `message.conversation` with `n`. Its message is shown only if the owner may read it. |

The owner sees the time, the reader's name, the actor (`session` or
`api_key`), the action and the message, never the reader's IP address,
browser or API key name, and only their own accounts in `via_accounts`,
never another owner's share with the same reader. The entries hold the
owners as of the read (`via_owners`), so they stay in the owner's Log after
an unshare, and after `account move` with the owner of their time. A read of
mail the reader also has in an own account counts only for message reads,
not in `n`. So two reads do not reach the owner: a further page, or a page
over all of the reader's mail, whose messages the reader also has in own
accounts, and a conversation whose only message in the shared account is
the one opened (that message has its own `message.open`).

## Customer groups

From 1.4.0 on, every entry records the [customer group](./users#customer-groups)
it belongs to, at the time it is written:

- An entry about a user or an account (user and account administration,
  shares, syncs and fills of an account, retention rules, holds that name a
  user or an account, purges, API keys, group changes) has that user's group,
  or the group of the account's owner, even when an admin of another group
  made it. An entry about a user or account that the change removes keeps the
  group it had; a move into another group has the new group, and
  `from_group` in its details names the old one.
- An entry of a user acting as themselves (logins, their own profile,
  passkeys and 2FA, their own searches and reads, also of shared accounts,
  a sync of all their accounts) has that user's group.
- Instance-level entries have no group, whoever made them: deletion runs and
  file sweeps (`retention.preview` to `retention.sweep`), holds that name no
  user or account, `data.backup`, `audit.export`, `jobs.retry`,
  `instance.secret_key_accepted`, and failed
  logins of a name that is not a user.
- `group.add`, `group.rename` and `group.remove` have the group they are
  about.
- A GoBD export's `data.export` and `data.export_done` have the export's
  group, set explicitly, also when the export covers several users (and so
  has no single target) and when a selected user moved to another group
  while it ran: `data.export_done` has the group of the export's snapshot,
  `data.export` the group of the check just before (they differ only when
  that run then stops, and its `data.export_done` says `aborted`). An export
  of accounts without an owner has no group, even if an account gets an
  owner meanwhile. The details never name a group; only admins see the
  group column, and members do not see command-line entries.

Entries from before 1.4.0 have no group. Older entries keep the names and
groups they had when they were written: a reader who later moved to another
group still appears under the old name and group.

Admins filter the log by group: **Group** on the [Log page](#the-log-page),
`group` in `GET /api/audit` and `--group` in [`./ma audit`](#writing-it-out).
"No group" (`group=none`, `--without-group`) lists the entries without one:
those from before 1.4.0 and the instance-level ones. The filter goes by the
group an entry was written under: a user's or account's move into another
group is listed under the new group, and the old one is only in the
details (`from_group`), which the filter does not read.

A removed group's entries keep its id. They stay selectable by that id
(`group=<id>` in the API, `--group-id` on the command line), so an
offboarded customer's log can still be exported. Such an entry shows the
group as "Removed group 7" on the Log page and as `{"id": 7, "removed":
true}` in the API. An id that no group and no entry has is refused.

## Auditors

An auditor (a tax advisor or tax auditor, see
[Security](../reference/security#login)) reads an owner's mail only through
an auditor grant. Every request of an auditor that reads archive data is
recorded: the account list (`account.list`), every folder listing and search
page, also empty ones, and every opened message, HTML body, download and
attachment; such a read fails closed when its entry cannot be written. Every
refused request (`auditor.denied`, best effort: the request stays refused
whether or not its entry is written) and every grant choice
(`auditor.select`) is recorded too. Reads of the auditor's own data are not:
the session, the status, the own Log and the own grant list. Each entry of an auditor's
session carries the grant (`auditor_grant`) and its owner (`via_owners`),
which puts it into the owner's Log; while the session has no grant yet (an
auditor with several grants before picking one), the login, its denials and
password changes carry the owners of all the auditor's active grants. A
[GoBD export](./gobd-export) of a grant's scope (`--grant`) carries the
grant and its owner too, on `data.export` and `data.export_done`.

| Entry | Recorded under |
|---|---|
| `auditor.grant`, `auditor.extend`, `auditor.revoke`, `auditor.grant_change` | The grant's group (the owner's), also when an admin of another group or the command line revokes it. A revocation caused by a group move has its own `auditor.revoke` in the group the grant was in; `user.group_change` is under the new group. |
| `auditor.select`, `auditor.denied`, `account.list`, and the auditor's logins, searches and reads | The auditor's group, which is the grant's. |

Who sees what:

- **The owner** sees their own grant entries, and the auditor's entries,
  others' changes of their grants and the operator's exports of them, marked as another user's read: the
  action, the target, the search terms (also of searches over all granted
  mail), the login method and reason, and the grant. Never the address,
  browser or API key name, and no other owner's accounts. An actor who is
  neither the owner nor the auditor, such as an admin, possibly of another
  group, or the command line, is shown as "operator". Older entries keep the
  auditor's name, also after the auditor left the group or was removed.
- **The auditor** sees their own entries, as every user does.
- **Admins** see everything, with the group.
- Nobody else: other members of the group and users of other groups see
  none of it.

`grant=ID` in `GET /api/audit` keeps one grant's entries: owners name their
own grants (an unknown and another owner's ID get the same `404`), admins
any grant.

## The fields

| Field | Content |
|---|---|
| Time | When the entry was written, from the database clock. |
| Actor | `session` (the web UI, also for login attempts), `cli` (the command line), or `api_key` (a script with an [API key](../reference/api#api-keys); the entry names the key and its owner). |
| User | ID and name as they were then. Entries stay when a user is removed. |
| Action | One of the actions above. |
| Target | A message (its SHA-256), an account or a user, with ID and name. |
| Group | The [customer group](#customer-groups) of the entry, from 1.4.0 on; none for instance-level entries and older ones. |
| Client IP | The address of the browser. Behind a reverse proxy, see [Behind a reverse proxy](#behind-a-reverse-proxy). |
| User agent | The browser's identification, up to 256 bytes, stored once per distinct value. |
| Details | The search parameters, the login method and reason, and the other values named above. |

Command line entries are the operator's: they have no user, IP or user
agent. Their details hold `os_user`, the operating system user (`65532` in
the Docker image), and `operator` from `MAIL_ARCHIVE_OPERATOR`, which `./ma`
sets to the user on the host. Neither is verified: whoever runs a command
holds the database password.

## Nothing happens without an entry

- **Reads, searches, exports, imports and syncs started by hand** are
  recorded first. If the entry cannot be written, the web UI answers `503`
  ("the audit log cannot be written; nothing was done") and the command
  fails, before any mail is sent, read or written.
- **Changes and logins** write their entry in the same database
  transaction: if the entry fails, the change is not saved and the login
  creates no session.
- **Failed logins and logouts** are recorded if possible. A failed login is
  refused anyway, and a logout always logs out.

## Append-only, and its limits

A trigger refuses `UPDATE`, `DELETE` and `TRUNCATE` on the log, on every
monthly partition and on the table of user agents, for every database role,
also in replication mode. The application's role has also given up these
privileges, which matters when it is not a superuser.

What this does not stop: whoever holds the database password owns the
tables. In the hosted instance that is the operator. That role can drop the
trigger, replace its function, grant itself the privileges back, detach and
drop a month, or drop the table. A PostgreSQL superuser can also edit the
files on disk. The trigger stops mistakes and every code path of the
application, not someone with the database password. In Docker Compose the
application connects as the database superuser.

A rollback of the database migration that created the log refuses to run
while the log has entries.

## Storage

The log is split into one partition per month (UTC). The web server creates
the partitions of the current and the next three months at start and every
hour; every command creates the current and the next month's. A missing
partition is created when an entry needs it.

One entry takes about 290 bytes including its indexes (measured with a mix
of opened messages, searches and inline images). A user with 300 entries a
day adds about 30 MB a year.

## The Log page

**Log** in the navigation shows the entries of the last 30 days, newest
first, as a table. Everyone sees their own entries: logins (also failed
attempts with their name), searches with their text, opened messages and
downloads, changes, and requests with their API keys. Admins see every
user's entries and the command line's. In **Who** they can pick several
users, the command line and API keys; the list then shows the entries of any
of them, for example alice's and the command line's. The **Who** column is
for admins; for everyone else, an entry from the command line says so under
its target.

With two or more [customer groups](#customer-groups), admins also get
**Group**, between **Action** and **Who**: all groups (the default), one
group, or "No group" for the entries from before 1.4.0 and the
instance-level ones. A group narrows the **Who** list to its users and keeps
only the picked users who belong to it; with **Who** set as well, the list
shows the entries of those users or actors in that group. The **Who**
column then names each entry's group under its user. Users who are not
admins never see groups. With one group, the page looks as before.

Owners of shared accounts also see other users' reads of them; see
[Shared accounts](#shared-accounts). The **Who** column then shows for them
too, with the reader's name, and the entry says what the reader saw: "jo's
search showed 2 messages of info", "jo's list showed …" for a list without
search, "jo's conversation view showed …", or "in info" for an opened
message or download. Search terms show only for a first page that named the
account; a read with an API key says "with an API key", without the key's
name. These actions read *Listed messages* (`message.list`), *Loaded more
results* (`message.page`), *Showed conversation* (`message.conversation`),
*Shared account* (`account.share`) and *Ended account share*
(`account.unshare`).

Owners who gave [auditor access](./users#auditor-access) see the auditor's
entries the same way, with the auditor's search terms, also of searches over
the whole case. Anyone else who changed the grant, such as the operator,
shows as **Operator**. A refused request (*Refused auditor request*) names
its method and route and why it was refused; *Granted*, *Extended*, *Ended*
and *Changed auditor access* name the case and, for an end, the reason;
*Chose audit case* is the auditor's choice of a case, and *Listed accounts*
the auditor's account list.

![Log page](/screenshots/log.png)

**From** and **To** are a date range, the same as in the search filters:
pick it in the calendar, or type the dates in your date format, shown under
the fields. The To day is included. An empty From shows the last 30 days and
empties To. In **Action**, pick one or several groups of actions (logins,
searches, reading, export and backup, accounts, user management, own login
settings, API keys, manual syncs, retention and legal holds, auditor access); the list shows
the entries of any of them. A field names what is picked, with the number of
picks and a button that clears them; with nothing picked it says "All
actions" or "Everyone" and does not filter. The field cuts a long text with
"…", the open list does not: it is as wide as its longest entry, and an
entry wider than the screen, such as a long user name on a phone, takes two
lines or more. <kbd>Esc</kbd> closes an open list and then leaves the field;
<kbd>Delete</kbd> or <kbd>Backspace</kbd> on the field clears it. The filters are kept in the page address, so a reload,
the browser's Back button after opening a message from the list, and a
bookmark show the same entries. A bookmark without a From of its own keeps
showing the last 30 days. A user or a group that no longer exists is
dropped from the address. A message shows its subject only to a user who may read that
message too (in an own or shared account); otherwise the start of its
SHA-256. A click on a subject opens the message.

![The open Action list, every entry in full](/screenshots/log-action-menu.png)

Two kinds of rows are folded, so that the list reads like what happened:

- in older entries, the inline images of an HTML message (written as
  `message.part`) go under its "Showed HTML" row;
- searches typed into the search box, which writes one row per pause
  ("inv", "invoice"), go under the last search when each is the start of
  the next and they are less than 10 seconds apart.

"+2 more" shows the folded rows. The log itself keeps every row; only the
page groups them. Reading the log writes no entry.

The same entries come from `GET /api/audit` (see the
[JSON API](../reference/api#audit-log)). The page counts its days in the
browser's time zone; the API and `./ma audit` read a plain date as a UTC day.

## Writing it out

The operator exports the log on the command line, for example once a year
for the GoBD records:

```sh
./ma audit --since 2026-01-01 --format csv --out /data/audit-2026.csv
./ma audit --since 2026-10-01 --until 2026-10-31 --user alice --action read --format jsonl
./ma audit --since 2026-01-01 --group "Meier GmbH" --format csv --out /data/audit-meier-2026.csv
```

`--since` is required, `--until` is exclusive (a plain date includes that
day). A plain date is a day in UTC, not in your time zone; for another zone
give RFC 3339 with an offset, such as `2026-10-01T00:00:00+02:00`.
`--action` takes a group (`login`, `search`, `read`, `export`, `accounts`,
`users`, `profile`, `api_keys`, `sync`, `retention`) or an action, and is
repeatable. `--user` selects a current user's entries. `--group NAME`
selects the entries of a [customer group](#customer-groups) (the name in any
case), `--group-id N` those of a group by its id, also of a removed group,
and `--without-group` those without one: from before 1.4.0 and
instance-level work; the three exclude each other. Without `--out`, the
entries go to stdout. A file must not exist yet; it is written with mode
0600 and appears when it is complete. Each run is recorded as
`audit.export`, with `group_id` (and `group` for `--group`), or
`without_group`.

CSV has the columns `at`, `actor`, `user_id`, `user_name`, `api_key_name`,
`action`, `target_type`, `target_id`, `target_name`, `client_ip`,
`user_agent`, `details` (as JSON) and `group_id` (the customer group's id,
empty without one; JSON lines: `groupId`, missing without one). A cell that starts with `=`, `+`,
`-`, `@`, a tab or a carriage return gets a leading `'`, so a spreadsheet
does not run a search query as a formula. JSON lines carry the values
unchanged.

## Behind a reverse proxy

For the operator. The server records the address it is connected from. Behind a
reverse proxy (Caddy, nginx, `tailscale serve`) that is the proxy, and with
Docker's port publishing on `127.0.0.1` it is the gateway of the Compose
network. List the proxy in `MAIL_ARCHIVE_TRUSTED_PROXIES`: then the client's
address is taken from the proxy's `X-Forwarded-For` header, and the login
limits count per client again (IPv6 per `/64`). The same setting decides
whether the proxy's `X-Forwarded-Proto: https` counts, which makes the session
cookie `Secure` and sends HSTS (see [Network
access](../reference/security#network-access)); from loopback it always counts.
Until the proxy is listed, the server logs a warning once that names the
address the proxy connects from.

For Caddy on the same computer in front of Docker Compose, trust the gateway
of the Compose network. `docker network ls` lists it, usually as
`<directory>_default`:

```sh
docker network inspect mail-archive_default --format '{{(index .IPAM.Config 0).Gateway}}'
```

Then set, for example, `MAIL_ARCHIVE_TRUSTED_PROXIES=172.18.0.1` in `.env`.
If the proxy serves HTTPS and a login before that did not count as HTTPS (in
Docker Compose, for example), log in once more after setting the variable,
because the cookie of that login is the wrong one. List only the proxy:
every address in the list may name any client. And trust it only if all
access goes through the proxy; a client that reaches the web port directly
from a trusted address could choose its own.
