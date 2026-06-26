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
├── glossary.md               # one-page pivot table (skill → user phrases → endpoints → flows)
├── skills/<id>.md            # one per business-domain specialty — primary read for runtime agent
├── flows/<id>.md             # one playbook per user journey (intent, no HTTP detail)
└── endpoints/<id>.md         # one per discovered backend call — HTTP detail lives here
```

Schema version: `2`. Every generated file's frontmatter carries `schema_version: 2`.
Endpoint ids are *proposed* — derived from frontend call sites, never validated
against any external registry. Every endpoint entry carries `proposed: true`.

## Reading order

When invoked, work through these in order:

1. **`references/output-schemas.md`** — exact schemas for all generated
   files (AGENTS.md, APP.md, glossary, flow, skill, endpoint). Treat as a contract.
2. **`references/lint-contract.md`** — the 15 rules. In this version
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

- **One skill = one business domain.** A skill aggregates endpoints that
  share a domain vocabulary and invariants — e.g. `shopping` (catalog
  reads + order writes) or `account` (user profile reads + writes). Do
  not produce a skill per endpoint (the v1 anti-pattern). If a domain
  has only one endpoint, the skill still exists at the domain level.
- **No HTTP detail outside `endpoints/`.** Method, path, params,
  response shape, auth, source — only in endpoint frontmatter. Skill
  and flow files never carry HTTP detail.
- **The runtime word "tool" must not appear inside `.flow-map/`.**
  Endpoint frontmatter uses `endpoint`/`endpoint-id`, skills use
  `suggested_endpoints`, flows reference skills. Downstream (aikdm,
  bundle, runtime agent) calls these things tools — that mapping is
  intentional and happens outside the discovery layer.
- **Endpoints are the complete inventory.** Every backend call surfaced
  by call-site discovery is emitted as `endpoints/<id>.md`, even when
  no skill suggests it. `used_by_skills[]` may be `[]`.
- **`suggested_endpoints[]` is advisory.** Discovery proposes which
  endpoints belong to a domain; aikdm may add, drop, or re-annotate
  when wiring tools downstream. Discovery is a proposer, not the final
  authority.
- **`<!-- HUMAN id="..." -->` blocks survive regeneration verbatim** on
  flows, skills, and endpoints. `<!-- AGENT id="..." -->` blocks are
  regenerated. Material outside any block is structural.
- **Output confined to `.flow-map/`**. The skill never modifies source
  files outside that directory.
- **Anti-goals:** never generate MCP server code, runtime agent prompts,
  or call any registry API. Never run target-repo code (no
  `npm run dev`, no tests). Never assume an MCP server exists.

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
body / response shape if statically resolvable, and source
coordinates `<file>:<line>`. If a URL is dynamic enough that you can't
resolve it (e.g. a variable type segment), capture it as `unresolved`
with a one-line reason.

**`body_shape` and `response_shape` are JSON Schema, not TypeScript.**
When you resolve a body or response shape, emit it as an inline JSON
Schema object — `{ "type": "object", "properties": { ... }, "required":
[ ... ] }` — never as a TypeScript-style type literal string. Downstream
consumers wire this value directly into the LLM-visible tool argument
schema, and a free-form string collapses to "tool takes no arguments",
which causes the agent to call POST/PUT/PATCH endpoints with empty
bodies. When the shape isn't statically resolvable, emit `null` (body)
or `"unknown"` (response).

### 3. Resolve & group

- Deduplicate the call-site list by `(method, path)`. Assign each unique
  call site an endpoint id (`<resource>.<verb>` dotted-lower-camel — see
  step 4 for naming).
- Group call sites by entry file → one **flow** per entry. Filter out
  entries with zero calls.
- **Cluster endpoints into business-domain skills.** This is the v2 job
  the LLM (you) must do. Group endpoints into the smallest set of
  business-domain skills such that:
    - Endpoints in one skill share domain vocabulary and invariants.
    - A skill's `suggested_endpoints[]` is small enough (typically 1–6)
      to fit a focused domain prompt.
    - Single-endpoint domains are fine when a domain genuinely has one
      action (e.g. a public health-check `ping`).
    - Resource-shaped clustering (one domain per backend resource) is a
      reasonable default but not required. Two resources that always
      appear together (e.g. `cart` + `orders` in a checkout-focused app)
      may be one skill.
- **Skill `id` must be a descriptive multi-word kebab-case phrase that
  names what the agent does in this domain — never a bare single noun.**
  Prefer verb-noun or noun-noun phrases (e.g. `manage-orders`,
  `browse-product-catalog`, `update-user-profile`, `check-system-health`)
  over `orders`, `catalog`, `account`, `health`. The id is what an
  on-demand dispatcher reads when picking a skill at runtime; a vague
  single-word name like `orders` is hard for the dispatcher (and the
  human reviewer) to discriminate. Same rule for `name` — write a short
  human-readable phrase ("Manage orders") not a single word ("Orders").
- Compute the unresolved rate. **If it exceeds 25 %, stop and report.**
  Do not proceed to render.

### 4. Propose endpoint ids

- Default naming convention is `<resource>.<verb>` dotted-lower-camel
  (e.g. `users.update`). On first run for a given repo, check whether
  the user has an existing convention — if so, ask before generating.
- Generate one unique endpoint id per `(method, path)`.
- Mark every entry `proposed: true`. The skill must never emit
  `proposed: false`. Downstream consumers (aikdm) decide if an endpoint
  becomes a wired MCP tool; discovery only proposes the id.

### 5. Render

Read each template under `assets/templates/` and produce the
corresponding wiki file. Write under `<repo>/.flow-map/`:

- `AGENTS.md` — index, retrieval table, mermaid overview, Skills,
  Flows, and Endpoints tables. Frontmatter includes
  `generated_from_sha` (current HEAD).
- `APP.md` — stack, invariants, auth model, conventions, boundaries.
- `glossary.md` — thin one-page pivot: skill → user phrases →
  suggested endpoints → flows that surface this skill.
- `skills/<id>.md` — one per business-domain specialty. Frontmatter
  carries `suggested_endpoints[]` (advisory). Body sections: When to
  use, Domain vocabulary, Endpoint selection guide, Failure modes,
  Flows that surface this skill.
- `flows/<id>.md` — one per user journey. Tool-name-free, HTTP-free.
  Steps reference skills as markdown links to `skills/<id>.md`.
  `skills_used[]` entries carry only `skill:` and `skill_ref:` (no role).
- `endpoints/<id>.md` — one per discovered backend call. Frontmatter
  carries `proposed: true`, method, path, params, response shape, auth,
  source coordinates, `used_by_skills[]`. Body: 1–2-sentence overview,
  request/response detail, notes.

**Deriving the `workflow:` field on each flow:** read the entry file
plus its near transitive imports for control-flow signal. Translate
into a mermaid `flowchart TD` per `references/output-schemas.md`'s
"Workflow notation" subsection. Map call-site sequences to skill nodes
(`id[<skill-id>]`) and early-return / guard checks to decision nodes
(`id{<question?>}`). Loops in the source (while, for-each polling)
become back-edges between existing nodes; do not introduce a
dedicated loop node.

**Supported mermaid dialect (strict — emit only these shapes):**

- Header: `flowchart TD` (or `flowchart LR`). Nothing else.
- Stadium start/end: `start([start])`, `e([end])`.
- Skill rectangle: `id[<skill-id>]`.
- Decision diamond: `id{<question?>}`.
- Plain edge: `a --> b`.
- Labeled edge: `a -- yes --> b` (with single spaces around the
  label; typically `yes` or `no`).

**Do NOT emit:**

- `id{{label}}` parallel-fanout nodes. Flatten `Promise.all` and
  parallel awaits to a serial sequence in declared order — the
  downstream editor models flows as a single-output graph.
- `&`-joined fanouts (`a & b --> c`). Emit two edges: `a --> c` and
  `b --> c`.
- Pipe-delimited edge labels (`a -->|yes| b`). Use the `--` form
  shown above instead.

When control flow can't be determined with `medium`+ confidence,
fall back to a linear chain through `skills_used` in declared
order, e.g.

```
flowchart TD
  start([start]) --> s_<id1>[<id1>] --> s_<id2>[<id2>] --> e([end])
