# AGENTS.md

Cross-tool entry point for any coding agent working in this repo (Claude Code, Codex,
Copilot CLI, Gemini CLI, etc.). Claude-Code-specific workflow — Makefile targets, test
plans, debug helpers — lives in [`CLAUDE.md`](CLAUDE.md).

## Project-wide conventions

Cross-repo standards are the single source of truth in the docs repo
[`DACdigital/openbbc-docs`](https://github.com/DACdigital/openbbc-docs), under
[`docs/conventions/`](https://github.com/DACdigital/openbbc-docs/tree/main/docs/conventions):

- [`architecture.md`](https://github.com/DACdigital/openbbc-docs/blob/main/docs/conventions/architecture.md) — bounded contexts, layering, dependency direction.
- [`api.md`](https://github.com/DACdigital/openbbc-docs/blob/main/docs/conventions/api.md) — URL-path versioning, error envelope, backward compatibility.
- [`persistence.md`](https://github.com/DACdigital/openbbc-docs/blob/main/docs/conventions/persistence.md) — data ownership per bounded context, migration discipline.
- [`events.md`](https://github.com/DACdigital/openbbc-docs/blob/main/docs/conventions/events.md) — `<context>.<entity>.<pastTenseAction>` naming, `schemaVersion`.
- [`naming.md`](https://github.com/DACdigital/openbbc-docs/blob/main/docs/conventions/naming.md) — repo/file/symbol naming.
- [`testing.md`](https://github.com/DACdigital/openbbc-docs/blob/main/docs/conventions/testing.md) — unit / integration / e2e levels.

When any of these disagrees with something written locally, the docs-repo copy wins —
raise a PR against the docs repo, not against this file.

## What this repo is

Three components in one monorepo (per [openbbc-docs `docs/repos/openbbc.md`](https://github.com/DACdigital/openbbc-docs/blob/main/docs/repos/openbbc.md)):

- [`open-bbcd/`](open-bbcd/) — Go 1.27+ daemon. Backoffice UI + REST API + deployed agent
  runtime + MCP-over-REST bridge (`http_endpoint` tool-backend kind). Single binary, no
  local disk state after migration 026 inlined the discovery zip on `agents.discovery_zip`.
- [`aikdm/`](aikdm/) — Python 3.12+ CLI managed with `uv`. Subcommands `generate-agent`,
  `evaluate`, `train-agent`. Out-of-process, DB-unaware; talks REST to `open-bbcd`.
- [`bbc-discovery/`](bbc-discovery/) — Claude Code plugin marketplace shipping
  `flow-map-compiler`. Pure markdown, no build step.

## Repo-specific rules — Go (open-bbcd)

- **Format + lint**: `make fmt` and `make lint` in `open-bbcd/` before every commit.
  gofmt is non-negotiable.
- **Tests**: `-race` on every unit test (`make test`). Integration tests (`make
  test-integration`) run against a real Postgres via `-p 1`; the same target runs in CI. Do
  not mock the database in integration tests — CI has the real service running.
- **Migrations**: append-only under `open-bbcd/migrations/NNN_<name>.sql`, applied by
  `goose` embedded via `//go:embed`. Never edit a merged migration file; add a corrective
  one. Migrations must stay backward-compatible during rollout (add nullable, deploy code,
  backfill, tighten in a follow-up) per [`persistence.md § Migrations discipline`](https://github.com/DACdigital/openbbc-docs/blob/main/docs/conventions/persistence.md).
- **State machines are DB-enforced**: `agent_versions.status ∈ {INITIALIZING, PENDING,
  DRAFT, TRAINING, READY, DEPLOYED}` (migration 025 CHECK constraint); "at most one
  DEPLOYED per chain" (migration 011); "at most one active training per eval"
  (`idx_ts_one_active_per_eval`); "at most one DRAFT per dataset" (migration 019 partial
  unique index). If you need a new state, update the CHECK constraint in the migration —
  the app must not enforce it alone.
- **Handler / repository split**: HTTP handlers in `internal/handler/*.go` are thin
  glue over `internal/repository/*.go`. Cross-aggregate invariants (e.g. "feedback attached
  only to assistant messages") live in the repo layer, not in the SQL — Postgres does not
  do partial FKs.
- **Server-rendered `html/template` + htmx**. No SPA. Do not introduce a JS bundler /
  frontend framework in `open-bbcd/`. The BO chat surface is intentionally
  server-rendered.
- **Distroless runtime**: `open-bbcd/Dockerfile` runs on
  `gcr.io/distroless/static-debian12:nonroot`, CGO off. Anything that needs `curl` or a
  shell must go in `aikdm-runner` or a separate image.
- **`open-bbcd healthcheck`** subcommand reads only `SERVER_PORT`; a broken `DATABASE_URL`
  must not fail the container `HEALTHCHECK`. Preserve this when refactoring the
  subcommands in `cmd/open-bbcd/main.go`.

## Repo-specific rules — Python (aikdm)

- **Deps**: `uv` only, from `aikdm/`. `uv sync --all-extras` for dev; `uv sync --frozen` in
  the Dockerfile. Do not add `pip`, `poetry`, or `requirements.txt`.
- **Tests**: `make test` runs unit tests with the LLM mocked. `RUN_SMOKE=1 make
  test-smoke` gates real-LLM tests (paid). Do not remove the smoke gate.
- **Schemas**: Pydantic + versioned YAML. Section structure for the agent bundle lives in
  `aikdm/schemas/prompt-v1.yaml`; a breaking change bumps the file and starts a `v2`.
- **CLI**: `click`. Subcommands live in `aikdm/aikdm/cli.py`. Non-zero exit emits
  structured JSON on stderr — `{"error":"<kind>","details":"<msg>"}`. Codes: `1`
  unexpected, `2` input/config, `3` LLM. Preserve this contract when adding subcommands.
- **DB-unaware invariant**: `aikdm` never opens a DB connection. The alpha-drainer sidecar
  `scripts/seed_bundle.py` (packaged into the `aikdm-runner` image) is the one place that
  writes to Postgres; keep that separation.
- **LLM abstraction**: Google ADK + LiteLLM. Do not import provider SDKs directly
  (`anthropic`, `openai`, `google.generativeai`) — go through LiteLLM so the multi-provider
  toggle (`AIKDM_MODEL_*`) keeps working.

## Repo-specific rules — Claude Code plugin (bbc-discovery/flow-map-compiler)

- **Contract triple stays in sync**:
  [`references/output-schemas.md`](bbc-discovery/flow-map-compiler/references/output-schemas.md) ↔
  [`references/lint-contract.md`](bbc-discovery/flow-map-compiler/references/lint-contract.md) ↔
  [`assets/templates/*.tmpl`](bbc-discovery/flow-map-compiler/assets/templates/). A PR that
  touches one must update the others.
- **LOCKED anti-goals** (from the skill's frontmatter):
  - Never generate MCP server code.
  - Never generate runtime agent prompts.
  - Never call any registry API.
  - Never run target-repo code (no `npm run dev`, no tests).
  - Never assume an MCP server exists.
  - Output confined to `.flow-map/`; never touch source files outside it.
- **Schema version `2`.** Every generated file's frontmatter carries `schema_version: 2`;
  every endpoint entry carries `proposed: true`.
- **`<!-- HUMAN id="..." -->` blocks survive regeneration**; `<!-- AGENT id="..." -->`
  blocks are regenerated. Never merge these two modes.

## Cross-cutting rules

- **Auth-agnostic ship**: `open-bbcd` never enforces authentication or authorization on
  any route. Operators front it with a gateway that verifies the caller and rewrites
  `user_id`. Do not add auth middleware without an accompanying doc-repo
  `docs/superpowers/specs/` design.
- **Two tool-backend kinds are the whole set**: `http_endpoint` (MCP-over-REST bridge) and
  `mcp_client` (proxy to an existing MCP server). A new integration pattern requires a
  new `kind` value + a migration + a spec in the docs repo.
- **Discovery zip is inline on the DB**: `agents.discovery_zip BYTEA` (migration 026).
  Do not reintroduce disk-based `DISCOVERY_STORAGE_DIR` — the app must stay
  disk-stateless so the k8s pod can be replaced without a PVC.
- **Header-override paths are distinct**: `chat_sessions.backend_header_overrides` is
  per-backend (migration 016); `evals.header_overrides` is flat (migration 023);
  `deployed_sessions` has none today. Do not conflate them.
- **CronJobs and one-shot scripts are the same code path**: `scripts/process_pending_*.sh`
  are `flock`-protected, serial, continue-on-error. Behaviour must be identical whether
  they're invoked from cron, kubectl, or an operator's shell. Alpha drainer needs
  `DATABASE_URL`; eval and training drainers do not.

## When in doubt

- Cross-repo standard question → check the docs repo's
  [`docs/conventions/`](https://github.com/DACdigital/openbbc-docs/tree/main/docs/conventions).
- Local Claude Code workflow (commands, test recipes) → see [`CLAUDE.md`](CLAUDE.md).
- Feature design → open a spec in the docs repo under `docs/superpowers/specs/`; the
  `/new-spec` and `/spec-review` skills live there.
