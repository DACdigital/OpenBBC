# Architecture

## Two audiences, two artifact tiers

The wiki serves two distinct readers and the file types are scoped to
each:

| File type            | Audience 1 (runtime agent) | Audience 2 (MCP-server author) |
|----------------------|----------------------------|--------------------------------|
| `flows/*.md`         | yes — primary              | no                             |
| `capabilities/*.md`  | yes — secondary            | yes — primary                  |
| `tools-proposed.json`| no                         | yes — exclusive                |
| `AGENTS.md`, `APP.md`, `glossary.md` | yes — primary  | reference only                 |

**Flow files are tool-name-free.** They reference **intent keys**
(kebab-case verb-noun) and link to glossary anchors. Tool names never
appear in flow bodies — neither in prose, nor in mermaid diagrams, nor in
frontmatter. Lint rule 9 enforces no HTTP leakage; rule 13 enforces the
two-hop link path.

**Glossary is the indirection layer.** It maps user phrases → intent
keys → currently-proposed tool names → capability anchors. When the MCP
server lands and tools get named, the glossary's "Proposed tool" column
is the only thing that updates. Flow files stay byte-identical.

**Capability files are the technical bridge.** They contain HTTP detail
(method, path, params, auth, response), the proposed tool name, and the
source coordinates. Both the runtime agent (when it needs HTTP detail
to debug) and the MCP-server author read these.

**`tools-proposed.json` is the dev handoff.** Machine-readable, never
loaded into the agent's runtime context.

## Four phases

```
┌──────────────┐    ┌──────────────┐    ┌──────────────┐    ┌──────────────┐
│  1. INGEST   │ →  │  2. COMPILE  │ →  │   3. QUERY   │ →  │  4. FEEDBACK │
│  raw sources │    │  → wiki tree │    │ agent reads  │    │ → back to src│
└──────────────┘    └──────────────┘    └──────────────┘    └──────────────┘
       ↑                                                              │
       └──────────────────────────────────────────────────────────────┘
```

The skill owns Phases 1, 2, and 4. Phase 3 is the runtime agent and is
out of scope; the skill must produce output correctly shaped for it.

### Phase 1 — Ingest (deterministic, no LLM)

Static analysis only. No prose. Output goes to `.flow-map/.cache/`.

1. **Recon.** Detect:
   - Framework adapter (`next` / `react` / …) via
     `scripts/adapters/<id>.mjs#probe()`.
   - Language (ts / js).
   - Monorepo layout (pnpm/yarn workspaces, turbo, nx).
   - API client libraries from `package.json`: axios, ky,
     @tanstack/react-query, swr, @apollo/client, urql, graphql-request,
     @trpc/client, openapi-fetch, orval, @hey-api/openapi-ts,
     @kubb/core, zod, @hookform/resolvers.
   - In-repo OpenAPI / GraphQL specs.

   Output: `.flow-map/.cache/recon.json`.

2. **Enumerate.** The active adapter's `enumerateEntries(repoRoot)`
   returns the entry-point files (e.g. `app/**/page.tsx` for Next App,
   route components for react-router). Build the import graph via
   `npx dependency-cruiser -T json`.

   Output: `.flow-map/.cache/entries.json`,
   `.flow-map/.cache/deps.json`.

3. **Trace.** From each entry, BFS the import graph. On each reachable
   file run only the ast-grep rule packs corresponding to libraries the
   recon detected.

   Output: `.flow-map/.cache/callsites.ndjson`.

4. **Resolve.** ts-morph driver with `skipAddingFilesFromTsConfig: true`
   and targeted `addSourceFilesAtPaths`. Resolve types, follow Zod, deref
   OpenAPI, walk tRPC `AppRouter`.

   Output: `.flow-map/.cache/endpoints.json`,
   `.flow-map/.cache/unresolved.json`.

**Done when:** `endpoints.json` lists every flow's call sites with method,
path, and typed params — or marks them unresolved with a reason.
Unresolved rate must be reported. Refuse to proceed to Phase 2 if it
exceeds 25% (configurable, hard-fails by default).

### Phase 2 — Compile (LLM-driven, scaffolded by templates)

Cache is input; wiki + `tools-proposed.json` are output. Templates carry
structure; the LLM only fills prose-shaped slots inside `<!-- AGENT -->`
blocks.

1. `scripts/group.mjs` — group call sites into flows using the active
   adapter's `groupFlows(callsites)` rule.
2. `scripts/propose-tools.mjs` — apply the user-confirmed naming
   convention.
3. Render `AGENTS.md`, `APP.md`, `glossary.md`.
4. Render `capabilities/<name>.md` — one per resource (group by first
   path segment, or by OpenAPI tag if present).
5. Render `flows/<id>.md` — one per flow. Tool names link to capability
   anchors.
6. Render `tools-proposed.json`.

**Done when:** `lint.mjs` passes (all 15 rules).

### Phase 4 — Feedback

1. **Drift detection.** `scripts/check-drift.mjs` re-runs Phase 1 cheaply
   and diffs against `AGENTS.md` frontmatter's `generated_from_sha`. Used
   as pre-commit hook or CI gate.
2. **Human-block preservation.** Phase 2 reads existing `.flow-map/<file>`
   before writing, extracts `<!-- HUMAN id="..." -->` blocks, splices
   them back into the regenerated file at the same anchor. Orphaned
   blocks land under `## Orphaned human notes` at the bottom.
3. **Targeted regen.** `--only changed`, `--only flow:<id>`,
   `--only capability:<name>`, `--check`.

## Pluggable framework adapters

Every supported framework is a module under `scripts/adapters/<id>.mjs`
exporting:

```js
export function probe(repoRoot) {/* returns confidence 0..1 */}
export async function enumerateEntries(repoRoot) {/* → file paths */}
export function groupFlows(callsites, entries) {/* → flows array */}
export const meta = { id: "next", router: "app|pages|both", language: "ts" }
```

Adding a new framework means dropping a new file in `scripts/adapters/`.
Recon picks the adapter with the highest probe score. Multiple workspaces
in a monorepo can use different adapters.

API-client detection (`scripts/rules/*.yml`) is shared across all
adapters — `fetch`, `axios`, etc. look the same regardless of framework.

## Cache vs. tracked output

- `.flow-map/.cache/` — git-ignored. Regenerated on every run.
- `.flow-map/AGENTS.md`, `APP.md`, `glossary.md`, `flows/`,
  `capabilities/`, `tools-proposed.json` — git-tracked.
