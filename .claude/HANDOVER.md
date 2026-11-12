# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Last release
- **v1.5.0** (2026-11-05, main 7482e83, release run 42013698896, digest sha256:de611eb4…):
  - encryption at rest (#392: #508, #510, #513, #518, #519);
  - auditor access for tax advisors and auditors (#393: #509, #511, #515, #517);
  - GoBD export on the command line (#394: #507, #512).
- Migrations 00036 (auditor) and 00037 (blob_encryption).
- Milestone 1.5.0 is closed.
- **1.5.1 pending** (milestone 1.5.1, maintainer OK 2026-11-12): #524 is merged as #525 (main a3609fd). It moves the Go toolchain to 1.27.4 (GO-2026-6859, net/http) and golang.org/x/net to 0.62.0 (GO-2026-6860 = CVE-2026-78661 from the image scan, GO-2026-6866). None of these is reachable in practice: only `html.NewTokenizer` is used, and there is no `MaxBytesReader`. But `vulncheck` on 1.27.3 has failed every run since 2026-11-11. The release needs the maintainer (see Maintainer actions).
- Release test #520 (Mac Safari, iPhone, iPad, VoiceOver) runs after the release; a failure goes into 1.5.1.
- Earlier: v1.4.0 (main 774a2a0), customer groups (#470); release test #504 passed.

## Open PRs
- Dependabot #523 (go-pdfium 1.21.3 → 1.21.4 with PDFium engine 8057 → 8111, yaml v3 3.0.5 → 3.0.6; it replaces #491) and #522 (vitepress alpha.20 → alpha.21, docs site only): milestone 1.6.0. Merge them only **after the 1.5.1 release**. The new PDFium has no advisory and is not patch material (coordinator, 2026-11-12). Each needs `@dependabot rebase` onto main (vulncheck was red without #525), green CI and one review. #523: a test of the PDF text extraction (`internal/attachtext`) in CI is enough. The rebase also removes the stale x/net v0.61.0 lines in `go.sum`.

## Open tickets
- **1.6.0:** these are `ready`:
  - #395 SMTP AUTH/TLS (#454–#456);
  - #396 start without CLI (#451–#453). Part 3 (#453) runs `encryption.EnableAtSetup` from #392.
  - #398 WebKit e2e with #343.
- **1.6.0, in `plan`:** #397 operator/quotas/mail alerts (#446–#450), with the quota per customer group and alerts to admins only.
- **Not planned yet:** #432 database encryption, #418 OCR for scanned PDFs.
- **1.7.0 (Microsoft and Google):** #391 OAuth2 (`ready`, #426–#430); #475 journaling from Microsoft 365 (after #430, not planned).
- **Follow-up:** #516, the demo auditor's grant window after `DOCS_NOW` + 90 days (before late January 2027).
- **Device checks:** #372 round 2, Android parts open; #520 is the 1.5.0 release test.

## Active decisions
- **Operating model** (maintainer, 2026-11-01): one shared instance in full-service mode, where the operator is its admin and manages the customers' users and accounts. Individual instances for single customers on request. Customer groups shipped in 1.4.0.
- **Coordinator autonomy** (maintainer, 2026-10-31):
  - The coordinator decides alone on UI wording, technical trade-offs, small product details inside an approved goal, and tightening security, and records these decisions.
  - It always asks about new goals and milestone scope, releases, and anything that widens access or weakens security or privacy (including `security.md` guarantees).
  - It reports at milestones and blockers only.
- **Customer groups** (maintainer, 2026-11-02/03):
  - One shared instance, with groups separating customers logically.
  - No group admin. Sharing works by exact name inside the group.
  - The group UI is hidden while there is only one group; the first extra group is created on the CLI.
  - Auditors belong to one group, and grants stay inside it. Exports never span groups.
- **Encryption at rest** (#392):
  - New instances start with it on (maintainer decision 6); existing ones switch offline with `encryption enable`.
  - `rekey` creates a new keyring key. `rotate-key` keeps the keyring key and replaces one slot: `--replace` names the slot, and without it the command refuses when the configured key is not the archive key (coordinator, #437 and #519 review rounds 2 and 3).
  - `rekey` drops every slot whose key is not at hand. It keeps the configured key, the keys given with `--keep-key-file` and a new recovery key, and it prints the new recovery key before the conversion. `add-key` and `remove-key` need the database.
  - The database stays plaintext until #432.
- **Merge mechanics:** the merge tool pins with `expectedHeadSha`, not `sha`. A branch behind main is refused for required checks; bring main in with a merge commit.
- **GitHub operations without an MCP tool** (maintainer, 2026-11-01): agents do them themselves, using the REST fallback in `.claude/github-operations.md`. In this cloud session the classifier refused `curl` with the token (Auto-Mode Bypass), so milestones stayed undone.
- **Disk:** the shared Go build cache fills the sandbox. Trimming it (`find /root/.cache/go-build -type f -mmin +180 -delete`) needs the maintainer to switch to "Accept edits" first.
- **Target hardware:** plans state memory and CPU cost on 4 GB RAM / 2 cores and stay within it.
- #459 (Safari): the server rewrites links in served HTML mail, and navigations to `/api` with `Sec-Fetch-Site` `cross-site` or `same-site` get 403. Stored mail, `/raw`, parts and exports stay byte-identical.
- **Product details:**
  - Target group: individuals and small businesses.
  - #321: Disable stays usable while a sync runs; only Remove is blocked.
  - German term for the deletion grace period: "Karenzzeit" (#339).
- **Releases and PR bodies:**
  - No backports, only the newest line; old tags and images stay.
  - Patch releases carry only `bug`/`docs` PRs.
  - Avoid "fix #N" and other closing keywords in PR bodies for issues that stay open. Conversely, check that "Closes #N" really closed the issue: #435 stayed open after #513.

## Maintainer actions
- **Release 1.5.1:** Actions → Release → Run workflow, from main a3609fd or later, dry run first. Then rescan `ghcr.io/pklnx/mail-archive:1.5.1`: CVE-2026-78661 must be gone (#524, last Done-when item). After the release the coordinator closes milestone 1.5.1.
- #520 release test of 1.5.0.
- Manual checks from #519, which no test covers: `./ma encryption rotate-key --new-key-file /keys/archive-2.key` through Compose (service `mail-archive-keys`: the new file owned by 65532 with mode 0400, then `.env` switched and `docker compose up -d`), and `./ma encryption rekey` on a real instance, including `--keep-key-file`.
- On the shared instance:
  - Back up, then run `./ma migrate` (00036, 00037).
  - Encryption at rest is optional: `./ma encryption keygen`, keep the archive key and the recovery key off the server, then `./ma encryption enable` with web and worker stopped (operations.md, "Encryption at rest").
