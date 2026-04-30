# flow-map-compiler

Compile any frontend repo into a `.flow-map/` agent wiki — a structured,
LLM-readable knowledge base that documents the app's business flows,
backend capabilities, and proposes MCP tool names for the not-yet-built
server.

This is a **Claude Code skill packaged as a plugin**. Once installed, ask
Claude things like *"compile a flow map for this repo"* or *"refresh the
flow map"* and the skill kicks in automatically.

## What it does

Given a frontend repo (Next.js, SvelteKit, Nuxt, Remix, Astro, plain
React, Vue, SolidStart, Qwik, or anything else), the skill produces a
`.flow-map/` directory with:

- **`AGENTS.md`** — entry point. Reading order, intent → flow table,
  flow & capability indexes, overview Mermaid diagram.
- **`APP.md`** — stack, framework, auth model, providers, invariants,
  boundaries.
- **`flows/<id>.md`** — one file per user-facing entry point (page,
  route, server action). Describes intent, preconditions, sequence,
  postconditions, failure modes — **without** naming HTTP methods,
  URL paths, or tool names.
- **`capabilities/<name>.md`** — one file per backend resource group
  (`users`, `orders`, …). Each tool subsection has method, path, params,
  request/response shape, auth, source coordinates, and a proposed
  MCP tool name.
- **`glossary.md`** — the single indirection layer mapping intent keys
  to capability anchors and currently-proposed tool names.
- **`tools-proposed.json`** — machine-readable catalog of proposed MCP
  tools (separate handoff artifact for the engineer building the
  server).

Everything is `proposed: true`. No MCP server is assumed to exist.

## Why two audiences

The wiki is read by two consumers, and the file structure reflects that:

1. **A runtime agent** that drives MCP tools wrapping the backend.
   It needs semantics, intent, sequencing, preconditions, invariants,
   failure modes, and domain vocabulary — not HTTP wiring. So `flows/`
   are tool-name-free and link through `glossary.md`.
2. **The engineer who will build that MCP server.** They need
   methods, paths, params, shapes, auth, and source coordinates. So
   `capabilities/` and `tools-proposed.json` carry all the HTTP detail.

When the MCP server lands and tools get renamed, only `glossary.md`
and `capabilities/` change. Flow files stay byte-identical.

## Flows vs. capabilities vs. intents

Three concepts, three jobs. The wiki keeps them in separate files so
each can change independently.

| Concept | What it is | Lives in | Names a tool? |
|---|---|---|---|
| **Flow** | A user journey through the app, anchored on an entry point. Describes preconditions, sequence of steps, postconditions, failure modes. *Semantic, durable.* | `flows/<id>.md` | No — never |
| **Intent** | A kebab-case key (`write-user-profile`) that names *what the agent wants to do*. The link between a flow's narrative and the backend operation that fulfils it. *Stable identifier.* | Anchors in `glossary.md`; referenced from flows as `[label](glossary.md#<intent>)` | No — points at one |
| **Capability** | A backend resource group (`users`, `orders`, …) with the HTTP wiring: method, path, params, shapes, auth, source line, and the proposed tool name. *Concrete, may churn.* | `capabilities/<name>.md` | Yes — owns it |

### Worked example

A user clicks Save on `/profile`.

