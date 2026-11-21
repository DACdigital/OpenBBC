# E2E harness: fake MCP + scripted LLM, full stack in compose

**Repo:** DACdigital/OpenBBC. **Lands as:** `e2e/README.md` (this design) + `e2e/` suite.
**Change level:** C0. Test-only: it changes no API, DB, event or contract, and needs no spec in
openbbc-docs.
**Base:** #66 merges first, then #67 is rebased onto `main`, and the #66 ↔ #67 aikdm conflict
(`eval/target.py` and `runner.py`: `Skill` tool vs MCP router) is resolved and reviewed **inside
#67**. Branch `test/e2e-harness` is cut from the rebased #67 and stays test-only. It retargets to
`main` once #67 merges.

## Why

No test today exercises the whole chain:

    daemon → export → aikdm → result upload → training

Instead:
- fake MCP servers exist only inside single Go or Python tests;
- the drainer scripts and the real binary's configuration are never run together;
- the two features just built (MCP tools in evals and training, #67; the `Skill` tool from version
  prompts, #66) are each verified only on their own side.

We want one suite that proves, on every PR, that:
- BO chats, deployed turns, evals and training all reach an authenticated MCP server with the
  right identity;
- the `Skill` tool works on every path.

## Goals

- A deterministic, key-free suite, runnable locally (`make e2e`) and as a CI job.
- It drives the **real** `open-bbcd` binary and the **real** `scripts/run_eval.sh` and
  `scripts/train_from_session.sh`, over the real container network.
- It asserts, at the MCP server, exactly which auth and identity headers arrived on each path.

## Non-goals

- Testing real model quality. The LLM is scripted. A manual real-model smoke test can come later.
- Multi-agent versions (their eval and training export is 409 by design).
- The BO UI or browser rendering. The suite drives REST and form endpoints, not HTML.
- Load or performance testing.

## Anti-goals

- **Flakiness.** No sleeps-as-sync, no wall-clock-dependent assertions, no shared state between
  scenarios that depends on order.
- **Faking open-bbcd internals.** If a scenario only passes because a Go fake stands in for real
  wiring, it defeats the purpose.
- **Touching developer stacks.** The suite uses its own compose project name and ports, and never
  connects to other local databases or containers.

## Approaches considered

- **A. Full stack in docker compose, driven by pytest — chosen.** Real binary, real scripts, real
  network. It is slower (minutes) and has more moving parts, but it is the only option that catches
  wiring and configuration bugs.
- **B. Go in-process harness** with aikdm as a subprocess. Fast and debuggable, but it skips the
  binary's configuration and the drainer scripts.
- **C. Fakes in compose, daemon on the host via `go run`.** Quicker locally, but it diverges from CI
  and deployment, so it would mean two setups.

**LLM:** a scripted fake (deterministic, CI-able) was chosen over a real LLM.

## Design

### Topology (`e2e/compose.yml`, project name `openbbc-e2e`)

| Service | Image | Purpose |
|---|---|---|
| `postgres` | postgres:16-alpine | a fresh DB per run; migrations applied by the runner via goose |
| `open-bbcd` | built from repo `open-bbcd/Dockerfile` | the real daemon. Env: `OPENBBC_LLM_ADAPTER=bifrost`, `OPENBBC_DEFAULT_MODEL=openai/fake-agent`, `OPENAI_BASE_URL=http://fake-llm:8000/v1`, `OPENAI_API_KEY=e2e` |
| `fake-mcp` | `e2e/fakes` image | the authenticated MCP server |
| `fake-llm` | `e2e/fakes` image | the scripted OpenAI-compatible LLM |
| `runner` | built from `Dockerfile.aikdm-runner` | runs `run_eval.sh` and `train_from_session.sh` exactly as the drainer does. Env: `OPENBBCD_URL=http://open-bbcd:8080`, `AIKDM_MODEL_TARGET=openai/fake-target`, `AIKDM_MODEL_JUDGE=openai/fake-judge`, `AIKDM_MODEL_USER_SIMULATOR=openai/fake-sim`, `AIKDM_MODEL_TEACHER=openai/fake-teacher`, plus LiteLLM's OpenAI base URL set to fake-llm |

- The pytest driver runs on the host (or in the CI job) against the published ports. It starts
  runner jobs with `docker compose run --rm runner scripts/run_eval.sh <id>`.
- Host ports are fixed but unusual (e.g. open-bbcd 18080, fake-llm 18001, fake-mcp 18002), and can
  be overridden by env.

### `fake-mcp` (Python, `mcp` SDK, streamable HTTP at `/mcp`)

Auth is checked on **every** HTTP request, including initialize and `tools/list`:
- `Authorization: Bearer e2e-static-key` is required. Missing or wrong → HTTP 401. This stands for
  the server-to-server key kept in the backend's `default_headers`.
- `X-Test-Identity` is optional at the HTTP layer but required by every tool. If it is missing,
  the tool returns `isError: true` with `{"error": "missing identity"}`.

Tools:
- `search(query: str)` → text `"results for <query> as <identity>"`. It declares an `outputSchema`,
  so mocked runs can synthesize from it.
- `get_project(id: str)` → structured JSON echoing `id` and `identity`.

Control API (plain HTTP on the same app, for tests only):
- `GET /_calls` → every request seen: method, tool name, args, and all headers.
- `POST /_reset` → clears that list.
- `POST /_mode {"down": true}` → every request gets 503, for the discovery-failure scenario.

### `fake-llm` (Python, FastAPI, `POST /v1/chat/completions`)

- **Role is chosen by the request's `model` field:** `fake-agent` (open-bbcd), `fake-target`,
  `fake-sim`, `fake-judge`, `fake-teacher`.
- **Scripts:**
  - Each role has a FIFO queue of canned replies, loaded by the test through
    `POST /_script {role: [reply, …]}`.
  - A reply is either a text message or a list of tool calls (name + args).
  - A tool-call reply's arguments may say `{"$from_offered": "Skill"}`. The fake checks that tool
    was offered and fails loudly if it wasn't.
- **Empty queue:**
  - `fake-sim` and `fake-judge` fall back to a deterministic default: the simulator replays the
    reference user turn verbatim, and the judge passes every criterion with the reason "e2e".
  - The other roles return HTTP 500 with "script exhausted", which fails the scenario loudly.
- **Request log:** `GET /_requests?role=` returns every request: messages, offered tools, and
  model.
- **Reset:** `POST /_reset`.
- **Streaming:** responses use streaming when the client asks for it (open-bbcd streams to AG-UI),
  so both modes are implemented.

### Seeding (pytest fixtures)

- Create the agent and its version through the minimal existing REST/DB path, then land
  `e2e/fixtures/bundle.yaml` with `scripts/seed_bundle.py`. The wizard is not used, because it
  needs a real generator LLM.
- The bundle has:
  - `main_prompt`, including a `skills_index`;
  - skills `draft-section` (with a prompt) and `empty-skill` (with an empty prompt);
  - no HTTP endpoint tools.
- Register the `fake-mcp` backend as `tool_backends.kind = mcp_client`, named `kb`, with
  `url = http://fake-mcp:8000/mcp` and `default_headers = {Authorization: Bearer e2e-static-key}`.
  Attach it to the version through `agent_version_mcp_backend`, using the existing REST
  endpoints.
- Create a dataset with one CLOSED version holding one reference session. It has 2 user turns and
  a recorded `kb__search` call, for the mock-mode replay.
- Deploy the version through the existing REST endpoint for scenario 2.

### Scenarios (`e2e/tests/`)

Each test resets both fakes first and loads its own scripts.

1. **BO chat with session header override.**
   - Set the overrides `{kb: {X-Test-Identity: bo-user}}` on the chat session, then send a message.
   - The agent script is: call `kb__search` → call `Skill(draft-section)` → final text.
   - Assert:
     - `fake-mcp` saw `tools/call search` carrying both the Bearer and `X-Test-Identity: bo-user`;
     - the tool result in the persisted messages contains "as bo-user";
     - the `Skill` result contains the `draft-section` prompt;
     - the `Skill` enum offered (from the fake-llm request log) is exactly `["draft-section"]`.
2. **Deployed turn with the routing envelope.**
   - Send a turn with `X-Openbbcd-Mcp-Headers` = base64url of
     `{"kb": {"X-Test-Identity": "deployed-user"}}`.
   - Same script, same assertions, with identity `deployed-user`.
3. **Missing identity.**
   - A BO chat with no override. The script calls `kb__search` and then gives a final text.
   - Assert:
     - the turn completes;
     - the tool result is an error containing "missing identity";
     - the agent's second LLM request includes that error.
4. **Real-mode eval.**
   - Create an eval with `mock_mcp_tools=false` and `mcp_header_overrides {kb: {X-Test-Identity: eval-user}}`.
   - Run `run_eval.sh` in the runner.
   - The target script per turn is: call `kb__search` → call `Skill` → text.
   - Assert:
     - the export has `mcp_backends.kb` with both tools;
     - `fake-mcp` saw `tools/call` with `eval-user`;
     - the eval ends `DONE` with score < 1.0. The judge script fails exactly one criterion, because
       `POST /training-sessions` refuses a perfect-score source eval, and scenario 7 trains from
       this eval;
     - the stored transcript has the real "as eval-user" result and a `Skill` call with
       `source: skill`;
     - the target's system prompt has no `<skill` block.
5. **Mock-mode eval.**
   - Create an eval with `mock_mcp_tools=true`, then run it.
   - Assert:
     - `fake-mcp` saw only initialize and `tools/list` (from the export), with no `tools/call`;
     - the recorded `kb__search` result was replayed (`source: replayed`);
     - the eval ends `DONE`.
6. **Discovery failure.**
   - `fake-mcp` is down. Create an eval, then run it.
   - Assert:
     - the export returns 502 with `mcp_discovery_failed`;
     - the eval ends `FAILED` with `mcp_discovery_failed: kb: …`.
   - Then repeat for a training session sourced from scenario 4's eval (in its own fixture): the
     session ends `FAILED` with the same prefix, and `/start` is never called.
7. **Training.**
   - Create a training session from scenario 4's eval with `epochs=1`, `patience=1`, then run
     `train_from_session.sh`.
   - The teacher script returns one patch to `skill.draft-section.prompt`. The judge scripts make
     the baseline fail one criterion and the candidate pass all of them, so the candidate is
     promoted.
   - Assert:
     - `fake-mcp` saw `tools/call` with `eval-user` during both the baseline and the candidate;
     - the session ends `DONE`;
     - a new version exists whose `skill_prompts.draft-section` holds the patched text;
     - a BO chat on the new version calling `Skill(draft-section)` returns the patched text.

Scenarios 4 and 7 share a module-scoped eval fixture. Every other scenario sets up its own state.

### Running

- `make e2e`:
  1. `docker compose -p openbbc-e2e -f e2e/compose.yml up -d --build --wait`
  2. Apply migrations.
  3. `uv run pytest e2e/tests`
  4. `down -v`, always.
- `make e2e-up` / `e2e-down` keep the stack running for debugging.
- **CI:** a new job `e2e` in `.github/workflows/ci.yml`. It needs the Go and aikdm jobs, runs
  `make e2e`, and uploads the compose logs on failure. Target time: under 10 minutes.
- `e2e/` gets its own `pyproject.toml` (pytest, httpx, pyyaml), managed with uv. The fakes image
  has its own (`mcp`, fastapi, uvicorn).

## Constraints

- Build on the real configuration surface only: env vars that already exist, plus REST endpoints
  and scripts. If a scenario can't be driven without a new product hook, stop and raise it.
  Don't add test-only branches to product code.
- Version pins match the repo (the `mcp` SDK version the same as aikdm's).
- No network access beyond the compose network. No API keys anywhere.

## Reading results

Assertions go through JSON endpoints where they exist (deployed sessions, `.json` list and status
endpoints). Where the only surface is HTML (BO chat messages, eval transcripts, version prompts),
tests read Postgres **read-only** with plain SELECTs. Seeding already writes the DB via
`seed_bundle.py`. No new product endpoints are added for testing.

## Left to the builder

- The exact REST calls for creating agents, backends, datasets, chats and deployments: use
  whatever the existing handlers expose.
- Fixture IDs and names, port numbers, and timeout values.
- How readiness is awaited (compose healthchecks plus polling the eval and training status
  endpoints with a deadline).
- How the fake-llm emulates streaming: whatever both the Bifrost OpenAI provider and LiteLLM
  accept.
- The internal file layout under `e2e/`.
