# OpenBBC Architecture

## System Overview

```
                                    ┌─────────────────────────────────────────────────────────────────┐
                                    │                         OpenBBC Platform                        │
                                    └─────────────────────────────────────────────────────────────────┘

    ┌───────────────┐                                                                      ┌───────────────┐
    │               │                                                                      │               │
    │  Client Repo  │                                                                      │   Client's    │
    │  (frontend)   │                                                                      │   Backend     │
    │               │                                                                      │  (MCP wrap)   │
    └───────┬───────┘                                                                      └───────▲───────┘
            │                                                                                      │
            │ scans                                                                                │ MCP Protocol
            ▼                                                                                      │ (SSE/HTTP)
    ┌───────────────┐         .flow-map/          ┌────────────────────────────────────────────────┴───────┐
    │ flow-map-     │           zip               │                      open-bbcd                         │
    │ compiler      │ ──────────────────────────► │  ┌──────────────────────────────────────────────────┐  │
    │ (CC skill)    │                             │  │                 Backoffice UI                    │  │
    │               │                             │  │  - Agent + version configurator                  │  │
    └───────────────┘                             │  │  - MCP backends CRUD (/mcp)                      │  │
                                                  │  │  - Chat + feedback                               │  │
                                                  │  │  - Datasets (DRAFT → CLOSED)                     │  │
                                                  │  │  - Evals + Training sessions                     │  │
                                                  │  └──────────────────────────────────────────────────┘  │
                                                  │                           │                            │
                                                  │                           ▼                            │
                                                  │  ┌──────────────────────────────────────────────────┐  │
                                                  │  │                   REST API                       │  │
                                                  │  └──────────────────────────────────────────────────┘  │
                                                  │                           │                            │
                                                  │         ┌─────────────────┴─────────────────┐          │
                                                  │         ▼                                   ▼          │
                                                  │  ┌─────────────┐                    ┌─────────────┐    │
                                                  │  │ BO chat     │                    │  Deployed   │    │
                                                  │  │ (any ver.)  │                    │   Agent     │    │
                                                  │  │             │                    │  (AG-UI)    │    │
                                                  │  └─────────────┘                    └─────────────┘    │
                                                  └────────────────────────────┬───────────────────────────┘
                                                                               │
                              ┌────────────────────────────────────────────────┼────────────────────┐
                              │                                                │                    │
                              ▼                                                ▼                    ▼
                      ┌───────────────┐                               ┌───────────────┐    ┌───────────────┐
                      │               │      REST (eval/train)        │               │    │               │
                      │    aikdm      │ ◄─────────────────────────────│  PostgreSQL   │    │    Client     │
                      │    (CLI)      │      via scripts/*.sh         │               │    │  (Frontend)   │
                      │               │                               │               │    │               │
                      └───────┬───────┘                               └───────────────┘    └───────────────┘
                              │
                    ┌─────────┼──────────┐
                    ▼         ▼          ▼
              generate-  evaluate   train-agent
                agent
```

aikdm is out-of-process and DB-unaware. It only talks to open-bbcd through the REST API (via `scripts/run_eval.sh` and `scripts/train_from_session.sh`); it never opens a DB connection of its own.

## Components

### 1. flow-map-compiler (CC discovery skill)

**Type:** Claude Code skill, shipped as a plugin from `bbc-discovery/flow-map-compiler/`.
**Purpose:** Extract business flows + backend endpoints from a client's **frontend** repo.

| Aspect | Details |
|--------|---------|
| Input  | Client frontend repo (Next.js / React / SvelteKit / Nuxt / Remix / Astro / …). |
| Output | `.flow-map/` wiki inside the target repo: `flows/`, `capabilities/`, `agents/`, plus `<name>.zip` uploaded via the wizard. |
| Style  | Fully agent-driven. No script pipeline — the skill *is* a procedure Claude executes. |

Skill contract lives at `bbc-discovery/flow-map-compiler/skills/flow-map-compiler/SKILL.md`; the "contract triple" (`references/output-schemas.md` ↔ `references/lint-contract.md` ↔ `assets/templates/*.tmpl`) must stay in sync.

---

### 2. open-bbcd

**Type:** Go daemon.
**Purpose:** Backoffice UI + REST API + deployed agent runtime, all in one binary.

#### 2.1 Backoffice UI

