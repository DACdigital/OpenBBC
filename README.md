# OpenBBC

Build and deploy custom AI agents for your backend — fast.

## Problem

You have a backend with APIs. You want to expose it as an AI-powered chat interface. The challenge: passing domain knowledge to the agent so it actually understands your business logic.

## Solution

OpenBBC speeds up time to market for custom agent deployments that integrate with your existing backend via MCP protocol and can be embedded in any frontend via AG-UI.

## How It Works

```
┌──────────┐      AG-UI       ┌──────────┐       MCP        ┌──────────┐
│  Your    │ ──────────────►  │ OpenBBC  │ ──────────────►  │   Your   │
│ Frontend │                  │  Agent   │                  │ Backend  │
└──────────┘                  └──────────┘                  └──────────┘
```

1. **Discovery** — CC skill scans your repo, extracts business logic per intent
2. **Generate** — Alpha agent created with structured prompts
3. **Feedback** — Domain experts refine agent via backoffice chat
4. **Evaluate** — Score agent on datasets, iterate
5. **Deploy** — Ship agent with AG-UI support

## Components

| Component | Description |
|-----------|-------------|
| **CC Discovery Skill** | Claude Code skill that extracts business logic from your codebase |
| **open-bbcd** | Golang service: backoffice, REST API, agent runtime |
| **aicademy** | Python CLI for agent generation, training, evaluation |

## Key Features

- **Resource mapping** — Resources gathered per intent during discovery
- **MCP toolkit** — Connect to existing MCPs or wrap your REST/GraphQL APIs
- **Session proxying** — User auth passed through to backend (agent acts within user's scope)
- **Versioning** — Full version history for agents and datasets
- **Evaluation** — Score agents on dataset versions with Geval

## Tech Stack

| Component | Technology |
|-----------|------------|
| open-bbcd | Golang |
| aicademy | Python (click, Google ADK) |
| Storage | PostgreSQL |
| Client protocol | AG-UI |
| Backend protocol | MCP (SSE/Streamable HTTP) |

## Documentation

- [Design Document](docs/DESIGN.md) — Problem, flow, versioning
- [Architecture](docs/ARCHITECTURE.md) — Components, data flows, protocols

## Getting Started

Coming soon.

## Contributing

Coming soon. See [CONTRIBUTING.md](CONTRIBUTING.md) when available.

## License

Apache 2.0 — see [LICENSE](LICENSE)
