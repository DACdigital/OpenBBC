# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository layout

OpenBBC is a monorepo for a platform that turns a backend + a frontend repo into a deployable AI agent. Three top-level components, each independent:

| Path | What it is | Language |
|---|---|---|
| `aikdm/` | Python CLI: generates structured prompt bundles from FlowMapConfig YAML. Out-of-process, open-bbcd-unaware. | Python |
| `bbc-discovery/` | Claude Code plugin marketplace. Currently ships one plugin (`flow-map-compiler`) — a discovery skill that compiles a frontend repo into a `.flow-map/` agent wiki. Pure markdown + plugin manifests, no build step. | Markdown / SKILL.md |
| `docs/` | `DESIGN.md` and `ARCHITECTURE.md` — read these for the big picture (discovery → generate → feedback → evaluate → deploy). |
| `open-bbcd/` | Core service: backoffice UI + REST API + (future) agent runtime. The only buildable program in the repo today. | Go |

The root `.claude-plugin/marketplace.json` references the `bbc-discovery` plugin via a `git-subdir` source pointing back into this monorepo.

## aikdm (Python CLI)

Module: `aikdm` · Python 3.12+ · `uv` for dependency management.

### Common commands (run from `aikdm/`)

```bash
uv sync --all-extras                              # install deps
make test                                         # unit + integration (LLM mocked)
make test-smoke                                   # real LLM, gated by RUN_SMOKE=1
uv run aikdm generate-agent --help                # CLI help
uv run aikdm generate-agent \                     # generate a bundle
  --config /path/to/flow-map-config.yaml \
  --output /path/to/bundle.yaml
```

### Architecture (what spans multiple files)

- **Out-of-process, open-bbcd-unaware.** Single command (`aikdm generate-agent`) takes a `FlowMapConfig` YAML in, emits a bundle YAML out, streams NDJSON progress to stderr. No DB driver, no HTTP back to open-bbcd. Anything (open-bbcd subprocess, cron, Argo) can invoke it.
- **Two-agent loop.** `aikdm/agents.py` builds an ADK generator and critic. `aikdm/orchestrator.py` runs up to `AIKDM_CRITIC_ROUNDS` rounds (default 2) with early exit on empty issues. Final-round issues land in `metadata.critic_notes` as advisory; the bundle is always produced.
- **Single mocking seam at `models.build_model(role)`** — all tests substitute this to avoid network calls. A secondary seam at `agents._run_generator` / `_run_critic` lets orchestrator tests bypass ADK entirely.
- **Schema-driven prompt structure.** `aikdm/schemas/prompt-v1.yaml` declares main-prompt and skill-prompt sections with XML tag names and section sources (`wizard_copied` / `llm_synthesized` / `config_derived`). Adding a section is a YAML edit + version bump.
- **External skills handled by the orchestrator, not the LLM.** Whatever the generator emits, the orchestrator enforces: skills with `external: true` in input go to `bundle.external_actions[]`, never `bundle.skills[]`.

### Exit codes

`0` success / `1` unexpected / `2` input or config error / `3` LLM error. Structured `{"error":"<kind>","details":"<msg>"}` JSON on stderr for non-zero exits.

## open-bbcd (Go service)

Module: `github.com/DACdigital/OpenBBC/open-bbcd` · Go 1.22+ · PostgreSQL 15+ · migrations via `goose`.

### Common commands (run from `open-bbcd/`)

```bash
docker-compose up -d              # start Postgres
cp .env.example .env && source .env
make migrate-up                   # apply all migrations (needs $DATABASE_URL)
make run                          # go run ./cmd/open-bbcd
make build                        # → bin/open-bbcd
make test                         # go test -v -race ./...
go test -v -race ./internal/handler -run TestWizardSubmit   # one test
make migrate-create name=add_foo  # new goose SQL migration
```

`make migrate-up` requires `goose` on `$PATH` (`go install github.com/pressly/goose/v3/cmd/goose@latest`) and `$DATABASE_URL` exported.

### Architecture (what spans multiple files)

