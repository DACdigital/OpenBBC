---
schema_version: 1
id: update-user-record
name: Update user record
description: "Use when the user wants to change their account name or email"
user_phrases:
  - "change my name"
  - "update my email"
  - "fix my profile"
  - "my email is wrong"
role: write
capability_ref: capabilities/users.md#users-update
proposed_tool: users.update
flows_using_this: [update-profile]
confidence: high
---

# Update user record

<!-- AGENT id="overview" -->
Persist the authenticated user's edited profile fields (name, email)
to the backend. This is the single write surface on the user record
in v0; there is no create or delete from this skill.
<!-- /AGENT -->

## When to use

The user has stated a change to their own account fields and wants
that change persisted. Use after the form values have been validated
client-side; the server will re-validate and reject with 422 if the
input is malformed.

Trigger phrases:
- "change my name"
- "update my email"
- "fix my profile"
- "my email is wrong"

## Preconditions

1. User is signed in (a bearer token is present in client-side storage).
2. The user has supplied at least one new field value to write.
3. The acting user is updating their own record (id == self).

## Flows that surface this skill

- [update-profile](../flows/update-profile.md) — entry from the
  `/profile` route's form submit.

## Failure modes

| Result | Meaning | What to do |
|---|---|---|
| 401 | Bearer token missing or expired | Ask the user to sign in again |
| 403 | User trying to update someone else | Refuse and explain the boundary |
| 422 | Validation failed (e.g., bad email) | Echo the error and ask for a fix |
| 5xx | Backend transient failure | Retry once with backoff, then surface |

## Examples

**1.** User says: *"change my name to Sam"*

Expected tool call shape (proposed — exact arguments depend on the
final MCP server):

```
users.update({ id: <self>, name: "Sam" })
```

**2.** User says: *"my email is wrong, it's actually sam@example.com"*

Expected tool call shape:

```
users.update({ id: <self>, email: "sam@example.com" })
```

<!-- HUMAN id="notes" -->
<!-- /HUMAN -->
