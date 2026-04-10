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
                      │   aicademy    │ ◄─────────────────────────────│  PostgreSQL   │    │    Client     │
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

### 3. aicademy

**Type:** Python CLI
**Purpose:** Agent generation, training, evaluation (async jobs)

**Tech Stack:**
- Python
- click (CLI framework)
- Google ADK (agent framework)

#### Capabilities

| Command | Description |
|---------|-------------|
| Alpha Generator | Generate alpha agent from discovery output |
| Geval | Evaluate agent on dataset |
| Training | RL-based agent improvement (future) |

#### Prompt Output Format

Supports both formats:

| Format | Description |
|--------|-------------|
| **Regular** | Plain markdown prompt |
| **Structural** | Grouped by category (guardrails, personality, resources, etc.) |

---

### 4. PostgreSQL

**Type:** Relational database
**Purpose:** Persistent storage for all platform data

**Stores:**
- Agents (with versions)
- Datasets (with versions)
- Resources (with prompts)
- Evaluation scores

---

## Resources

Resources are backend capabilities (endpoints, tools) that the agent uses to fulfill user requests.

### Discovery & Mapping

Resources are **gathered per intent/process** during the CC Discovery phase:

```
┌──────────────┐
│   Intent A   │──► Resource 1, Resource 2
├──────────────┤
│   Intent B   │──► Resource 2, Resource 3
├──────────────┤
│   Intent C   │──► Resource 1, Resource 4
└──────────────┘
```

### MCP Toolkit

open-bbcd includes its **own MCP toolkit** for resource connectivity:

- Each resource has its own **prompt/description** (similar to MCP tool descriptions)
- Prompts are used during training to teach agent how to use each resource
- Enables fine-grained control over agent's understanding of resources

```
┌─────────────────────────────────────────────────┐
│                   open-bbcd                     │
│  ┌───────────────────────────────────────────┐  │
│  │              MCP Toolkit                  │  │
│  │  ┌─────────┐ ┌─────────┐ ┌─────────┐     │  │
│  │  │Resource │ │Resource │ │Resource │     │  │
│  │  │+ prompt │ │+ prompt │ │+ prompt │     │  │
│  │  └────┬────┘ └────┬────┘ └────┬────┘     │  │
│  └───────┼───────────┼───────────┼──────────┘  │
└──────────┼───────────┼───────────┼─────────────┘
           │           │           │
           ▼           ▼           ▼
      ┌─────────┐ ┌─────────┐ ┌─────────┐
      │Existing │ │ Custom  │ │Existing │
      │  MCP    │ │MCP wrap │ │  MCP    │
      └─────────┘ └─────────┘ └─────────┘
```

### Resource Sources

| Source | Description |
|--------|-------------|
| **Existing MCP servers** | Connect directly to client's already-wrapped MCP endpoints |
| **Custom MCP wrappers** | Create MCP wrappers over existing REST/GraphQL/other APIs |

### Session Proxying

User session is **passed/proxied** through the entire chain:

```
┌──────────┐        ┌──────────┐        ┌──────────┐
│  Client  │ ─────► │ open-bbcd│ ─────► │ Backend  │
│          │ AG-UI  │  (agent) │  MCP   │ Resource │
│ session  │        │          │        │          │
│   token  │ ─────► │  proxy   │ ─────► │ auth     │
└──────────┘        └──────────┘        └──────────┘
```

- Agent acts within user's permission scope
- Backend resources receive authenticated requests
- No privilege escalation - agent can only do what user can do

---

## Data Flow

### Flow 1: Discovery → Alpha Agent

```
┌──────────┐    scan     ┌──────────┐   structured   ┌──────────┐   generate   ┌──────────┐
│  Client  │ ─────────►  │    CC    │ ────────────►  │ open-bbcd│ ──────────►  │ aicademy │
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
│ open-bbcd│ ────────►  │ aicademy │ ─────────► │ Postgres │
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
| aicademy ↔ open-bbcd | REST/HTTP | Job coordination |
| aicademy ↔ Postgres | SQL | Direct DB access for heavy jobs |

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
