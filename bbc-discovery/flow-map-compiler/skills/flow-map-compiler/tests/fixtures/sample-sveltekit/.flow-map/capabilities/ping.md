---
schema_version: 1
capability: ping
summary: "TODO: one-sentence summary of the ping capability."
tools:
  - tool: ping.list
    proposed: true
    does: "TODO: one-line semantic description"
    method: GET
    path: "/api/ping"
    auth: unknown
    confidence: high
    source: src/routes/+page.svelte:3
flows_using_this: [view-home]
---

# Ping

<!-- AGENT id="overview" -->
TODO: what ping represents in the domain; constraints and invariants.
<!-- /AGENT -->

## Concepts the agent must know

- TODO: invariant for ping.

## When to reach for which tool

- "TODO: user intent phrase" → `ping.list`

---

## ping.list  {#ping-list}

**Proposed tool name:** `ping.list` (proposed — no MCP server yet)
**HTTP:** `GET /api/ping`
**Auth:** unknown
**Confidence:** high
**Source:** `src/routes/+page.svelte:3`

**Path params:** none

**Query params:** none

**Body shape:**

`unknown`

**Response shape:**

`unknown`

**When to call it:**
- "TODO: phrase"

**Used by flows:** [view-home](../flows/view-home.md)

---


## Things this capability cannot do

(none documented yet)

<!-- HUMAN id="notes" -->
<!-- /HUMAN -->
