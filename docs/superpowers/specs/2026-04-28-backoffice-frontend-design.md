# Backoffice Frontend Design

**Date:** 2026-04-28
**Scope:** Agents list UI + alpha agent creation wizard (issue #4 + frontend)
**Status:** Approved

---

## Problem

open-bbcd has a REST API but no human interface. Domain experts need to see all agent versions and kick off the alpha agent generation flow from a browser.

---

## Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Frontend tech | htmx + Go templates | No build pipeline, single binary, fits Go idioms |
| Layout | Sidebar app shell | Architecture doc lists 6+ future sections; shell slots them in without redesign |
| Data model | Named agents with version chains | "Single agent on day one" was too narrow — user confirmed multiple named agents each with their own version history |
| Wizard UX | Full-page, single `<form>` | 6 steps + file upload is too complex for a modal; single form = no server session state |
| Wizard schema | Embedded YAML file, versioned | Schema is owned by aicademy and will evolve; versioning lets old agents record which schema generated them |

---

## Data Model Changes

Two new migrations on top of the existing `agents` table.

### Migration 003 — versioning fields

```sql
ALTER TABLE agents
  ADD COLUMN parent_version_id UUID REFERENCES agents(id),
  ADD COLUMN status VARCHAR(50) NOT NULL DEFAULT 'DRAFT';
```

**Status values:** `INITIALIZING` | `DRAFT` | `TESTED` | `DEPLOYED`

- `parent_version_id = NULL` → alpha version (first in chain)
- `name` is immutable per chain — all versions in a chain share the same name
- Version number is derived at query time: position in the `parent_version_id` chain (no stored column)

### Migration 004 — wizard input storage

```sql
ALTER TABLE agents
  ADD COLUMN wizard_input   JSONB,
  ADD COLUMN schema_version VARCHAR(20);
```

- `wizard_input`: raw wizard answers as JSON (allows YAML regeneration without re-running wizard)
- `schema_version`: e.g. `"v1"` — which wizard schema generated this agent

### Grouped agent query

Fetch chain roots (`parent_version_id IS NULL`) then join all versions per chain. Single query, no N+1.

---

## File Structure

```
open-bbcd/
├── cmd/open-bbcd/main.go              (unchanged)
├── internal/
│   ├── handler/
│   │   ├── api.go                     (unchanged — existing JSON REST routes)
│   │   ├── ui.go                      (NEW — serves HTML pages)
│   │   └── wizard.go                  (NEW — handles wizard POST, assembles YAML)
│   └── types/
│       └── agent.go                   (modified — add Status, ParentVersionID, WizardInput, SchemaVersion)
├── web/
│   ├── templates/
│   │   ├── layout.html                (base shell: sidebar + content slot)
│   │   ├── agents.html                (agents list page)
│   │   └── wizard/
│   │       ├── wizard.html            (wizard container with single <form>)
│   │       └── step.html              (generic step partial — renders any field type)
│   └── static/
│       └── htmx.min.js                (vendored, no CDN)
├── schemas/
│   └── wizard-v1.yaml                 (wizard schema, embedded in binary)
└── migrations/
    ├── 003_add_agent_versioning.sql
    └── 004_add_agent_wizard_input.sql
```

Assets and templates are embedded in the binary via `//go:embed`. No external file serving needed.

---

## Routes

### New UI routes (added to `api.go`)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/` | Redirect → `/agents/ui` |
| `GET` | `/agents/ui` | HTML — agents list, grouped by name |
| `GET` | `/agents/new` | HTML — wizard shell, renders step 1 |
| `GET` | `/agents/new/step/{n}` | htmx partial — swaps step content (1–6) |
| `POST` | `/agents/wizard` | Process wizard form, create agent, redirect |
| `GET` | `/static/*` | Embedded static files |

### Existing JSON REST routes (unchanged)

`GET /agents`, `POST /agents`, `GET /agents/{id}`, `POST /resources`, `GET /resources/{id}`, `GET /agents/{agent_id}/resources`, `GET /health`

---

## UI Components

### Sidebar shell (`layout.html`)

Persistent left sidebar with nav links. Only **Agents** is active; Datasets, Scores, Resources render as disabled/greyed links for future sections.

### Agents list page (`agents.html`)

- Header: "Agents" title + "**+ Create Alpha Agent**" button (links to `/agents/new`)
- Agents grouped by name, each group shows:
  - Group header: agent name + version count
  - Version rows (newest first): version number (v1, v2…), name, created date, status badge
- Status badge colours: `INITIALIZING` blue · `DRAFT` grey · `TESTED` yellow · `DEPLOYED` green

### Wizard (`wizard/`)

Single `<form action="/agents/wizard" method="POST" enctype="multipart/form-data">` wraps all steps. htmx swaps step content via `GET /agents/new/step/{n}`, but all inputs remain in the DOM so the final submit sends everything in one request. No server session state — if the user closes the tab, progress is lost (acceptable).

**Steps are driven entirely by the schema.** The handler loads `wizard-v1.yaml` at startup, holds it in memory, and serves field N for step N. The template (`step.html`) is a single generic partial that branches on field type (`text`, `textarea`, `file`). Adding, removing, or reordering questions requires only a schema change — no template or handler code changes.

**Progress indicator:** numbered dots (total count from schema), completed steps show a checkmark, current step highlighted.

**`step.html` renders based on field type:**

| Field type | Rendered as |
|------------|-------------|
| `text` | `<input type="text" name="{key}">` |
| `textarea` | `<textarea name="{key}">` |
| `file` | `<input type="file" name="{key}" accept=".yaml,.yml">` |

Back/Next navigation: Next renders the next step partial via htmx; Back renders the previous step partial. Previous steps' inputs remain as hidden fields in the DOM. Navigating Back discards the current step's unsaved input (acceptable — same trade-off as closing a modal).

**Future:** when aicademy exposes the schema over an API, the handler fetches it remotely instead of reading the embedded file. The wizard rendering code does not change.

---

## Wizard Submit Flow

`POST /agents/wizard` (handler in `wizard.go`):

1. Parse multipart form — collect all fields (keys come from schema `wizard` block, no hardcoded field names)
2. Schema already loaded in memory at startup — note schema version `"v1"`
3. Assemble combined YAML with three root keys:

```yaml
version: v1
wizard:
  name: "Customer Support Bot"
  scope: "Handle customer inquiries end-to-end..."
  should_do: "Answer product questions, track orders..."
  should_not_do: "Never discuss competitor pricing..."
  business_domain: "E-commerce platform for fashion brands..."
discovery:
  # raw content of the uploaded discovery YAML merged here
  intents:
    - name: track_order
      resources: [get_order, get_shipment]
```
4. Insert agent row:
   - `name` = step 1 input
   - `status` = `INITIALIZING`
   - `parent_version_id` = `NULL` (alpha)
   - `wizard_input` = wizard text answers as JSON
   - `schema_version` = `"v1"`
   - `prompt` = `""` (populated later by aicademy)
5. HTTP 303 redirect → `/agents/ui`

User lands back on the list and sees their new agent in `INITIALIZING` state.

---

## Wizard Schema Versioning

`schemas/wizard-v1.yaml` is embedded in the binary via `//go:embed`. It defines the questions the wizard presents (field names, labels, types). Example structure:

**Schema definition file (`wizard-v1.yaml`):**

```yaml
version: v1
wizard:
  name:
    label: "Agent name"
    type: text
    required: true
  scope:
    label: "Describe the scope of your agent"
    type: textarea
    required: true
  should_do:
    label: "What should your agent do?"
    type: textarea
    required: true
  should_not_do:
    label: "What should your agent never do?"
    type: textarea
    required: true
  business_domain:
    label: "Describe your platform business domain"
    type: textarea
    required: true
  discovery_file:
    label: "Upload discovery file"
    type: file
    required: false
```

The `wizard` block is a map of technical variable names → field metadata. Order of steps follows key order (YAML spec preserves insertion order). The wizard renders steps dynamically from this schema (one field per step). When aicademy changes the expected input format:

1. New file `schemas/wizard-v2.yaml` is added
2. Handler updated to read `wizard-v2.yaml` and tag agents with `schema_version: "v2"`
3. Old agents retain `schema_version: "v1"` — YAML can always be reconstructed from `wizard_input` + the correct schema version

Schema file is owned by aicademy's requirements; open-bbcd treats it as a config layer.

---

## Out of Scope (this spec)

- Viewing/editing individual agent versions
- Running/testing an agent from the UI
- Dataset management, score dashboard
- Feedback chat
- aicademy integration (INITIALIZING → DRAFT transition)
- Authentication / user management
