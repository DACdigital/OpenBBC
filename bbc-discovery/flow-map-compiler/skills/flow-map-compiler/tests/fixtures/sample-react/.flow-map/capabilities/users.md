---
schema_version: 1
capability: users
summary: "Read and modify the authenticated user's account record"
tools:
  - tool: users.update
    proposed: true
    does: "Update the authenticated user's name and/or email"
    method: PATCH
    path: "/api/users/{id}"
    auth: bearer
    confidence: high
    source: src/api/users.ts:17
flows_using_this: [update-profile]
---

# Users

<!-- AGENT id="overview" -->
The `users` capability covers operations on a single user account record. The runtime agent should treat user records as tenant-scoped: a user can only modify their own record, and the bearer token determines the acting identity. Field-level permissions are enforced server-side, so the agent does not need to filter inputs -- but it should refuse on behalf of the user when the user asks to change a field that this capability cannot change.
<!-- /AGENT -->

## Concepts the agent must know

- The `id` path parameter is always the acting user's own id, sourced client-side from the session.
- All writes are authenticated with a bearer token; missing or expired tokens produce 401.
- The backend currently exposes only one verb on this capability: `update`. There is no create or delete from this surface.

## When to reach for which tool

- "change my name" → `users.update`
- "fix my email" → `users.update`
- "update my profile" → `users.update`

---

## users.update  {#users-update}

**Proposed tool name:** `users.update` (proposed — no MCP server yet)
**HTTP:** `PATCH /api/users/{id}`
**Auth:** bearer
**Confidence:** high
**Source:** `src/api/users.ts:17`

**Path params:** `id: string` (required)

**Query params:** none

**Body shape:**

`unknown`

**Response shape:**

`unknown`

**When to call it:**
- "change my name to X"
- "update my email to Y"
- "fix my profile"

**Used by flows:** [update-profile](../flows/update-profile.md)

---


## Things this capability cannot do

- Cannot delete a user account (no tool surface).
- Cannot change another user's record (server enforces 403).
- Cannot rotate a password (separate capability, not yet documented).

<!-- HUMAN id="notes" -->
<!-- /HUMAN -->
