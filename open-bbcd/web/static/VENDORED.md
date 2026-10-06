# Vendored static assets

Third-party JS/CSS pinned to specific versions, checked into the repo so the
binary is self-contained and offline-deployable. Update by fetching the new
release into this directory and bumping the version + checksum here.

## drawflow

- Version: 0.0.59
- License: MIT
- Source: https://github.com/jerosoler/Drawflow
- CDN used for fetch: https://cdn.jsdelivr.net/npm/drawflow@0.0.59/dist/

## dagre

- Version: 0.8.5
- License: MIT
- Source: https://github.com/dagrejs/dagre
- CDN used for fetch: https://cdn.jsdelivr.net/npm/dagre@0.8.5/dist/

## htmx

- Version: see `htmx.min.js` (existing — vendored prior to this PR)

## htmx response-targets extension

- File: `htmx-response-targets.min.js`
- Version: 2.0.4 (npm `htmx-ext-response-targets`, peer `htmx.org ^2.0.2`;
  matches the vendored htmx 2.0.4)
- License: 0BSD (same as htmx)
- Source: https://github.com/bigskysoftware/htmx-extensions/tree/main/src/response-targets
- CDN used for fetch: https://cdn.jsdelivr.net/npm/htmx-ext-response-targets@2.0.4/dist/response-targets.min.js
  (the package's own minified build, unmodified)
- sha256: `bde3061dd9b0c7a0b3c7bae63784312bbb7a3a798411b750fe1a5c07a286e63a`

## marked

- Version: 14.1.4
- License: MIT
- Source: https://github.com/markedjs/marked
- CDN used for fetch: https://cdn.jsdelivr.net/npm/marked@14.1.4/

## openbbc-flow.js

- First-party module. Bridges Drawflow ⇄ mermaid `flowchart TD` and runs Dagre
  for auto-layout when no saved positions exist. See its file header for
  details.
