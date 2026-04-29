---
name: flow-map-compiler
description: >
  Use this skill whenever the user wants to compile, generate, refresh, or
  audit a "flow map" — the .flow-map/ wiki that documents a frontend repo's
  business flows and backend endpoints as MCP-agent context. Trigger
  proactively on phrases like "flow map", "flow-map", "agent context for
  this app", "document this app for an agent", "MCP knowledge base",
  "propose MCP tools", "refresh flow docs", or whenever the user is
  preparing a frontend repo (Next.js, React, or any framework) to be driven
  by an MCP-tool-calling agent. Also use when the user opens a repo that
  already has a .flow-map/ directory and asks about drift, regeneration, or
  why specific flows are out of date. The skill is framework-agnostic via
  pluggable adapters under scripts/adapters/.
when_to_use: >
  Flow map generation, refresh, drift detection on .flow-map/, proposing
  MCP tool names from frontend call sites, scaffolding agent context for a
  newly-onboarded frontend repo.
user-invocable: true
argument-hint: "[--only changed|flow:<id>|capability:<name>] [--check] [--adapter <id>]"
allowed-tools:
  - Read
  - Grep
  - Glob
  - Write
  - Edit
  - Bash(node *)
  - Bash(git rev-parse *)
  - Bash(git diff *)
  - Bash(git status *)
paths:
  - "**/package.json"
  - "app/**/*.{ts,tsx,js,jsx}"
  - "src/app/**/*.{ts,tsx,js,jsx}"
  - "pages/**/*.{ts,tsx,js,jsx}"
  - "src/pages/**/*.{ts,tsx,js,jsx}"
  - "src/**/*.{ts,tsx,js,jsx}"
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

## What this skill produces

```
<repo>/.flow-map/
├── AGENTS.md                 # entry point + retrieval indices
├── APP.md                    # app-wide invariants, conventions, boundaries
├── glossary.md               # domain term ↔ tool/capability lookup
├── flows/<id>.md             # one playbook per business flow (intent, no HTTP)
├── capabilities/<name>.md    # one per resource group (HTTP detail lives here)
└── tools-proposed.json       # machine-readable handoff for MCP-server author
```

Schema version: `1`. Every generated file's frontmatter (and
`tools-proposed.json`) carries `schema_version: 1`. Tool names are
*proposed* — derived from frontend call sites, never validated against any
external registry. Every tool reference carries `proposed: true`.

## Reading order

When invoked, work through these in order:

1. **`references/build-plan.md`** — the full 6-step build sequence and
   current status. Always re-read to find out which step we are on. Step 1
   (vertical slice) is the only step partially live today.
2. **`references/output-schemas.md`** — exact schemas for all generated
   files (AGENTS.md, APP.md, glossary, flow, capability, tools-proposed.json).
   Treat these as a contract.
3. **`references/lint-contract.md`** — all 15 lint rules. Step 1 wires
   rules 3, 4, 5, 7, 9, 10, 13. The compile pipeline must satisfy *all*
   rules before output is considered done.
4. **`references/architecture.md`** — the four phases (Ingest → Compile →
   Query → Feedback) and the role of pluggable framework adapters.

For framework-specific behavior, read the relevant adapter spec under
`references/adapters/` *only when you need it* — adapters are loaded on
demand, not eagerly.

## Hard rules (LOCKED)

These do not change without explicit user approval. If something feels
wrong, stop and ask.

- **Flows are tool-name-free.** Flow files refer to **intent keys**
  (kebab-case verb-noun, e.g. `write-user-profile`) and link to glossary
  anchors. They never name a proposed MCP tool, never show HTTP method or
  URL path. The glossary is the single indirection layer that maps an
  intent to its currently-proposed tool name and capability anchor — when
  the MCP server lands and tools get renamed, only the glossary updates;
  flow files don't churn.
