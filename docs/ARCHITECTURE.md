# OpenBBC Architecture

## System Overview

```
                                    ┌─────────────────────────────────────────────────────────────────┐
                                    │                         OpenBBC Platform                        │
                                    └─────────────────────────────────────────────────────────────────┘

    ┌───────────────┐                                                                      ┌───────────────┐
    │               │                                                                      │               │
    │  Client Repo  │                                                                      │   Client's    │
    │  (target)     │                                                                      │   Backend     │
    │               │                                                                      │  (MCP wrap)   │
    └───────┬───────┘                                                                      └───────▲───────┘
            │                                                                                      │
            │ scans                                                                                 │ MCP Protocol
            ▼                                                                                      │ (SSE/HTTP)
    ┌───────────────┐         structured          ┌────────────────────────────────────────────────┴───────┐
    │               │           output            │                      open-bbcd                         │
    │ CC Discovery  │ ──────────────────────────► │  ┌──────────────────────────────────────────────────┐  │
    │    Skill      │                             │  │                 Backoffice UI                    │  │
    │               │                             │  │  - Agent config        - Dataset management      │  │
    └───────────────┘                             │  │  - Version management  - Feedback chat           │  │
                                                  │  │  - Run/test agent      - Score dashboard         │  │
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
                                                  │  │ Test Agent  │                    │  Deployed   │    │
                                                  │  │ (internal)  │                    │   Agent     │    │
                                                  │  │             │                    │  (AG-UI)    │    │
                                                  │  └─────────────┘                    └─────────────┘    │
                                                  └────────────────────────────┬───────────────────────────┘
                                                                               │
                              ┌────────────────────────────────────────────────┼────────────────────┐
                              │                                                │                    │
                              ▼                                                ▼                    ▼
                      ┌───────────────┐                               ┌───────────────┐    ┌───────────────┐
                      │               │      async jobs               │               │    │               │
                      │   aikdm    │ ◄─────────────────────────────│  PostgreSQL   │    │    Client     │
                      │    (CLI)      │ ─────────────────────────────►│               │    │  (Frontend)   │
                      │               │      read/write               │               │    │               │
                      └───────────────┘                               └───────────────┘    └───────────────┘
                              │                                               │
                              │                                               │
                    ┌─────────┴─────────┐                         ┌──────────┴──────────┐
                    │                   │                         │                     │
                    ▼                   ▼                         ▼                     ▼
             ┌───────────┐       ┌───────────┐             ┌───────────┐         ┌───────────┐
             │  Alpha    │       │   Geval   │             │  Agents   │         │ Datasets  │
             │ Generator │       │           │             │ (versions)│         │ (versions)│
             └───────────┘       └───────────┘             └───────────┘         └───────────┘
```

## Components

### 1. CC Discovery Skill

**Type:** Claude Code Skill
**Purpose:** Extract business logic from client's codebase

| Aspect | Details |
|--------|---------|
| Input | Client repository path |
| Output | Structured data (standardized format for alpha agent) |
| Extracts | Business domain, processes, capabilities, endpoints/MCPs |

**Capabilities:**
- Scans repository via CC plugin/skills/slash-commands
- Identifies backend endpoint usage patterns
- Maps business logic to agent context
- Outputs structured markdown/JSON

**Out of scope (for now):**
- Resource/integration registration

---

### 2. open-bbcd

**Type:** Golang daemon/service
**Purpose:** Core platform service - backoffice + agent hosting

#### 2.1 Backoffice UI

Admin interface for platform management:

| Feature | Description |
|---------|-------------|
| Agent Config | Configure agent scope, guardrails, personality |
| Version Management | View/edit agent versions (immutable history) |
| Run/Test Agent | Internal chat to test any agent version |
| Feedback Chat | Annotate responses, create training data |
| Dataset Management | View/edit/version datasets |
| Score Dashboard | View evaluation scores by version pairs |

#### 2.2 REST API

Backend API for backoffice UI and external integrations.

#### 2.3 Agent Runtime

| Mode | Description |
|------|-------------|
| **Test Mode** | Run any agent version internally via backoffice |
| **Deployed Mode** | One agent version exposed via AG-UI for clients |

Constraints:
- One test agent at a time
- One deployed agent at a time
- Agent runs inside open-bbcd binary (no separate deployment)

---

### 3. aikdm

**Type:** Python CLI
**Purpose:** Agent generation (alpha) today. Geval and training planned.

