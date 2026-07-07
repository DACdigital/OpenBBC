# aikdm

Prompt-generation, evaluation, and training toolkit for OpenBBC agents. Ships three subcommands: `generate-agent` (initial alpha prompt from a FlowMapConfig), `evaluate` (score an agent version against a dataset), and `train-agent` (N-epoch teacher/judge hill-climb).

## What it does

Takes a `FlowMapConfig` YAML (the output of open-bbcd's configurator UI) and produces a structured prompt bundle for use as a deployed agent's system prompt.

## Install

```
cd aikdm
uv sync --all-extras
```

## Run

```
ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY \
  uv run aikdm generate-agent \
    --config /path/to/flow-map-config.yaml \
    --output /path/to/bundle.yaml
```

Output is YAML with `metadata`, `main_prompt`, `capabilities[]`, `skills[]`, and `external_actions[]`. Omit `--output` to stream to stdout. Progress events go to stderr as NDJSON.

## Environment variables

| Var | Required | Default | Purpose |
|---|---|---|---|
| `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `GEMINI_API_KEY` | one per provider used | — | provider credential |
| `AIKDM_MODEL_GENERATOR` | no | `claude-opus-4-7` | generator model |
| `AIKDM_MODEL_CRITIC` | no | `claude-opus-4-7` | critic model |
| `AIKDM_CRITIC_ROUNDS` | no | `2` | max critic loop rounds |
| `AIKDM_LOG_LEVEL` | no | `info` | human log level |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | unexpected internal error |
| 2 | input or config error (bad YAML, missing API key, schema violation) |
| 3 | LLM / API error (timeout, invalid structured output after retry) |

## Tests

```
make test        # unit + integration, all LLM-mocked
make test-smoke  # real LLM, requires RUN_SMOKE=1 + API key
```

## Schemas

- `schemas/prompt-v1.yaml` — output contract (section/tag layout for main + skill prompts)

The input contract (`FlowMapConfig`) is currently enforced by the Pydantic models in `aikdm/schemas.py`. A `schemas/flow-map-config-v1.yaml` is the planned future externalization of that contract.

Both schemas are versioned. Adding a section or field is a YAML edit + version bump.

## Architecture

See `docs/superpowers/specs/2026-05-27-aikdm-alpha-agent-generation-design.md` (local artifact, not in repo).
