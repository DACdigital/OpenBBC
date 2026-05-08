# Flow-Map Configurator — Wizard Step for Reviewing and Editing Discovery Output

**Date:** 2026-05-07
**Status:** Approved (pending implementation plan)
**Component:** `open-bbcd` (primary) and `bbc-discovery/flow-map-compiler` (additive change)

## Background

The agent-creation wizard's last input step uploads a `.flow-map/` zip produced by the `flow-map-compiler` discovery skill (see `2026-05-07-wizard-zip-upload-design.md`). The zip is persisted as an opaque blob; nothing reads it back today.

The compiled `.flow-map/` is a structured snapshot of the user's frontend repo — flows, skills, capabilities, an app context document, a glossary, and a proposed-tools JSON. Each flow and skill has YAML frontmatter plus a markdown body. Capabilities have richer frontmatter (HTTP shapes per tool) plus prose. The current example fixture lives at `.test-project/frontend/.flow-map/`.

The wizard needs a step *after* the upload where the user reviews the discovered surface, decides which flows participate in alpha-agent generation, edits flow and skill metadata, edits each flow's control flow, and adds custom skills the discovery didn't pick up. The output is a single YAML document that becomes the agent's source of truth.

## Goal

Implement a configurator UI mounted on a freshly-created agent row immediately after the zip upload. The configurator parses the zip into a structured config, lets the user shape it through a three-tab editor (Flows / Skills / Capabilities), and finalizes the agent on confirmation. The final config is downloadable as YAML and is the input to downstream alpha-agent generation.

## Non-goals

- Custom *flows* (user-created from scratch, not in the zip). Only flows from the zip can be selected/edited in v1. Custom *skills* are in scope.
- Editing markdown prose bodies. Only structured fields are editable; prose is shown read-only.
- Editing capabilities. Capabilities are derived from discovery and are read-only.
- Re-running discovery from inside the configurator. To refresh the snapshot, the user re-uploads a new zip via the wizard.
- Resume-setup UX for partially-configured agents. The data model supports it (`status='INITIALIZING'` rows persist with parsed config), but a list-page CTA and recovery flow are out of scope.
- Migrating existing agent rows. We are pre-production; the migration drops the now-redundant `wizard_input` column. If production data exists at implementation time, the migration must instead backfill `wizard_input → flow_map_config` before dropping.
- Persisting Drawflow editor sessions across users (no collaborative editing). One configurator session per agent at a time; concurrent edits last-write-wins.

## Lifecycle

The wizard splits into two phases. Phase 1 keeps the existing schema-driven, htmx-only wizard. Phase 2 is a separate full page mounted on the same agent row.

```
┌─ Phase 1: schema-driven wizard (htmx form, hidden inputs)         (unchanged)
│  /agents/new/step/{1..6}      (name, scope, should_do, ..., upload zip)
│  POST /agents/wizard          ─►  agent row created with status=INITIALIZING
│                                   zip persisted via storage.Storage (existing)
│                                   server unzips + parses → flow_map_config JSONB
│                                   303 redirect to /agents/{id}/configure
│
└─ Phase 2: configurator page (htmx + Drawflow island)              (new)
   GET  /agents/{id}/configure[/flows|/skills|/capabilities[/{id}]]
   POST /agents/{id}/configure/...      granular edits, write JSONB
   POST /agents/{id}/finalize           flips INITIALIZING → DRAFT, redirect to /agents/ui
```

Key behaviours:

- The wizard schema YAML is unchanged. The configurator is **not** schema-driven — it is a hard-coded second phase.
- An `INITIALIZING` agent always has its zip parsed and a `flow_map_config` JSONB present. If parsing fails, `flow_map_parse_error` is set and the configurator surfaces it with a re-upload path.
- Finalize is a small confirmation page with a primary `POST /agents/{id}/finalize`. No "step 7" indicator — the configurator is its own thing.
- Synchronous parse on submit. Today's zips are small (a handful of flows × a few KB).

## Data model

### Storage layout

New migration `006_add_flow_map_config.sql`:

