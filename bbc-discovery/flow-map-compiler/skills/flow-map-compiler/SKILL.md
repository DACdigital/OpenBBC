---
name: flow-map-compiler
description: >
  Use this skill whenever the user wants to compile, generate, refresh, or
  audit a "flow map" — the .flow-map/ wiki that documents a frontend repo's
  business flows and backend endpoints as MCP-agent context. Trigger
  proactively on phrases like "flow map", "flow-map", "agent context for
  this app", "document this app for an agent", "MCP knowledge base",
  "propose MCP tools", "refresh flow docs", or whenever the user is
  preparing a frontend repo (Next.js, React, SvelteKit, Nuxt, Remix, Astro,
  or any other framework) to be driven by an MCP-tool-calling agent. Also
  use when the user opens a repo that already has a .flow-map/ directory
  and asks about drift or regeneration. The skill is framework-agnostic and
  fully agent-driven — there is no script pipeline; you, the agent, do the
  discovery, extraction, grouping, naming, and rendering by reading source
  directly.
when_to_use: >
  Flow map generation, refresh, drift detection on .flow-map/, proposing
  MCP tool names from frontend call sites, scaffolding agent context for a
  newly-onboarded frontend repo.
user-invocable: true
allowed-tools:
  - Read
  - Grep
  - Glob
  - Write
  - Edit
  - Bash(git rev-parse *)
  - Bash(git diff *)
  - Bash(git status *)
paths:
  - "**/package.json"
  - "app/**/*.{ts,tsx,js,jsx}"
  - "src/app/**/*.{ts,tsx,js,jsx}"
  - "pages/**/*.{ts,tsx,js,jsx}"
  - "src/pages/**/*.{ts,tsx,js,jsx}"
  - "src/**/*.{ts,tsx,js,jsx,svelte,vue,astro}"
  - "components/**/*.{ts,tsx,js,jsx}"
  - "lib/**/*.{ts,tsx,js,jsx}"
  - "openapi.{yaml,yml,json}"
  - "schema.graphql"
---

# flow-map-compiler

Compile a frontend repository into a structured `.flow-map/` wiki that two
audiences will read:

1. **A runtime agent** that drives MCP tools wrapping the backend. The wiki
   gives it semantics, intent, sequencing, preconditions, invariants,
   failure modes, and domain vocabulary.
2. **The engineer who will build that MCP server.** The wiki documents
   which endpoints exist, with what params and shapes, and proposes tool
   names — because no MCP server exists yet and someone has to specify
   it.

This skill is **fully agent-driven**. There is no script pipeline. You read
source, recognise routing conventions and call patterns, and write the
wiki yourself, anchored on the templates and contracts shipped with the
skill.

## What this skill produces

```
<repo>/.flow-map/
├── AGENTS.md                 # entry point + retrieval indices
├── APP.md                    # app-wide invariants, conventions, boundaries
├── glossary.md               # one-page pivot table (skill → capability → tool)
├── skills/<id>.md            # one per agent skill — primary read for runtime agent
├── flows/<id>.md             # one playbook per user journey (intent, no HTTP)
├── capabilities/<name>.md    # one per resource group (HTTP detail lives here)
└── tools-proposed.json       # machine-readable handoff for MCP-server author
```

> "Agent skill" here means one file describing one tool the runtime
> agent can invoke — not the same as the parent Claude Code SKILL.md
> plugin. Same word, different concept. In docs and conversation
> referring to both, prefer "agent skill" for the output artifact and
> "this skill" / "the flow-map-compiler skill" for the producer plugin.

Schema version: `1`. Every generated file's frontmatter (and
`tools-proposed.json`) carries `schema_version: 1`. Tool names are
*proposed* — derived from frontend call sites, never validated against any
external registry. Every tool reference carries `proposed: true`.

## Reading order

When invoked, work through these in order:

1. **`references/output-schemas.md`** — exact schemas for all generated
   files (AGENTS.md, APP.md, glossary, flow, capability, tools-proposed.json).
   Treat as a contract.
