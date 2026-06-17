---
schema_version: 2
id: users.update
proposed: true
method: PATCH
path: /api/users/{id}
path_params: [{ name: id, type: string, required: true }]
query_params: []
body_shape: unknown
response_shape: unknown
auth: bearer
source: src/api/users.ts:17
used_by_skills: [account]
confidence: high
openapi_operation_id: null
---

# users.update

<!-- AGENT id="overview" -->
Update the authenticated user's record. The `id` path parameter is always the
acting user's own id, sourced client-side from the session.
<!-- /AGENT -->

## Request

**HTTP:** `PATCH /api/users/{id}`
**Auth:** bearer
**Path params:** `id: string` (required)
**Query params:** none
**Body shape:** `unknown` (TS shape `UpdateUserInput` resolves to `{ name?: string; email?: string }` at source but the discovery walk recorded `unknown`)

## Response

**Response shape:** `unknown` (TS shape `User` at source; discovery walk recorded `unknown`)

## Notes

Called from `src/pages/Profile.tsx` after the user submits the edit form. The
client throws `Error("updateUser failed: <status>")` on non-2xx; the agent
should map common statuses (401 → re-auth, 403 → not your record, 4xx → echo
to user, 5xx → retry once).

<!-- HUMAN id="notes" -->
<!-- /HUMAN -->
