# OpenBBC

Turn a backend + a frontend repo into a deployable AI agent.

## What is this?

OpenBBC is a monorepo for a platform that generates, evaluates, trains, and runs custom AI agents grounded in your backend's business logic. The pipeline starts from a target frontend repo — a Claude Code discovery skill compiles it into a structured wiki — and ends with a versioned agent bundle served over AG-UI to any client you point at it.

The repo is split into three independent components, each with its own README:

- [`aikdm/`](./aikdm/README.md) — Python CLI that generates prompt bundles, evaluates them against datasets, and hill-climbs them with a teacher/judge loop.
- [`open-bbcd/`](./open-bbcd/README.md) — Go service: backoffice UI, REST API for agents / evals / training sessions / datasets / MCP backends / deployed runtime, PostgreSQL persistence.
- [`bbc-discovery/`](./bbc-discovery/README.md) — Claude Code plugin marketplace, currently shipping the `flow-map-compiler` skill that produces the `.flow-map/` wiki `aikdm` consumes.

The three parts talk over files and HTTP, not shared libraries — you can run `aikdm` and the discovery skill standalone against any backend, and `open-bbcd` orchestrates them via subprocess + REST when you use the full platform.

## How it fits together

```
  target frontend repo
        │
        │  Claude Code skill (bbc-discovery/flow-map-compiler)
        ▼
  .flow-map/  (flows, capabilities, proposed MCP tools)
        │
        │  wizard upload + text fields
        ▼
  ┌─────────────────────────────────────────────────────────────┐
  │  open-bbcd  (Go, :8080)                                     │
  │  ─ backoffice UI (htmx)                                     │
  │  ─ REST: /agents /evals /training-sessions /datasets /mcp   │
  │  ─ PostgreSQL: versioned agents, evals, datasets, sessions  │
  │  ─ deployed agent runtime: /deployed/{agent}/sessions ...   │
  │    (AG-UI streaming, MCP-mediated backend calls)            │
  └─────────────────────────────────────────────────────────────┘
        │                    │                     │
        │ generate-agent     │ evaluate            │ train-agent
        │ (FlowMapConfig)    │ (eval-input.yaml)   │ (session)
        ▼                    ▼                     ▼
  ┌─────────────────────────────────────────────────────────────┐
  │  aikdm  (Python CLI, out-of-process)                        │
  │  ─ generate-agent : two-agent loop → prompt bundle          │
  │  ─ evaluate       : score bundle against dataset (judge)    │
  │  ─ train-agent    : N-epoch teacher/judge hill-climb        │
  └─────────────────────────────────────────────────────────────┘
```

Four glue scripts drive the round-trip through open-bbcd:

- `scripts/run_eval.sh <eval_id>` — fetches `eval-input.yaml`, runs `aikdm evaluate`, uploads the result.
- `scripts/train_from_session.sh <session_id>` — picks up a PENDING training session, runs `aikdm train-agent`, and drives it to DONE (or FAILED).
- `scripts/process_pending_evals.sh` — batch drain of all PENDING evals. `flock`-protected, serial, continue-on-error. Suitable for cron.
- `scripts/process_pending_trainings.sh` — same, for PENDING training sessions.

## Components

| Path | What it is | Language |
|---|---|---|
| [`aikdm/`](./aikdm/) | Python CLI: generate / evaluate / train agent prompt bundles. Out-of-process, open-bbcd-unaware. | Python 3.12+ |
| [`open-bbcd/`](./open-bbcd/) | Core service: backoffice UI, REST API, deployed runtime. The only long-running binary in the repo. | Go 1.22+ |
| [`bbc-discovery/`](./bbc-discovery/) | Claude Code plugin marketplace. Ships `flow-map-compiler` — pure markdown + SKILL.md, no build. | Markdown / SKILL.md |

## Tech stack

| Component | Stack |
|---|---|
| `open-bbcd` | Go 1.22+, PostgreSQL 15+, htmx, goose (embedded as a Go library), `lib/pq`, distroless base image |
| `aikdm` | Python 3.12+, `uv`, Google ADK, LiteLLM, Click, Pydantic |
| `bbc-discovery` | Markdown + `SKILL.md` (Claude Code plugin, no build) |

## Getting started

