---
paths:
  - "internal/web/ui/**"
  - "internal/web/routes.go"
  - "internal/web/middleware.go"
  - "internal/web/handlers.go"
  - "internal/web/neutralize.go"
  - "internal/web/inline.go"
  - "internal/web/preamble.go"
  - "web/src/frameFit.ts"
  - "docs/reference/security.md"
---
# Serving layer
- A PR here gets `second-review`; the reviewer checks security property 3.
- Neutralizing is the last step on the served bytes (#266).
- The Fetch Metadata guard and its navigation allowlist live in `protect` (`internal/web/middleware.go:49`).
- Evidence for a serving change is raw requests against the real route table.
- A change to a guarantee in `docs/reference/security.md` is the maintainer's decision.