- **No HTTP detail in flow files.** No `GET `, `POST `, `fetch(`,
  `axios.`, or `/api/` paths in `flows/*.md`.
- **Capabilities own HTTP detail and proposed tool names.** Each
  capability subsection has method, path, params, response shape, auth,
  source coordinates, and the proposed tool name. Proposed names appear
  *only* in capability files, glossary entries, and `tools-proposed.json`
  — never in flows.
- **`tools-proposed.json` is a separate handoff artifact.** Not loaded into
  the runtime agent's context. Bidirectionally consistent with capability
  frontmatter (lint rule 14).
- **`<!-- HUMAN id="..." -->` blocks survive regeneration verbatim.**
  `<!-- AGENT id="..." -->` blocks are regenerated. Anything outside any
  block is structural and always regenerated.
- **Idempotence:** if source hasn't changed, regeneration produces
  byte-identical output. No timestamps in body prose — only in frontmatter.
- **Output confined to `.flow-map/`**. The skill never modifies a source
  file in the target repo.
- **Anti-goals:** never generate MCP server code, runtime agent prompts, or
  call any registry API. Never run target-repo code (no `npm run dev`, no
  tests). Never assume an MCP server exists.

## Invocation contract

When asked to "compile" / "generate" / "refresh" the flow map, run this
procedure:

1. **Confirm context.** Detect the framework adapter
   (`node scripts/adapters/probe.mjs <repoRoot>`). Multiple adapters can
   match in a monorepo; ask the user which workspace to target if
   ambiguous. Confirm naming convention and OpenAPI/GraphQL specs from
   `references/build-plan.md#open-questions` on first run.

2. **Phase 1 — ingest.** Run in order:

   ```sh
   node scripts/recon.mjs          <repoRoot>
   node scripts/enumerate.mjs      <repoRoot>
   node scripts/trace.mjs          <repoRoot>
   node scripts/ingest-openapi.mjs <repoRoot>   # optional, if recon found a spec
   node scripts/resolve.mjs        <repoRoot>
   ```

   Each script writes to `<repoRoot>/.flow-map/.cache/`. If
   `unresolved.json`'s rate exceeds 25 %, stop and report — Phase 2
   refuses to advance.

   The trace step runs every active rule pack against every reachable
   file. Active packs are determined by `detect(pkg.json)`:

   | Stack you're targeting | Packs that fire |
   |---|---|
   | Plain `fetch` | `fetch` |
   | `axios` | `axios` |
   | TanStack Query / SWR over fetch/axios | `tanstack-query` or `swr` (wrappers) + the underlying client pack |
   | tRPC | `trpc` |
   | Apollo / urql GraphQL | `apollo` / `urql` |
   | Next.js Server Actions | `server-actions` |
   | `openapi-fetch` typed client | `openapi-fetch` |
   | orval / kubb / hey-api / openapi-codegen / Kiota | `ingest-openapi.mjs` (reads the spec directly) |

   Coverage for each pack is asserted by `tests/snippets/run.mjs`.

3. **Generate prose.** Phase 2 compile takes a `prose.json` sidecar in
   `.flow-map/.cache/` keyed by `<file-id>.<agent-block-id>` →
   markdown. **You write that file** — examine the cache, then write
   prose for every `<!-- AGENT id="..." -->` slot the templates declare.
   When unsure of an answer, leave a one-line `TODO:` explanation; lint
   tolerates these but flags them in the staleness report.

4. **Phase 2 — compile.**

   ```sh
   node scripts/compile.mjs <repoRoot>          # full regen
   node scripts/compile.mjs <repoRoot> --check  # drift only
   node scripts/compile.mjs <repoRoot> --only flow:<id>
   node scripts/compile.mjs <repoRoot> --only capability:<name>
   node scripts/compile.mjs <repoRoot> --only changed
   ```

   Existing `<!-- HUMAN id="..." -->` blocks are extracted before
   regeneration and spliced back at the same anchor. Orphaned blocks
   land under `## Orphaned human notes` at the bottom; never silently
   deleted.

