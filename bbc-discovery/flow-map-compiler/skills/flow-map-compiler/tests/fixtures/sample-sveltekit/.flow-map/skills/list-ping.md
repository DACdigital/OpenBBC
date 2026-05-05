---
schema_version: 1
id: list-ping
name: List ping
description: "Use when the home view loads and needs to fetch the ping list from the backend"
user_phrases:
  - "TODO: phrase 1"
  - "TODO: phrase 2"
role: read
capability_ref: capabilities/ping.md#ping-list
proposed_tool: ping.list
flows_using_this: [view-home]
confidence: medium
---

# List ping

<!-- AGENT id="overview" -->
TODO: 2–3 sentences describing what this skill returns and when the
runtime agent should reach for it. The fixture is intentionally
sparse — real wikis fill these in from frontend call-site context.
<!-- /AGENT -->

## When to use

The home route needs to render a list of ping records on mount. This
skill is read-only and idempotent; safe to call any time the view
needs to refresh.

Trigger phrases:
- "TODO: phrase 1"
- "TODO: phrase 2"

## Preconditions

1. TODO: precondition

## Flows that surface this skill

- [view-home](../flows/view-home.md) — entry from the SvelteKit
  home route's data load.

## Failure modes

| Result | Meaning | What to do |
|---|---|---|
| 401 | auth missing/expired | ask the user to sign in again |
| non-2xx | operation failed | surface the error to the user |

## Examples

**1.** User says: *"TODO: phrase 1"*

Expected tool call shape (proposed — exact arguments depend on the
final MCP server):

```
ping.list()
```

<!-- HUMAN id="notes" -->
<!-- /HUMAN -->
