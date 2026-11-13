---
paths:
  - ".github/workflows/**"
  - "scripts/ci-*"
---
# CI
- Images come through mirror.gcr.io with Docker Hub as fallback (`scripts/ci-docker-mirror.sh`, `scripts/ci-pull-images.sh`); the PostgreSQL services of `test`, `web-e2e` and `web-e2e-webkit` have no fallback. Rerun a pull failure only per the flaky-test rule.
- `web-e2e-webkit` runs the e2e suite in Playwright's WebKit (`pnpm e2e:webkit`, all but the tests tagged `@cdp`, `@chromium-layer`, `@chromium-launch`; #398); not required, a red result blocks merging. It installs WebKit's system libraries with apt on every run (`playwright install --with-deps webkit`); a slow apt step is not a flake.