5. **Lint.**

   ```sh
   node scripts/lint.mjs <repoRoot>/.flow-map
   ```

   Must exit 0 silently. Otherwise fix the run that broke it.

6. **Drift check (optional / pre-commit).**

   ```sh
   node scripts/check-drift.mjs <repoRoot>
   ```

   Diffs the current source against the `generated_from_sha` recorded in
   `AGENTS.md` frontmatter. Lists added/changed/removed endpoints; exit
   1 if anything changed.

## Current status

Steps 1–5 are wired end-to-end on the two reference fixtures. Step 6
(real-repo validation) remains for the user to drive.

- Two reference fixtures under `tests/fixtures/`: `sample-nextjs/` (App
  Router) and `sample-react/` (Vite + react-router). Both share the same
  `update-profile` flow and `users` capability — proves the wiki is
  framework-invariant downstream of adapters.
- Phase 1 pipeline: `scripts/{recon,enumerate,trace,ingest-openapi,resolve}.mjs`
  + two adapters (`scripts/adapters/{next,react}.mjs`) + nine shipped
  rule packs covering the dominant React/Next stacks today: `fetch`,
  `axios`, `openapi-fetch`, `trpc`, `apollo`, `urql`, `server-actions`,
  `tanstack-query` (wrapper), `swr` (wrapper). orval/kubb/hey-api are
  covered uniformly via `ingest-openapi.mjs` reading the spec.
  Per-pack coverage tests in `tests/snippets/`. Adding more clients =
  drop a new `.mjs` in `scripts/rules/`.
- Phase 2 pipeline: `scripts/{group,propose-tools,compile}.mjs`.
- Phase 4: HUMAN-block preservation in `compile.mjs`,
  `scripts/check-drift.mjs`, `--only flow:<id>`,
  `--only capability:<name>`, `--only changed`, `--check`.
- All 15 lint rules enforced.
- Idempotence verified: re-run on unchanged source produces
  byte-identical output.

What is not yet shipped:

- Real-repo validation (audience-1 prompt set, audience-2 scaffold
  test).
- ast-grep + ts-morph integration (would lift confidence on
  dynamic-URL call sites that the current regex packs flag as
  unresolved).
- Dedicated `orval`/`kubb`/`hey-api` packs (the OpenAPI ingestion path
  already covers the *endpoints* they generate; dedicated packs would
  add hook-call site annotations).
- GraphQL schema-graph resolution (current Apollo/urql packs extract
  operation names but not argument/return types).

## Verifying the pipeline

```sh
cd cc-discovery/flow-map-compiler

# Per-rule-pack coverage smoke test (no fixtures touched):
node tests/snippets/run.mjs
node tests/snippets/openapi.mjs

# Full pipeline against either fixture (replace with a real repo path
# in production):
for s in recon enumerate trace ingest-openapi resolve group propose-tools compile ; do
  node scripts/$s.mjs tests/fixtures/sample-nextjs
done
node scripts/lint.mjs tests/fixtures/sample-nextjs/.flow-map

# Drift check (re-runs Phase 1 cheaply, reports added/changed/removed):
node scripts/check-drift.mjs tests/fixtures/sample-nextjs

# Targeted regen:
node scripts/compile.mjs tests/fixtures/sample-nextjs --only flow:update-profile
node scripts/compile.mjs tests/fixtures/sample-nextjs --only capability:users
node scripts/compile.mjs tests/fixtures/sample-nextjs --check
```

Both fixtures exit lint silently and re-running compile is byte-identical
to the previous run.

## Recommended target-repo `.gitignore`

```
# generated by flow-map-compiler — regenerated on every run
.flow-map/.cache/
```

The wiki itself (`AGENTS.md`, `APP.md`, `glossary.md`, `flows/`,
`capabilities/`, `tools-proposed.json`) is git-tracked.
