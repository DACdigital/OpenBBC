# Build plan

A 6-step sequence. Each step has a checkpoint. Do not advance until the
checkpoint passes.

## Where we are

Steps 1–5 are wired end-to-end on the two reference fixtures. Step 6
(real-repo validation) remains for the user to drive against actual
client repos.

| Step | Status |
|---|---|
| 1 — Vertical slice (templates + Step-1 lint subset) | done |
| 2 — Full templates + 15-rule lint | done |
| 3 — Phase 1 deterministic ingest (recon/enumerate/trace/resolve, adapters, rule packs) | done — minimum viable |
| 4 — Phase 2 LLM compile (group/propose-tools/compile) | done |
| 5 — Phase 4 feedback (HUMAN preservation, drift, --only flags) | done |
| 6 — Real-repo validation (audience-1 ≥ 85 %, audience-2 dev scaffolds MCP server) | not started (user-driven) |

The Phase 1 pipeline currently uses a minimum-viable pattern matcher
(scripts/rules/fetch.mjs only). Adding axios/trpc/react-query/etc.
support is a single new file in `scripts/rules/` — see
`scripts/rules/README.md` for the contract and per-pack design notes.

The Phase 2 compile produces machine-derived intent keys (e.g.
`update-user-record`); a `prose.json` sidecar in `.flow-map/.cache/`
provides the LLM-authored prose for `<!-- AGENT -->` blocks. When the
sidecar is absent or a key is missing, output contains `TODO:` markers
that lint tolerates but a follow-up review pass should resolve.

## Open questions (ask the user on first real-repo run)

1. Does regeneration write to the working tree (default) or to a side
   branch / separate worktree?
2. Are there in-repo OpenAPI / GraphQL spec files? If yes, where?
3. Is this a monorepo? If yes, which workspace is the frontend?
4. Tool naming convention — default is dotted lower-camel
   (`<resource>.<verb>`, e.g. `users.update`). Does the user prefer
   snake_case, single-word, or alignment with an existing tool registry in
   another repo?

## Step 1 — Vertical slice

Pick one frontend repo (or a synthesized fixture). Pick one flow.
Hand-write `.flow-map/.cache/endpoints.json`. Hand-write
`assets/templates/flow.md.tmpl` and `assets/templates/capability.md.tmpl`.
Hand-write `scripts/lint.mjs` covering only rules 3, 4, 5, 7, 9, 10, 13.
Generate one `flows/<id>.md` and the `capabilities/<name>.md` it links to
(by hand or one-shot LLM call). Lint passes.

You cannot validate cross-link integrity without both files; plan to write
two files.

In this build we ship **two fixtures** sharing one flow & one capability —
`sample-nextjs/` and `sample-react/` — to prove the wiki shape is
framework-invariant from day one.

**Checkpoint:** valid `flows/<id>.md` + `capabilities/<name>.md` exist for
each fixture, lint passes silently, the user agrees the output looks like
good agent context. Stop and ask before continuing.

## Step 2 — Templates and lint, no LLM

Write all five templates: `AGENTS.md.tmpl`, `APP.md.tmpl`,
`glossary.md.tmpl`, `flow.md.tmpl`, `capability.md.tmpl`. Hand-write a
`tools-proposed.json` for the test target. Extend `scripts/lint.mjs` to
cover all 15 rules. Hand-fill the templates for the test target (use a
hand-written `recon.json`). Run lint on the whole hand-filled
`.flow-map/`. All rules pass.

**Checkpoint:** the template + lint contract is solid before any LLM is in
the loop.

## Step 3 — Phase 1 deterministic pipeline

Build:

- `scripts/recon.sh` — detect framework adapter, language, monorepo
  layout, API client libraries, in-repo OpenAPI/GraphQL specs.
- `scripts/enumerate.sh` — glob entry points via the chosen adapter; build
  the import graph with `npx dependency-cruiser -T json`.
- `scripts/trace.sh` — BFS the import graph; on each reachable file run
  only the ast-grep rule packs in `scripts/rules/` for libraries the
  recon detected.
