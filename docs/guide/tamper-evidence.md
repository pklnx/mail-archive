# Tamper evidence

mail-archive can sign, every day, which messages the archive holds. If a
message disappears afterwards, `verify` names it, with the day it was
attested. This page explains what that proves, what it does not, and what
you have to do for it to be worth anything.

## Tamper-evident, not tamper-proof

The attestations do not stop anyone with access to PostgreSQL or the data
directory from deleting mail. They make a removal visible afterwards.

Someone who can change the database, the files **and** has the private key
can also rewrite the chain on the host. The key lies in the data directory,
so every backup and every copy of it contains the key. The evidence
therefore never rests on the key staying secret, but on copies kept
somewhere the archive host cannot write: the public key and the attestation
file. Without those copies, the attestations only catch mistakes and
careless tampering.

## What it proves, and what not

Each attestation covers the messages archived since the previous one, lists
their SHA-256 in a leaves file, and is chained to the previous attestation
and signed with an Ed25519 key. From that:

- **Proven:** every message archived up to an attestation was in the archive
  at that time. A message removed or changed since then shows up in
  `verify` as `removed` (or `attestation`), with its hash and the
  attestation that covered it. A message inserted into an attested range
  afterwards shows up as `unattested`. Deletions recorded by the archive
  itself (retention, once it exists) are signed with their hash, reason and
  time and are not findings.
- **Not proven:** that every message reached the archive. IMAP accounts are
  snapshots, see [what each source
  guarantees](./accounts). Messages archived and removed between two
  attestations leave no trace. Locations, folders and flags are not covered,
  only message content.
- A database user can delete a message and insert a matching deletion record
  before the next attestation; the job then signs it as a deletion. It is not
  hidden: the deletion is in the signed chain with its hash, reason and time,
  and can be checked against the retention policy.

## What you do

1. **Create the key once** and copy the public key off the host, for
   example into a password manager, on a printout, or to your auditor:
   ```sh
   ./ma attest keygen
   ./ma attest public-key
   ```
   The web server writes an attestation every
   `MAIL_ARCHIVE_ATTEST_INTERVAL` (default 24h), the first one at its next
   start. `./ma status` shows the newest one.

2. **Copy the attestation file off the host, regularly**, to a place the
   archive host cannot write: another machine that pulls it, WORM storage,
   or an e-mail to an external mailbox. Keep dated copies rather than one
   that is overwritten: a host under someone else's control could otherwise
   hand over a shortened file. A daily pull from another machine, as root
   there because the files belong to the container user:
   ```sh
   #!/bin/sh
   # /usr/local/bin/pull-attestations, run daily by cron on another machine
   set -eu
   src=root@archive-host:/path/to/mail-archive/data/attest
   dst=/srv/mail-archive-attest
   day=$(date +%F)
   rsync -a --link-dest="$dst/latest" "$src/attestations.jsonl" "$src/signing.pub" "$src/leaves" "$dst/$day/"
   ln -sfn "$dst/$day" "$dst/latest"
   ```
   ```sh
   15 5 * * * /usr/local/bin/pull-attestations
   ```
   It copies the public file and the leaves, never `signing.key`.
   `--link-dest` makes unchanged files hard links, so each day costs only
   the new leaves file.

3. **Check against the copy**, from the archive host:
   ```sh
   ./ma attest check --file /offhost/latest/attestations.jsonl --public-key /offhost/signing.pub
   ```
   `--public-key` trusts only the keys you give, not the ones on the host.
   The copy must be a prefix of the database chain: attestations that the
   copy has and the database lacks are findings. Without a database, for
   example on the machine with the copy:
   ```sh
   mail-archive attest check --offline --file /srv/mail-archive-attest/latest/attestations.jsonl \
     --public-key /safe/signing.pub
   ```
   This checks the chain, the signatures, every leaves file next to the file
   against its signed digest, and that each total follows from the previous
   one, so a message removed without a record shows up there too.

4. **Run `verify` weekly**, as in the [backup
   example](./operations#backups). It checks the attestations first and
   reports `attest-stale` when the newest one is older than twice the
   interval, so a job that stopped is noticed.

5. **Limit who has access** to PostgreSQL and the data directory, and keep
   backups where the archive host cannot change them.

## Findings

| Finding | Meaning |
|---|---|
| `removed` | An attested message is gone and no deletion was recorded. Listed with its hash and the attestation that covered it. |
| `unattested` | A row inside an attested range that the attestation does not list: inserted afterwards. |
| `attestation` | An attestation no longer matches the database: another message at the same number, changed deletion records, or, without its leaves file, only the counts. |
| `attest-file` | The attestation file differs from the database, a line does not parse, or a leaves file is missing or does not match its signed digest. |
| `chain` | A broken chain, a bad hash or signature, or a key that is not trusted. |
| `attest-stale` | A key exists and the newest attestation is older than twice `MAIL_ARCHIVE_ATTEST_INTERVAL`. |

Notes, which are no findings: the file lags behind the database (the next
attestation copies the entries), deletions not yet signed, a key change
(rotation), and messages archived again after a restore (`re-archived`).

## GoBD

For archives kept under the German GoBD, the chain supports the
unalterability requirement as evidence: it documents when a message was in
the archive and makes a later removal visible. It is not a certification and
does not replace the procedure documentation (Verfahrensdokumentation),
which should describe the key, the off-host copies and who checks them.

## Restore from a backup

A backup is the dump first, then the data directory, so after a restore the
attestation file in the data directory can be ahead of the database. The
web server at its start, `./ma attest` and `./ma backup` (with a key) then
import the missing
attestations into the database. They check them first (chain, trusted key,
leaves file), move the message numbering past them, and give new numbers to
messages archived after the restore inside an imported range. Run
`./ma attest` right after `./ma migrate`, before anything archives.

`./ma verify` then lists every attested message the restore lost as
`removed`. Those still on a server come back with the next sync. The
others are in the data directory as orphans: `./ma verify --fix-orphans`
moves them to `data/orphans/<time>/`; copy that directory into `./import`
and bring them back with `./ma import NAME --from /import/<time> --format eml --folder restored`.
Either way they turn into `re-archived` notes.

If the file and the database disagree in any other way, nothing is
written, and `attest` and the web server log it; `verify` shows what
differs.

## Key loss and rotation

If the key is lost, or may be known to others:

```sh
./ma attest keygen --rotate
```

The current public key moves to `attest/retired-keys.pem`, where it stays
trusted for older attestations, and a new key signs the next one. `verify`
notes the change. Copy the new `signing.pub` off the host as well. When you
check with `--public-key`, pass the old and the new public key.

If the key file is gone while attestations exist, the web server logs an
error and writes none until you run `attest keygen --rotate`; it still
starts and archives.

To keep the key out of the data directory and its backups, point
`MAIL_ARCHIVE_ATTEST_KEY_FILE` at a separate mount. The attestation file,
the leaves and the public keys always stay in `data/attest/`.
