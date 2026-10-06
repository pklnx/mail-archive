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

Administrators must complete TOTP setup on their first web login. Admins cannot disable TOTP; another admin or `./ma user reset-2fa NAME` can reset it, after which the admin must set it up again.

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

- **Reset TOTP:** logs the user out everywhere and requires TOTP setup again. The last usable admin cannot be reset.
- **Reset password:** generates a new password, shown once. The user is logged
  out everywhere and must change it at the next login.
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
