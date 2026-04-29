---
schema_version: 1
framework:
  name: next
  version: "15.0.0"
  router: app
api_clients: [fetch, server-actions]
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
sample-nextjs is a single-screen Next.js App Router application used as a flow-map vertical-slice fixture. It exists to validate that the wiki shape is framework-invariant -- a parallel sample-react fixture exposes the identical user flow against the identical backend contract.
<!-- /AGENT -->

## Stack

- next 15.0.0
- Language: ts
- API clients: fetch, server-actions

## Invariants

1. The acting user's id is sourced client-side from `localStorage.userId`.
2. All authenticated requests carry a bearer token from `localStorage.token`.
3. All API paths are mounted under `/api` and called as relative URLs.
4. Error responses are surfaced verbatim to the user.

## Auth model

The user signs in elsewhere; the bearer token and user id are persisted in `localStorage`. Each request reads both at invocation time. There is no automatic refresh -- a 401 response is surfaced as `please sign in again`.

## Conventions

- Form submission is wrapped in a `try / finally` that flips a `saving` flag.
- Errors set local component state and render below the form.
- One API helper per resource lives under `lib/api/`.

## Boundaries

1. The agent must never attempt to mutate another user's record.
2. The agent must never invent fields not declared in the relevant capability's body shape.
3. The agent must never read or transmit the bearer token in agent output.

<!-- HUMAN id="extra-context" -->
<!-- /HUMAN -->