- **Flow** `flows/update-profile.md` — the journey:
  > 1. User fills out the form and clicks Save.
  > 2. Agent validates the input.
  > 3. Agent does [write user profile](glossary.md#write-user-profile).
  > 4. On success, show "Saved". On 409, ask the user to refresh.

  Notice: no HTTP method, no URL, no tool name.

- **Intent** `write-user-profile` — the kebab-case link target. The
  glossary anchors it:
  > ### write user profile {#write-user-profile}
  > - Capability: [`capabilities/users.md#users-update`](capabilities/users.md#users-update)
  > - Proposed tool: `users.update`
  > - Role: write

- **Capability** `capabilities/users.md` — the wiring:
  > ## users.update  {#users-update}
  > **Proposed tool name:** `users.update`
  > **HTTP:** `PATCH /api/users/{id}`
  > **Body shape:** `{ name: string, email: string }`
  > **Source:** `lib/api/users.ts:17`

### Why the indirection

So the flow stops being a moving target.

- If someone renames `users.update` → `userProfile.write`, only
  `glossary.md` and `capabilities/users.md` change. Every flow that
  references `#write-user-profile` is unchanged.
- The runtime agent reads flows for *semantics* and the glossary
  for *which tool*. Two different concerns, two different files.
- The engineer building the MCP server reads `capabilities/` and
  `tools-proposed.json`. They don't care about flows.

Same data, three views, decoupled.

## How it works

The skill is **fully agent-driven** — no script pipeline. Claude reads
source directly and walks six steps:

1. **Recon.** Read `package.json`(s); detect framework, language,
   monorepo layout, in-repo OpenAPI/GraphQL specs, and which routing
   conventions exist on disk (`app/`, `pages/`, `src/routes/`, …).
2. **Trace.** From each routing-convention dir, find entry-shaped
   files (`page.{ts,tsx}`, `+page.svelte`, `+server.*`, …). For each
   entry, follow imports and extract HTTP/RPC call sites: `fetch`,
   `axios`, `ky`, `@tanstack/react-query`, `swr`, `@apollo/client`,
   `urql`, `@trpc/client`, `openapi-fetch`, Server Actions, GraphQL.
   Record method, path, auth, source `<file>:<line>`. Mark anything
   not statically resolvable as `unresolved`.
3. **Resolve & group.** Dedupe by `(method, path)`. Group call sites
   by entry → flows. Group endpoints by first path segment (or
   OpenAPI tag) → capabilities.
4. **Propose tools.** Default naming: `<resource>.<verb>`
   (dotted-lower-camel). Every entry marked `proposed: true`.
5. **Render.** Fill the templates in `assets/templates/`, write
   `<repo>/.flow-map/`. Schemas defined in
   `references/output-schemas.md`.
6. **Self-check.** Walk the 15-rule lint contract in
   `references/lint-contract.md`. Fix or re-render if anything fails.

Full procedure: see `skills/flow-map-compiler/SKILL.md`.

## Hard rules (locked)

These are invariants the skill must not violate. Verbatim from
`SKILL.md`:

- **Flows are tool-name-free.** Flow files refer to intent keys
  (kebab-case verb-noun) and link to glossary anchors. They never name
  a proposed MCP tool, never show HTTP method or URL path.
- **No HTTP detail in flow files.** No `GET `, `POST `, `fetch(`,
  `axios.`, or `/api/` paths in `flows/*.md`.
- **Capabilities own HTTP detail and proposed tool names.** Method,
  path, params, response shape, auth, source coordinates, and proposed
  tool name. Proposed names appear *only* in capability files, glossary
  entries, and `tools-proposed.json`.
- **`tools-proposed.json` is a separate handoff artifact.** Not loaded
  into the runtime agent's context. Bidirectionally consistent with
  capability frontmatter (lint rule 14).
- **`<!-- HUMAN id="..." -->` blocks survive regeneration verbatim.**
  `<!-- AGENT id="..." -->` blocks are regenerated. Anything outside
  any block is structural and always regenerated.
- **Output confined to `.flow-map/`**. The skill never modifies a
  source file in the target repo.
- **Anti-goals:** never generate MCP server code, runtime agent
  prompts, or call any registry API. Never run target-repo code
  (no `npm run dev`, no tests). Never assume an MCP server exists.

## Install

This is just a Claude Code skill. The simplest way to use it is to
drop the skill folder into `~/.claude/skills/` — no plugin or
marketplace required.

### Option A — install the skill directly (recommended)

Pick whichever one-liner you prefer. All three drop the same SKILL.md
+ assets into `~/.claude/skills/flow-map-compiler/`.

**With [`npx skills`](https://github.com/vercel-labs/skills)** —
community installer that resolves GitHub subfolders directly:

```sh
npx skills add https://github.com/DACdigital/OpenBBC/tree/main/bbc-discovery/flow-map-compiler/skills/flow-map-compiler -g
```

The `-g` flag installs globally to `~/.claude/skills/`; without it,
the skill goes to `<cwd>/.claude/skills/` (project-local). Re-run to
update.

**With [`degit`](https://github.com/Rich-Harris/degit)** — minimal
GitHub subfolder cloner, no git history:

```sh
npx degit DACdigital/OpenBBC/bbc-discovery/flow-map-compiler/skills/flow-map-compiler \
  ~/.claude/skills/flow-map-compiler
```

**With plain `git`** — no extra tools needed:

```sh
git clone --depth=1 https://github.com/DACdigital/OpenBBC.git /tmp/openbbc \
  && cp -r /tmp/openbbc/bbc-discovery/flow-map-compiler/skills/flow-map-compiler \
           ~/.claude/skills/flow-map-compiler \
  && rm -rf /tmp/openbbc
```

Claude Code [watches `~/.claude/skills/` for changes](https://code.claude.com/docs/en/skills#live-change-detection)
and picks the skill up live — no restart needed. The skill is now
available as `/flow-map-compiler` (no namespace) and triggers
automatically on phrases like "flow map".

To uninstall: `rm -rf ~/.claude/skills/flow-map-compiler`.

For a project-scoped install (committed to your repo, available to
collaborators), use `<repo>/.claude/skills/flow-map-compiler/`
instead of `~/.claude/skills/` — or run `npx skills add ...` without
the `-g` flag from your project root.

> *Why no `uvx` option?* `uv`/`uvx` runs Python entry points from
> PyPI; there's no established convention for shipping a Claude
> Code skill as a Python package today. The npm-side ecosystem
> (`npx skills`, `degit`) is where the install tooling lives.

### Option B — install via the bundled plugin / marketplace

The same skill is also packaged as a plugin in the
[`bbc-discovery`](../) marketplace. Use this path if you want
versioned updates via `/plugin update` and namespaced invocation
(`/flow-map-compiler:flow-map-compiler`).

From a local clone:

```sh
# inside Claude Code
/plugin marketplace add /absolute/path/to/OpenBBC/bbc-discovery
/plugin install flow-map-compiler@bbc-discovery
```

From GitHub (no clone needed):

```sh
/plugin marketplace add https://raw.githubusercontent.com/DACdigital/OpenBBC/main/bbc-discovery/.claude-plugin/marketplace.json
/plugin install flow-map-compiler@bbc-discovery
```

The raw-URL form pins to `main`. Until this branch is merged, use
the local-clone form instead. The short
`/plugin marketplace add DACdigital/OpenBBC` form does **not** work
— it requires `marketplace.json` at the repo root, and Claude Code
does not yet support a subdirectory path for github marketplace
sources ([anthropics/claude-code#20268](https://github.com/anthropics/claude-code/issues/20268)).

## Usage

Trigger phrases (any of these auto-load the skill):

- "compile a flow map"
- "generate flow docs for this repo"
- "refresh the flow map"
- "MCP knowledge base for this app"
- "propose MCP tools for this frontend"
- "document this app for an agent"

Or invoke directly:

```
/flow-map-compiler                         # Option A (standalone skill)
/flow-map-compiler:flow-map-compiler       # Option B (plugin)
```

Output lands in `<repo>/.flow-map/`. Commit it — it's part of the repo's
documentation, not a build artifact.

## Updating

When a new version is released, bump your installed copy:

```
/plugin update flow-map-compiler@bbc-discovery
```

The plugin uses explicit `version` pinning (`0.1.0` today) — you only
get updates when the version field in `plugin.json` is bumped.

## Status

**v0, LLM-only.**

- Discovery, extraction, grouping, naming, and rendering all happen
  inside Claude (no Node scripts).
- Works on any frontend stack because the agent reads source directly
  and applies the contract (templates + 15-rule lint).
- **No byte-identical idempotence** — LLM-authored prose may churn on
  re-runs even when source is unchanged. Schema and structure are
  stable.
- **Drift detection is manual** — re-run the skill and `git diff
  .flow-map/`. A scripted drift checker may return in v1.
- **Unresolved-rate threshold:** the skill refuses to ship if more
  than 25 % of call sites are statically unresolvable.

## What's in this plugin

```
flow-map-compiler/
├── .claude-plugin/plugin.json           # plugin manifest
├── README.md                            # this file
└── skills/flow-map-compiler/
    ├── SKILL.md                         # the procedure (~340 lines)
    ├── assets/templates/                # 5 output templates
    ├── references/
    │   ├── output-schemas.md            # file-shape contract
    │   └── lint-contract.md             # 15-rule self-check
    └── tests/fixtures/
        ├── sample-nextjs/               # input + canonical .flow-map/
        ├── sample-react/
        └── sample-sveltekit/
```

The fixtures act as few-shot examples — three real-shaped frontend
sources paired with their canonical generated wikis, so the skill has
something to anchor on.

## License

Apache-2.0.