```sql
-- +goose Up
ALTER TABLE agents
  ADD COLUMN flow_map_config       JSONB,
  ADD COLUMN flow_map_parse_error  TEXT;

ALTER TABLE agents
  DROP COLUMN IF EXISTS wizard_input,
  DROP COLUMN IF EXISTS schema_version;

-- +goose Down
ALTER TABLE agents
  DROP COLUMN IF EXISTS flow_map_config,
  DROP COLUMN IF EXISTS flow_map_parse_error;

ALTER TABLE agents
  ADD COLUMN wizard_input   JSONB,
  ADD COLUMN schema_version VARCHAR(20);
```

`wizard_input` and `schema_version` go away because their content folds into `flow_map_config` (Phase-1 fields move to the root of the same document — see "Output YAML" below).

### `flow_map_config` JSONB shape

```jsonc
{
  "schema_version": 1,

  // Phase-1 wizard answers, at root.
  "name": "my-agent",
  "scope": "...",
  "should_do": "...",
  "should_not_do": "...",
  "business_domain": "...",

  // Phase-2 discovery snapshot.
  "source": {
    "compiler_schema_version": 1,
    "generated_from_sha": "4f3d33fcc...",
    "app_name": "test-project-frontend",
    "stack": { "framework": "react", "router": "react-router-dom" }
  },

  "capabilities": [
    {
      "name": "orders",
      "summary": "...",
      "tools": [ /* verbatim from capabilities/<name>.md frontmatter */ ],
      "prose_md": "...the markdown body, kept for read-only display..."
    }
  ],

  "skills": [
    {
      "id": "place-order",
      "origin": "discovered",            // "discovered" | "custom"
      "name": "Place order",
      "description": "...",
      "user_phrases": ["place my order", "check out"],
      "role": "write",                   // "read" | "write"
      "capability_ref": "orders",        // capabilities[].name, or null when external=true
      "external": false,                 // true → custom skill with no discovered capability
      "external_note": null,             // free text only when external=true
      "proposed_tool": "orders.create",
      "prose_md": "..."                  // present iff origin=discovered
    }
  ],

  "flows": [
    {
      "id": "place-order",
      "origin": "discovered",
      "included": true,                  // selection toggle
      "name": "Place order",
      "description": "...",
      "intent": "...",
      "user_phrases": [...],
      "preconditions": [...],
      "postconditions": [...],
      "side_effects": [...],
      "confidence": "high",
      "workflow": {
        "mermaid": "flowchart TD\n  start([start]) --> ...",
        "layout": {                      // editor sidecar; never written into .md sources
          "n1": { "x": 40,  "y": 40 },
          "n2": { "x": 40, "y": 140 }
        }
      },
      "prose_md": "..."
    }
  ]
}
```

Notes:

- Workflow node types initially shipped: `start`, `end`, `skill` (references `skills[].id`), `decision`. Loops are modeled as back-edges between existing nodes — no dedicated node type.
- `skill_id` on a workflow `skill` node is a *reference*, not a copy. Renaming a skill renames its label everywhere.
- Deleting a skill is allowed only when `origin=custom` and no flow's workflow references it.
- `included=false` flows are kept in the config but excluded from any output downstream alpha generation consumes. Cheaper and reversible vs deletion.
- `prose_md` is the original markdown body, preserved verbatim for read-only display. Custom skills omit it.
- **Custom skill IDs** are server-assigned on `POST /agents/skills`: slugified from the user-provided `name`, suffixed with a short random discriminator if the slug collides with an existing `skills[].id`. The backend rejects user-supplied IDs to keep `discovered` and `custom` namespaces clean.
- `tools-proposed.json` from the zip is validated for presence and well-formedness on ingest but is not folded into `flow_map_config` — capability data is already present per-capability under `capabilities[].tools`. The proposed-tools JSON is the future MCP server's input, not the configurator's.

### Parser

New `internal/flowmap/` package: `func Parse(r io.Reader) (Config, error)`.

1. Validates the zip structure (`AGENTS.md`, `APP.md`, `flows/*.md`, `skills/*.md`, `capabilities/*.md`, `tools-proposed.json`).
2. For each `.md`: yaml-parses frontmatter (between `---` fences), stores the body verbatim as `prose_md`.
3. For each flow: locates the `workflow:` field in frontmatter (a multiline mermaid `flowchart` block). Validates that every `skill` node label resolves to a `skills_used[].skill`. Stores in `workflow.mermaid`. `workflow.layout` starts empty — the editor auto-lays out on first visit and persists positions on first save.
4. **Fallback** for legacy zips lacking `workflow:`: derive a linear chain `start → skills_used[0] → skills_used[1] → ... → end` and store as `workflow.mermaid`.
5. Errors surface as `types.ErrFlowMapInvalid` carrying the offending file path.