Server-rendered (Go `html/template` + htmx). Entrypoint at `internal/handler/api.go:194`. No SPA.

| Area | URL prefix | Details |
|---|---|---|
| Agents list + wizard | `/agents/ui`, `/agents/new` | Wizard uploads a `.flow-map/` zip. Schema-driven, `web/schemas/wizard-v1.yaml`. |
| Agent-level config | `/agents/{agent_id}/configure/{versions,inputs,architecture/*}` | Frozen structural data (flows, endpoints, endpoint→backend wiring). |
| Version-level config | `/agent_versions/{version_id}/configure/{prompts,architecture/*,mcp,finalize}` | Editable prompts + MCP attachments per version. |
| BO chat | `/agent_versions/{id}/chat` | Test any version; feedback and dataset assignment inline. |
| MCP backends | `/mcp` | CRUD over `tool_backends` (kind: `http_endpoint` \| `mcp_client`). |
| Datasets | `/datasets` | DRAFT ↔ CLOSED lifecycle, session assignment. |
| Evals | `/evals`, `/agent_versions/{id}/evals` | List + detail for eval runs. |
| Training | `/training-sessions` | List + detail for training runs. |

#### 2.2 REST API

JSON surface for scripts + external callers. Highlights:

- `/evals/{id}/export.yaml`, `/start`, `/result`, `/fail` — the surface `scripts/run_eval.sh` drives.
- `/training-sessions[/{id}[/json|/report.json|/start|/complete|/fail]]` — the surface `scripts/train_from_session.sh` drives.
- `/deployed/{agent_id}/sessions[/…]` — the AG-UI-facing deployed runtime.
- `/health` — used by the container `HEALTHCHECK` (`open-bbcd healthcheck` subcommand).

#### 2.3 Agent Runtime

Two orchestrator instances share a single stateless `tools.Builder` (`internal/handler/api.go:123`):

| Mode | Store | Notes |
|---|---|---|
| **BO chat** | `chat_sessions` / `chat_messages` | One session per agent version × user; per-session `backend_header_overrides` (migration 016). |
| **Deployed** | `deployed_sessions` / `deployed_messages` | Exposed to client frontend via AG-UI. One agent deployed per chain (migration 011). |

Both live inside the `open-bbcd` binary today.

---

### 3. aikdm

**Type:** Python 3.12+ CLI, `uv` for deps.
**Purpose:** LLM-heavy work — generation, evaluation, training. Runs out-of-process; open-bbcd invokes it via `scripts/*.sh` wrappers that poke the REST API.

**Tech stack:** click (CLI), Pydantic (schemas), Jinja2 (templates), PyYAML, Google ADK with LiteLLM (multi-provider: Anthropic, OpenAI, Gemini).

#### Commands (`aikdm/aikdm/cli.py`)

| Command | Reads | Writes | What it does |
|---|---|---|---|
| `generate-agent` | `flow-map-config.yaml` | `bundle.yaml` | Two-agent (generator + critic) loop that emits an agent prompt bundle. Critic notes ride along in `metadata.critic_notes`. |
| `evaluate` | `eval-input.yaml` (agent version + dataset version) | `eval-result.json` | Per-session: simulator replays user turns → target answers → judge scores per-criterion. Global `sum(passed)/sum(total)`. |
| `train-agent` | Same `eval-input.yaml` as `evaluate` + `--epochs N --patience K` | `bundle.yaml` + `training-report.json` in `--out` dir | N-epoch teacher/eval hill-climb (`aikdm/aikdm/train/orchestrator.py`). Each epoch: teacher proposes patches → apply → eval → promote-if-strictly-better. Early stop on plateau or perfect score. |

`evaluate` and `train-agent` share the eval pipeline: training reuses eval as its reward function.

#### Prompt bundle format

Single YAML:
- `metadata` — schema versions, models used, critic rounds, token usage, critic notes.
- `main_prompt` — assembled XML system prompt (role, scope, personality, guardrails).
- `capabilities[]` — structured pass-through of `flow_map_config.capabilities` (name, description, proposed_tool).
- `skills[]` — per-skill prompts, each with a `<capabilities>` block naming the skill's MCP servers.
- `external_actions[]` — non-internal skills the agent must redirect users to.

Section structure is declared in `aikdm/schemas/prompt-v1.yaml` (versioned).

