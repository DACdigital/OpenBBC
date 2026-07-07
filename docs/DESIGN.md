# OpenBBC Design Document

## Problem Statement

You want to expose your backend capabilities as an app. The challenge is the **agent part** which utilizes that app — domain knowledge needs to be passed to the agent.

**Our solution** speeds up time to market for custom agent deployments that can be integrated on the frontend as a chat interface.

## Assumptions

- Backend is **already wrapped** by some MCP server (FastAPI MCP / Spring MCP / etc.)
- Client is a frontend which utilizes the **AG-UI protocol**
- Single AI agent on day one (multi-agent system in future iterations)

## Architecture Overview

```
┌─────────────┐      AG-UI        ┌──────────────────────────────────────┐       MCP Protocol       ┌─────────────┐
│             │     Protocol      │            open-bbcd                 │      SSE/Streamable      │             │
│  Client     │ ───────────────►  │  ┌────────────────────────────────┐  │ ──────────────────────►  │   Backend   │
│  (Frontend) │                   │  │   AI Agent (deployed version)  │  │         HTTP             │  (MCP wrap) │
└─────────────┘                   │  └────────────────────────────────┘  │                          └─────────────┘
                                  │                                      │
                                  │  + Backoffice UI + REST API          │
                                  │  + Agent + dataset versioning        │
                                  │  + Evals + training sessions         │
                                  └──────────────────────────────────────┘
                                                   ▲
                                                   │
                        ┌──────────────────────────┼──────────────────────────┐
                        │                          │                          │
                        ▼                          ▼                          ▼
               ┌─────────────┐            ┌─────────────┐            ┌─────────────┐
               │ flow-map-   │            │   aikdm     │            │  PostgreSQL │
               │ compiler    │            │   (CLI)     │            │  (Storage)  │
               │ (CC skill)  │            │             │            │             │
               │             │            │ generate    │            │ agents      │
               │ scans FE    │            │ evaluate    │            │ datasets    │
               │ repo →      │            │ train       │            │ evals       │
               │ .flow-map/  │            │             │            │ training    │
               └─────────────┘            └──────┬──────┘            └─────────────┘
                                                 │ REST via
                                                 │ scripts/*.sh
                                                 └──────────► open-bbcd
```

aikdm is out-of-process and DB-unaware. It only speaks REST to open-bbcd (through `scripts/run_eval.sh` and `scripts/train_from_session.sh`); no direct DB access.

### Components

| Component | Description |
|-----------|-------------|
| **Client (Frontend)** | UI that communicates with the deployed agent via AG-UI protocol. |
| **open-bbcd** | Go binary: backoffice UI + REST API + agent runtime + agent/dataset versioning. |
| **AI Agent** | Runs inside `open-bbcd` (both the BO test chat and the deployed AG-UI runtime). Calls backend tools via MCP. |
| **Backend (MCP wrapped)** | Client's existing backend exposed via MCP protocol. |
| **flow-map-compiler** | Claude Code skill from `bbc-discovery/flow-map-compiler/`. Scans a client **frontend** repo and produces a `.flow-map/` wiki. |
| **aikdm** | Python CLI. Ships three subcommands: `generate-agent`, `evaluate`, `train-agent`. |
| **PostgreSQL** | Storage for agents, versions, datasets, evals, training sessions. |

## Flow

The platform is a five-phase pipeline: **discover → generate → feedback → evaluate → deploy** (with training folded in as automated iteration on evaluate → generate).

### Phase 0: Frontend repo → structured discovery

1. Point Claude Code at the target frontend repo with the `flow-map-compiler` skill (shipped from `bbc-discovery/flow-map-compiler/`).
2. The skill scans call sites and produces a `.flow-map/` directory: `flows/` (business flows, tool-name-free), `capabilities/` (backend endpoints with proposed tool names), `agents/`, plus a zip of the whole tree.
3. The skill is **fully agent-driven** — no script pipeline. See `bbc-discovery/flow-map-compiler/skills/flow-map-compiler/SKILL.md` for the procedure and the contract triple (`output-schemas.md` ↔ `lint-contract.md` ↔ templates).

Output of Phase 0 is the input to Phase I.

**Owner:** `flow-map-compiler` plugin (Claude Code skill).

### Phase I: Alpha Agent Generation

1. Upload the `.flow-map/` zip through the open-bbcd wizard (`GET /agents/new`, schema-driven from `web/schemas/wizard-v1.yaml`).
2. Domain experts flesh out scope + guardrails + personality in the backoffice.
3. `aikdm generate-agent` produces the prompt bundle (`main_prompt`, `capabilities[]`, `skills[]`, `external_actions[]`; two-agent generator + critic loop, critic notes ride along as advisory).
4. Bundle lands as **version 1** in Postgres. Split storage as of migration 017:
   - `agents.architecture` — flows, endpoints, endpoint→backend wiring. Structural, **frozen** on first version.
   - `agent_versions.prompts` — `main_prompt` + skill prompts. **Editable** per version.
   - `agents.capabilities[]` — pass-through of the discovery output.