- `scripts/resolve.mjs` — ts-morph driver with
  `skipAddingFilesFromTsConfig: true` and targeted
  `addSourceFilesAtPaths`. Resolve argument types, follow Zod, dereference
  OpenAPI via `@apidevtools/swagger-parser`, walk tRPC `AppRouter` if
  present.
- `scripts/rules/*.yml` — one ast-grep rule pack per supported client
  library (fetch, axios, trpc, react-query, swr, apollo, urql,
  openapi-fetch, orval, hey-api, kubb, server-actions). Adding a new
  library is a new yml, not editing detection logic.
- `scripts/adapters/*.mjs` — one adapter per supported framework (next,
  react, …). Each adapter exposes `probe()`, `enumerateEntries(repoRoot)`,
  `groupFlows(callsites)`.

Compare cache output against the hand-written cache from Step 2. Iterate
until unresolved rate ≤ 15%.

**Checkpoint:** generated `endpoints.json` is indistinguishable from the
hand-written version on the slice that overlaps.

## Step 4 — Phase 2 compile

Build:

- `scripts/group.mjs` — group call sites into flows by entry point + the
  adapter's grouping rule (e.g. nearest layout for Next App Router; route
  component for react-router).
- `scripts/propose-tools.mjs` — apply the user-confirmed naming convention
  to each unique endpoint.
- `scripts/compile.mjs` — render all five output file types plus
  `tools-proposed.json` from cache + templates.
- SKILL.md orchestrates phases.

Lint passes. Diff generated output against Step 2's hand-filled output —
they should be substantially the same in structure, even if prose differs.

**Checkpoint:** running the skill end-to-end on the test target produces
a `.flow-map/` that lints clean and reads like good agent context.

## Step 5 — Phase 4 feedback

- Implement `<!-- HUMAN -->` block extraction & re-splice in
  `compile.mjs`. Orphaned blocks land under `## Orphaned human notes` at
  the bottom — never silently dropped.
- Build `scripts/check-drift.mjs` — re-run Phase 1 cheaply, diff against
  `AGENTS.md` frontmatter `generated_from_sha`, list changed/added/removed
  endpoints and flows.
- Implement `--only changed`, `--only flow:<id>`,
  `--only capability:<name>`, `--check` flags.

**Checkpoint:** introduce a known source change → `--check` reports the
diff; running without `--check` rewrites only affected files; an edited
HUMAN block is preserved verbatim.

## Step 6 — Real-repo validation

Run on 2–3 real frontend repos of varying complexity. Validate both
audiences:

- **Audience 1 (runtime agent):** feed the generated `.flow-map/` to an
  agent (no MCP tools attached, just the docs); ask 20 representative
  user questions. Score: did it pick the right flow? Did it identify the
  right tool sequence? Aim ≥ 85%.
- **Audience 2 (MCP-server author):** hand `tools-proposed.json` to a
  developer; ask them to scaffold an MCP server. They should be able to
  start without re-reading source. If they can't, the JSON is missing
  something — iterate.

**Checkpoint:** both audiences are served. Skill is shippable.

## Final-deliverable checklist

Before declaring the skill done, confirm:

- [ ] All files in `flow-map-compiler/` exist and match the layout
- [ ] SKILL.md frontmatter matches the spec exactly
- [ ] All 15 lint rules implemented and passing on the test target
- [ ] All five templates have `<!-- HUMAN -->` and `<!-- AGENT -->` blocks
- [ ] All ast-grep rule packs exist (one yml per supported client library)
- [ ] All framework adapters exist with `probe / enumerate / group`
- [ ] `--check`, `--only flow:<id>`, `--only capability:<name>`,
      `--only changed` flags work
- [ ] Idempotence verified: `run; run; git diff` is empty the second time
- [ ] HUMAN-block preservation verified by editing a block and regenerating
- [ ] Drift detection verified by changing source and running `--check`
- [ ] `.flow-map/tools-proposed.json` produced and validated by rule 14
- [ ] Every tool reference in flow files links to a capability anchor
      (rule 13)
- [ ] Every `proposed: true` flag present where required
- [ ] Audience-1 validation passed (≥ 85% on 20 questions)
- [ ] Audience-2 validation passed (a dev can scaffold an MCP server)
- [ ] At least one real-world frontend repo produces a clean `.flow-map/`
