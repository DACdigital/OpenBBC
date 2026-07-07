# Production Integration

How to run `open-bbcd` in your own infrastructure, wire your frontend to it, and reason about the trust boundaries. Read the [README](../README.md) and [ARCHITECTURE.md](./ARCHITECTURE.md) first — this doc assumes you know the phase model (discovery → generate → feedback → evaluate → train → deploy).

> **Security note up front.** `open-bbcd` ships **auth-agnostic**. Nothing on the mux checks credentials. Anyone who can reach the port can create sessions, run agents, deploy versions, or scrape the backoffice. **You must** shield it behind your own auth gateway before exposing it outside a trusted network. Details in [Auth model](#auth-model) below.

---

## 1. Deploying `open-bbcd` as an internal service

`open-bbcd` is a single Go binary (`~25 MB` distroless image) plus a Postgres 15+ dependency. Two supported paths:

### 1a. Docker Compose (single-instance)

The repo ships a top-level `docker-compose.yml` for local dev + single-node production:

```bash
git clone git@github.com:DACdigital/OpenBBC.git
cd OpenBBC
cp open-bbcd/.env.example open-bbcd/.env
# Set ANTHROPIC_API_KEY (or the provider keys your agents need).
docker compose up -d
```

- Migrations apply automatically on startup (embedded `goose` — no CLI needed).
- The container `HEALTHCHECK` uses `open-bbcd healthcheck` (no `curl` in distroless).
- Named volumes `postgres-data` and `discovery-data` persist across `docker compose down`.
- `aikdm` sits behind a compose profile: `docker compose --profile aikdm run --rm aikdm ...` — invoke it one-shot from cron or a job runner.

### 1b. Standalone containers or k8s (bring your own Postgres)

Both images are built multi-arch (`linux/amd64`, `linux/arm64`). Point `DATABASE_URL` at your managed Postgres:

```
DATABASE_URL=postgres://user:pass@your-pg:5432/openbbcd?sslmode=require
SERVER_HOST=0.0.0.0
SERVER_PORT=8080
DISCOVERY_STORAGE_DIR=/data/discovery
ANTHROPIC_API_KEY=…
```

Ship the discovery-storage directory on a persistent volume — it holds uploaded discovery zips referenced by every agent version.

**Subcommands** (all in the same binary):
- `open-bbcd` / `open-bbcd serve` — start the HTTP server (runs migrations on boot).
- `open-bbcd migrate` — apply pending migrations and exit. Useful as a k8s pre-deploy `Job`.
- `open-bbcd healthcheck` — probe `http://127.0.0.1:$SERVER_PORT/health`, exit 0/1. Reads only `SERVER_PORT`; a broken `DATABASE_URL` will not fail the probe. Used by the container `HEALTHCHECK` and can be reused for k8s `livenessProbe`.

**Not shipped yet:** a helm chart, published registry images, and multi-replica-safe migrations. For > 1 replica today, run `open-bbcd migrate` from a single pre-deploy job and set replica count on `serve` only after it exits 0. See Roadmap in the top-level README.

---

## 2. Integrating your frontend

Your frontend never talks to your backend directly — it talks to `open-bbcd`, which mediates via MCP. The client protocol is [AG-UI](https://github.com/ag-ui-protocol/ag-ui) over Server-Sent Events. Every agent that reaches your users must first be marked `DEPLOYED` in the backoffice; the deployed runtime lives at `/deployed/*`.

### 2.1. Mark a version as deployed (operator flow)

Do this once, via backoffice or REST:

```bash
curl -X POST "$OPENBBCD_URL/agents/$AGENT_ID/deploy"
```

There is a DB-enforced singleton: at most one `DEPLOYED` version per agent chain. Deploying a new version implicitly rotates the previous one.

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
  "id":            "9c7…",
  "agent_id":      "your-agent-id",
  "user_id":       "your-app-user-id",
  "title":         "optional session title",
  "created_at":    "…"
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

Response is an AG-UI SSE stream. Each event is one of the AG-UI event types (`RUN_STARTED`, `TEXT_MESSAGE_START/CONTENT/END`, `TOOL_CALL_START/ARGS/END`, `TURN_END`, `ERROR`, etc.). Wire it into any AG-UI client (React SDK, custom, whatever your FE uses).

### 2.4. Listing + managing sessions

```
GET    /deployed/{agent_id}/sessions?user_id=X      # sessions for that user
GET    /deployed/{agent_id}/sessions/{id}?user_id=X # one session (with messages)
PATCH  /deployed/{agent_id}/sessions/{id}/title     # rename
DELETE /deployed/{agent_id}/sessions/{id}?user_id=X # delete (cascades messages)
```

`user_id` is required on every read and must match the session's owner. That check is inside the repo layer — a mismatch returns `404 Not Found` (no existence leak).

---

## 3. MCP layer — `open-bbcd` calls your backend, not your FE

When your agent needs data from your backend, it calls **MCP tools**, not your REST endpoints. `open-bbcd` orchestrates the mapping.

### The wiring

1. **Register MCP backends** you own (typically wrappers around your REST/GraphQL surface as MCP over SSE or Streamable HTTP). REST for this lives at `POST /mcp`, and the backoffice UI has editors at `/mcp/{id}`.
2. **Per-agent endpoint → backend mapping.** In the agent configurator (`/agents/{id}/configure/architecture/endpoints`), every backend endpoint identified during discovery is wired to a specific MCP backend. This is agent-level and frozen at first version creation.
3. **Per-version MCP attachments.** In `/agent_versions/{id}/configure/architecture/mcp` you attach concrete MCP backends the version is allowed to see, with optional per-attachment `note` guidance rendered into the prompt.
4. **At runtime**, when the agent decides to call a tool, `open-bbcd` looks up the endpoint → backend mapping and dispatches an MCP tool call over the wire. Your backend sees an MCP request. Your FE sees an AG-UI `TOOL_CALL_*` event.

**You do not build MCP clients into your FE.** Your FE only speaks AG-UI to `open-bbcd`. Your MCP backends only speak MCP to `open-bbcd`. The FE and the backend never talk to each other directly.

### Building MCPs for your existing backend

If your backend is a REST/GraphQL service, you have two options:
- **Wrap your REST with an MCP shim** — one MCP server per backend, each tool = one endpoint. Standard MCP transports (SSE, Streamable HTTP) are what `open-bbcd` speaks.
- **Adopt native MCP servers** — if you're already using MCP for other tooling, register the same servers here.

Either way, `open-bbcd` becomes the fan-out point. Your agents call an MCP tool; `open-bbcd` proxies it to the registered backend; the response flows back through the AG-UI stream to your FE.

---

## 4. Headers → your backend (and the gap)

MCP calls that go out from `open-bbcd` to your backends can carry per-session HTTP headers. This is how you thread auth tokens, tenant scoping, correlation ids, etc. through the MCP layer.

### Backoffice chat sessions (fully supported)

For sessions under `/agent_versions/{version_id}/chat/*` (i.e. the backoffice chat UI + the eval / dataset feedback flow), `chat_sessions.header_overrides` (migration 016) stores a per-session, per-backend map:

```
POST /agent_versions/{version_id}/chat/{session_id}/headers
{
  "your-backend-mcp-id": {
    "Authorization": "Bearer …",
    "X-Tenant-Id":   "…"
  }
}
```

Those headers get merged into every MCP tool call outbound to that backend for the lifetime of the session. Eval runs use the same mechanism — see the `header_overrides` column on `evals` (migration 023) which mirrors this shape for eval-time MCP calls.

### Deployed runtime sessions (documented gap)

`deployed_sessions` (the production path for real users) **does not** currently store `header_overrides`. There is no `POST /deployed/{agent_id}/sessions/{id}/headers` endpoint. This means: if your production agent calls MCP tools, the outbound HTTP headers to your backends are whatever `open-bbcd`'s default MCP client sets — no per-session auth token pass-through.

**Workarounds today:**
- Bake auth tokens into the MCP backend configuration (server-to-server credential, not per-user).
- Perform per-user auth checks inside your MCP backend against a shared secret plus a header your gateway injects.
- Run one MCP backend per tenant if isolation is coarse.

**Future work:** extending `deployed_sessions` with `header_overrides` and adding a `POST /deployed/{agent_id}/sessions/{id}/headers` route to match the backoffice API. Track this as a follow-up if you need it.

---

## 5. Auth model

**`open-bbcd` has no built-in authentication or authorization.** Every route is open. This is a deliberate design choice — auth policy varies wildly between deployments (SSO, mTLS, API gateway, tenant-scoping, etc.), and baking one in would push assumptions onto every operator.

Your job is to put an auth layer in front of `open-bbcd`. Typical shapes:

### The surfaces to protect

- **Backoffice UI** (`/`, `/agents/ui`, `/agents/new*`, `/agents/{id}/configure/*`, `/agent_versions/{id}/configure/*`, `/mcp*`, `/datasets*`, `/evals`, `/training-sessions`) — internal-only. Restrict by IP allowlist, private VPC, or authenticated employee SSO.
- **REST APIs for automation** (`/evals/*`, `/training-sessions/*`, `/datasets/*`, `/agents/*/deploy`, `/agents/*/undeploy`) — internal-only, same trust boundary as the backoffice UI. Cron scripts (`scripts/process_pending_*.sh`) hit these unauthenticated.
- **Deployed runtime** (`/deployed/{agent_id}/*`) — the only externally-facing surface. Protect it with your customer-auth layer (session cookie, bearer token, mTLS, whatever fits your app). The endpoints accept `user_id` from the request body/query as the sole scope; `open-bbcd` trusts that value.

### What "trusts `user_id`" means in practice

The `user_id` in a deployed-session request body/query is not verified. If a client says `user_id: "alice"`, `open-bbcd` treats the request as Alice — no signature, no cross-check. That's fine when the request has already passed through your auth gateway that (a) verified the caller and (b) injected the caller's identity as `user_id`. It is catastrophic if the endpoint is reachable without that gate.

The repo has a sensible existence-leak defence: `GET /deployed/{agent_id}/sessions/{id}?user_id=X` returns `404` (not `403`) when the id belongs to a different user, so a malicious caller can't enumerate other users' sessions by trying random `user_id`s. But this only matters after your gateway has already established who the caller is.

### Recommended gateway pattern

1. Terminate customer auth at your ingress (Envoy, nginx, an API gateway, whatever).
2. Rewrite the request to inject a verified `user_id` — replace whatever the client sent with the identity your gateway trusts.
3. Forward to `open-bbcd`.

For the backoffice surface, prefer restricting network reachability (private VPC, VPN, employee SSO in front) over trying to intercept individual routes — the surface is large enough that a whitelist approach is brittle.

### Not supported

- Multi-tenant hard isolation inside a single `open-bbcd` instance. All data lives in one database; deployed sessions are scoped by `user_id` string only. If you need tenant isolation for compliance reasons, run one `open-bbcd` per tenant.

---

## 6. Batch operations (cron)

Two batch scripts drain the PENDING queues that operators / automation build up. Both are `flock`-protected, serial, continue-on-error, and safe to schedule at any frequency (a slower predecessor will make the follower exit 0 immediately).

```
*/10 * * * *  OPENBBCD_URL=http://localhost:8080 /path/to/repo/scripts/process_pending_evals.sh
*/15 * * * *  OPENBBCD_URL=http://localhost:8080 /path/to/repo/scripts/process_pending_trainings.sh
```

Both scripts:
- Enumerate PENDING work via the JSON endpoints (`GET /evals.json?status=PENDING`, `GET /training-sessions.json?status=PENDING`).
- Delegate each item to the existing one-shot script (`run_eval.sh` / `train_from_session.sh --yes`).
- Log to stdout with ISO-8601 timestamps; cron / journald owns retention.
- Exit `0` on all-success or empty queue, `1` if any per-item failed, `2` on infra/config error.

Trainings are minutes-per-epoch, so the 15-minute cadence above is a starting recommendation; more aggressive scheduling just makes follower invocations no-op faster.

---

## 7. Provider LLM keys

`open-bbcd` calls Anthropic (default) for backoffice chat and orchestration. `aikdm` can call Anthropic, OpenAI, or Gemini depending on `AIKDM_MODEL_*` env overrides.

Set the keys in each service's environment; the compose file wires the aikdm profile with `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY` from the shell environment (all optional, default empty). In production, secrets should come from your platform's secret store, not `.env` files.

---

## 8. Known gaps / roadmap items

- **Auth**: shipping an optional auth middleware (JWT verifier, mTLS, similar) so operators aren't forced to run a gateway. Blocked on picking a first-supported scheme.
- **Header pass-through on deployed sessions**: parity with backoffice chat sessions. See section 4.
- **Multi-replica safe migrations**: switch from goose's package API to `Provider` with `SessionLocker` so multiple replicas can boot simultaneously without racing on migrations. Currently OK for single-instance compose.
- **Registry publish + Helm chart**: no published images yet; deploy by building locally or in your CI.
- **Timeout-based reset of stuck `IN_PROGRESS` items**: if a batch script dies mid-run, the item stays IN_PROGRESS forever. Manual DB fix needed today.

These are tracked in the top-level README's Roadmap section.
