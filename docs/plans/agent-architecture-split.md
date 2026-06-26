# Agent-level architecture, version-level prompts

## Goal

Re-shape the data model so that the *structural* parts of an agent (endpoints, flows, skills metadata, endpoint→backend wiring) are owned by the **agent** and frozen after the first wizard submit, while *editable* parts (main prompt, skill prompts, MCP attachments + guidance notes) live on **agent versions**. Editing prompts spawns a new version; architecture is read-only forever on a given agent. Pagination is added to every list view at the same time.

Destructive migration is OK — no production data to preserve (seed script regenerates everything).

## Data model delta

### `agents`
| col | change | shape |
|---|---|---|
| `architecture` | **add** `JSONB NOT NULL DEFAULT '{}'` | `{ endpoints[], flows[], skills_meta[], external_mcps_meta[] }` |
| `finalized_at` | **add** `TIMESTAMPTZ` | set automatically when first version lands; null until then |

### `agent_versions`
| col | change | shape |
|---|---|---|
| `prompts` | **add** `JSONB NOT NULL DEFAULT '{}'` | `{ main_prompt, skill_prompts{}, critic_notes? }` |
| `bundle` | **drop** | data split into `agents.architecture` + `agent_versions.prompts` |

### Wiring tables
| table | change |
|---|---|
| `agent_version_endpoint_backend` | **drop** |
| `agent_endpoint_backend` (new) | `(agent_id UUID, endpoint_id TEXT, backend_id UUID); PK (agent_id, endpoint_id)` |
| `agent_version_mcp_backend` | **keep** — MCP attachment + guidance note remain version-scoped per UX requirement |