**Tech Stack:**
- Python 3.12+, uv
- click (CLI), Pydantic (schemas), Jinja2 (templates), PyYAML
- Google ADK with LiteLLM backends (multi-provider: Anthropic, OpenAI, Gemini)

#### Capabilities

| Command | Description |
|---------|-------------|
| `aikdm generate-agent` | Generate alpha agent prompt bundle from FlowMapConfig YAML. |

#### Prompt Output Format

Single YAML bundle:
- `metadata` — schema versions, models used, critic rounds, token usage, critic notes
- `main_prompt` — assembled XML system prompt (role, scope, personality, guardrails, etc.)
- `capabilities[]` — structured pass-through of `flow_map_config.capabilities` (name, description, proposed_tool)
- `skills[]` — per-skill prompts with `<capabilities>` blocks naming each skill as an MCP server
- `external_actions[]` — non-internal skills the agent must redirect users to

Section structure is declared in `aikdm/schemas/prompt-v1.yaml` (versioned).

---

### 4. PostgreSQL

**Type:** Relational database
**Purpose:** Persistent storage for all platform data

**Stores:**
- Agents (with versions, including bundle JSONB)
- Chat sessions + messages (per-version test conversations)
- Datasets (with versions)
- Capabilities (with prompts) — legacy `resources` table; rename pending real MCP wiring
- Evaluation scores

---

## Capabilities

Capabilities are backend interfaces (endpoints, tools) that the agent uses to fulfill user requests. The term is canonical across the repo: the discovery skill emits `.flow-map/capabilities/`, `FlowMapConfig.Capabilities` carries them through the wizard/configurator, and the aikdm bundle's `capabilities[]` block is the runtime-readable list.

> **Note on terminology:** an older `resources` table + `/resources` REST surface still exists in open-bbcd. It's CRUD-with-no-producer today and will be renamed to `capabilities` when real MCP wiring lands.

### Discovery & Mapping

Capabilities are **gathered per intent/process** during the CC Discovery phase:

```
┌──────────────┐
│   Intent A   │──► Capability 1, Capability 2
├──────────────┤
│   Intent B   │──► Capability 2, Capability 3
├──────────────┤
│   Intent C   │──► Capability 1, Capability 4
└──────────────┘
```

### MCP Toolkit

open-bbcd includes its **own MCP toolkit** for capability connectivity:

- Each capability has its own **prompt/description** (similar to MCP tool descriptions)
- Prompts are used during training to teach agent how to use each capability
- Enables fine-grained control over agent's understanding of capabilities

```
┌─────────────────────────────────────────────────┐
│                   open-bbcd                     │
│  ┌───────────────────────────────────────────┐  │
│  │              MCP Toolkit                  │  │
│  │  ┌──────────┐┌──────────┐┌──────────┐    │  │
│  │  │Capability││Capability││Capability│    │  │
│  │  │+ prompt  ││+ prompt  ││+ prompt  │    │  │
│  │  └────┬─────┘└────┬─────┘└────┬─────┘    │  │
│  └───────┼───────────┼───────────┼──────────┘  │
└──────────┼───────────┼───────────┼─────────────┘
           │           │           │
           ▼           ▼           ▼
      ┌─────────┐ ┌─────────┐ ┌─────────┐
      │Existing │ │ Custom  │ │Existing │
      │  MCP    │ │MCP wrap │ │  MCP    │
      └─────────┘ └─────────┘ └─────────┘
```

### Capability Sources

| Source | Description |
|--------|-------------|
| **Existing MCP servers** | Connect directly to client's already-wrapped MCP endpoints |
| **Custom MCP wrappers** | Create MCP wrappers over existing REST/GraphQL/other APIs |

### Session Proxying

User session is **passed/proxied** through the entire chain:

```
┌──────────┐        ┌──────────┐        ┌──────────┐
│  Client  │ ─────► │ open-bbcd│ ─────► │ Backend  │
│          │ AG-UI  │  (agent) │  MCP   │Capability│
│ session  │        │          │        │          │
│   token  │ ─────► │  proxy   │ ─────► │ auth     │
└──────────┘        └──────────┘        └──────────┘
```

- Agent acts within user's permission scope
- Backend capabilities receive authenticated requests
- No privilege escalation - agent can only do what user can do

---

## Data Flow

### Flow 1: Discovery → Alpha Agent