#### Exit codes

`0` success · `1` unexpected · `2` input/config error · `3` LLM error. Structured `{"error":"<kind>","details":"<msg>"}` JSON on stderr for non-zero exits.

---

### 4. PostgreSQL

**Type:** Relational store. Postgres 15+, `goose` for migrations (currently at `024_training_sessions`).

**Stores:**
- Agents + versions (linked-list versioning via `agents.parent_version_id`; agent-level `architecture` JSONB + version-level `prompts` JSONB after migration 017).
- Chat sessions + messages, per-message feedback (`chat_message_feedback` with `expected_output`, `judge_criteria`).
- Datasets + versions + session assignments (`datasets`, `dataset_versions`, `dataset_version_sessions`).
- Tool backends + wiring (`tool_backends`, `agent_endpoint_backend`, `agent_version_mcp_backend`).
- Evals + per-session breakdowns (`evals`, `eval_sessions`).
- Training sessions (`training_sessions`).

The legacy `resources` table (migration 002) and its `/resources` CRUD surface still exist but are not on the shipped MCP path. Real MCP wiring lives on `tool_backends` + the two wiring tables above.

---

## Capabilities

Capabilities are backend interfaces (endpoints, tools) the agent uses. The term is canonical: the discovery skill emits `.flow-map/capabilities/`, `FlowMapConfig.Capabilities` carries them through the wizard/configurator, and the aikdm bundle's `capabilities[]` block is the runtime-readable list.

### Discovery & Mapping

Capabilities are gathered per intent/process during discovery:

```
┌──────────────┐
│   Intent A   │──► Capability 1, Capability 2
├──────────────┤
│   Intent B   │──► Capability 2, Capability 3
├──────────────┤
│   Intent C   │──► Capability 1, Capability 4
└──────────────┘
```

### Session Proxying

The user's session token rides all the way through:

```
┌──────────┐        ┌──────────┐        ┌──────────┐
│  Client  │ ─────► │ open-bbcd│ ─────► │ Backend  │
│          │ AG-UI  │  (agent) │  MCP   │Capability│
│  token   │        │  proxy   │        │  auth    │
└──────────┘        └──────────┘        └──────────┘
```

Agent acts within user's permission scope. No privilege escalation.

---

## MCP wiring

Migrations 014, 015, 017. Two axes: what the agent can *see* (MCP attachments) and how endpoints resolve to a concrete backend (endpoint→backend wiring).

**Tables:**

- `tool_backends` (migration 014) — one row per backend, kind ∈ `{http_endpoint, mcp_client}`, opaque `config` JSONB.
- `agent_endpoint_backend` (migration 017) — agent-level endpoint→backend mapping. Endpoints are frozen on the agent (structural), so every version of the same agent sees the same wiring for a given endpoint id.
- `agent_version_mcp_backend` (migration 015) — version-level MCP attachments with an editable `note`. Different versions of the same agent can attach different MCPs (or the same MCP with different guidance).

Migration 017's split (agent-level architecture vs version-level prompts) is the reason endpoint wiring is agent-keyed but MCP attachments stay version-keyed: the endpoint list is structural, the MCP guidance is prompt-editable.

**Handlers:**

- `internal/handler/backends.go` — `/mcp` CRUD (list, new, create, test-connection, edit, update, delete). `POST /mcp/test` pings a backend before saving.
- `internal/handler/configurator.go` — version-scoped wiring UI:
  - `GET /agent_versions/{id}/configure/architecture/mcp` — attach/detach MCP backends, edit notes.
  - `POST /agent_versions/{id}/architecture/mcp/{backendID}/toggle` and `/notes`.
  - `POST /agent_versions/{id}/endpoints/{endpointID}/backend` — set per-endpoint backend at the version level (kept for compatibility with the old per-version model; the agent-level surface below is the primary path today).
- `internal/handler/agent_detail.go` — agent-scoped endpoint wiring:
  - `GET /agents/{id}/configure/architecture/endpoints` — see all endpoints + current backends.
  - `POST /agents/{id}/configure/architecture/endpoints/bulk` — bulk-assign backend to every endpoint (single transaction in `AgentWiringRepository.SetEndpointBackendBulk`, `internal/repository/agent_wiring.go:39`).
  - `POST /agents/{id}/configure/architecture/endpoints/{endpointID}/backend` — per-endpoint.

