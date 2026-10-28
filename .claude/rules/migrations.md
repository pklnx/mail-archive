---
paths:
  - "internal/store/migrations/**"
  - "internal/store/migratelock.go"
  - "internal/store/collation.go"
  - "internal/store/store.go"
  - "cmd/mail-archive/maintenance.go"
---
# Migrations
- A schema change gets `db-migration` and `second-review` (CLAUDE.md, Ticket flow 6).
- Never edit a released migration; add a new one; `Down` must work (`docs/development/index.md`, "Database migrations").
- A migration that locks a large table gets a row in `docs/guide/operations.md`, "One-time steps", advising to stop the web server where the ALTER blocks deliveries.
- goose runs on its own pool with `MinConns`/`MinIdleConns` zeroed; keep it (`internal/store/store.go:245-250`, #274, #279).
- Databases built on `postgres:18-alpine` need the collation rebuild in `./ma migrate` (#159).
