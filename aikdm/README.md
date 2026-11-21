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
| `AIKDM_MODEL_GENERATOR` | no | `claude-haiku-4-5` | generator model (`generate-agent`) |
| `AIKDM_MODEL_CRITIC` | no | `claude-haiku-4-5` | critic model (`generate-agent`) |
| `AIKDM_MODEL_TARGET` | no | `claude-haiku-4-5` | model the agent under test runs on in `evaluate` / `train-agent` (the scoring target; the back office flags it when it differs from open-bbcd's `OPENBBC_DEFAULT_MODEL`) |
| `AIKDM_MODEL_JUDGE` | no | `claude-haiku-4-5` | judge model that grades each criterion |
| `AIKDM_MODEL_USER_SIMULATOR` | no | `claude-haiku-4-5` | user-simulator model (rewords user turns in `paraphrase` mode) |
| `AIKDM_MODEL_TEACHER` | no | `claude-haiku-4-5` | teacher model that proposes prompt patches in `train-agent` |
| `AIKDM_CRITIC_ROUNDS` | no | `2` | max critic loop rounds |
| `AIKDM_LOG_LEVEL` | no | `info` | human log level |
| `AIKDM_EVAL_USER_TURN_MODE` | no | `verbatim` | eval user turns: `verbatim` sends them as recorded; `paraphrase` has the simulator LLM reword them |
| `AIKDM_EVAL_HISTORY_MODE` | no | `simulated` | eval history: `simulated` shows the target its own earlier replies; `reference` shows the recorded reference replies (requires `verbatim`) |

`load_settings` needs a provider key for all six `AIKDM_MODEL_*` models, so when you switch provider, switch all six.

## Eval and training modes

`evaluate` and `train-agent` score agents with the same eval runner, so the two
`AIKDM_EVAL_*` settings above apply to both. Training has no separate modes.

How a session is replayed:
- The recorded session is compiled into a fixed script. N recorded user turns always produce
  N simulated turns.
- Each criterion is judged only on the reply to the turn it was written for. The judge runs
  at temperature 0.

**User turns** (`AIKDM_EVAL_USER_TURN_MODE`): what the "user" sends on turn k.
- `verbatim` (default): the recorded user message, unchanged. No simulator call, so it is the
  cheapest and most repeatable.
- `paraphrase`: the simulator LLM rewords the recorded message to fit the agent's last reply.
  It changes wording only, never which turn is sent, and falls back to the recorded text if
  it fails.

**History** (`AIKDM_EVAL_HISTORY_MODE`): what the agent sees as turns 1…k-1.
- `simulated` (default): its own earlier replies from this run. Tests multi-turn coherence.
- `reference`: the recorded replies from the original session; only reply k is generated.
  Each turn is tested in isolation, so an early mistake can't snowball. Least noisy, but
  multi-turn coherence goes untested.

| User turns | History | Use for |
|---|---|---|
| `verbatim` | `simulated` | Default. Realistic full-conversation test. |
| `verbatim` | `reference` | Most stable score; comparing prompt versions turn by turn. |
| `paraphrase` | `simulated` | Datasets whose user turns only make sense as answers to the agent ("yes, the second one"). |
| `paraphrase` | `reference` | Rejected at startup (`ConfigError`). |

Rules of thumb:
- **Compare scores only within one combination.** Each eval records it in
  `aikdm_meta.user_turn_mode` / `aikdm_meta.history_mode`, each training report in
  `models.user_turn_mode` / `models.history_mode`. Keep the same combination for a
  whole training run and for any eval you compare against it.
- **Set the variables where aikdm actually runs.** Evals and trainings queued from the back
  office are run by the aikdm runner, not by open-bbcd: the Helm `evals` / `trainings`
  CronJobs (set `aikdmRunner.extraEnv`), or the `aikdm` docker-compose service (export the
  variables in the shell that runs `docker compose`). open-bbcd's own environment has no
  effect on them. Each eval result records the models and modes in `aikdm_meta`; each
  training report records them in `models`.
- **Inconclusive judgments** (`inconclusive: true`: target crash, turn not reached, judge
  error) count as failed in the score, but the training teacher ignores them, so it doesn't
  patch prompts to fix infrastructure errors.
- **Expect some noise.** On a 10-session dataset with Haiku, repeated evals of the same
  version still spread by about 6–9 points (lowest with `reference`). `train-agent` promotes
  only on a strictly higher score, so an improvement smaller than that can go unpromoted.
  Use `reference` when you need the most stable signal.

## Evaluating MCP-backed agents

When the evaluated agent version has MCP backends attached (`mcp_client` kind), open-bbcd's
`GET /evals/{id}/export.yaml` lists their tools and writes them under `mcp_backends`. The
model is offered the same `<backend>__<tool>` names, descriptions and input schemas as a
deployed turn. `train-agent` uses the same export, so the baseline and every candidate get
the tools too.

- **Mock mode** (`mock_mcp_tools: true`): no MCP server is contacted during the run. A call
  that matches a recorded call in the reference transcript (same name and arguments) returns
  the recorded result. Any other call returns a result synthesized from the tool's
  `output_schema`, or a generic success stub when the server declared none. The export
  carries no URL or headers in this mode.
- **Real mode** (`mock_mcp_tools: false`): `<backend>__*` calls go to the live server over
  streamable HTTP, with the exported headers. There is one connection per backend per
  simulated session, opened on the first call and closed when the session ends. A failed
  connect is sticky for that session. The model sees the same JSON as at runtime:
  `{"content": [...], "text": "..."}`, or `{"error": "..."}` for transport errors. HTTP
  endpoint tools are unchanged.
- **Identity headers.** Set them per backend in the BO "Evaluate" modal (or
  `mcp_header_overrides` on `POST /agent_versions/{id}/evals`). They override the backend's
  `default_headers` per header and are sent both when the export lists tools (both modes)
  and on every real-mode call. Use a dedicated, long-lived test identity, not a real user's
  credentials: values are stored on the eval and written into the export in plain text.
- **Discovery must succeed.** If any attached backend can't be reached when the export is
  built, the export answers 502 (`mcp_discovery_failed`), a pending eval is marked FAILED,
  and a training session fails instead of staying PENDING. Tune the per-backend timeout with
  `OPENBBC_EVAL_MCP_DISCOVERY_TIMEOUT` on open-bbcd (default `15s`).
- **Point real-mode evals at a seeded or test backend.** Side-effecting tools really
  execute, and results depend on the server's data.

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