```bash
git clone <repo>
cd OpenBBC
cp open-bbcd/.env.example open-bbcd/.env
# Edit open-bbcd/.env — set ANTHROPIC_API_KEY (and/or OPENAI_API_KEY / GEMINI_API_KEY).
# DATABASE_URL is already correct for the bundled Postgres.

docker compose up -d
# → Postgres + open-bbcd start. open-bbcd embeds goose and auto-applies migrations,
#   then serves on :8080. The container HEALTHCHECK probes /health.
```

Then:

- Backoffice: <http://localhost:8080/agents/ui>
- Run `aikdm` ad-hoc against a mounted config:
  ```bash
  docker compose --profile aikdm run --rm aikdm --help
  ```
  The `aikdm` service is in a compose profile so it doesn't run as a daemon — it's a one-shot CLI. Files land in `./aikdm-work/` (mounted at `/work`).
- Drive an eval end-to-end (from repo root):
  ```bash
  OPENBBCD_URL=http://localhost:8080 scripts/run_eval.sh <eval_id>
  ```
- Drive a training session (create the PENDING row via the Train button on an eval detail page, then):
  ```bash
  OPENBBCD_URL=http://localhost:8080 scripts/train_from_session.sh <session_id>
  ```
- Cron-friendly batch drains (see [PRODUCTION.md](docs/PRODUCTION.md) for suggested schedules):
  ```bash
  OPENBBCD_URL=http://localhost:8080 scripts/process_pending_evals.sh
  OPENBBCD_URL=http://localhost:8080 scripts/process_pending_trainings.sh
  ```

Per-component quickstarts (running each service outside compose, running tests locally, etc.) live in the component READMEs. Deploying openbbc into your own infra and wiring your frontend to it lives in [`docs/PRODUCTION.md`](./docs/PRODUCTION.md).

## Deployment

- Both images are built multi-arch (`linux/amd64`, `linux/arm64`) via BuildKit.
- `open-bbcd` ships on `gcr.io/distroless/static-debian12:nonroot` (~25 MB); `aikdm` ships on `python:3.12-slim` (~275 MB).
- `open-bbcd` auto-applies migrations on startup. Two subcommands ship in the same binary:
  - `open-bbcd migrate` — run migrations and exit (useful for pre-deploy jobs).
  - `open-bbcd healthcheck` — probe `http://127.0.0.1:$SERVER_PORT/health`, exit 0/1. Reads `SERVER_PORT` only, so a broken `DATABASE_URL` doesn't fail the container `HEALTHCHECK`.
- `docker-compose.yml` at the repo root is single-instance. Multi-replica k8s / Helm charts are future work — see the Roadmap.

## Development

- Tests live next to the code in each component. From each subdirectory:
  - `cd open-bbcd && make test` (Go, `-race`) or `make test-integration` (`-p 1`, what CI runs).
  - `cd aikdm && make test` (Python, LLM mocked). `RUN_SMOKE=1 make test-smoke` for real-LLM tests.
- CI: `.github/workflows/ci.yml` runs on PRs to `main`. Two parallel jobs (`test-go`, `test-python`), gated by a `check` job that only passes when both are green.
- Per-component dev instructions: [`open-bbcd/README.md`](./open-bbcd/README.md) · [`aikdm/README.md`](./aikdm/README.md) · [`bbc-discovery/README.md`](./bbc-discovery/README.md).

## Documentation

- [`docs/DESIGN.md`](./docs/DESIGN.md) — product design and the phase model (discovery → generate → feedback → evaluate → train → deploy).
- [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) — technical architecture, subsystem deep-dives, data model.
- [`docs/PRODUCTION.md`](./docs/PRODUCTION.md) — deploying openbbc as your internal service, integrating your frontend over AG-UI, header pass-through, MCP layer, auth model.
- [`CLAUDE.md`](./CLAUDE.md) — Claude Code project instructions (repo layout, conventions, cross-component contracts).

## Roadmap

- Registry publish to GHCR with versioned image tags.
- Helm chart + multi-replica k8s deployment path.
- Multi-replica-safe migrations (goose `Provider` with `SessionLocker`).
- OCI `LABEL` metadata (source, revision, created) injected via CI build args.
- SHA-pinned base images for reproducible builds.
- BuildKit cache mounts for faster CI rebuilds.

## License

Apache 2.0 — see [LICENSE](./LICENSE).
