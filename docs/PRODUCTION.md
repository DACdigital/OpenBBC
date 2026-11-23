# Production Integration

How to deploy `open-bbcd` in your own infrastructure, wire your frontend to it, and
configure the MCP layer. Architecture, trust model, and the *why* behind these recipes
live in the docs repo: **[DACdigital/openbbc-docs](https://github.com/DACdigital/openbbc-docs)**.

> **Security note up front.** `open-bbcd` ships **auth-agnostic**. Nothing on the mux checks
> credentials. **You must** shield it behind your own auth gateway before exposing it
> outside a trusted network. Practical guidance in [Auth model](#5-auth-model) below.

---

## 1. Deploying `open-bbcd`

`open-bbcd` is a single Go binary (~57 MB distroless image) plus a Postgres 15+ dependency.
Three paths:

### 1a. Docker Compose (local dev, single-node)

```bash
git clone git@github.com:DACdigital/OpenBBC.git
cd OpenBBC
cp open-bbcd/.env.example open-bbcd/.env
# Set ANTHROPIC_API_KEY (or the provider keys your agents need).
docker compose up -d
```

- Migrations apply automatically on startup (embedded `goose` — no CLI needed).
- The container `HEALTHCHECK` uses `open-bbcd healthcheck` (no `curl` in distroless).
- Named volume `postgres-data` persists across `docker compose down`. `open-bbcd` itself
  keeps no local disk state (discovery zip lives in Postgres per migration 026).
- `aikdm` sits behind a compose profile: `docker compose --profile aikdm run --rm aikdm ...`
  — invoke it one-shot from cron or a job runner.

### 1b. Standalone containers (bring your own Postgres)

Both images are built multi-arch (`linux/amd64`, `linux/arm64`) and published on every PR
(same-repo only), merge to `main`, and `v*` tag:

- `ghcr.io/dacdigital/openbbc/open-bbcd`
- `ghcr.io/dacdigital/openbbc/aikdm-runner`
- `ghcr.io/dacdigital/openbbc/aikdm`

Tag rules: `pr-<num>`, `main`, `sha-<short>`, semver, `latest`.

Minimum config:

```
DATABASE_URL=postgres://user:pass@your-pg:5432/openbbcd?sslmode=require
SERVER_HOST=0.0.0.0
SERVER_PORT=8080
ANTHROPIC_API_KEY=…
```

No local disk required — the discovery zip is stored as `BYTEA` on `agents.discovery_zip`
(migration 026), so Postgres is the only stateful component.

**Subcommands** (all in the same binary):

- `open-bbcd` / `open-bbcd serve` — start the HTTP server (runs migrations on boot).
- `open-bbcd migrate` — apply pending migrations and exit. Useful as a k8s pre-deploy `Job`.
- `open-bbcd healthcheck` — probe `http://127.0.0.1:$SERVER_PORT/health`, exit 0/1. Reads
  only `SERVER_PORT`; a broken `DATABASE_URL` will not fail the probe. Used by the
  container `HEALTHCHECK` and reusable for k8s `livenessProbe` / `readinessProbe`.

### 1c. Kubernetes (Helm chart)

The chart at [`deploy/helm/openbbc/`](../deploy/helm/openbbc/) ships an `open-bbcd`
`Deployment` (default one replica) + `Service` + optional `Ingress`, an optional in-cluster
Postgres `StatefulSet` (or point `externalDatabase.url` at a managed DB), and three
`CronJob`s running the `aikdm-runner` image:

- alphas at `*/5` (mounts `DATABASE_URL` because `seed_bundle.py` writes to Postgres)
- evals at `*/10`
- trainings at `*/15`

Pick a `TAG` from the image publish table above:

```bash
TAG=main
helm upgrade --install openbbc deploy/helm/openbbc \
  --namespace openbbc --create-namespace \
  --set openbbcd.image.tag=$TAG \
  --set aikdmRunner.image.tag=$TAG \
  --set aikdmRunner.secrets.ANTHROPIC_API_KEY=sk-ant-... \
  --set openbbcd.ingress.enabled=true \
  --set openbbcd.ingress.hosts[0].host=openbbc.example.com \
  --set openbbcd.ingress.hosts[0].paths[0].path=/ \
  --set openbbcd.ingress.hosts[0].paths[0].pathType=Prefix
```

If your cluster nodes can't pull anonymously from `ghcr.io/dacdigital/…` (i.e. the package
is private), create an `imagePullSecrets` entry for a GHCR PAT and pass
`--set imagePullSecrets[0].name=<secret>`.

Multi-replica is not migration-safe today — every pod runs migrations on boot via embedded
`goose` and racing pods can corrupt migration state. The chart ships one replica by
default; scaling `openbbcd.replicaCount > 1` needs a pre-install migrations `Job` in the
chart (open follow-up).

---

## 2. Integrating your frontend

Your frontend never talks to your backend directly — it talks to `open-bbcd`, which
mediates via MCP. The client protocol is
[AG-UI](https://github.com/ag-ui-protocol/ag-ui) over Server-Sent Events. Every agent that
reaches your users must first be marked `DEPLOYED` in the backoffice; the deployed runtime
lives at `/deployed/*`.

### 2.1. Mark a version as deployed (operator flow)

Do this once, via backoffice or REST:

```bash
curl -X POST "$OPENBBCD_URL/agents/$AGENT_ID/deploy"
```

There is a DB-enforced singleton: at most one `DEPLOYED` version per agent chain.
Deploying a new version implicitly rotates the previous one.

### 2.2. Start a session (FE → open-bbcd)

```
POST /deployed/{agent_id}/sessions
Content-Type: application/json

{
  "user_id": "your-app-user-id",
  "title":   "optional session title"
}
```

Response (`201 Created`):
```json
{
  "id":         "9c7…",
  "agent_id":   "your-agent-id",
  "user_id":    "your-app-user-id",
  "title":      "optional session title",
  "created_at": "…"
}
```

Store the session `id` in your FE state. All subsequent turns use it.

### 2.3. Send a turn (streaming)

```
POST /deployed/{agent_id}/sessions/{session_id}/turn?user_id=your-app-user-id
Accept: text/event-stream
Content-Type: application/json

{
  "content": "user prompt text",
  "role":    "user"
}
```

Response is an AG-UI SSE stream. Each event is one of the AG-UI event types
(`RUN_STARTED`, `TEXT_MESSAGE_START/CONTENT/END`, `TOOL_CALL_START/ARGS/END`, `TURN_END`,
`ERROR`, etc.). Wire it into any AG-UI client (React SDK, custom, whatever your FE uses).

### 2.4. Listing + managing sessions

```
GET    /deployed/{agent_id}/sessions?user_id=X      # sessions for that user
GET    /deployed/{agent_id}/sessions/{id}?user_id=X # one session (with messages)
PATCH  /deployed/{agent_id}/sessions/{id}/title     # rename
DELETE /deployed/{agent_id}/sessions/{id}?user_id=X # delete (cascades messages)
```

`user_id` is required on every read and must match the session's owner. That check is
inside the repo layer — a mismatch returns `404 Not Found` (no existence leak).

---

## 3. MCP layer — `open-bbcd` calls your backend, not your FE

When your agent needs data from your backend, it calls **MCP tools**, not your REST
endpoints directly. `open-bbcd` orchestrates the mapping.

### The wiring

1. **Register backends** you own. Two kinds under `POST /mcp` (or the `/mcp/{id}`
   backoffice editor):
   - `http_endpoint` — `open-bbcd` calls a plain REST endpoint on your backend and exposes
     it to the agent as an MCP tool. No MCP server on your side required. This is
     `open-bbcd`'s built-in MCP-over-REST bridge.
   - `mcp_client` — `open-bbcd` proxies to an existing MCP server (SSE or Streamable HTTP
     transport).
2. **Per-agent endpoint → backend mapping.** In the agent configurator
   (`/agents/{id}/configure/architecture/endpoints`), every backend endpoint identified
   during discovery is wired to a specific backend. This is agent-level and frozen at
   first version creation.
3. **Per-version MCP attachments.** In `/agent_versions/{id}/configure/architecture/mcp`
   you attach concrete backends the version is allowed to see, with optional
   per-attachment `note` guidance rendered into the prompt.
4. **At runtime**, when the agent decides to call a tool, `open-bbcd` looks up the
   endpoint → backend mapping and dispatches the call. Your backend sees either an HTTP
   request (`http_endpoint`) or an MCP request (`mcp_client`). Your FE sees an AG-UI
   `TOOL_CALL_*` event either way.

**You do not build MCP clients into your FE.** Your FE only speaks AG-UI to `open-bbcd`.
Your backend only speaks its native protocol to `open-bbcd`. The FE and the backend never
talk to each other directly.

---

## 4. Headers → your backend (and the gap)

MCP calls that go out from `open-bbcd` to your backends can carry per-session HTTP
headers. This is how you thread auth tokens, tenant scoping, correlation ids, etc.

### Backoffice chat sessions (fully supported)

For sessions under `/agent_versions/{version_id}/chat/*` (backoffice chat UI +
eval / dataset feedback flow), `chat_sessions.header_overrides` (migration 016) stores a
per-session, per-backend map:

```
POST /agent_versions/{version_id}/chat/{session_id}/headers
{
  "your-backend-id": {
    "Authorization": "Bearer …",
    "X-Tenant-Id":   "…"
  }
}
```

Those headers get merged into every outbound call to that backend for the session's
lifetime. Eval runs use the same mechanism — see the `header_overrides` column on `evals`
(migration 023).

### Deployed runtime sessions (documented gap)

`deployed_sessions` **does not** currently store `header_overrides`, and there is no
`POST /deployed/{agent_id}/sessions/{id}/headers` endpoint. Outbound headers from the
deployed runtime are whatever the default MCP/HTTP client sets — no per-session auth
token pass-through.

**Workarounds today:**
- Bake static tokens into the backend configuration (server-to-server credential, not
  per-user).
- Perform per-user auth checks inside your backend against a shared secret plus a
  header your gateway injects.
- Run one backend per tenant if isolation is coarse.

---

## 5. Auth model

`open-bbcd` has no built-in authentication or authorization. Every route is open. Put an
auth gateway in front.

### Surfaces to protect

- **Backoffice UI** (`/`, `/agents/ui`, `/agents/new*`, `/agents/{id}/configure/*`,
  `/agent_versions/{id}/configure/*`, `/mcp*`, `/datasets*`, `/evals`,
  `/training-sessions`) — internal-only. Restrict by IP allowlist, private VPC, or
  authenticated employee SSO.
- **REST automation** (`/evals/*`, `/training-sessions/*`, `/datasets/*`,
  `/agents/*/deploy`, `/agents/*/undeploy`) — internal-only, same trust boundary as the
  backoffice UI. Cron drainers hit these unauthenticated.
- **Deployed runtime** (`/deployed/{agent_id}/*`) — the only externally-facing surface.
  Protect it with your customer-auth layer (session cookie, bearer token, mTLS). The
  endpoints accept `user_id` from the request body/query as the sole scope; `open-bbcd`
  trusts that value.

### Recommended gateway pattern

1. Terminate customer auth at your ingress (Envoy, nginx, an API gateway).
2. **Rewrite** the request to inject a verified `user_id` — replace whatever the client
   sent with the identity your gateway trusts.
3. Forward to `open-bbcd`.

Existence-leak defence in the repo: `GET /deployed/{agent_id}/sessions/{id}?user_id=X`
returns `404` (not `403`) when the id belongs to a different user, so a malicious caller
can't enumerate other users' sessions by trying random `user_id`s.

### Not supported

- Multi-tenant hard isolation inside a single `open-bbcd` instance. All data lives in
  one database; deployed sessions are scoped by `user_id` string only. If you need
  tenant isolation, run one `open-bbcd` per tenant.

---

## 6. Batch operations (cron)

Three batch scripts drain the PENDING queues. All are `flock`-protected, serial,
continue-on-error, and safe to schedule at any frequency (a slower predecessor makes the
follower exit 0 immediately). Same code path runs from cron or from the Helm chart's
CronJobs.

```
*/5  * * * *  OPENBBCD_URL=http://localhost:8080 /path/to/repo/scripts/process_pending_alphas.sh
*/10 * * * *  OPENBBCD_URL=http://localhost:8080 /path/to/repo/scripts/process_pending_evals.sh
*/15 * * * *  OPENBBCD_URL=http://localhost:8080 /path/to/repo/scripts/process_pending_trainings.sh
```

All three:

- Enumerate PENDING work via `GET /agent_versions.json?status=PENDING`,
  `GET /evals.json?status=PENDING`, `GET /training-sessions.json?status=PENDING`.
- Delegate each item to the existing one-shot script (`generate_alpha.sh`,
  `run_eval.sh`, `train_from_session.sh --yes`).
- Log to stdout with ISO-8601 timestamps; retention is owned by cron / journald / kubelet.
- Exit `0` on all-success or empty queue, `1` if any per-item failed, `2` on infra/config
  error.

Alpha generation is one aikdm LLM call (~30–60s) per version — `*/5` is fine. The alpha
drainer needs `DATABASE_URL` in addition to LLM keys because `seed_bundle.py` writes
directly to Postgres. Trainings are minutes-per-epoch — `*/15` is a starting
recommendation.

---

## 7. Provider LLM keys

`open-bbcd` calls one LLM provider for backoffice chat and the deployed runtime, chosen at
boot by `OPENBBC_LLM_ADAPTER`. `aikdm` can call Anthropic, OpenAI, or Gemini depending on
`AIKDM_MODEL_*` env overrides.

**`open-bbcd` adapter** (full table in [`open-bbcd/README.md`](../open-bbcd/README.md)):

- `OPENBBC_LLM_ADAPTER=anthropic` (default) — direct Anthropic API. Reads
  `ANTHROPIC_API_KEY`; `OPENBBC_DEFAULT_MODEL` is a bare Anthropic model id (default
  `claude-sonnet-4-6`).
- `OPENBBC_LLM_ADAPTER=bifrost` — the embedded Bifrost SDK, one provider per deployment.
  `OPENBBC_DEFAULT_MODEL` is required and has the form `<provider>/<model>` (split on the
  first `/`, e.g. `openai/gpt-4o`), with provider one of `anthropic, cerebras, cohere,
  deepseek, gemini, groq, mistral, openai, openrouter, xai`. Anything else fails boot.
- `<PROVIDER>_API_KEY` (e.g. `OPENAI_API_KEY`, `XAI_API_KEY`) — only the selected
  provider's key is read. A missing key does not fail boot; LLM-backed endpoints fail at
  the first call.
- `<PROVIDER>_BASE_URL` — optional endpoint override (e.g. a corporate proxy), only for
  `openai`, `anthropic`, `cohere`, `mistral`. Must be `https`; plain `http` is accepted
  only for a loopback host. Set for another provider, or invalid → boot fails.

Mount only the selected provider's key into `open-bbcd`. On Helm, set
`OPENBBC_LLM_ADAPTER` / `OPENBBC_DEFAULT_MODEL` through `openbbcd.extraEnv` and the key
through `openbbcd.envFromSecret`.

To confirm which adapter serves traffic, check the boot line `"llm adapter ready"` (its
`adapter` field is `anthropic` or `bifrost:<provider>`), or set `LOG_LEVEL=debug`: every LLM
call then logs an `"llm call"` line with `adapter`, `model`, `stop_reason`, token counts,
`reasoning_chars` and `duration_ms`. `reasoning_chars` counts the reasoning ("thinking")
characters the model streamed; only the `bifrost` adapter fills it (0 on `anthropic`), and
the reasoning text itself is never shown to users.

### 7.1. An OpenAI-compatible gateway (e.g. a client's Bifrost gateway)

Both services can call a model behind an OpenAI-compatible gateway:

- **`open-bbcd`:** `OPENBBC_LLM_ADAPTER=bifrost`,
  `OPENBBC_DEFAULT_MODEL=openai/<gateway model id>` (e.g. `openai/vllm/glm-5.3-flash`),
  `OPENAI_API_KEY=<gateway key>`, `OPENAI_BASE_URL=https://<gateway>` — **without** `/v1`
  (the adapter appends `/v1/chat/completions`).
- **`aikdm`:** `AIKDM_MODEL_<ROLE>=openai/<gateway model id>` for all six roles,
  `OPENAI_API_KEY=<gateway key>`, `OPENAI_BASE_URL=https://<gateway>/v1` — **with** `/v1`
  (LiteLLM's convention).

### 7.2. Per-model request extras: `OPENBBC_LLM_REQUEST_EXTRAS`

Some models need provider-specific request extras. `OPENBBC_LLM_REQUEST_EXTRAS` adds extra
HTTP headers and top-level JSON body fields to every outbound LLM request for models whose
name matches a glob. `open-bbcd` and `aikdm` read the same variable with the same meaning;
set the same value in both. Unset or `[]` means no extras: requests are exactly as without
the variable.

Worked example — GLM 5.3 behind a Bifrost gateway only keeps its thinking short with this
header **and** body field (without them it uses 3–4× the output tokens and time, and can
hit `OPENBBC_MAX_TOKENS`):

```sh
OPENBBC_LLM_REQUEST_EXTRAS='[{"match":"*/glm-*","headers":{"x-bf-passthrough-extra-params":"true"},"body":{"chat_template_kwargs":{"reasoning_effort":"low"}}}]'
```

- **Shape.** A JSON array of rules `{"match": "<glob>", "headers": {…}, "body": {…}}`.
  `match` is required; at least one of `headers` (header name → string value) and `body`
  (a JSON object) is required, and an empty object counts as absent. No other keys.
- **Matching.** Rules are tried in order and the **first** match wins (rules are never
  merged). The glob is tested against the whole effective model string, case-sensitively:
  `*` matches any run of characters including none and including `/`; `?` matches exactly
  one character; every other character (including `[`, `]`, `\`) is literal. The model
  string is the configured one with the service default applied: `OPENBBC_DEFAULT_MODEL`
  in `open-bbcd` (default `claude-sonnet-4-6` on the `anthropic` adapter), and each role's
  `AIKDM_MODEL_<ROLE>` in `aikdm` (default `claude-haiku-4-5`). The two services may spell
  the same model differently (`openai/vllm/glm-5.3-flash` vs `openai/glm-5.3-flash`), so
  prefer leading-`*` globs such as `*/glm-*` that fit both. `open-bbcd` resolves the rule
  once at boot.
- **Body merge.** On `bifrost` the body is deep-merged into the provider request (the
  adapter also sets Bifrost's passthrough flag, without which extra fields are dropped).
  On `anthropic` each top-level key replaces that whole key. In `aikdm` the fields go to
  LiteLLM's `extra_body`.
- **Reserved, rejected at boot.** Top-level body keys `model`, `messages`, `tools`,
  `tool_choice`, `stream`, `stream_options`, `max_tokens`, `max_completion_tokens`,
  `system`; body keys containing any of `. * ? | # @ \`; headers (any case)
  `Authorization`, `x-api-key`, `x-goog-api-key`, `anthropic-version`, `Content-Type`,
  `Content-Length`, `Host`, `Transfer-Encoding`; header names that are not valid HTTP
  tokens. Any invalid value fails `open-bbcd` boot (`load config: OPENBBC_LLM_REQUEST_EXTRAS:
  rule <i>: <field>: …`) and makes `aikdm` exit with code 2 (`"error":"config"`).
- **Not reserved.** Other fields the adapters set — e.g. `temperature`, `top_p`,
  `thinking`, `metadata` — **can** be overwritten by a rule. That is deliberate: you own
  the config.
- **Provider coverage.** Tested on `bifrost` with provider `openai` (the gateway path) and
  on the direct `anthropic` adapter. Other `bifrost` providers get the same mechanism, but
  it is not separately tested; whether a provider accepts an extra is up to it.
- **Startup log.** Each service logs one line per rule (index, `match`, header names, body
  keys — never values) and which rule is active (`open-bbcd`: `"llm request extras
  active"` with `rule` and `match` next to `"llm adapter ready"`; `aikdm`: one line per
  role).
- **Secrets.** Header values may be credentials. Put the variable in a Secret, not a
  ConfigMap or a committed `.env`: on Helm use the Secret named by `openbbcd.envFromSecret`
  for `open-bbcd`, and the Secret named by `aikdmRunner.secrets.existingSecret` for
  `aikdm` (`aikdmRunner.extraEnv` reaches the evals and trainings CronJobs but **not** the
  alphas CronJob, so generation only gets it from the Secret). `open-bbcd` only sends to a
  `<PROVIDER>_BASE_URL` that is `https` (or plain `http` to loopback); `aikdm` does not
  check LiteLLM's `OPENAI_BASE_URL`, so make sure it is `https` when a header carries a
  credential.

### 7.3. LLM timeouts (`open-bbcd`)

- `OPENBBC_LLM_REQUEST_TIMEOUT_SECONDS` — positive integer. On `bifrost` it is the
  per-request timeout (default 300 when unset). On `anthropic` it is applied only when
  set (unset: no timeout, as before) and bounds a **whole streaming turn**, including
  reading the stream, so set it above your longest expected turn.
- `OPENBBC_LLM_STREAM_IDLE_TIMEOUT_SECONDS` — integer 1–3600, `bifrost` only: the longest
  gap between stream chunks before the call is aborted (default 120 when unset). Raise it
  for models that think silently for long stretches.

Invalid values fail boot. Longer timeouts let a slow gateway finish; they do not make it
faster.

Set the keys in each service's environment. The compose file wires the aikdm profile
with `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY` from the shell environment
(all optional, default empty). The Helm chart takes them as Secret values on
`aikdmRunner.secrets.*`. In production, secrets should come from your platform's secret
store, not `.env` files.
