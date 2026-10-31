---
paths:
  - ".github/workflows/**"
  - "scripts/ci-*"
---
# CI
- Images come through mirror.gcr.io with Docker Hub as fallback (`scripts/ci-docker-mirror.sh`, `scripts/ci-pull-images.sh`); the PostgreSQL services of `test`, `web-e2e` and `web-e2e-webkit` have no fallback. Rerun a pull failure only per the flaky-test rule.
- `web-e2e-webkit` runs only `web/e2e/frame-links.spec.ts` in WebKit until #398 (`pnpm e2e:webkit`); not required, a red result blocks merging.