2. **`references/lint-contract.md`** — the 16 rules. In this version
   there is no `lint.mjs`; you walk these rules yourself before declaring
   the run done. Output that fails any rule must not ship.
3. **`assets/templates/*.tmpl`** — the structural skeletons you fill.
   Use them as authoring scaffolds; respect the `<!-- AGENT id="..." -->`
   and `<!-- HUMAN id="..." -->` block markers.
4. **`tests/fixtures/sample-{nextjs,react,sveltekit}/`** — three reference
   repos with hand-curated `.flow-map/` outputs. Use them as few-shot
   examples for tone, depth, and shape.

## Hard rules (LOCKED)

These do not change without explicit user approval. If something feels
wrong, stop and ask.

- **Flows are tool-name-free.** Flow files refer to **skills** by id
  (kebab-case verb-noun, e.g. `write-user-profile`) and link to
  `skills/<id>.md`. They never name a proposed MCP tool, never show
  HTTP method or URL path. Each skill's `proposed_tool` frontmatter
  field is the indirection layer — when the MCP server lands and tools
  get renamed, only those frontmatter fields update; flow files don't
  churn.
- **No HTTP detail in flow files.** No `GET `, `POST `, `fetch(`,
  `axios.`, or `/api/` paths in `flows/*.md`.
- **Capabilities own HTTP detail and proposed tool names.** Each
  capability subsection has method, path, params, response shape, auth,
  source coordinates, and the proposed tool name. Proposed names appear
  *only* in capability files, skill frontmatter (`proposed_tool`),
  glossary rows, and `tools-proposed.json` — never in flow bodies.
- **One skill = one intent = one capability tool.** Do not bundle
  unrelated tools into a single skill (mega-skill anti-pattern). If
  two flows surface the same skill, they both link to the same
  `skills/<id>.md`.
- **`tools-proposed.json` is a separate handoff artifact.** Not loaded into
  the runtime agent's context. Bidirectionally consistent with capability
  frontmatter (lint rule 14).
- **`<!-- HUMAN id="..." -->` blocks survive regeneration verbatim.**
  `<!-- AGENT id="..." -->` blocks are regenerated. Anything outside any
  block is structural and always regenerated.
- **Output confined to `.flow-map/`**. The skill never modifies a source
  file in the target repo.
- **Anti-goals:** never generate MCP server code, runtime agent prompts,
  or call any registry API. Never run target-repo code (no `npm run dev`,
  no tests). Never assume an MCP server exists.

## How to run (you, the agent)

When the user asks to "compile" / "generate" / "refresh" the flow map for
a repo, do the following yourself — no scripts to invoke.

### 1. Recon

Read top-level files and detect:

- **Framework.** From `package.json` deps, by priority order: `next`,
  `@sveltejs/kit`, `nuxt`, `@remix-run/*`, `astro`, `solid-start` /
  `@solidjs/start`, `@builder.io/qwik-city`, `vue`, `react`. First match
  wins. If none match, mark as `unknown`. For Next, distinguish App
  Router (`app/` or `src/app/` present) vs Pages Router (`pages/` or
  `src/pages/`) vs both.
- **Language.** TypeScript if a `tsconfig.json` exists, otherwise JS.
- **Monorepo layout.** Check for pnpm/yarn workspaces, turbo.json, or
  nx.json. If monorepo, ask the user which workspace to target if it is
  ambiguous.
- **API client libraries** present in deps: `axios`, `ky`,
  `@tanstack/react-query`, `swr`, `@apollo/client`, `urql`,
  `graphql-request`, `@trpc/client`, `openapi-fetch`, `orval`,
  `@hey-api/openapi-ts`, `@kubb/core`. Plain `fetch` is always assumed
  available.
- **In-repo specs.** Look for `openapi.{yaml,yml,json}`,
  `schema.graphql`, or any `.openapi.*` files.
- **Routing conventions on disk.** Note which of `app/`, `src/app/`,
  `pages/`, `src/pages/`, `src/routes/`, `app/routes/`, `routes/`
  exist. These become the entry-point search roots.

