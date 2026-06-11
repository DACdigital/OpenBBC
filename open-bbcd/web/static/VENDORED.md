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

## marked

- Version: 14.1.4
- License: MIT
- Source: https://github.com/markedjs/marked
- CDN used for fetch: https://cdn.jsdelivr.net/npm/marked@14.1.4/

## openbbc-flow.js

- First-party module. Bridges Drawflow ⇄ mermaid `flowchart TD` and runs Dagre
  for auto-layout when no saved positions exist. See its file header for
  details.