### `flow-map-compiler` change (additive only)

Each flow's frontmatter gains a single new field:

```yaml
workflow: |
  flowchart TD
    start([start]) --> p{{parallel}}
    p --> s_read_product_catalog[read-product-catalog]
    p --> s_read_order_history[read-order-history]
    s_read_product_catalog & s_read_order_history --> d{cart empty?}
    d -- yes --> e([end])
    d -- no --> s_place_order[place-order]
    s_place_order --> e
```

The existing `## Sequence` mermaid `sequenceDiagram` block and prose body remain untouched.

Files touched in `bbc-discovery/flow-map-compiler/`:

- `references/output-schemas.md` — document the new `workflow` field
- `references/lint-contract.md` — new rule: skill-node labels in `workflow` must equal a `skills_used[].skill`
- `assets/templates/flow.md.tmpl` — emit the field
- `SKILL.md` — derivation guidance: take observed call-site control flow + decision-points prose, fall back to a linear chain through `skills_used` when control flow can't be determined
- Re-render canonical fixtures in `evals/tests/fixtures/`

This change is additive (existing zip output is unchanged in every other respect), so PR0 (compiler change) and PR1 (configurator ingest) can land in either order.

## Configurator UI

One full page at `/agents/{id}/configure` with three tabs: Flows / Skills / Capabilities. Each tab is a list/detail two-pane.

```
┌─ /agents/{id}/configure ──────────────────────────────────────┐
│  Agent: <name>                                                │
│  [ Flows ]  [ Skills ]  [ Capabilities ]      [ Finalize → ]  │
├───────────────────────────────────────────────────────────────┤
│ ┌─────────────────┐ ┌───────────────────────────────────────┐ │
│ │  list pane      │ │  detail pane                          │ │
│ │  (active tab)   │ │  (selected row)                       │ │
│ └─────────────────┘ └───────────────────────────────────────┘ │
└───────────────────────────────────────────────────────────────┘
```

- Tabs are `<a href>` links — full-page navigation, deep-linkable: `/configure/flows`, `/configure/skills`, `/configure/capabilities`. Default is Flows.
- List pane is htmx — clicking a row swaps only the detail pane via `hx-target=#detail`, with `hx-push-url` updating to `/configure/flows/{flowId}`.
- "Finalize →" is a small confirmation page; primary `POST /agents/{id}/finalize`.

### Flows tab

**List pane.** One row per flow: name, included-toggle (checkbox), node count, skill count.

```
☑ place-order        5 nodes · 3 skills
☑ browse-products    3 nodes · 1 skill
☐ update-profile     4 nodes · 2 skills    (excluded)
```

The toggle posts to `POST /agents/{id}/configure/flows/{flowId}/included`.

**Detail pane.** Two stacked sections:

1. **Workflow editor** (top, ~60% vertical) — Drawflow canvas. Loads `workflow.mermaid`, renders nodes at `workflow.layout` positions (auto-layout when empty). Toolbar: `+ skill`, `+ decision`, `+ end`, `Auto-layout`, `Reset to source`. Save is debounced (~500ms) — every drag/edit POSTs to `POST /agents/{id}/configure/flows/{flowId}/workflow` with the serialized mermaid + layout. A "Saved · 2s ago" indicator sits in the toolbar.
2. **Metadata** (collapsible) — form with name, description, intent, user phrases, preconditions, postconditions, side effects. POST per field group. Below: the original `prose_md` rendered read-only.

There is no "+ new flow" button.

### Skills tab

**List pane.** One row per skill with an `origin` badge (`discovered` | `custom`) and capability binding shown inline. Filter chips: `all | discovered | custom`. Bottom: `+ Add skill` button.

**Detail pane.** Form with:

- `name`, `description`, `user_phrases` (chip input), `role` (read | write), `proposed_tool`
- `capability_ref` — dropdown of existing capabilities + an `external` option. Selecting `external` reveals an `external_note` textarea; switching back hides and clears it.
- For `discovered` skills: a read-only **Source** section showing the original `prose_md`.
- For `custom` skills: a small banner ("Custom skill — added by you").

Endpoints:

