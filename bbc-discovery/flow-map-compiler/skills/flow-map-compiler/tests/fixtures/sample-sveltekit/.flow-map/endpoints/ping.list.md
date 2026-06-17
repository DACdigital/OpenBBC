---
schema_version: 2
id: ping.list
proposed: true
method: GET
path: /api/ping
path_params: []
query_params: []
body_shape: null
response_shape: unknown
auth: none
source: src/routes/+page.svelte:3
used_by_skills: [health]
confidence: high
openapi_operation_id: null
---

# ping.list

<!-- AGENT id="overview" -->
Read the backend's ping/health endpoint. Returns a list of ping records;
the exact response shape is not statically resolvable from the source.
<!-- /AGENT -->

## Request

**HTTP:** `GET /api/ping`
**Auth:** none
**Path params:** none
**Query params:** none
**Body shape:** none (GET)

## Response

**Response shape:** `unknown` (discovery walk could not statically resolve the shape from `src/routes/+page.svelte:3`; the agent should treat the response as an opaque list until the backend contract is documented)

## Notes

Called from `src/routes/+page.svelte` on page load. No bearer token is required.
On non-2xx the SvelteKit page surfaces the error to the user; the agent should
do the same.

<!-- HUMAN id="notes" -->
<!-- /HUMAN -->
