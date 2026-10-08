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

### Filters

Prefixes in the search field narrow the results. They combine with each other
and with the words around them, and are not case sensitive.

| Prefix | Finds |
|---|---|
| `from:sam` | Messages whose sender's name or address contains `sam`. |
| `to:taxadvisor` | Messages with `taxadvisor` in a To or Cc name or address. Bcc is not stored. |
| `to:"Jordan Lee"` | Quotes allow spaces. |
| `attachment:lease` | Messages with an attachment whose file name contains `lease`. |
| `has:attachment` | Messages with at least one attachment. |
| `after:2024-01-01` | Sent on or after 1 January 2024. |
| `before:2025-01-01` | Sent before 1 January 2025, so up to 31 December 2024. |

Dates are days in your browser's time zone. If a prefix appears twice, the
last one counts. Anything else stays a search word, for example `subject:`,
an invalid date or a URL.

Recipients and attachment names are found only with `to:` and `attachment:`,
not by plain words: otherwise your own address would match nearly every
message you received. The contents of attachments are not searched.

The filter row under the search field sets the same prefixes for From, To or
Cc, the date range and attachments. It writes them into the search field, and
prefixes you type show up in it. On a phone it is behind the filter button
and opens by itself when the search contains a prefix. The URL keeps the
whole search, so Back, reload and bookmarks keep the filters.

A message has an attachment when a part is marked as one, or when it is a
named file that is not text, such as a PDF that Apple Mail sends inline.
Images embedded in the message body (`cid:`) do not count. Messages with
attachments show a paperclip in the list.

![Search with a recipient, a date and a word](/screenshots/search.png)

Messages archived by an older version need a one-time `./ma reindex` before
filters and full-text search find them; see [Upgrades](./operations#upgrades).

## Conversations

A message that belongs to a conversation shows it above its text: the
messages before and after it, oldest first, with date, sender and subject.
The message it answers is marked "In reply to", answers to it "Reply", and
the open message "This message". Select one to open it. Messages from INBOX
and Sent come together here, also from different accounts.

![A reply with its conversation above the text](/screenshots/conversation.png)

"Group by conversation" in the filter row lists one row per conversation:
its newest message, with the number of its messages as a badge. The arrow
on the right shows the earlier ones below it. With a search or filters, a
row is the newest message that matches, the badge counts the matches, and
the arrow lists the other matches. The URL keeps the setting (`group=1`).

![The list grouped by conversation, one conversation expanded](/screenshots/grouped.png)

A conversation is found through the `References` and `In-Reply-To` headers
that mail programs add to replies; subjects are not compared. A reply
without these headers starts a conversation of its own. You only see the
messages in your own accounts, also when someone else archived the other
half of a conversation.

## Appearance and language

Light and dark mode follow the system. The sun/moon button next to the search
field overrides it; the choice is remembered in the browser.

![Dark mode](/screenshots/mail-dark.png)

The UI is in German or English, chosen from the browser's language
preferences (English for anything else). Dates and numbers follow the same
setting. Error messages from the server are always in English.
