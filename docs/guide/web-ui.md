# Web UI and search

`docker compose up -d web` serves the UI on <http://localhost:8080>. Log in
with your user.

The header at the top holds the logo and the name of the app, which leads to
all mail, the menu button on smaller screens (see [Layout](#layout)) and, on
the right, the person icon. That icon opens the user panel with your name,
your [profile](./users#your-profile), **Keyboard shortcuts**, the light/dark
switch and **Log out**; a click outside it or <kbd>Esc</kbd> closes it. If
signing out fails (the server cannot be reached or answers with an error),
you stay signed in: an error under the header says so, with the reason, and
**Log out** again tries once more.

![The user panel, opened from the person icon](/screenshots/user-panel.png)

The navigation on the
left lists **All mail**, **Only in archive** (once there is such mail, see
below), [Accounts](./accounts), **Log** (the [audit log](./audit-log) of what
you did) and **Profile**; admins also find [Users](./users) and
[Retention](./retention) there, and see everyone's entries in the log. On a
large screen the navigation is a narrow column of icons that shows its
labels when you point at it or move the focus into it.

![The three-column mail view](/screenshots/mail-light.png)

## Layout

- **Folders:** the column next to the navigation lists each account with
  its folders and message counts. A count is the number of distinct
  messages in the folder, as in its list; messages no longer on the server
  still count. Next to an account its state shows: *receiving* for an smtp
  account, *failing*, *removed*, *disabled* or *imported*, and *syncing*
  while an IMAP account syncs (*importing* while mail is imported into
  another account). **All folders** under an account lists all of
  its mail. Once a [reconcile](./syncing#reconcile) found messages deleted
  on all your servers, **Only in archive** in the navigation lists them,
  with their number.

  Folders form a tree, split at the server's hierarchy delimiter (`/` or
  `.`): `Archive/2024` shows as `2024` under `Archive`, with the full name as
  tooltip. A parent that is only part of a path, not a folder of its own, is
  shown without a count. The arrow next to an account or a parent folder
  collapses or expands it; clicking the name still selects it. The arrow
  keys move through the tree. **Collapse all** and **Expand all** do this
  for the whole column. A collapsed parent shows, in italics, the sum of the
  counts below it: a message in two of those folders counts twice. When you
  open a folder inside a collapsed one, for example from a link, its path
  expands. The state is remembered per user in this browser; on a shared
  browser it keeps listing your account names and folder paths after you
  log out. After an upgrade, an account's folders form a tree from its next
  sync; until then, and for removed accounts, the list stays flat.

  The column's width is yours to set, from 12 to 28 rem (192 to 448 pixels
  at the default font size). Drag the handle on its right edge, or focus the
  handle: the left and right arrow keys change the width, **Home** and
  **End** set the narrowest and the widest, and **Enter** collapses the
  column. A double-click on the handle resets the width. The button next to
  **Folders** at the top also collapses the column, to a narrow rail of
  account labels (a dot marks an account with sync problems); the same button expands
  it again. Width and state are remembered per user in this browser.
- **Message list:** newest first, with search at the top. More messages load
  as you scroll. A line above the list counts what is loaded ("42 messages",
  "More than 50 matches" while more can load). Headings group the messages
  by date and stay at the top while you scroll: **Today**, **Yesterday**,
  **This week** (from Monday), **This month**, then the month for earlier
  months of this year and the year before that. When a search finds
  nothing, **Clear search** empties it, and **Search all mail** repeats it
  outside the selected account or folder.
- **Message:** headers, where the message was found (every account and
  folder), attachments and the body. A place marked *renumbered* is from
  before the server renumbered that folder; the message is listed there
  again under its new number if it is still on the server. A place marked
  *no longer on the server* (in amber, with the date it was last seen) was
  deleted there; the archive keeps the message.

Above an open message, the arrows go to the previous and next message of
the list, with the position in between ("3 of 42"; "3 of 42+" while more
can load). At the end of what is loaded, the next arrow loads more. Paging
does not add history entries.

Everything you select is part of the URL, so the back button, reloads and
bookmarks work. Back in the browser after paging returns to the list, not to
the previous message.

The interface behaves like an app rather than a web page: the page does not
bounce or reload when you pull past its end, and a double tap does not zoom
in (pinching still does). Lists and panes scroll on their own. The
navigation, the buttons and the message list are not selectable as text, so
a long press or a double click there does not start a selection; the open
message (its subject, addresses, date and text) can be selected and copied
as before. On touch screens, text fields use a larger font, so the phone
does not zoom in when you tap into one.

On a smaller screen, such as a tablet or a phone, the menu button at the
left of the header opens the navigation over the page; choosing an entry,
the button again or <kbd>Esc</kbd> closes it. The folders are behind the
**Folders** button left of the search field: it opens the folder column
over the list, and choosing a folder, a click next to it or <kbd>Esc</kbd>
closes it again.

On a phone the list and the message are separate screens. Above the list
the current folder shows, with its account below it, or **All mail**; the
search field takes the full width below it, and under it the quick filters
**Has attachment** and **Group by conversation**, which one tap turns on or
off (see [Filters](#filters)). An open message fills the screen and
scrolls as one page. The arrow at the top left leads back to the list, the
arrows on the right page through it; this bar stays at the top while you
scroll.

On a phone the header of a message is more compact still: the subject on
up to two lines, then the sender's name and the time (the date for older
mail), and one row of actions (see [Reading mail](#reading-mail)). There
**Details** and the conversation are icons too; the conversation's icon
shows the number of its messages. **Details** shows the whole subject,
**From** with the address, **To**, **Cc**, the full **Date**, **Found in**,
**Retention** and the envelope or origin. The conversation opens as a sheet
below the bar at the top; a tap outside it closes it.

<img src="/screenshots/mail-phone.png" alt="A message on a phone" width="320">
<img src="/screenshots/mail-phone-nav.png" alt="The navigation on a phone, opened with the menu button" width="320">
<img src="/screenshots/mail-phone-filters.png" alt="The list on a phone with the quick filter Has attachment selected and a From filter set" width="320">

## Sync problems

When one of your accounts keeps failing to sync (by default three syncs in a
row) or has not synced successfully for two of its sync intervals, a banner above
the mail names it: red for failing, amber for not synced for a while.
**Open accounts** leads to the Accounts page, where the account's row is
tagged *failing*; the account's own page says how often in a row and since
when the sync failed, with the last error under *Last sync*. The banner
disappears after the next successful sync. It refreshes with the account
list every 30 seconds; screen readers hear it when the set of failing
accounts changes. Disabled accounts never appear in it.

Admins see only their own accounts by name. For other users' accounts they
get a count ("2 accounts of other users keep failing") and **Show users**,
where each user with failing accounts is marked; only the owner, or
`./ma status` on the command line, shows which accounts they are. See
[Syncing](./syncing#alerts) for what counts as failing.

## Only in archive

The [account page](./accounts) shows for each IMAP account how many of its
messages are no longer on its server; the number links to them. **Check
server** next to **Sync** syncs the account and compares every folder with
the server right away; folders compared in the last 5 minutes are skipped,
so a second click does not query the server again. Without the button, each
folder is compared once a day. See [Reconcile](./syncing#reconcile).

![A message that is only in the archive](/screenshots/only-in-archive.png)

## Reading mail

A message scrolls as one unit: the reading pane next to the list, or the
whole page on a phone. Header and text move together.

The header is compact: the subject, **From** and **Date**, and one row of
actions. In that row the icons switch between the HTML and the text
version and download the `.eml`, next to **Load remote images** for HTML
mail. At the end of the row, **Conversation (n)** opens the conversation
(see [Conversations](#conversations)) and **Details** shows the rest:
**To**, **Cc**, **Found in**, **Retention** and the envelope or origin.

HTML mail is shown in a sandboxed frame that grows with the mail: no
scripts, no forms, no plugins. The message scrolls as one page, and links
in the mail work on the first click; they open in a new tab. Links from a
mail into the archive itself are blocked where possible, and the server
refuses most of them in any case (see [HTML mail](../reference/security#html-mail)).
Content a mail puts into a declarative shadow root or a `srcdoc` frame is
not shown, and links that an SVG picture shows through `<use>` do not
open.
A few mails are taller than any frame (for example a picture as high as
the window); those, and very long ones, keep the height of the visible
area and scroll inside it.
HTML mail is rendered like in other mail clients: a mail with a doctype in
standards mode, one without in quirks mode.
**Remote images stay blocked** because they tell the sender that you opened
the message (tracking pixels). Click **Load remote images** for one message
to allow them. Images embedded in the message (`cid:`) are always shown if
they are PNG, JPEG, GIF or WebP, up to 10 MiB per message; other types such
as SVG, and images past that limit, show as a broken image. In dark
mode, the mail keeps its own colors and is shown as a white sheet on the
dark background, since most HTML mail is designed for white.

Plain-text mail keeps its line breaks. Quoted text (lines starting with `>`)
is indented and muted; quotes within the quote, from the second level on,
are folded behind **Show quoted text**. A signature after a `-- ` line is
set smaller and quieter. Web addresses (`http://` and `https://`) are
clickable and open in a new tab.

<!-- screenshot: a plain-text reply with an indented quote, a folded older quote and a signature -->

**Found in** (under **Details**) lists every folder that holds the message. For a copy an smtp
account received, an **Envelope** section shows the sender and recipients the
mail server delivered it to, BCC included; for a copy it imported from IMAP
([fill](./journaling#bring-in-the-mail-from-before-journaling)), an **Origin**
section names the mailbox it came from and says that there is no envelope.
Both are folded; a click on the title opens them.

**Show text** (an icon, its name in the tooltip) switches to the plain-text
version. **Download .eml** (an icon too) saves the original message, which
any mail client opens. For the header on a phone, see [Layout](#layout).

Attachments are tiles below the headers, with the file type (such as PDF or
DOCX), the name and the size; on a phone they scroll sideways. A tile is a
download; only PNG, JPEG, GIF and WebP images open in the browser. There are
no thumbnails on purpose: every attachment that is loaded is written to the
[audit log](./audit-log) as `message.part`, so previews would log every
attachment of every message you open.

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
| `to:taxadvisor` | Messages with `taxadvisor` in a To or Cc name or address, or among the envelope recipients of your [smtp accounts](./journaling) (BCC included). Bcc headers are not stored. |
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

The filter button right of the search field opens the filters over the
list; they are closed at first on every screen, and the button is marked
while a prefix or grouping is set. They set the same prefixes for From, To
or Cc, the date range and attachments: they write them into the search
field, and prefixes you type show up in them. The URL keeps the whole
search, so Back, reload and bookmarks keep the filters. A click outside the
filters, the filter button or <kbd>Esc</kbd> closes them.

The date range has two fields, **Since** and **Until**; both days count.
Type the dates in your date format (the hint below the fields shows it,
for example `mm/dd/yyyy`), or pick them in the calendar that opens below
the field. In the search field the range becomes `after:` the first day and
`before:` the day after the last one. Above the fields, **Any time**,
**Last 7 days**, **Last 30 days**, **This year** and **Custom** choose a
range in one step: the presets set only the first day (today counts among
the seven and thirty days), **Any time** removes both dates, and **Custom**
keeps them and moves to **Since**. A range you edit yourself, or one no
preset gives, shows as **Custom**. In the calendar, <kbd>Esc</kbd> closes
the calendar first and then the filters.

![The filters with Last 30 days chosen](/screenshots/search-filters.png)

Every filter that is set also shows as a tag below the search field, with
its value, for example `From: sam`; the × on a tag removes that filter, and
the focus moves on to the next tag. A date range shows as one tag with both
days, for example `Date: Oct 1, 2026 – Oct 31, 2026`. **Edit filters**
after the tags opens the filters and moves the focus to the field of the
first tag, in their order (From, To or Cc, the date range, attachments,
grouping).

On a phone the filters fill the width of the screen. **Has attachment** and
**Group by conversation** are two tags of their own right below the search
field: one tap turns the filter on or off, without opening the filters. A
selected one is highlighted, and these two filters do not show again among
the tags of set filters.

A message has an attachment when a part is marked as one, or when it is a
named file that is not text, such as a PDF that Apple Mail sends inline.
Images embedded in the message body (`cid:`) do not count. Messages with
attachments show a paperclip in the list.

![Search with a recipient, a date and a word](/screenshots/search.png)

Messages archived by an older version need a one-time `./ma reindex` before
filters and full-text search find them; see [Upgrades](./operations#upgrades).

## Conversations

When a message belongs to a conversation, **Conversation (n)** in its
action row opens the list over the message, oldest first, with date,
sender and subject, and with the open message selected. It shows all
messages of the conversation; for a very long one the oldest are left out,
and a note says how many. The message it answers is marked "In reply to",
answers to it "Reply", and the open message "This message". Pick one to
open it; a click or tap outside the list or <kbd>Esc</kbd> closes it. The
arrow keys move through the list. Messages from INBOX and Sent come
together here, also from different accounts.

![A reply with its conversation open over the text](/screenshots/conversation.png)

"Group by conversation" in the filters (on a phone also a tag below
the search field) lists one row per conversation:
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

## Keyboard shortcuts

| Key | Does |
|---|---|
| <kbd>/</kbd> | Moves to the search field. |
| <kbd>j</kbd> | Next message; with no message open, opens the first one. |
| <kbd>k</kbd> | Previous message. |
| <kbd>u</kbd> or <kbd>Esc</kbd> | Back to the list; <kbd>Esc</kbd> also closes the navigation, the folders opened over the page or the user panel. |
| <kbd>g</kbd> then <kbd>a</kbd> | All mail. |
| <kbd>?</kbd> | Lists the shortcuts. |

They do not apply while you type in a field (<kbd>Esc</kbd> leaves the
field), while a dialog is open or while the user panel is open. With no message open, the reading pane
points to <kbd>?</kbd>.

## Install as an app

The UI can be added to the home screen or the dock and then opens in its
own window, without the browser's address bar.

- **iPhone and iPad (Safari):** Share, then **Add to Home Screen**.
- **Android and desktop (Chrome):** **Install** in the browser menu or in the
  address bar.

Browsers offer this only when the site is served over HTTPS (or opened as
`localhost` on the same computer); see
[Network access](../reference/security#network-access). The app is the same
web page: it needs the server and stores nothing for offline use.

## Appearance and language

Light and dark mode follow the system. **Switch to dark mode** or **Switch
to light mode** in the user panel overrides it; on the login page the
sun/moon button does. The choice is remembered in the
browser.

The colours are those of the pklnx design system: IBM's Carbon with Purple
as the interactive colour of buttons, links and selected items, in both
modes. The keyboard focus is outlined in Purple in light mode and in white
in dark mode.

![Dark mode](/screenshots/mail-dark.png)

The UI is in German or English. Each user chooses the language and the
format of dates and numbers in their profile, on the **Display** tab. The choice is stored on the server, so it follows you to other
browsers and devices.

- **Automatic (browser)**, the default, takes the language from the
  browser's preferences: German if German comes first, otherwise English.
- **Dates and numbers** offers German (Germany, Austria, Switzerland) and
  English (UK, Ireland, US) formats. Month and day names follow this setting
  too. On Automatic, it uses the browser's format for the chosen language,
  for example `de-AT` for an Austrian browser, and plain German or English
  when the browser has none.

The login page of a browser shows the settings of the last user who logged
in there. Error messages from the server are always in English.
