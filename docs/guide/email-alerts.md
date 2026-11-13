# Email alerts

Admins can get alerts by mail: when the syncs of an account or the runs of
a [recurring fill](./journaling#keep-importing-recurring-fill) keep failing
and when they work again, when mail was
[deleted on the server](./syncing#alert-for-mail-deleted-on-the-server) in
bulk, and when the [storage](./operations#storage-and-quotas) of the
instance or of a customer group changes its state (`warn`, `full`,
`overflow`, back to `ok`) or the data disk falls below its free-space
reserve (`disk_low`).

Alert mail goes to **admins only**: each admin sets one address and
confirms it. Account owners and other members of a customer group get no
alert mail; they see a [banner](./web-ui#sync-problems) in the web UI. A
[webhook](./operations#webhook-alerts) can run beside alert mail, for a
pager or a monitoring tool; the two are independent.

## Turning it on (operator)

Alert mail goes through a mail relay that the operator sets in `.env`, with
its password in a file (see [Configuration](../reference/configuration)):

```sh
KEEPIA_ARCHIVE_MAIL_URL=smtps://alerts@relay.example.com:465
KEEPIA_ARCHIVE_MAIL_PASSWORD_FILE=/secret-key/mail.password
KEEPIA_ARCHIVE_MAIL_FROM=Mail Archive <archive@example.com>
```

Put the password on one line in `secret-key/mail.password` (Compose mounts
that directory read-only as `/secret-key`), readable only by the container
user (`chmod 0400`), then restart the web server
(`docker compose up -d web`) and check the relay:

```sh
./ma notify mail-test --to you@example.com
```

Without `KEEPIA_ARCHIVE_MAIL_URL` there is no alert mail, and the profile has
no **Notifications** tab. See [Email alerts](./operations#email-alerts) in
the Operations guide for what the relay must offer.

## Your address (admins)

In your [profile](./users#your-profile), on the **Notifications** tab:

1. Enter the address and click **Send code**. A 6-character code is mailed
   to it.
2. Enter the code within 30 minutes and click **Confirm address**.

Until the code is entered, the address gets no alert mail. A code can be
entered 5 times; at most 3 codes are sent per hour. **Send a new code**
sends another one; a new address replaces the old one and needs its own
code. **Remove address** stops alert mail to you. Setting, confirming and
removing the address are written to the [audit log](./audit-log), without
the address.

![Profile page, tab Notifications with a confirmed address](/screenshots/profile-notifications.png)

If the relay refuses your address as unknown (a reply `5.1.x`), it is
marked unconfirmed: a banner above the mail and the tab say so, and the
address gets no alert mail until you confirm it again. Other refusals of
the relay leave it confirmed; the operator finds them in the log.

## What a mail says

One mail per change and admin, in your UI language (German or English;
with **Automatic** the language of the browser when you set the address,
otherwise English). For example:

> **Mail archive: storage of group "Müller GmbH" at 82 %**
>
> Storage of customer group "Müller GmbH" reached the warning level since
> 2026-11-12 08:30 UTC: 41.0 GiB of 50.0 GiB (82 %) used.
>
> Customer group: Müller GmbH
> Event ID: storage:4:2
> Open: https://archive.example.com

A mail names accounts, their owners and customer groups, with a link to the
web UI (`KEEPIA_ARCHIVE_PUBLIC_URL`). It never contains error texts, IMAP user
names, folder names, server names, subjects or addresses from mail: the
error of a failing account is on its page in the web UI. Times are in UTC.
The event ID is the one the [webhook](./operations#webhook-alerts) uses.

Each change is mailed once to every admin with a confirmed address: a
webhook that fails and retries does not repeat it, and `warn`, `ok` and
`warn` again are three mails. Mail keeps its own record of what it
announced: while the webhook fails, recoveries and new changes are still
mailed when they happen, and nothing is mailed twice or for a change that
was undone before the archive looked (a group at `warn` and back at `ok`
within half a minute gets no mail).

When alert mail is first turned on, the first mails tell the current
state once: accounts and recurring imports that keep failing, storage that
is not at `ok`, and mail deleted on the server within the last 24 hours.
Nothing at `ok` is announced. Until an admin confirms an address nothing
is sent at all, and the first admin to confirm one gets only the changes
after that. Whether you still get it is checked when the
mail is sent: an admin who was demoted, locked or removed meanwhile, or
whose new address is not confirmed yet, gets nothing.
