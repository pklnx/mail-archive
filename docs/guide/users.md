# Users

Everyone who uses the web UI has their own login. Each user sees only their
own accounts and the mail found in them; see
[Security](../reference/security#login).

## The first admin

The first user is created on the command line, usually right after
installing:

```sh
./ma user add patrick --admin
```

Administrators must set up two-factor authentication (TOTP) at their first
web login, after choosing their own password. Admins cannot turn it off.
Another admin, or `./ma user reset-2fa NAME` on the command line, can reset
it; the admin then sets it up again at the next login.

This first user also gets all accounts that were added before any user
existed.

## Adding users

Admins open **Users** at the bottom of the sidebar and click **Add user**.
They enter a name (lowercase letters, digits, `.`, `-`, `_`) and choose
whether the new user is an admin.

![Users page](/screenshots/users.png)

The server generates a password such as `k7mq2-x9tbr-4hdne-p3wza` and shows
it **once**. Hand it over safely. At the first login the user must choose
their own password before they can do anything else.

On the command line, `./ma user add NAME` asks for a password instead, and
`./ma user reset-password NAME` generates one like the web UI does.

## Managing users

For every other user, admins can:

- **Reset 2FA:** only shown while the user has 2FA on. Turns it off and logs
  the user out everywhere. Admins, and everyone when 2FA is required, set it
  up again at the next login.
- **Reset password:** generates a new password, shown once. The user is logged
  out everywhere and must change it at the next login. Their passkeys are
  removed too, unless you untick the box: after a break-in, a new password
  alone would not lock the intruder out.
- **Remove passkeys:** removes all passkeys of the user and logs them out
  everywhere, for example after a lost phone.
- **Lock / Unlock:** a locked user is logged out at once and cannot log in.
- **Make admin / Remove admin.**
- **Remove:** only for users without accounts. A user who still owns accounts
  (also removed ones) must hand them over first, on the command line:
  `./ma account move NAME --user OLD --to NEW`.

The last admin who can log in can be neither locked, removed nor demoted.
Admins cannot change their own login on this page: they use their profile,
or another admin does it.

Admins only manage logins. They see how many accounts a user has, but not
which, and never other users' mail.

## Your own password

Click your name at the bottom of the sidebar to open your profile and change
your password. You need the current one. Other devices are logged out; the
one you use stays logged in.

![Profile page](/screenshots/profile.png)

Wrong current passwords count like failed logins: after 5 within 15 minutes,
you have to wait.

## Two-factor authentication

In your profile, **Set up 2FA** shows a QR code. Scan it with an
authenticator app (for example Aegis, 2FAS or Google Authenticator) or type
the key below it, then enter the 6-digit code from the app.

You then get 10 recovery codes, shown **once**. Keep them somewhere safe:
each one replaces a code from the app a single time, for example when the
phone is lost. You can generate new ones in your profile; the old ones stop
working.

From then on, the login asks for a code after the password. A code works
only once and only for about 30 seconds before or after its time.

Users who are not admins can turn 2FA off in their profile with their
password and a code, unless the server requires it for everyone
(`MAIL_ARCHIVE_REQUIRE_2FA=true`, see
[Configuration](../reference/configuration)).

If you lose both the app and the recovery codes, an admin resets your 2FA.

## Passkeys

A passkey signs you in with Face ID, Touch ID, Windows Hello or a security
key, without password and code. It works only at the address in
`MAIL_ARCHIVE_PUBLIC_URL` (see [Configuration](../reference/configuration)),
over HTTPS or on `localhost`.

In your profile, enter a name for the passkey (like *iPhone*), your
password and, if you use 2FA, a code, then click **Add passkey** and confirm
on your device. You can have up to 10 passkeys.

At the login, click **Sign in with a passkey**, or pick the passkey in the
user name field when the browser offers it.

**Remove** in your profile deletes a passkey and logs out your other
devices. Your password keeps working; admins must still keep TOTP set up.

The passkey itself stays on your device or in your password manager. Chrome
and Safari are told to forget it; Firefox and some password managers keep
offering it, and signing in with it then says it is not registered. Delete
it there too (in Firefox: Settings, Privacy & Security, Passkeys; on a Mac:
System Settings, Passwords).
