---
schema_version: 2
id: account
name: Account
domain: "Updating the acting user's own profile"
description: "Use when the user wants to change their profile (name, email)"
user_phrases:
  - "change my name"
  - "fix my email"
  - "update my profile"
suggested_endpoints:
  - endpoint: users.update
    role: write
    when: "User confirms a change to their name or email"
flows_using_this: [update-profile]
confidence: high
---

# Account

<!-- AGENT id="overview" -->
The account skill covers the single write surface on the acting user's own profile record. Because requests are scoped by a bearer token, only the signed-in user's own record is ever reachable through this skill. The backend enforces field-level permissions and tenant isolation server-side.
<!-- /AGENT -->

## When to use

Use this skill when the user has stated they want to change their own profile fields (name or email) and wants that change persisted.

Trigger phrases:
- "change my name"
- "fix my email"
- "update my profile"

## Domain vocabulary

- The bearer token scopes every call to the acting user; no cross-user writes are possible through this skill.
- The `id` path parameter is always the acting user's own id, sourced client-side from the session.
- The backend enforces field-level permissions; the agent does not need to filter inputs, but should refuse if the user asks to change a field this skill does not accept.
- Only `name` and `email` are editable through this surface.
- There is no delete or create surface on this skill.
- Password rotation is out of scope and handled by a separate, undocumented capability.

## Endpoint selection guide

There is exactly one endpoint backing this skill: `users.update`. Call it only on explicit user confirmation of a change — do not call it speculatively or before the user has confirmed the new values.

## Failure modes

| Result | Meaning | What to do |
|---|---|---|
| 401 | Bearer token missing or expired | Ask the user to sign in again |
| 403 | Server refused — modifying another user's record | Surface the error; this should not happen for the acting user's own record |
| 5xx | Backend transient failure | Retry once, then surface the error to the user |

## Flows that surface this skill

- [update-profile](../flows/update-profile.md)

<!-- HUMAN id="notes" -->
<!-- /HUMAN -->