Migration: `017_agent_level_architecture.sql`. Down is `DROP TABLE agent_endpoint_backend; ALTER TABLE agents DROP COLUMN architecture, finalized_at; ALTER TABLE agent_versions DROP COLUMN prompts; CREATE TABLE agent_version_endpoint_backend (...)` (we don't restore bundle; this is one-way in practice — destructive is acceptable).

## Flow changes

### Wizard submit (`POST /agents/wizard`)
Today: writes `wizard_input` + status `INITIALIZING`, then later the bundle lands on the version's `bundle` column.
New: when the bundle lands (existing aikdm pipeline), the handler **splits it**:
1. `agents.architecture = { endpoints: bundle.tools, flows: bundle.flows, skills_meta: bundle.skills (without prompts), external_mcps_meta: bundle.external_actions }`
2. `agent_versions.prompts = { main_prompt: bundle.main_prompt, skill_prompts: <one per skill> }`
3. `agents.finalized_at = now()` — implicit finalize, no button.

aikdm contract is unchanged; the split is a pure open-bbcd-side operation.

### Configurator (`/agent_versions/{id}/configure/architecture/*`)
- Endpoints / flows / skills tabs: load from `agents.architecture` for the version's parent agent. UI becomes read-only (drop edit affordances; "View only — architecture is frozen for this agent" banner).
- Endpoint→backend dropdown: reads/writes `agent_endpoint_backend` keyed by `agent_id`. Visible on every version of the agent (same wiring).
- MCP subtab: stays per-version. Attach/detach/note all keyed by `agent_version_id`.

### Prompts tab (new)
Currently doesn't exist as an edit surface. New tab `/agent_versions/{id}/configure/prompts`:
- Shows current version's `prompts.main_prompt` (textarea) + skill prompts (collapsible per-skill textareas).
- Save button → POST creates a **new** `agent_versions` row:
  - `agent_id = current.agent_id`
  - `parent_version_id = current.id`
  - `prompts = <submitted form>`
  - Sessions / wiring / MCP attachments are NOT copied — they are inherent to the *agent*, except MCP attachments which we **copy from parent version** so the new version inherits its testing wiring.
  - Redirects to the new version's prompts tab.
- Banner on previous versions: "Read-only — this is version N of M. Edit the latest version to create a new one." (or just `disabled` textareas + a "View latest" link).

### MCP note save (fix)
Replace the silent `blur from:find textarea` form with an explicit **Save** button + transient "Saved ✓" pill. Endpoint stays the same (`POST .../mcp/{backendID}/note`). New: return a `text/html` fragment with the saved indicator (hx-swap="outerHTML"), auto-fade after 2s via tiny inline JS or `htmx-indicator` styling.

### Pagination
Add `?page=N&size=50` to every list:

| Page | Handler | Today's query |
|---|---|---|
| Sessions | `ChatHandler.SessionList` | `chats.ListSessions(versionID)` |
| Agent chains | `UIHandler.Agents` | `agents.ListGrouped(...)` |
| Agent versions | `UIHandler.Agents?agent=…` | `agents.ListVersions(agentID)` |
| MCP backends | `BackendsHandler.List` (or whatever the `/mcp` handler is named) | `backends.List()` |

Repo signature change pattern: `ListXxx(ctx, scopeID, page, size) ([]row, total int, err)`. Templates render Prev/Next + "page N of M" footer. Default page size 50, hard cap 200.

## Code change inventory

- **migrations/017_agent_level_architecture.sql** — schema delta described above.
- **internal/types/agent.go, agent_version.go** — add `Architecture` (struct) on `Agent`, `Prompts` on `AgentVersion`; drop `Bundle` from `AgentVersion`.
- **internal/types/flow_map.go** — `Architecture` and `Prompts` Go types matching JSONB shapes.
- **internal/repository/agent.go** — read/write `architecture`, `finalized_at`; queries change to read prompts off version.
- **internal/repository/agent_wiring.go (new)** — `agent_endpoint_backend` CRUD: `Map(ctx, agentID, endpointID, backendID)`, `List(ctx, agentID)`, `Unmap(ctx, agentID, endpointID)`.
- **internal/repository/version_wiring.go** — strip endpoint methods; keep MCP attachment methods.
- **internal/repository/chat.go** — add `(page, size)` to `ListSessions`, return total.
- **internal/handler/wizard.go** — on bundle landing, split into `architecture` + `prompts` + insert v1 version row + `finalized_at = now()`.
- **internal/handler/configurator.go** — load architecture from agent (not version); endpoint→backend handlers re-key to `agent_id`; render read-only.
- **internal/handler/prompts.go (new)** — `GET /agent_versions/{id}/configure/prompts` form, `POST` creates new version + redirects.
- **internal/llm/tools/builder.go** — fetch architecture from agent, prompts from version, endpoint wiring from `agent_endpoint_backend`, MCP attachments from `agent_version_mcp_backend`.
- **internal/chat/orchestrator.go** — load split data on Turn (agent + version).
- **internal/handler/deploy.go** — `validateAllEndpointsMapped` now checks `agent_endpoint_backend` keyed by agent_id, not per version.
- **web/templates/configurator/architecture.html, endpoints.html, flows.html, skills.html** — read-only treatment + agent-level banner.
- **web/templates/configurator/prompts.html (new)** — main + skill prompt editors with Save → new version.
- **web/templates/configurator/partials.html#mcp_row_detail** — Save button + saved-pill.
- **web/templates/chat/sessions.html, agents.html, versions.html, backends/list.html** — pagination footers.
- **internal/repository/integration_helper_test.go** — adjust seed helpers to populate `agents.architecture` and `agent_versions.prompts` instead of the dropped `bundle`.
- **scripts/e2e_seed_agent.sh** — verify still works end-to-end after the split.

## Task breakdown (execution order)

1. **Migration 017** — schema delta. `make migrate-up` cleanly applies; `make migrate-down` cleanly reverts. Smoke `psql \d agents`.
2. **Types + repos** — add Architecture/Prompts structs, `agent_endpoint_backend` repo; drop bundle field. Make `go build` pass.
3. **Wizard split** — split incoming bundle into agent/version columns + set `finalized_at`. Smoke: run `scripts/e2e_seed_agent.sh`, inspect DB rows.
4. **Builder/orchestrator read path** — runtime reads from new columns. Smoke: chat with seeded agent, products list works end-to-end.
5. **Configurator UI** — read-only architecture pages; endpoint→backend dropdown re-keyed to agent. Smoke: open `/agent_versions/.../configure/architecture/*`, no edit buttons, wiring persists.
6. **MCP note Save button** — replace blur form; saved indicator. Smoke: type note, Save, see ✓, reload, value persisted.
7. **Prompts tab** — new editor + new-version-on-save flow. Smoke: edit prompt, see new version row, version chain reflects parent link, sessions list of old version is intact.
8. **Pagination** — all four list views. Smoke: seed > 50 items, navigate Prev/Next, deep-link `?page=2` works.
9. **Tests** — repository tests for new shape; handler integration tests for wizard split + prompts-edit-creates-version.
10. **Sanity** — `make test` green, `make build` green, manual smoke of the wizard→configure→chat path.

## Out of scope (defer)

- Re-discovery / re-finalize. If you need a different architecture, create a new agent. (One-way finalize, confirmed.)
- Editing skill prompts independently of main prompt with finer-grained "what changed" diff — current spec ships one big Save.
- Cursor-based pagination — offset/limit is fine for BO scale.
- Backwards-compat shims for the old bundle column — dropped clean.

## Open question to resolve before coding

None — defaults locked in. If anything in this spec doesn't match your intent, flag before I start migration 017.