- `POST /agents/{id}/configure/skills/{skillId}` — update.
- `POST /agents/{id}/configure/skills` — add custom.
- `DELETE /agents/{id}/configure/skills/{skillId}` — delete (custom only, and only if no flow workflow references it; otherwise 409).

### Capabilities tab

Read-only. List by `name`; detail shows the full `prose_md` rendered + tools table. Banner at top: *"Capabilities are derived from the discovery snapshot and cannot be edited here. To change them, re-run discovery."*

### Drawflow integration

- Vendored: `web/static/drawflow.min.js` + `drawflow.min.css`. Provenance recorded in `web/static/VENDORED.md`.
- Custom node-type registration (~40 LoC JS) for `start`, `end`, `skill`, `decision`. Each renders as a small card with the appropriate shape and color.
- Mermaid round-trip lives in two places:
  - `internal/flowmap/mermaid.go` — Go parser/serializer for save-time validation.
  - `web/static/openbbc-flow.js` (~150 LoC) — JS module that reads the mermaid string into Drawflow nodes and serializes back.
- Both parsers are restricted to the dialect the editor produces. They share canonical fixtures (`internal/flowmap/testdata/*.mermaid`); both test suites assert byte-equal round-trips.

## Output YAML — agent's source of truth

Storage is JSONB. The YAML view is rendered on demand by `GET /agents/{id}/config.yaml` (`Content-Type: application/yaml`, `Content-Disposition: attachment`). YAML mirrors JSONB 1:1, flat at the root:

```yaml
schema_version: 1

# phase-1 wizard fields
name: my-agent
scope: "..."
should_do: "..."
should_not_do: "..."
business_domain: "..."

# phase-2 discovery snapshot
source:
  compiler_schema_version: 1
  generated_from_sha: 4f3d33fcc...
  app_name: test-project-frontend
  stack:
    framework: react
    router: react-router-dom

capabilities:
  - name: orders
    summary: "..."
    tools:
      - tool: orders.create
        method: POST
        path: /api/orders
        # ...
    prose_md: |
      # Orders
      ...

skills:
  - id: place-order
    origin: discovered
    name: Place order
    description: "..."
    user_phrases: ["place my order", "check out"]
    role: write
    capability_ref: orders
    external: false
    proposed_tool: orders.create
    prose_md: |
      ...

flows:
  - id: place-order
    origin: discovered
    included: true
    name: Place order
    # ...all metadata...
    workflow:
      mermaid: |
        flowchart TD
          start([start]) --> ...
      layout:
        n1: { x: 40,  y: 40 }
        n2: { x: 40, y: 140 }
    prose_md: |
      ...
```

When the user clicks "Finalize", `status` flips `INITIALIZING → DRAFT`. The YAML at `GET /agents/{id}/config.yaml` is then the agent's stable config snapshot. There is no public write endpoint for YAML — the granular configurator endpoints are the only writers.

## Implementation slicing

Five PRs, ordered. The configurator becomes useful at the end of PR1 (read-only) and progressively more interactive after each.

### PR0 — flow-map-compiler emits `workflow:` field (additive)

Lives in `bbc-discovery/flow-map-compiler/`. Files: `assets/templates/flow.md.tmpl`, `references/output-schemas.md`, `references/lint-contract.md`, `SKILL.md`, plus re-rendered `evals/tests/fixtures/`. Can ship before or in parallel with PR1; PR1's parser handles missing-`workflow:` zips via the linear-chain fallback.

### PR1 — ingest + read-only configurator

Foundation. After this, an uploaded zip is parsed and viewable; nothing is editable.

- Migration `006_add_flow_map_config.sql` (above).
- `internal/flowmap/` package: zip extraction, frontmatter parsing, mermaid `flowchart` validation (skill-ref check), full `Config` building, linear-chain fallback for missing `workflow:`.
- `internal/types/flow_map.go`: `FlowMapConfig` struct mirroring the JSONB/YAML shape.
- Repository: extend `agents` queries; add `UpdateFlowMapConfig(ctx, agentID, config)` and `GetFlowMapConfig(ctx, agentID)`.
- `wizard.Submit`: after the existing zip-to-storage write, call `flowmap.Parse` on the same upload, write `flow_map_config` (or `flow_map_parse_error` on failure), redirect to `/agents/{id}/configure` instead of `/agents/ui`.
- `internal/handler/configurator.go`: routes for `GET /agents/{id}/configure` (default Flows tab), `/configure/flows`, `/configure/flows/{flowId}`, `/configure/skills`, `/configure/skills/{skillId}`, `/configure/capabilities`, `/configure/capabilities/{capName}`. All render-only.
- Templates: `web/templates/configurator/{layout,flows,skills,capabilities}.html` + list/detail partials.
- Tests: parser unit tests (canonical fixture round-trip + missing-workflow fallback + invalid skill-ref); handler tests for each route returning 200 with expected sections.

