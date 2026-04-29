# Rule packs

One file per supported HTTP / RPC client library. The loader at
`_loader.mjs` auto-discovers every `*.mjs` file in this directory (the
underscore prefix excludes non-pack files). Adding a new client library
means dropping a new `.mjs` file here; no detection logic changes.

## Pack contract

Each pack must export:

```js
export const meta = { id: "<lib-name>", client: "<short-id>" };
export function detect(pkgJson) { /* boolean */ }
export function extract(filePath, fileText) {
  return { calls: [...], unresolved: [...] };
}
```

`detect` receives the parsed `package.json` and returns whether this
client library is in use. `extract` runs against each reachable source
file and emits typed call sites or unresolved entries.

A typed call site:

```js
{
  file:        "lib/api/users.ts",
  line:        17,
  method:      "PATCH",
  path:        "/api/users/{id}",
  auth:        "bearer" | "cookie" | "none",
  client:      "fetch",
  confidence:  "high" | "medium" | "low",
  raw_snippet: "fetch(`/api/users/${id}`, { ... })"
}
```

An unresolved entry:

```js
{
  file:    "components/Search.tsx",
  line:    88,
  snippet: "fetch(`/api/search?q=${q}&type=${t}`)",
  reason:  "type variable not statically resolvable"
}
```

## Coverage map

The packs below are *the* HTTP / RPC surface most React + Next.js apps
use in production today. Coverage is checked by
`tests/snippets/run.mjs` (per-pack) and `tests/snippets/openapi.mjs`
(spec ingestion). Both are zero-deps.

| Pack | Detect via package.json | Extracts | Test |
|---|---|---|---|
| `fetch.mjs` | always (native) | `fetch(url, opts)` | ✓ |
| `axios.mjs` | `axios` | `axios.<verb>(url, …)`, `axios({method,url})`, `<inst>.<verb>(...)` | ✓ |
| `openapi-fetch.mjs` | `openapi-fetch` | `client.GET/POST/...("/path", …)` | ✓ |
| `trpc.mjs` | `@trpc/{client,react-query,server}` | `trpc.x.y.{useQuery,useMutation,query,mutate}` | ✓ |
| `apollo.mjs` | `@apollo/client` | `gql\`...\`` operations (query / mutation / subscription) | ✓ |
| `urql.mjs` | `urql`, `@urql/core`, `@urql/next` | same gql shape as apollo | ✓ |
| `server-actions.mjs` | `next` | `"use server"` modules → exported async functions | ✓ |
| `tanstack-query.mjs` | `@tanstack/react-query`, `react-query` | wrappers only — endpoint comes from inner fetch/axios pack | ✓ |
| `swr.mjs` | `swr` | wrappers only — endpoint comes from inner fetcher pack | ✓ |

**OpenAPI spec ingestion** (`scripts/ingest-openapi.mjs`) is the
multiplier for *generated* clients. orval, kubb, hey-api,
openapi-typescript-codegen, and Kiota all derive from the same OpenAPI
spec — so reading the spec covers all of them in one shot. Run it after
trace; every operation in the spec is appended to `callsites.ndjson` as
a `client="openapi-spec"` synthetic call. Endpoints with no consuming
flow surface in `endpoints.json` under `backend_declared`.

## Pack contract — wrappers channel

In addition to `calls` and `unresolved`, a pack may return a `wrappers`
array. Wrappers are file+line annotations that *don't* introduce new
endpoints; they tag a region as "wrapped in react-query / SWR / etc."
so the compile step can carry that context into prose. `tanstack-query`
and `swr` are pure-wrapper packs.

```js
return {
  calls: [],
  unresolved: [],
  wrappers: [
    { file: "Profile.tsx", line: 42, hook: "useMutation", kind: "mutation" }
  ],
};
```

## Not yet shipped

- `orval.mjs`, `kubb.mjs`, `hey-api.mjs` — the OpenAPI ingestion path
  covers the generated *endpoints*; a dedicated pack would add per-call
  wrapping detection (e.g. "this call site uses the generated
  `useGetUserById` hook"), which is non-essential for v1.
- Real GraphQL schema resolution — currently we extract operation names
  only, not argument/return types. A schema.graphql ingestion path
  parallel to `ingest-openapi.mjs` would close that gap.
- ts-morph type resolution — current packs are regex+scope; ts-morph
  would lift confidence on dynamic call sites.