```

Annotate the flow's `confidence:` field accordingly. Lint rule 17
will check that every skill node's label is a declared
`skills_used[].skill`.

Match `references/output-schemas.md` exactly: every required
frontmatter key, every section heading, every block marker. Endpoint
ids never appear in flow bodies and never carry HTTP detail outside
`endpoints/*.md`. Skills carry `suggested_endpoints[]` (advisory)
referencing endpoint ids; flows reference skills.

### 6. Self-check

Walk every rule in `references/lint-contract.md` against the rendered
output. All 15 must pass. The most common slips:

- Rule 5 — flow `description` must start with `Use when`.
- Rule 9 — no HTTP methods, `fetch(`, `axios.`, or `/api/` strings in
  flow or skill bodies. HTTP detail lives only in `endpoints/`.
- Rule 13 — every skill link from a flow must resolve; every endpoint
  id from a skill's `suggested_endpoints[]` must resolve to a file in
  `endpoints/`.
- Rule 14 — `suggested_endpoints[]` and endpoints' `used_by_skills[]`
  must round-trip in both directions.
- Rule 15 — every flow's `workflow:` mermaid block must reference only
  skill ids declared in that flow's `skills_used[]`.

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
  endpoint:<id>`, `--only changed` are not supported in this
  version. If asked to refresh just one flow or endpoint, do it by
  reading and rewriting that file alone, applying the same procedure to
  the affected subset.

If the manual drift loop becomes painful in practice, a tiny driver
script can be reintroduced in a later revision.

## Verifying the skill

Three reference fixtures ship with the skill:

- `tests/fixtures/sample-nextjs/` — Next.js App Router. One `update-profile`
  flow, one `account` domain skill, one endpoint backing the profile write.
- `tests/fixtures/sample-react/` — Vite + react-router. Same flow/skill/endpoint
  shape as nextjs (proves the wiki is framework-invariant).
- `tests/fixtures/sample-sveltekit/` — SvelteKit. Single home flow, one `health`
  domain skill, one `ping`-style endpoint.

Each fixture carries a hand-curated `.flow-map/` directory as the
canonical reference output.

To verify the skill end-to-end on a fixture:

1. Move the canonical `.flow-map/` aside (e.g. rename to
   `.flow-map.gold/`).
2. Run the procedure above against the fixture from scratch.
3. Diff the regenerated `.flow-map/` against `.flow-map.gold/`. Schema,
   frontmatter keys, file IDs, skill ids, endpoint ids, and domain skill
   names should match. Prose may differ — that is acceptable.
4. Walk the lint contract; all 15 rules must pass.
5. Restore the canonical directory.

For HUMAN-block preservation, insert a hand-edited
`<!-- HUMAN id="testnote" -->...<!-- /HUMAN -->` block in
`flows/update-profile.md`, regenerate, and confirm the block survives
verbatim at the same anchor.

## Target-repo `.gitignore`

Track everything under `.flow-map/`. There is no cache directory in v0,
so nothing to ignore.
