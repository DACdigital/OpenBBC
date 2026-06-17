---
schema_version: 2
id: health
name: Health
domain: "Read the backend's health/ping endpoint"
description: "Use when the user wants to confirm the backend is reachable or list ping records"
user_phrases:
  - "is the backend up"
  - "ping the server"
  - "show me the ping log"
suggested_endpoints:
  - endpoint: ping.list
    role: read
    when: "User asks for a health check or any ping-related query"
flows_using_this: [view-home]
confidence: high
---

# Health

<!-- AGENT id="overview" -->
The `health` domain wraps the only backend health endpoint. Use it for liveness probes and surfacing ping records to the user.
<!-- /AGENT -->

## When to use

Use this skill when the user wants to confirm that the backend is reachable, run a health check, or retrieve ping records.

Trigger phrases:
- "is the backend up"
- "ping the server"
- "show me the ping log"

## Domain vocabulary

- The endpoint is unauthenticated (`auth: none`); no bearer token or session is required.
- Response shape is currently unknown to the discovery walk — the source `+page.svelte:3` fetches it without a TypeScript type annotation. Treat the response as an opaque list until the backend contract is documented.
- There is no write surface on this skill; it is read-only and idempotent.

## Endpoint selection guide

There is exactly one endpoint backing this skill: `ping.list`. Call it on any health or ping-related ask — no preconditions are needed since the endpoint requires no authentication.

## Failure modes

| Result | Meaning | What to do |
|---|---|---|
| 5xx | Backend is down or erroring | Surface the error to the user |
| Network error | Connectivity problem | Surface the error to the user |

## Flows that surface this skill

- [view-home](../flows/view-home.md)

<!-- HUMAN id="notes" -->
<!-- /HUMAN -->