Capture the framework's preferred file-path → URL-route mapping (e.g.
Next App Router: `app/users/[id]/edit/page.tsx` → `/users/{id}/edit`;
SvelteKit: `src/routes/users/[id]/+page.svelte` → `/users/{id}`).

### 2. Trace

For each routing-convention dir present, find entry-shaped files:

| Dir | Entry shapes |
|---|---|
| `app/`, `src/app/` (Next) | `**/page.{ts,tsx,js,jsx}`, `**/route.{ts,tsx,js,jsx}` |
| `pages/`, `src/pages/` (Next/Nuxt/Vue) | `**/*.{ts,tsx,js,jsx,vue}` minus `_app`, `_document`, and `pages/api/*` |
| `src/routes/` (SvelteKit) | `**/+page.{svelte,ts,js}`, `**/+server.{ts,js}` |
| `app/routes/`, `routes/` (Remix) | `**/*.{ts,tsx,js,jsx}` |

Skip `node_modules/`, `.next/`, `dist/`, `build/`, `.svelte-kit/`,
`.flow-map/`.

For each entry, follow imports (just enough breadth to reach call
sites — you don't need a perfect import graph) and record every HTTP /
RPC call site. Patterns to recognise:

- `fetch("...", { method, headers })` and `fetch(\`...\`)`
- `axios.get|post|put|patch|delete(...)`, `axios({ method, url })`
- `ky.get|post|...`
- `@tanstack/react-query` `useQuery` / `useMutation` wrappers — chase
  through to the underlying client (usually `fetch` or `axios`)
- `swr` `useSWR(key, fetcher)` — same: chase the fetcher
- `@apollo/client` / `urql` / `graphql-request` operations — extract
  operation name, type (query/mutation), and any input variables
- `@trpc/client` calls (`trpc.users.update.mutate(...)`)
- `openapi-fetch` typed clients (`client.PATCH("/users/{id}", ...)`)
- Next.js Server Actions — files or functions starting with
  `"use server"`; treat each exported async function as a callable
  endpoint.

For each call site record: method, path (with templated segments
preserved as `{param}`), auth source if discernible (header inspection),
typed body / response shape if statically resolvable, and source
coordinates `<file>:<line>`. If a URL is dynamic enough that you can't
resolve it (e.g. a variable type segment), capture it as `unresolved`
with a one-line reason.

### 3. Resolve & group

- Deduplicate the call-site list by `(method, path)`.
- Group call sites by entry file → one **flow** per entry. Filter out
  entries with zero calls.
- For each unique endpoint, group by first path segment (or by OpenAPI
  tag if a spec exists) → one **capability** per group. Pick a stable
  short name (e.g. `users`, `orders`, `auth`).
- Compute the unresolved rate. **If it exceeds 25 %, stop and report.**
  Do not proceed to render.

### 4. Propose tools

- Default naming convention is `<resource>.<verb>` dotted-lower-camel
  (e.g. `users.update`). On first run for a given repo, check whether
  the user has an existing convention or tool registry — if so, ask
  before generating.
- Generate one unique `proposed_name` per `(method, path)`.
- Mark every entry `proposed: true`. The skill must never emit
  `proposed: false`.

### 5. Render

Read each template under `assets/templates/` and produce the
corresponding wiki file. Write under `<repo>/.flow-map/`:

- `AGENTS.md` — index, retrieval table, mermaid overview, Skills
  table, Flows table, Capabilities table. Frontmatter includes
  `generated_from_sha` (current HEAD).
- `APP.md` — stack, invariants, auth model, conventions, boundaries.
- `glossary.md` — thin one-page pivot: skill → user phrases →
  capability anchor → proposed tool. Each row links to the
  per-skill body in `skills/<id>.md`.
- `skills/<id>.md` — one per agent skill. Frontmatter carries the
  `proposed_tool` (the indirection layer). Body sections: When to
  use, Preconditions, Flows that surface this skill, Failure modes,
  Examples.
- `flows/<id>.md` — one per user journey. Tool-name-free. Steps
  reference skills as markdown links to `skills/<id>.md`.
- `capabilities/<name>.md` — one per capability group. HTTP detail,
  proposed tool name, source coordinates per tool subsection.
- `tools-proposed.json` — bidirectional with capability frontmatter.

Match `references/output-schemas.md` exactly: every required
frontmatter key, every section heading, every block marker. Tool names
never appear in flow bodies or frontmatter; flows link to
`skills/<id>.md`; skills carry `capability_ref` to a capability anchor.

### 6. Self-check

Walk every rule in `references/lint-contract.md` against the rendered
output. All 16 must pass. The most common slips:

- Rule 5 — flow `description` must start with `Use when`.
- Rule 9 — no HTTP methods, `fetch(`, `axios.`, or `/api/` strings in
  flow bodies.
- Rule 13 — every `skills/<id>.md` link from a flow must resolve;
  every `capability_ref` anchor from a skill must exist in the
  linked capability file.
- Rule 14 — `tools-proposed.json` and capability `tools:` frontmatter
  must enumerate the same set of tool names.
- Rule 15 — every `proposed: true` flag is present where required.
- Rule 16 — every skill's `proposed_tool` matches a tool in its
  referenced capability, and `flows_using_this:` round-trips between
  skill and capability.

If any rule fails, fix the offending file (or fix the upstream
recon/trace data and re-render). Do not ship a lint-failing wiki.

Report unresolved-rate, any `TODO:` markers left for the user to
review, and any flows or capabilities you flagged low-confidence.

## HUMAN-block preservation

Before writing any wiki file, if the target file already exists, read
it and extract every `<!-- HUMAN id="..." -->...<!-- /HUMAN -->` block.
Splice each block back into the regenerated file at the matching
anchor.

If a block's `id` no longer corresponds to an anchor in the new file
(structure changed, intent removed), append it under
`## Orphaned human notes` at the bottom of the file — never silently
drop human content.

`<!-- AGENT id="..." -->` blocks are always regenerated. Material
outside any block is structural and always regenerated.

## Drift / idempotence (v0)

This version of the skill is intentionally minimal:

- **Drift detection is manual.** Re-run the skill, then `git diff
  .flow-map/` to see what changed. There is no `--check` mode.
- **Byte-identical idempotence is not guaranteed**, because LLM-authored
  prose may shift slightly between runs. **Schema and structure must be
  identical** run-to-run on unchanged source. If the working tree's
  source has not changed since `AGENTS.md`'s `generated_from_sha`,
  prefer to skip the run rather than churn prose.
- **No targeted regen flags.** `--only flow:<id>`, `--only
  capability:<name>`, `--only changed` are not supported in this
  version. If asked to refresh just one flow or capability, do it by
  reading and rewriting that file alone, applying the same procedure to
  the affected subset.

If the manual drift loop becomes painful in practice, a tiny driver
script can be reintroduced in a later revision.

## Verifying the skill

Three reference fixtures ship with the skill:

- `tests/fixtures/sample-nextjs/` — Next.js App Router. `update-profile`
  flow, `users` capability.
- `tests/fixtures/sample-react/` — Vite + react-router. Same flow and
  capability as nextjs (proves the wiki is framework-invariant).
- `tests/fixtures/sample-sveltekit/` — SvelteKit. Single `home` flow
  hitting `/api/ping`.

Each fixture carries a hand-curated `.flow-map/` directory as the
canonical reference output.

To verify the skill end-to-end on a fixture:

1. Move the canonical `.flow-map/` aside (e.g. rename to
   `.flow-map.gold/`).
2. Run the procedure above against the fixture from scratch.
3. Diff the regenerated `.flow-map/` against `.flow-map.gold/`. Schema,
   frontmatter keys, file IDs, skill ids, capability names, and
   proposed tool names should match. Prose may differ — that is
   acceptable.
4. Walk the lint contract; all 16 rules must pass.
5. Restore the canonical directory.

For HUMAN-block preservation, insert a hand-edited
`<!-- HUMAN id="testnote" -->...<!-- /HUMAN -->` block in
`flows/update-profile.md`, regenerate, and confirm the block survives
verbatim at the same anchor.

## Target-repo `.gitignore`

Track everything under `.flow-map/`. There is no cache directory in v0,
so nothing to ignore.