```
┌──────────┐    scan     ┌──────────┐   structured   ┌──────────┐   generate   ┌──────────┐
│  Client  │ ─────────►  │    CC    │ ────────────►  │ open-bbcd│ ──────────►  │ aikdm │
│   Repo   │             │ Discovery│      data      │   API    │   request    │  alpha   │
└──────────┘             └──────────┘                └──────────┘              └────┬─────┘
                                                                                    │
                                                            store                   │
                                                     ┌──────────────────────────────┘
                                                     ▼
                                              ┌──────────┐
                                              │ Postgres │
                                              │ (v1)     │
                                              └──────────┘
```

### Flow 2: Feedback → Dataset

```
┌──────────┐   chat    ┌──────────┐   feedback   ┌──────────┐
│  Admin   │ ───────►  │ open-bbcd│ ──────────►  │ Postgres │
│   (BO)   │ ◄───────  │  (test)  │   + save     │ (dataset)│
└──────────┘  response └──────────┘              └──────────┘
```

### Flow 3: Evaluation

```
┌──────────┐  trigger   ┌──────────┐   fetch    ┌──────────┐
│ open-bbcd│ ────────►  │ aikdm │ ─────────► │ Postgres │
│   (BO)   │            │  (geval) │ ◄───────── │          │
└──────────┘            └────┬─────┘   data     └──────────┘
                             │                        ▲
                             │    store score         │
                             └────────────────────────┘
```

### Flow 4: Deployment

```
┌──────────┐   deploy   ┌──────────┐   AG-UI    ┌──────────┐
│  Admin   │ ────────►  │ open-bbcd│ ◄────────► │  Client  │
│   (BO)   │  version   │ (agent)  │            │ Frontend │
└──────────┘            └────┬─────┘            └──────────┘
                             │
                             │ MCP
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
| Agent ↔ Backend | MCP (SSE/HTTP) | Tool calls to client's backend |
| Admin ↔ open-bbcd | REST/HTTP | Backoffice API |
| aikdm ↔ open-bbcd | REST/HTTP | Job coordination |
| aikdm ↔ Postgres | SQL | Direct DB access for heavy jobs |

---

## Deployment (Current)

Single-binary deployment:

```
┌─────────────────────────────────────┐
│            open-bbcd                │
│  ┌─────────┐  ┌─────────┐          │
│  │   API   │  │  Agent  │          │
│  └─────────┘  └─────────┘          │
└─────────────────┬───────────────────┘
                  │
                  ▼
           ┌─────────────┐
           │  PostgreSQL │
           └─────────────┘
```

**Future:** Operator pattern for multi-agent deployments

## Evaluate

Once a dataset version is closed, any agent version can be evaluated
against it. Evaluation is scenario-driven: aikdm spins up a user-simulator
agent that replays the original session's user turns (with paraphrase
allowed) against the tested agent, then a judge agent scores the resulting
transcript against the acceptance criteria captured on each feedback row.

Data flow:

1. **Open-bbcd UI** — on the agent version's Versions tab, click **Evaluate**,
   pick a dataset + closed version, confirm. Creates an eval row in `PENDING`.
2. **Operator** — copies the eval id from the URL and runs
   `OPENBBCD_URL=http://localhost:8080 scripts/run_eval.sh <eval_id>`.
3. **Script** — GETs `/evals/{id}/export.yaml`, POSTs `/evals/{id}/start`
   (flips to `IN_PROGRESS`), invokes `aikdm evaluate`, POSTs the result
   JSON to `/evals/{id}/result` (or `/fail` on error).
4. **Aikdm** — for each session:
   - `simulator.py` produces the next user turn (LiteLLM via ADK).
   - `target.py` invokes the tested agent using LiteLLM's completion API
     with the bundle's tools translated to function schemas. Tool calls
     go through `tool_mock.py`, which A) replays exact matches from the
     original transcript, and B) synthesizes a plausible payload from the
     tool's `body_shape`/`response_shape` when no match is available.
   - `judge.py` scores each criterion against the completed transcript.
5. **Open-bbcd** — persists per-session results, computes the global
   pass-rate score, renders the eval detail page.

Scenario-testing inspiration: [langwatch/scenario](https://github.com/langwatch/scenario).

Score formula: **global pass-rate** = `sum(passed_criteria) / sum(total_criteria)`
across all sessions in the eval. Every criterion is worth the same;
bigger sessions carry proportionally more weight.

Agent-version detail's *Avg eval* column is a plain mean of `DONE` eval
scores for that version (not weighted by dataset size).
