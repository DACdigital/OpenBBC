# OpenBBC

Turn a backend + a frontend repo into a deployable AI agent.

## What is this?

OpenBBC is a monorepo for a platform that generates, evaluates, trains, and runs custom AI agents grounded in your backend's business logic. The pipeline starts from a target frontend repo — a Claude Code discovery skill compiles it into a structured wiki — and ends with a versioned agent bundle served over AG-UI to any client you point at it.

The repo is split into three independent components:

| Path | What it is | Language |
|---|---|---|
| [`aikdm/`](./aikdm/) | Python CLI: generate / evaluate / train agent prompt bundles. Out-of-process, `open-bbcd`-unaware. | Python 3.12+ |
| [`open-bbcd/`](./open-bbcd/) | Core service: backoffice UI, REST API, deployed agent runtime. The only long-running binary in the repo. | Go 1.22+ |
| [`bbc-discovery/`](./bbc-discovery/) | Claude Code plugin marketplace. Ships `flow-map-compiler` — pure markdown + SKILL.md, no build. | Markdown / SKILL.md |

The three parts talk over files and HTTP, not shared libraries — you can run `aikdm` and the discovery skill standalone against any backend, and `open-bbcd` orchestrates them via subprocess + REST when you use the full platform.

## Getting started (local dev)

```bash
git clone git@github.com:DACdigital/OpenBBC.git
cd OpenBBC
cp open-bbcd/.env.example open-bbcd/.env
# Edit open-bbcd/.env — set ANTHROPIC_API_KEY (and/or OPENAI_API_KEY / GEMINI_API_KEY).
# DATABASE_URL is already correct for the bundled Postgres.

docker compose up -d
# → Postgres + open-bbcd. open-bbcd embeds goose and auto-applies migrations, then serves on :8080.
```

- Backoffice: <http://localhost:8080/agents/ui>
- One-shot `aikdm`: `docker compose --profile aikdm run --rm aikdm --help` (files land in `./aikdm-work/`).
- Per-component quickstarts (running each service outside compose, running tests) live in the component READMEs.

Deployment recipes — compose, Helm on k8s, integrating your frontend over AG-UI, MCP layer wiring, header overrides, batch cron drains, provider keys — live in [`docs/PRODUCTION.md`](./docs/PRODUCTION.md).

## Development

- Tests live next to the code in each component. From each subdirectory:
  - `cd open-bbcd && make test` (Go, `-race`) or `make test-integration` (`-p 1`, what CI runs).
  - `cd aikdm && make test` (Python, LLM mocked). `RUN_SMOKE=1 make test-smoke` for real-LLM tests.
- CI: `.github/workflows/ci.yml` runs on PRs to `main`. Two parallel jobs (`test-go`, `test-python`), gated by a `check` job that only passes when both are green.
- Images: `.github/workflows/publish-images.yml` builds and pushes `open-bbcd`, `aikdm-runner`, and `aikdm` to `ghcr.io/dacdigital/openbbc/*` on every PR (same-repo only), merge to `main`, and `v*` tag.
- Per-component dev instructions: [`open-bbcd/README.md`](./open-bbcd/README.md) · [`aikdm/README.md`](./aikdm/README.md) · [`bbc-discovery/README.md`](./bbc-discovery/README.md).

## Documentation

- Architecture, design, NFRs, conventions, and the domain model live in the docs repo:
  **[DACdigital/openbbc-docs](https://github.com/DACdigital/openbbc-docs)**. Start there for any question about *why* something is shaped the way it is.
- Operator-facing recipes (deploy, integrate, cron) stay in this repo: [`docs/PRODUCTION.md`](./docs/PRODUCTION.md).
- Coding-agent rules: [`AGENTS.md`](./AGENTS.md) (cross-tool) + [`CLAUDE.md`](./CLAUDE.md) (Claude Code workflow).

## License

Apache 2.0 — see [LICENSE](./LICENSE).
