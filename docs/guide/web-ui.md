# Web UI and search

`docker compose up -d web` serves the UI on <http://localhost:8080>. Log in
with your user; your name (which opens your [profile](./users#your-own-password))
and **Log out** are at the bottom of the sidebar. Admins also find
[Users](./users) there.

![The three-column mail view](/screenshots/mail-light.png)

## Layout

- **Sidebar:** all mail, then each account with its folders and message
  counts. A count is the number of distinct messages in the folder, as in
  its list. A spinner marks accounts that are syncing; removed accounts are
  marked as such. **Manage accounts** at the bottom opens the
  [account page](./accounts).
- **Message list:** newest first, with search at the top. More messages load
  as you scroll.
- **Message:** headers, where the message was found (every account and
  folder), attachments and the body. A place marked *renumbered* is from
  before the server renumbered that folder; the message is listed there
  again under its new number if it is still on the server.

Everything you select is part of the URL, so the back button, reloads and
bookmarks work. On a phone the columns become separate screens.

<img src="/screenshots/mail-phone.png" alt="A message on a phone" width="320">

## Reading mail

HTML mail is shown in a sandboxed frame: no scripts, no forms, no plugins.
**Remote images stay blocked** because they tell the sender that you opened
the message (tracking pixels). Click **Load remote images** for one message
to allow them. Images embedded in the message are always shown.

**Show text** switches to the plain-text version. **Download .eml** saves the
original message, which any mail client opens. Attachments are downloads;
only PNG, JPEG, GIF and WebP images open in the browser.

## Search

Type in the search field; results update as you type. Selecting an account or
folder limits the search to it.

| Query | Finds |
|---|---|
| `invoice october` | Messages with both words. |
| `"tax appointment"` | The exact phrase. |
| `invoice OR receipt` | Either word. |
| `invoice -newsletter` | `invoice` but not `newsletter`. |
| `utilities.example` | Part of a subject or sender, also inside words. |

Search covers subject, sender and body text. Words are matched in German and
English forms: `invoices` finds `invoice`, `Rechnungen` finds `Rechnung`.
Matches are highlighted in the result snippets.

![Search results with highlighted matches](/screenshots/search.png)

Messages archived before full-text search existed need a one-time
`./ma reindex`; see [Upgrades](./operations#upgrades).

## Appearance and language

Light and dark mode follow the system. The sun/moon button next to the search
field overrides it; the choice is remembered in the browser.

![Dark mode](/screenshots/mail-dark.png)

The UI is in German or English, chosen from the browser's language
preferences (English for anything else). Dates and numbers follow the same
setting. Error messages from the server are always in English.