**Owner:** open-bbcd (wizard + configurator) + aikdm (`generate-agent`).

See [ARCHITECTURE.md § MCP wiring](ARCHITECTURE.md#mcp-wiring) for how endpoints and MCPs are attached.

### Phase II: Feedback & Dataset Creation

1. Admin picks a version and runs it in the backoffice chat (`/agent_versions/{id}/chat`).
2. Interact with the agent — build up scenario sessions.
3. Per assistant message, capture feedback: `rating` (up/down), `comment`, `expected_output`, and a JSONB array of `judge_criteria` (acceptance bullets). Stored in `chat_message_feedback`.
4. Assign the session to a dataset (`POST /agent_versions/{v}/chat/{s}/assign-dataset`). Sessions land on a DRAFT dataset version.
5. Close the draft: `POST /datasets/{id}/close-draft` flips it to CLOSED, locks the member sessions (`chat_sessions.locked_at`), and seeds the next DRAFT with the same sessions (cumulative — migration 020).

Dataset lifecycle:

```
     new dataset
          │
          ▼
   ┌───────────────┐   add sessions   ┌───────────────┐
   │ DRAFT v1      │ ───────────────► │ DRAFT v1      │
   │ (mutable)     │  + feedback      │ (with content)│
   └───────┬───────┘                  └───────┬───────┘
           │ close-draft                      │
           ▼                                  │
   ┌───────────────┐  seed              ┌─────▼─────────┐
   │ CLOSED v1     │ ─────────────────► │ DRAFT v2      │
   │ (locked)      │  cumulative        │ (v1 sessions) │
   └───────────────┘                    └───────────────┘
```

At most one DRAFT per dataset (partial unique index in migration 019). A CLOSED version is what you evaluate against.

**Owner:** open-bbcd backoffice.

See [ARCHITECTURE.md § Feedback + datasets](ARCHITECTURE.md#feedback--datasets).

### Phase III: Evaluation & Iteration

Evaluation scores an agent version against a **closed** dataset version. One `evals` row per run + one `eval_sessions` row per simulated session (migration 022).

1. Backoffice: on the version detail's Versions tab click **Evaluate**, pick a dataset + closed version, pick `mock_mcp_tools`, confirm. Creates an eval row in PENDING.
2. Operator: `scripts/run_eval.sh <eval_id>`. The script exports the input, flips to IN_PROGRESS, invokes `aikdm evaluate`, posts the result back.
3. Aikdm: per session, `simulator.py` produces user turns, `target.py` invokes the agent via LiteLLM completion with the bundle's tools, `tool_mock.py` either replays exact tool calls from the original transcript or synthesizes plausible payloads from shapes, `judge.py` scores each criterion.

**Score formula:** global pass-rate = `sum(passed_criteria) / sum(total_criteria)` across all sessions in the eval (migration 022). Every criterion counts equally; longer sessions weigh proportionally more.

**`mock_mcp_tools` toggle** (migration 023):

- default `true` — replay/synthesize tool responses offline. Deterministic.
- `false` — real MCP calls. Session-scoped `header_overrides` on the eval row proxy through to the backend (migration 023).

Once an eval is DONE and `score < 1.0`, the detail page shows a **Train** button. Automated iteration follows (Phase IV).

Human-in-the-loop iteration is always available: refine prompts, save as a new version, re-evaluate.

**Owner:** aikdm (`evaluate`) + open-bbcd (storage, UI).

See [ARCHITECTURE.md § Evals](ARCHITECTURE.md#evals).

### Phase IV: Training sessions (automated iteration)

Shipped in PR #43 (migration 024). A first-class domain object that automates "eval → improve prompts → re-eval" as a bounded loop.

1. Click **Train** on an eval detail page (guarded: eval must be DONE, score < 1.0, no active training session).
2. Server inserts a `training_sessions` row in PENDING. `source_eval_id`, `parent_version_id`, `epochs`, `patience` are captured up front.
3. Operator: `scripts/train_from_session.sh <session_id>`. The script flips to IN_PROGRESS, invokes `aikdm train-agent --input … --epochs N --patience K --out …`.
4. `aikdm train-agent` runs an N-epoch hill-climb (`aikdm/aikdm/train/orchestrator.py`):
   - Baseline eval on the parent version.
   - Each epoch: **teacher** LLM proposes patches to the prompt sections → apply → run the eval pipeline as reward → promote if candidate score is strictly greater than best.
   - Early stop on `patience` consecutive non-improvements, or on perfect score (`>= 1.0`).
5. Script prints a score diff, asks operator y/N, then `POST /training-sessions/{id}/complete` — which lands the new bundle as a new agent version and flips the session to DONE. `/fail` on error/rejection.

```
      ┌──────────────┐
      │  eval DONE   │
      │ score < 1.0  │
      └──────┬───────┘
             │ Train button
             ▼
      ┌──────────────┐
      │   PENDING    │
      └──────┬───────┘
             │ train_from_session.sh → start
             ▼
      ┌──────────────┐    ┌───────────────────────────────┐
      │ IN_PROGRESS  │◄──►│ aikdm train-agent             │
      │              │    │  epoch = teacher → eval → cmp │
      └──────┬───────┘    └───────────────────────────────┘
             │ complete / fail
             ▼
      ┌──────────────┐
      │   DONE       │  new agent version linked
      │  (or FAILED) │  via new_version_id
      └──────────────┘
```

Uniqueness: at most one PENDING/IN_PROGRESS session per eval (partial unique index in migration 024).

**Owner:** aikdm (`train-agent`) + open-bbcd (training sessions storage + UI).

See [ARCHITECTURE.md § Training sessions](ARCHITECTURE.md#training-sessions).

### Phase V: Deployment

1. Select a tested agent version.
2. Deploy via open-bbcd (agent runs inside the open-bbcd binary). One agent deployed per chain (migration 011).
3. Exposed to the client frontend via AG-UI (`/deployed/{agent_id}/…`).

**Owner:** open-bbcd.

## Resources

Resources — canonically **capabilities** in current code — are backend interfaces (endpoints, tools) the agent uses.

### Discovery

- Gathered per intent/process during Phase 0 (`flow-map-compiler`).
- Each capability maps to a specific backend interaction the agent needs (typically an MCP tool call).

### MCP Toolkit

open-bbcd resolves capabilities to concrete backends via two wiring tables:

- **Endpoint→backend** at the **agent** level (`agent_endpoint_backend`, migration 017) — structural, every version of the same agent sees the same wiring.
- **MCP attachments** at the **version** level (`agent_version_mcp_backend`, migration 015) — each version can attach different MCPs with different editable notes.

Backend definitions live in `tool_backends` (migration 014); kinds are `http_endpoint` and `mcp_client`. Managed via `/mcp` in the backoffice.

### Resource Sources

| Source | Description |
|--------|-------------|
| **Existing MCP servers** | Use client's already-wrapped MCP endpoints directly (`mcp_client`). |
| **Custom MCP wrappers** | Wrap existing APIs (REST, GraphQL, etc.) as MCP servers, or use `http_endpoint` backends. |

### Session Proxying

- User session is passed/proxied from chat through to the client's backend.
- Enables authenticated calls to backend capabilities on behalf of the user.
- Agent acts within user's permission scope.
- Session-scoped `backend_header_overrides` (migration 016) let admins inject auth/traceability headers per BO chat session; evals have their own flat `header_overrides` (migration 023).

```
┌──────────┐   AG-UI    ┌──────────┐   MCP + headers   ┌──────────┐
│  Client  │ ─────────► │ open-bbcd│ ────────────────► │ Backend  │
│ (session)│            │  (agent) │    (proxied)      │ Capability│
└──────────┘            └──────────┘                   └──────────┘
```

## Versioning

| Entity | Versioning |
|--------|------------|
| **Agent** | Linked list of versions via `agents.parent_version_id`. Cannot delete versions. Structural fields (endpoints, flows) frozen on the agent; prompts editable per version. |
| **Dataset** | Two-state lifecycle: `DRAFT` (mutable, at most one per dataset) → `CLOSED` (immutable snapshot). Closing seeds the next DRAFT with the closed version's sessions. |
| **Eval** | Row per (agent version, dataset version) run. States: `PENDING → IN_PROGRESS → DONE|FAILED`. Immutable once DONE. |
| **Training session** | Row per (source eval, parent version) run. States: `PENDING → IN_PROGRESS → DONE|FAILED`. DONE points at a newly-created agent version via `new_version_id`. |

## Open Source Scope

The **open-bbcd** service (including the hosted agent) is open source.

The wrapped MCP backend remains external / proprietary to users.

## Tech Stack

| Component | Technology |
|-----------|------------|
| open-bbcd | Go 1.22+, `database/sql` + `lib/pq`, `goose` migrations, `html/template` + htmx |
| aikdm | Python 3.12+ (click, Google ADK + LiteLLM, Pydantic, Jinja2), managed with `uv` |
| flow-map-compiler | Claude Code skill (markdown + templates, no build) |
| Storage | PostgreSQL 15+ |
| Deployment | Docker Compose (top-level `docker-compose.yml`), multi-stage distroless image |
| Protocol (client) | AG-UI |
| Protocol (backend) | MCP (SSE / Streamable HTTP) |

## Deployment

Two paths:

- **Local Go**: `make build && ./bin/open-bbcd` with `$DATABASE_URL` pointing at Postgres. Migrations auto-apply on boot (goose embedded via `//go:embed`).
- **Docker Compose** (top-level `docker-compose.yml`): brings up Postgres + open-bbcd; aikdm is behind the `aikdm` profile for one-off runs. The `open-bbcd` image is a multi-stage build ending in `gcr.io/distroless/static-debian12:nonroot`; container healthcheck uses the `open-bbcd healthcheck` subcommand.

## Out of Scope (for now)

- Agent operator / separate deployment (multi-agent, multi-tenant runtime).
- Multi-agent system on the same deployment.
- Multiple concurrent deployments per agent chain.
- Registry publish for prebuilt images.
- Helm chart / Kubernetes manifests.
- Image build in CI (today CI runs Go + Python tests only).
