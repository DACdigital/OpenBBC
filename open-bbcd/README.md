# open-bbcd

Core platform service for OpenBBC — backoffice UI, REST API, and the deployed agent runtime (AG-UI streaming over `/deployed/*`, MCP tool calls into your backend). See [`docs/PRODUCTION.md`](../docs/PRODUCTION.md) for the production integration path.

## Requirements

- Go 1.22+
- PostgreSQL 15+
- Docker Compose (for the bundled Postgres + service stack)
- goose CLI is only needed for authoring new migrations (`go install github.com/pressly/goose/v3/cmd/goose@latest`); at runtime the server embeds goose as a library and auto-applies migrations on startup.

## Quick Start

### 1. Start the stack (from the repo root)

The compose file lives at the repo root (not under `open-bbcd/`). It brings up Postgres and the open-bbcd service together:

```bash
cd /path/to/OpenBBC
docker compose up -d
```

### 2. Or run the service against your own Postgres

```bash
cd open-bbcd
cp .env.example .env
# Edit .env — DATABASE_URL is required
source .env
make run
```

Migrations are applied automatically when the server starts, so there is no separate migrate step in the happy path. Use `open-bbcd migrate` (below) if you want to apply them without booting the HTTP server.

## Binary subcommands

The `open-bbcd` binary dispatches on its first positional argument (`cmd/open-bbcd/main.go`):

| Command | Description |
|---------|-------------|
| `open-bbcd` / `open-bbcd serve` | Start the HTTP server. Loads config, connects to Postgres, applies pending migrations, then listens on `$SERVER_HOST:$SERVER_PORT`. |
| `open-bbcd migrate` | Apply pending migrations and exit. Requires `DATABASE_URL`. |
| `open-bbcd healthcheck` | Probe `http://127.0.0.1:$SERVER_PORT/health` and exit `0` on 2xx, `1` otherwise. Reads only `SERVER_PORT` — a broken `DATABASE_URL` will not fail the probe. Used by the container `HEALTHCHECK`. |

Unknown subcommands exit `2`.

## Make targets

| Target | Description |
|--------|-------------|
| `make build` | Build the binary to `bin/open-bbcd`. |
| `make run` | Run the service (`go run ./cmd/open-bbcd`). |
| `make test` | Unit + integration tests with `-race`. |
| `make test-integration` | Same as `make test` but serialized with `-p 1` (packages share the DB). Used by CI. |
| `make migrate-up` | Apply pending migrations via the goose CLI (needs `$DATABASE_URL`). |
| `make migrate-down` | Roll back the most recent migration. |
| `make migrate-status` | Show migration state. |
| `make migrate-create name=<name>` | Scaffold a new `NNN_<name>.sql` migration under `migrations/`. |

## API endpoints (overview)

The mux mixes server-rendered htmx UI with a JSON REST API. Fixed paths take precedence over `/agents/{id}`-style routes. This is a representative overview — see `internal/handler/api.go` for the full list.

### Server-rendered UI (htmx)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Redirects to `/agents/ui`. |
| GET | `/agents/ui` | Agent chain list (or version history when `?agent=<name>`). |
| GET | `/agents/new`, `/agents/new/step/{n}` | Wizard shell + per-step htmx fragments. |
| GET | `/agents/{agent_id}/configure/*` | Agent-level configuration screens. |
| GET | `/agent_versions/{version_id}/configure/*` | Version-level configuration screens. |

### Wizard

| Method | Path | Description |
|--------|------|-------------|
| POST | `/agents/wizard` | Submit wizard form + discovery zip; creates agent + initial version, stores the zip, redirects to `/agent_versions/{id}/configure`. |

### Evals

| Method | Path | Description |
|--------|------|-------------|
| POST | `/agent_versions/{version_id}/evals` | Create eval for a version. |
| GET | `/evals`, `/evals/{id}` | List / get eval. |
| GET | `/evals/{id}/export.yaml` | Export eval as YAML input. |
| POST | `/evals/{id}/start`, `/evals/{id}/result`, `/evals/{id}/fail`, `/evals/{id}/upload-result` | Lifecycle transitions. |

### Training sessions

| Method | Path | Description |
|--------|------|-------------|
| POST | `/training-sessions` | Create session (usually from the eval detail Train button, `PENDING`). |
| GET | `/training-sessions`, `/training-sessions/{id}` | List / get session (HTML). |
| GET | `/training-sessions/{id}/json`, `/training-sessions/{id}/report.json` | Session + report as JSON. |
| POST | `/training-sessions/{id}/start`, `/training-sessions/{id}/complete`, `/training-sessions/{id}/fail` | Lifecycle transitions. |

### MCP backends

| Method | Path | Description |
|--------|------|-------------|
| POST | `/mcp` | Create MCP backend. |
| GET | `/mcp`, `/mcp/{id}` | List / get. |
| POST | `/mcp/{id}` | Update. |
| POST | `/mcp/{id}/delete` | Delete. |

### Datasets

| Method | Path | Description |
|--------|------|-------------|
| POST | `/datasets` | Create draft dataset. |
| GET | `/datasets`, `/datasets/{id}` | List / get. |
| POST | `/datasets/{id}/close-draft` | Close draft (dataset becomes immutable). |

### Deployed runtime

| Method | Path | Description |
|--------|------|-------------|
| POST | `/deployed/{agent_id}/sessions` | Start a runtime session against a deployed agent. |
| GET | `/deployed/{agent_id}/sessions` | List sessions. |
| POST | `/deployed/{agent_id}/sessions/{id}/turn` | Send a turn to a session (plus related session subroutes). |

### Health

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Liveness probe. |

## Configuration

Config is env-driven (`internal/config/config.go`, `caarlos0/env` + `joho/godotenv`). `.env` is auto-loaded from the working directory if present.

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `DATABASE_URL` | — | Yes | PostgreSQL connection URL. |
| `SERVER_HOST` | `0.0.0.0` | No | Server bind host. |
| `SERVER_PORT` | `8080` | No | Server bind port. Also read by `open-bbcd healthcheck`. |
| `DISCOVERY_STORAGE_DIR` | `./data/discovery` | No | Local disk root for discovery zip blobs. In the compose file this is set to `/data/discovery` and backed by a named volume. |
| `DISCOVERY_MAX_UPLOAD_MB` | `50` | No | Max discovery zip size accepted by the wizard. |
| `ANTHROPIC_API_KEY` | — | No | Required only for `/chat/*` and related LLM-backed endpoints; other routes work without it. |
| `OPENBBC_DEFAULT_MODEL` | `claude-sonnet-4-6` | No | Default Anthropic model. |
| `OPENBBC_MAX_TOKENS` | `4096` | No | Per-response cap. |
| `OPENBBC_CHAT_TRANSPORT` | `agui` | No | Chat transport selector. |
| `OPENBBC_MAX_TOOL_ROUNDS` | `10` | No | Per-turn tool-call loop bound. |
