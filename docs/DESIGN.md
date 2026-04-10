# OpenBBC Design Document

## Problem Statement

You want to expose your backend capabilities as an app. The challenge is the **agent part** which utilizes that app - domain knowledge needs to be passed to the agent.

**Our solution** speeds up time to market for custom agent deployments that can be easily integrated on the frontend as a chat interface.

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
                                  │  + Backoffice REST API               │
                                  │  + Agent versioning                  │
                                  │  + Dataset storage                   │
                                  └──────────────────────────────────────┘
                                                   ▲
                                                   │
                        ┌──────────────────────────┼──────────────────────────┐
                        │                          │                          │
                        ▼                          ▼                          ▼
               ┌─────────────┐            ┌─────────────┐            ┌─────────────┐
               │ CC Discovery│            │  aicademy   │            │  PostgreSQL │
               │   Skill     │            │   (CLI)     │            │  (Storage)  │
               │             │            │             │            │             │
               │ scans repo  │            │ agent gen   │            │ agents      │
               │ extracts    │            │ training    │            │ datasets    │
               │ business    │            │ geval       │            │ versions    │
               │ logic       │            │             │            │ scores      │
               └─────────────┘            └─────────────┘            └─────────────┘
```

### Components

| Component | Description |
|-----------|-------------|
| **Client (Frontend)** | UI that communicates with deployed agent via AG-UI protocol |
| **open-bbcd** | Main Golang service: hosts deployed agent, backoffice REST API, agent/dataset versioning |
| **AI Agent** | Runs inside open-bbcd; processes requests and calls backend tools via MCP |
| **Backend (MCP wrapped)** | Client's existing backend exposed via MCP protocol |
| **CC Discovery Skill** | Claude Code skill that scans client repo to extract business logic/domain/processes |
| **aicademy** | Python CLI for agent generation, training, evaluation (async jobs) |
| **PostgreSQL** | Storage for agents, datasets, versions, and evaluation scores |

## Flow

### Phase I: Agent Context Profiling (CC Discovery)

1. Run **CC Discovery skill** on the target repository
2. Skill scans repo and checks where backend endpoints are used
3. Extracts business logic, domain, processes, capabilities
4. Output: **structured data** for alpha agent creation (standardized input)

**Owner:** CC Discovery Skill

### Phase II: Alpha Agent Generation

0. Structured profile from Phase I is input
1. Domain experts configure via **open-bbcd backoffice**
2. Define scope of agent + guardrails (what agent cannot do)
3. **aicademy** generates alpha agent with structured prompt
4. Alpha agent stored as **version 1** in PostgreSQL

**Owner:** aicademy CLI + open-bbcd

### Phase III: Feedback & Dataset Creation

1. Admin runs agent from selected version in **open-bbcd backoffice chat**
2. Interact with agent, create scenarios
3. Provide feedback: comments + expected output + judge criteria
4. Feedback on resources (should/should not use)
5. Save feedback session → creates **master dataset**

**Owner:** open-bbcd Backoffice

```
Dataset Structure:

┌─────────┐   ┌─────────┐   ┌─────────┐
│ Intent  │   │ Intent  │   │ Intent  │
└────┬────┘   └────┬────┘   └────┬────┘
     │             │             │
     ▼             ▼             ▼
┌─────────┐   ┌─────────┐   ┌─────────┐
│ Chat    │   │ Chat    │   │ Chat    │
│ Session │   │ Session │   │ Session │
│ +feedback│  │ +feedback│  │ +feedback│
└─────────┘   └─────────┘   └─────────┘
```

### Phase IV: Evaluation & Iteration

1. Run **Geval** in aicademy on (agent version, dataset version) pair
2. Store score in PostgreSQL via open-bbcd
3. Iterate: refine agent → new version → evaluate again

**Owner:** aicademy (Geval) + open-bbcd (storage)

**Human-in-the-loop:**
```
Agent Version + Dataset Version
              │
              ▼
       ┌─────────────┐
       │ Run Chat    │
       │ Session     │
       └──────┬──────┘
              │
              ▼
       ┌─────────────┐      ┌─────────────┐
       │   Human     │─────►│ Agent Judge │
       │ Feedback    │      │ (Geval)     │
       └─────────────┘      └──────┬──────┘
                                   │
                                   ▼
                            ┌─────────────┐
                            │ Store Score │
                            │ (Postgres)  │
                            └─────────────┘
```

**Automated RL Iterations:**
```
Epochs: N RL iterations
           │
           ▼
    ┌─────────────┐
    │ Run Chat    │
    │ Session     │◄──────────────┐
    │ (AI Tester) │               │
    └──────┬──────┘               │
           │                      │
           ▼                      │
    ┌─────────────┐               │
    │ aicademy    │───────────────┘
    │ (Geval)     │    feedback loop
    └──────┬──────┘
           │
           ▼
    Store scores + prompt config
    in PostgreSQL
```

### Phase V: Deployment

1. Select tested agent version
2. Deploy via **open-bbcd** (runs inside open-bbcd binary for now)
3. Exposed via REST API with **AG-UI protocol** support
4. Client frontend connects and interacts

**Owner:** open-bbcd

## Resources

Resources are backend capabilities (endpoints, tools) that the agent can use to fulfill user intents.

### Discovery

- Resources are **gathered per intent/process** during the CC Discovery phase
- Each resource maps to a specific capability the agent needs

### MCP Toolkit

open-bbcd connects to resources via its **own MCP toolkit**:

- Resources have their own **prompts** (like MCP tool descriptions) used during training
- This allows fine-tuning how the agent understands and uses each resource

### Resource Sources

| Source | Description |
|--------|-------------|
| **Existing MCP servers** | Use client's already-wrapped MCP endpoints directly |
| **Custom MCP wrappers** | Create MCP wrappers over existing APIs (REST, GraphQL, etc.) |

### Session Proxying

- User session is **passed/proxied** from chat through to client's backend
- Enables authenticated calls to backend resources on behalf of the user
- Agent acts within user's permission scope

```
┌──────────┐   AG-UI    ┌──────────┐   MCP + session   ┌──────────┐
│  Client  │ ─────────► │ open-bbcd│ ────────────────► │ Backend  │
│ (session)│            │  (agent) │    (proxied)      │ Resource │
└──────────┘            └──────────┘                   └──────────┘
```

## Versioning

| Entity | Versioning |
|--------|------------|
| **Agent** | Each saved change → new version. Cannot delete versions. |
| **Dataset** | Master dataset versioned. Editable. All versions visible. |
| **Score** | Tied to (agent version, dataset version) pair |

## Open Source Scope

The **open-bbcd** service (including hosted agent) is open source.

The wrapped MCP backend remains external/proprietary to users.

## Tech Stack

| Component | Technology |
|-----------|------------|
| open-bbcd | Golang |
| aicademy | Python (click, Google ADK) |
| Storage | PostgreSQL |
| Protocol (client) | AG-UI |
| Protocol (backend) | MCP (SSE/Streamable HTTP) |

## Out of Scope (for now)

- Agent operator / separate deployment
- Multi-agent system
- Multiple concurrent deployments
- Resource/integration registration in CC Discovery
