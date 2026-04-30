---
schema_version: 1
framework:
  name: sveltekit
  version: "^2.0.0"
  router: filesystem
api_clients: [fetch]
api_base_url:
  source: hardcoded
  name: null
  default: "/api"
auth:
  type: unknown
  token_source: null
  refresh: null
providers: []
---

# App context

<!-- AGENT id="overview" -->
TODO: 2-3 sentence app context.
<!-- /AGENT -->

## Stack

- sveltekit ^2.0.0
- Language: js
- API clients: fetch

## Invariants

1. TODO: list system-wide invariants.

## Auth model

TODO: where tokens come from, how attached, refresh, 401 behavior.

## Conventions

- TODO: cross-flow patterns.

## Boundaries

1. TODO: list things the agent must never do.

<!-- HUMAN id="extra-context" -->
<!-- /HUMAN -->