### PR2 — include/exclude + skills/capabilities edit paths

After this, everything except the workflow editor is interactive.

- `POST /agents/{id}/configure/flows/{flowId}/included`.
- `POST /agents/{id}/configure/skills/{skillId}` — update.
- `POST /agents/{id}/configure/skills` — add custom.
- `DELETE /agents/{id}/configure/skills/{skillId}` — only when `origin=custom` and unreferenced.
- Forms with htmx swaps; chip input for `user_phrases`; capability dropdown with `external` mode.
- Sentinel errors in `internal/types/errors.go`: `ErrSkillReferenced`, `ErrCapabilityReadOnly` (mapped in `handler/handler.go::Error`).
- Tests: each endpoint, plus the referential-integrity guard.

### PR3 — workflow editor

After this, the flow detail view is fully interactive.

- Vendor Drawflow: `web/static/drawflow.min.js` + `drawflow.min.css`. Provenance in `web/static/VENDORED.md`.
- `web/static/openbbc-flow.js` (~150 LoC): mermaid `flowchart` ↔ Drawflow JSON round-trip; custom node renderers; toolbar wiring.
- `internal/flowmap/mermaid.go`: Go-side mermaid `flowchart` parser/serializer.
- `POST /agents/{id}/configure/flows/{flowId}/workflow`: accepts `{mermaid, layout}`, validates mermaid (parses + checks every skill node resolves to an existing skill id), writes JSONB. 422 on invalid mermaid with location info.
- Server-rendered initial state writes the mermaid string and layout into hidden script tags the JS picks up.
- Tests: Go mermaid parser table tests over canonical workflows; integration test that round-trips a known mermaid through the save endpoint.

### PR4 — finalize + downloadable config

The closing step.

- `POST /agents/{id}/finalize`: validates `flow_map_config` is well-formed; flips `status` `INITIALIZING → DRAFT`; redirects to `/agents/ui`. 409 if status isn't `INITIALIZING`.
- `GET /agents/{id}/config.yaml`: renders JSONB to YAML (flat shape above). `Content-Type: application/yaml`, attachment.
- "Download config" link added to `/agents/ui?agent=<name>` for any agent in `DRAFT` or later.
- Tests: round-trip via `gopkg.in/yaml.v3` (write JSONB → YAML → parse YAML → match original) on the canonical fixture.

## Risks and open questions

- **Two mermaid parsers** (Go server-side, JS client-side) are the sharpest tooling cost. Mitigated by restricting the dialect to what the editor produces and sharing canonical fixtures across both test suites. Drift surfaces immediately in PR3 tests.
- **Pre-prod assumption.** PR1's migration drops `wizard_input` and `schema_version`. If production data exists at implementation time, change the migration to backfill `wizard_input → flow_map_config` first.
- **Drag-save UX.** Debounced save on every Drawflow change (~500ms). Server-side coalescing isn't needed; htmx idempotent POST handles it.
- **Drawflow native loop support** is acceptable but not pretty out of the box. If visual feedback for back-edges is poor, consider a small CSS-only enhancement on the JS side; not a blocker.
- **Concurrent edits.** Last-write-wins. No row-level versioning for the configurator session; if two operators open the same agent, the later save overwrites silently. Acceptable for the backoffice use case; revisit if multi-operator workflows become a thing.

## Conventions touched

Reuse the existing patterns described in `CLAUDE.md`:

- Sentinel errors in `internal/types/errors.go`, mapped in `handler/handler.go::Error`.
- Repository interfaces declared at the handler layer, not on the repo struct.
- Templates parsed in `NewUIHandler`-style constructors with shared `template.FuncMap`.
- Migrations as `migrations/NNN_<name>.sql` with goose annotations and a Down section.
