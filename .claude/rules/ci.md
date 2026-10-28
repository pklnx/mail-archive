---
paths:
  - ".github/workflows/**"
  - "scripts/ci-*"
---
# CI
- Images come through mirror.gcr.io with Docker Hub as fallback (`scripts/ci-docker-mirror.sh`, `scripts/ci-pull-images.sh`); the PostgreSQL services of `test` and `web-e2e` have no fallback. Rerun a pull failure only per the flaky-test rule.