The runtime tool builder (`internal/llm/tools.Builder`) resolves each capability at chat time via `toolBackendStoreAdapter` (`internal/handler/api.go:355`): endpoint→backend comes from `agentWiring`, MCP attachments from `wiring`.

---

## Feedback + datasets

Migrations 019, 020, 021.

**Feedback is per assistant message.** `chat_message_feedback` (migration 019) hangs off `chat_messages` (repo-layer enforces `role='assistant'`; Postgres doesn't do partial FKs). Fields:

- `rating` ∈ `{up, down}`
- `comment`, `expected_output` — free text
- `judge_criteria` — JSONB array of acceptance-criteria bullets (migration 021). Empty at insert time; dataset close-draft refuses if any member session's feedback row has an empty list.

**Datasets are versioned collections of chat sessions.**

- `datasets` — the identity + name.
- `dataset_versions` — status ∈ `{DRAFT, CLOSED}`, `version_num`, `close_note`. **At most one DRAFT per dataset** (partial unique index).
- `dataset_version_sessions` — the join. A CLOSED version is a snapshot; the next DRAFT is seeded with the CLOSED version's sessions (migration 020) so users see cumulative content, not an empty next version.
- A session belongs to at most one dataset — enforced at the repo layer, not the schema (migration 020 dropped the schema uniqueness in favour of allowing cross-version reuse within the same dataset).
- Closing a draft flips `chat_sessions.locked_at`, making the session immutable.

Handler: `internal/handler/datasets.go`. Routes: `/datasets`, `/datasets/{id}`, `/datasets/{id}/close-draft`, `/datasets/{id}/close-draft/confirm`. Chat-side assignment: `POST /agent_versions/{v}/chat/{s}/assign-dataset`.

---

## Evals

An eval scores an agent version against a closed dataset version. One `evals` row per run + one `eval_sessions` row per simulated session (migration 022).

**Row shape** (`evals`):

- `agent_version_id`, `dataset_version_id`, `status` ∈ `{PENDING, IN_PROGRESS, DONE, FAILED}`.
- `score` (double precision), `total_criteria`, `passed_criteria`, `error_message`, `aikdm_meta` (JSONB).
- `mock_mcp_tools` (bool, default true, migration 023), `header_overrides` (flat map, JSONB).
- Timestamps: `created_at`, `started_at`, `completed_at`.

**Per-session breakdown** (`eval_sessions`):

- `score`, `total_criteria`, `passed_criteria`, plus the full `transcript` and per-criterion `judgments` as JSONB.

**Score formula:** global pass-rate = `sum(passed_criteria) / sum(total_criteria)` across all sessions. Every criterion counts equally; longer sessions weigh proportionally more.

**`mock_mcp_tools` toggle** (migration 023):

- `true` (default): tool calls replay from the original transcript (exact match) or synthesize from `body_shape`/`response_shape`. Deterministic, offline.
- `false`: real MCP calls. Header overrides on the eval row (flat `map[string]string`, simpler than chat's per-backend layout) get proxied through to the MCP backend.

**Operator flow:**

1. Backoffice: on the agent-version detail's Versions tab click **Evaluate**, pick a dataset + closed version, pick `mock_mcp_tools`, confirm. Server creates the eval row in `PENDING`.
2. Operator: `OPENBBCD_URL=http://localhost:8080 scripts/run_eval.sh <eval_id>`.
3. Script: `GET /evals/{id}/export.yaml` → `POST /evals/{id}/start` → `uv run aikdm evaluate --input … --output …` → `POST /evals/{id}/result` (or `/fail` on error).
4. Aikdm: `simulator.py` produces user turns, `target.py` runs the agent via LiteLLM completion, `tool_mock.py` handles the tools branch, `judge.py` scores each criterion.
5. Backoffice persists per-session results and renders the eval detail page.

Scenario-testing inspiration: [langwatch/scenario](https://github.com/langwatch/scenario).

Agent-version detail's *Avg eval* column is a plain mean of DONE eval scores for that version (not weighted by dataset size).

The eval detail page's **Train** button opens the training-sessions flow (below) when the eval is DONE + score < 1.0 + no active session exists for that eval.

---

## Training sessions

First-class domain object shipped in PR #43 (migration 024). Automates iteration on a bundle: an eval says "score = 0.7" → training runs epochs against that same input → produces a new agent version with a better bundle.

**State machine:**

```
   Train button on eval detail
             │
             ▼
        PENDING ──────┐
             │        │
             ▼        │
      IN_PROGRESS     │  (fail from any state)
             │        │
             ▼        ▼
           DONE     FAILED
```

**Row shape** (`training_sessions`):

- `source_eval_id`, `parent_version_id`, `new_version_id` (NULL until DONE), `status`.
- `epochs`, `patience`, `initial_score`, `final_score`, `total_epochs_run`, `stopped_reason`, `training_report` (JSONB).
- `requested_at`, `started_at`, `completed_at`, `error_message`.

**Uniqueness:** partial unique index `idx_ts_one_active_per_eval` ensures at most one PENDING or IN_PROGRESS session per eval.

**Creation gate** (`internal/handler/eval.go:20-25`): the eval detail page shows Train only when the eval is DONE, score < 1.0, and no active session exists for that eval. Clicking creates the PENDING row.

**Operator flow:**

1. Backoffice: click **Train** on eval detail. Server inserts a PENDING row.
2. Operator: `OPENBBCD_URL=http://localhost:8080 scripts/train_from_session.sh <session_id> [--epochs N] [--patience K]`.
3. Script: `GET /training-sessions/{id}/json` → `GET /evals/{source_eval_id}/export.yaml` → `POST /training-sessions/{id}/start` (→ IN_PROGRESS) → `uv run aikdm train-agent --input … --epochs N --patience K --out …` → prompts operator y/N → `POST /training-sessions/{id}/complete` (creates a new agent version, → DONE) or `POST /training-sessions/{id}/fail` (→ FAILED with reason).
4. Aikdm's `run_training` (`aikdm/aikdm/train/orchestrator.py`) runs the teacher/judge loop reusing `run_eval` as reward. Strict `>` on candidate score; `patience` consecutive non-improvements → early stop. Perfect-baseline shortcut: if `initial_score >= 1.0`, skip the loop entirely.

**Endpoints:**

- `GET /training-sessions` — list.
- `GET /training-sessions/{id}` — detail (UI).
- `GET /training-sessions/{id}/json` — script fetch.
- `GET /training-sessions/{id}/report.json` — training report blob.
- `POST /training-sessions` — create (typically via Train button; the API is public).
- `POST /training-sessions/{id}/{start,complete,fail}` — state transitions.

---

## Chat header overrides

`chat_sessions.backend_header_overrides` — JSONB, one entry per session (migration 016). Structured as `{backend_id: {header: value}}` (per-backend layout). Proxied by the tool builder into MCP backend calls made from that session.

**UI:** the chat view has a Headers modal — `GET /agent_versions/{v}/chat/{s}/headers` and `POST` to update. Used mainly for auth/traceability when testing against real MCPs.

**Eval variant:** the eval row has its own `header_overrides` (migration 023) — a flat `map[string]string` rather than per-backend, because an eval targets one agent version + one dataset and a single set of overrides is enough.

---

## Data Flow

### Flow 1: Discovery → Alpha Agent

```
┌──────────┐    scan     ┌──────────┐  .flow-map/  ┌──────────┐   wizard     ┌──────────┐
│  Client  │ ─────────►  │ flow-map │ ────────────►│  wizard  │ ──────────►  │ open-bbcd│
│ Frontend │             │ compiler │     zip      │  upload  │              │ + aikdm  │
└──────────┘             └──────────┘              └──────────┘              │ generate │
                                                                             └────┬─────┘
                                                                                  │
                                                                                  ▼
                                                                           ┌──────────┐
                                                                           │ Postgres │
                                                                           │ (v1)     │
                                                                           └──────────┘
```

### Flow 2: Feedback → Dataset

```
┌──────────┐   chat    ┌──────────┐   feedback   ┌──────────┐   close    ┌──────────┐
│  Admin   │ ───────►  │ open-bbcd│ ──────────►  │ dataset  │ ─────────► │ dataset  │
│   (BO)   │ ◄───────  │  chat    │   +save      │  DRAFT   │            │  CLOSED  │
└──────────┘  response └──────────┘              └──────────┘            └──────────┘
```

### Flow 3: Evaluation

```
┌──────────┐  create    ┌──────────┐   start    ┌──────────┐   result   ┌──────────┐
│  Admin   │ ────────►  │ open-bbcd│ ─────────► │  aikdm   │ ─────────► │ open-bbcd│
│  (BO)    │            │  eval    │ (via .sh)  │ evaluate │ (via .sh)  │  score   │
└──────────┘            └──────────┘            └──────────┘            └──────────┘
```

### Flow 4: Training

```
Train button          create             start                 train-agent
┌──────────┐        ┌──────────┐        ┌──────────┐         ┌──────────┐
│ Eval     │ ─────► │ Training │ ─────► │ Training │ ──────► │  aikdm   │
│ detail   │        │ session  │        │ session  │(via .sh)│  epochs  │
│ page     │        │  PENDING │        │ IN_PROG. │         │          │
└──────────┘        └──────────┘        └──────────┘         └────┬─────┘
                                                                  │
                                                     complete     ▼
                                                    ┌──────────────────────┐
                                                    │ new version + DONE   │
                                                    │ (or FAILED w/ reason)│
                                                    └──────────────────────┘
```

### Flow 5: Deployment

```
┌──────────┐   deploy   ┌──────────┐   AG-UI    ┌──────────┐
│  Admin   │ ────────►  │ open-bbcd│ ◄────────► │  Client  │
│   (BO)   │  version   │ (agent)  │            │ Frontend │
└──────────┘            └────┬─────┘            └──────────┘
                             │
                             │ MCP + header overrides
                             ▼
                      ┌──────────┐
                      │ Client's │
                      │ Backend  │
                      └──────────┘
```

---

## Protocols

| Connection | Protocol | Description |
|------------|----------|-------------|
| Client ↔ Agent | AG-UI | Frontend chat integration |
| Agent ↔ Backend | MCP (SSE / Streamable HTTP) | Tool calls to client's backend |
| Admin ↔ open-bbcd | REST/HTTP | Backoffice API + htmx-rendered UI |
| aikdm ↔ open-bbcd | REST/HTTP | Eval jobs + training sessions via `scripts/*.sh` wrappers |

---

## Docker deployment

Top-level `docker-compose.yml` at the repo root brings up the full stack: Postgres 15 + `open-bbcd` + (behind the `aikdm` profile) `aikdm`. Both images are built multi-arch and multi-stage.

**Compose services:**

- `postgres`: Postgres 15, healthchecked with `pg_isready`.
- `open-bbcd`: waits on Postgres, publishes `:8080`, mounts `discovery-data` at `/data/discovery`, container `HEALTHCHECK` invokes `open-bbcd healthcheck`.
- `aikdm`: profile-gated (`--profile aikdm`), interactive/one-off container that mounts `./aikdm-work:/work`. Not part of the always-on stack.

**Images:**

- `open-bbcd/Dockerfile` — multi-stage, multi-arch (`$TARGETOS`/`$TARGETARCH`), builder on `golang:1.26`, runtime on `gcr.io/distroless/static-debian12:nonroot`. CGO off. `ENTRYPOINT = /usr/local/bin/open-bbcd`.
- `aikdm/Dockerfile` — multi-stage, builder on `python:3.12-slim` + `uv sync --frozen`, runtime on `python:3.12-slim` as user `aikdm` (uid 65532). `ENTRYPOINT = aikdm`.

**Binary subcommands** (`open-bbcd/cmd/open-bbcd/main.go:24-45`) — the built binary dispatches on its first arg:

- `open-bbcd` / `open-bbcd serve` — HTTP server. Auto-applies migrations on boot (`database.Migrate`, migrations embedded via `//go:embed` in `migrations/embed.go`).
- `open-bbcd migrate` — run migrations and exit.
- `open-bbcd healthcheck` — probe `http://127.0.0.1:$SERVER_PORT/health`, exit 0/1. Reads only `SERVER_PORT` so a broken `DATABASE_URL` doesn't fail the container `HEALTHCHECK`.

**CI** (`.github/workflows/ci.yml`) — two jobs on PRs to `main`: `test-go` (Postgres 15 service + `make test-integration`) and `test-python` (`uv sync --all-extras` + `make test` in `aikdm/`). A `check` job depends on both and gates merge.

**Future:** operator pattern for multi-agent deployments; helm chart; image publish to a registry.
