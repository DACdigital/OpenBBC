---
schema_version: 1
framework:
  name: react
  version: "19.0.0"
  router: react-router-dom
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
sample-react is a single-route Vite + React + react-router-dom application used as a flow-map vertical-slice fixture. It exists to validate that the wiki shape is framework-invariant -- a parallel sample-nextjs fixture exposes the identical user flow against the identical backend contract.
<!-- /AGENT -->

## Stack

- react 19.0.0
- Language: ts
- API clients: fetch

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
- One API helper per resource lives under `src/api/`.

## Boundaries

1. The agent must never attempt to mutate another user's record.
2. The agent must never invent fields not declared in the relevant capability's body shape.
3. The agent must never read or transmit the bearer token in agent output.

<!-- HUMAN id="extra-context" -->
<!-- /HUMAN -->