- **Single binary, embedded assets.** `web/assets.go` declares `//go:embed templates static schemas` so HTML templates, htmx, and the wizard YAML schema are all baked into the binary. `cmd/open-bbcd/main.go` wires config → DB → `handler.NewAPI(db)` and serves on `$SERVER_HOST:$SERVER_PORT`.
- **Routing is one mux in `internal/handler/api.go`.** It mixes: static assets at `/static/`, server-rendered UI at `/agents/ui` and `/agents/new[/step/{n}]`, an htmx-driven wizard submit at `POST /agents/wizard`, and a JSON REST API (`/agents`, `/agents/{id}`, `/resources`, `/agents/{agent_id}/resources`, `/health`). `GET /` redirects to `/agents/ui`. Fixed paths take precedence over `/agents/{id}` — keep that in mind when adding routes.
- **Layers:** `handler` (HTTP) → `repository` (SQL, `database/sql` + `lib/pq`) → `types` (domain + errors + wizard schema). `types/errors.go` defines sentinel errors; `handler/handler.go::Error` maps them to HTTP statuses (`ErrNotFound`→404, `ErrNameRequired`/`ErrPromptRequired`/`ErrAgentRequired`→400, else 500). Reuse this — don't return ad-hoc `http.Error` for domain errors.
- **Agent versioning is a linked list, not a column.** `agents.parent_version_id` (migration 003) points at the previous version of the *same* agent. `AgentRepository.ListGrouped` walks each row up to its chain root and groups versions into `AgentChain`s with computed `VersionNum` (oldest = 1). The chain's display name is always the **root** agent's name — names are treated as immutable per chain. The `/agents/ui` page lists chains; `?agent=<name>` on the same route renders that chain's version history (`agentVersionsPageData`).
- **Wizard is schema-driven.** `web/schemas/wizard-v1.yaml` declares fields with `label`, `type` (`text` / `textarea` / `file`), `required`, and `order`. The schema is parsed once at startup in `NewAPI` and passed to both `UIHandler` (renders one step per field via htmx fragment at `/agents/new/step/{n}`) and `WizardHandler` (validates + persists on submit). To add or change a step, edit the YAML — no Go code change needed unless you introduce a new field `type`. `WizardHandler.Submit` writes to `agents.wizard_input` (JSONB) with `status='INITIALIZING'` and stamps the schema version on the row.
- **htmx, no SPA.** `web/static/htmx.min.js` is the only frontend library; the wizard UX is htmx swaps over Go `html/template` partials (`web/templates/wizard/`). `renderTemplate` buffers execution to avoid partial responses on template error.
- **Config is env-driven** (`internal/config/config.go`, `caarlos0/env` + `joho/godotenv`). `DATABASE_URL` is required; everything else has defaults. `.env` is auto-loaded if present.

### Migration conventions

Migrations live in `migrations/NNN_<name>.sql` and use **goose annotations** (`-- +goose Up` / `-- +goose Down`). Always provide a Down. The `agents` table evolves additively across migrations 001→004 (versioning fields + JSONB wizard input added later) — when changing the schema, add a new migration rather than editing existing ones.

## bbc-discovery (Claude Code plugin marketplace)

This is **content, not code** — a plugin manifest plus a SKILL.md and assets. There is no build, no tests beyond the skill's own evals.

- The active plugin is `flow-map-compiler/`. Its `SKILL.md` is a procedure executed by Claude (no script pipeline — fully agent-driven).
- The skill has its own evals at `bbc-discovery/flow-map-compiler/skills/flow-map-compiler/evals/`. To smoke-test the canonical fixtures: `python evals/check_flow_map.py tests/fixtures/sample-<stack>/.flow-map --expect <eval-id>` (needs `pyyaml`).
- **Contract triple** that must stay in sync when changing the skill: `references/output-schemas.md` (file shape) ↔ `references/lint-contract.md` (16 self-check rules) ↔ `assets/templates/*.tmpl` (six output templates). Touching one usually means touching the others, and re-rendering the canonical fixtures.
- **Hard rules in the skill** (don't violate when editing it): flows are tool-name-free and HTTP-detail-free; capabilities own HTTP detail and proposed tool names; `<!-- HUMAN id="..." -->` blocks survive regeneration verbatim; output is confined to `.flow-map/` in the target repo.

## Conventions worth knowing

- **Sentinel errors over strings.** New domain errors go in `internal/types/errors.go` and get mapped in `handler/handler.go::Error`.
- **Repository interfaces are defined at the handler layer** (e.g. `WizardAgentRepository`, `GroupedAgentRepository`, `AgentRepository`), not on the repo struct — handlers depend on the narrowest interface they need. Match this when adding a new handler.
- **Templates are parsed in `NewUIHandler` once at startup**, with shared `template.FuncMap` (`statusClass`, `add`, `sub`, `urlEncode`). Add new helpers there, not inline.
- **Test files live next to the code** (`*_test.go` colocated with each package). `make test` runs everything with `-race`; there is no separate integration suite.
