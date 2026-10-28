---
paths:
  - "web/src/**"
  - "web/index.html"
---
# Web UI
- Carbon progress bars carry no `helperText`: it adds an untranslatable "Loading".
- Class names in views are Carbon's (`cds--`) or defined in `web/src/layout.css`, generic helpers prefixed `app-`; `web/src/layout.test.ts:88` enforces it.
- pnpm is 12.9.1 (`packageManager` in `web/package.json`); corepack is broken in cloud sessions. Call the cached `pnpm.mjs` through a wrapper in your own shim directory, and check its path exists first.
