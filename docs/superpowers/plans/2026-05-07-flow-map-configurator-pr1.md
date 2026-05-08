# Flow-Map Configurator — PR1 (ingest + read-only configurator) + PR0 (compiler workflow field)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the foundation for the wizard's Phase-2 configurator: parse the uploaded `.flow-map` zip on submit into a structured `flow_map_config` JSONB blob, render a read-only three-tab configurator at `/agents/{id}/configure`, and add an additive `workflow:` mermaid field to the flow-map-compiler so future zips carry it natively.

**Architecture:**
- **Part 0 (`bbc-discovery/flow-map-compiler/`)**: additive change to the discovery skill — new `workflow:` frontmatter field on each flow (multiline mermaid `flowchart`), three reference fixtures updated. No code, content only.
- **Part 1 (`open-bbcd/`)**: a new `internal/flowmap` package parses a `.flow-map` zip into a `types.FlowMapConfig`. Migration 006 adds `flow_map_config JSONB` and `flow_map_parse_error TEXT` to `agents`, and drops the now-redundant `wizard_input` / `schema_version` columns (Phase-1 fields fold into `flow_map_config`'s root). The wizard `Submit` handler parses the zip immediately after the storage write, persists the parsed config (or the error), and redirects to the new configurator. The configurator is a single full-page handler with three tabs (Flows / Skills / Capabilities), each list/detail. Read-only in PR1; PR2 introduces edit paths, PR3 adds the Drawflow workflow editor, PR4 finalize + YAML download.

**Tech Stack:** Go 1.26, `database/sql` + `lib/pq`, `archive/zip`, `gopkg.in/yaml.v3`, `html/template` + htmx, `github.com/yuin/goldmark` (new — markdown rendering for read-only prose).

**Spec:** `docs/superpowers/specs/2026-05-07-flow-map-configurator-design.md`

---

## File Structure

```
bbc-discovery/flow-map-compiler/skills/flow-map-compiler/
├── assets/templates/flow.md.tmpl                                    # MODIFY: emit workflow:
├── references/output-schemas.md                                     # MODIFY: document workflow:
├── references/lint-contract.md                                      # MODIFY: rule 17 workflow well-formedness
├── SKILL.md                                                         # MODIFY: §5 derivation guidance
└── tests/fixtures/
    ├── sample-react/.flow-map/flows/update-profile.md               # MODIFY: add workflow:
    ├── sample-nextjs/.flow-map/flows/update-profile.md              # MODIFY: add workflow:
    └── sample-sveltekit/.flow-map/flows/view-home.md                # MODIFY: add workflow:

open-bbcd/
├── go.mod / go.sum                                                  # MODIFY: add goldmark
├── migrations/
│   └── 006_add_flow_map_config.sql                                  # CREATE
├── internal/
│   ├── types/
│   │   ├── errors.go                                                # MODIFY: add ErrFlowMapInvalid
│   │   ├── flow_map.go                                              # CREATE: FlowMapConfig + nested types
│   │   ├── flow_map_test.go                                         # CREATE: round-trip JSON encode/decode
│   │   └── agent.go                                                 # MODIFY: drop WizardInput/SchemaVersion fields
│   ├── flowmap/
│   │   ├── parser.go                                                # CREATE: zip → FlowMapConfig
│   │   ├── parser_test.go                                           # CREATE
│   │   ├── mermaid.go                                               # CREATE: workflow validation (PR1 only validates)
│   │   ├── mermaid_test.go                                          # CREATE
│   │   └── testdata/sample-flowmap/                                 # CREATE: minimal .flow-map tree
│   │       ├── AGENTS.md
│   │       ├── APP.md
│   │       ├── glossary.md
│   │       ├── tools-proposed.json
│   │       ├── flows/place-order.md
│   │       ├── skills/place-order.md
│   │       └── capabilities/orders.md
│   ├── handler/
│   │   ├── handler.go                                               # MODIFY: map ErrFlowMapInvalid → 400
│   │   ├── wizard.go                                                # MODIFY: parse zip, write JSONB, redirect
│   │   ├── wizard_test.go                                           # MODIFY: cover parse path
│   │   ├── configurator.go                                          # CREATE: 3-tab read-only handler
│   │   ├── configurator_test.go                                     # CREATE
│   │   └── api.go                                                   # MODIFY: register routes
│   └── repository/
│       ├── agent.go                                                 # MODIFY: column changes
│       └── agent_test.go                                            # MODIFY: drop wizard_input refs
└── web/
    ├── templates/configurator/
    │   ├── layout.html                                              # CREATE: page chrome + tabs
    │   ├── flows.html                                               # CREATE
    │   ├── skills.html                                              # CREATE
    │   ├── capabilities.html                                        # CREATE
    │   └── _partials.html                                           # CREATE: list rows + detail panes
    └── static/
        └── configurator.css                                         # CREATE: styles for tabs, two-pane, lists
```

The `internal/flowmap` package owns one responsibility: ingest a zip and produce a validated `FlowMapConfig`. Handlers don't know anything about zip layout. The configurator handler depends on a narrow `ConfigGetter` interface; PR2/PR3 add `ConfigUpdater` alongside it.

---

# Part 0 — flow-map-compiler emits `workflow:` field

Five small content-only changes to the discovery skill. No code. Each flow file's frontmatter gains one new field. Existing prose, sequence diagrams, and lint rules stay untouched.

## Task P0.1: Update flow.md.tmpl to emit `workflow:`

**Files:**
- Modify: `bbc-discovery/flow-map-compiler/skills/flow-map-compiler/assets/templates/flow.md.tmpl`

- [ ] **Step 1: Read the template**

```bash
cat bbc-discovery/flow-map-compiler/skills/flow-map-compiler/assets/templates/flow.md.tmpl
```

Note the existing `skills_used:` block (lines ~17–22) and the `confidence:` line (~29) bracketing the frontmatter.

- [ ] **Step 2: Insert `workflow:` between `confidence:` and the closing `---`**

In `flow.md.tmpl`, locate this block:

```
confidence: {{confidence}}
---
```

Replace it with:

```
confidence: {{confidence}}
workflow: |
{{workflow_mermaid_indented_2}}
---
```

`{{workflow_mermaid_indented_2}}` is the multiline mermaid `flowchart` body, indented by two spaces so it nests correctly under YAML's `|` block scalar. The compiler is responsible for producing the indented value (next task documents the contract).

- [ ] **Step 3: Commit**

```bash
git add bbc-discovery/flow-map-compiler/skills/flow-map-compiler/assets/templates/flow.md.tmpl
git commit -m "feat(flow-map-compiler): emit workflow: mermaid flowchart in flow frontmatter"
```

---

## Task P0.2: Document the new field in output-schemas.md

**Files:**
- Modify: `bbc-discovery/flow-map-compiler/skills/flow-map-compiler/references/output-schemas.md`

- [ ] **Step 1: Locate the `flows/<id>.md` schema block**

```bash
grep -n "flows/<id>.md" bbc-discovery/flow-map-compiler/skills/flow-map-compiler/references/output-schemas.md
```

The schema lists frontmatter fields ending with `confidence: high|medium|low`.

- [ ] **Step 2: Insert the `workflow:` line after `confidence:`**

In the `flows/<id>.md` schema's frontmatter block, find:

```
confidence: high|medium|low
---
```

Replace with:

```
confidence: high|medium|low
workflow: |
  flowchart TD
    start([start]) --> <node-id>[<skill-id>]
    <node-id> --> end([end])
---
```

- [ ] **Step 3: Add a "Workflow notation" subsection**

After the `flows/<id>.md` schema block (before the next H2 `## capabilities/<name>.md`), insert:

````markdown
### Workflow notation

The `workflow:` frontmatter field on every flow is a multiline mermaid
`flowchart` block describing the flow's control flow. It is the
authoritative structured representation of the flow's algorithm; the
prose body remains for human readers.

Required node shapes (do not invent new ones):

| Shape           | Mermaid syntax                  | Meaning                              |
|-----------------|---------------------------------|--------------------------------------|
| Start           | `id([start])`                   | Single entry node, label = `start`   |
| End             | `id([end])`                     | Terminal node(s), label = `end`      |
| Skill call      | `id[<skill-id>]`                | Invokes the named agent skill        |
| Decision        | `id{<question?>}`               | Two-way branch, label edges `yes`/`no` |
| Parallel fanout | `id{{<label>}}`                 | Fan into multiple branches with `&`  |

Every `id[<skill-id>]` skill node's label MUST equal a `skills_used[].skill`
entry on the same flow. Loops are modeled as back-edges between existing
nodes — no dedicated node type. When call-site control flow can't be
determined (low-confidence fallback), emit a linear chain through
`skills_used` in declared order:

```
flowchart TD
  s_<a> --> s_<b> --> s_<c>
```
````

- [ ] **Step 4: Commit**

```bash
git add bbc-discovery/flow-map-compiler/skills/flow-map-compiler/references/output-schemas.md
git commit -m "docs(flow-map-compiler): document workflow: frontmatter field"
```

---

## Task P0.3: Add lint rule 17 for workflow well-formedness

**Files:**
- Modify: `bbc-discovery/flow-map-compiler/skills/flow-map-compiler/references/lint-contract.md`

- [ ] **Step 1: Update the rule-count language at the top**

In `lint-contract.md`, find:

```
The agent walks these 16 rules as a self-check
```

Replace with:

```
The agent walks these 17 rules as a self-check
```

- [ ] **Step 2: Add rule 17 to the table**

In the rules table, after the row for rule 16, add:

```
| 17 | Every flow file's frontmatter has a `workflow:` field; it is a multiline mermaid `flowchart` block; every `id[<skill-id>]` skill node's label appears in `skills_used[].skill` on the same flow; every node id referenced in an edge appears in the node list; every flowchart parses. |
```

- [ ] **Step 3: Add a "Workflow well-formedness (rule 17)" section**

At the bottom of `lint-contract.md`, append:

````markdown
## Workflow well-formedness (rule 17)

For each `flows/<id>.md`:

1. The frontmatter has a `workflow:` field whose value is a string.
2. The string starts with `flowchart TD` (or `flowchart LR`) followed by mermaid flowchart node and edge syntax.
3. Every node declared with `id[<label>]` (rectangle = skill call) has its label exactly equal to some `skills_used[].skill` entry on the same flow. Mismatches mean either the skill list is wrong or the workflow references something that should be added.
4. Every edge endpoint is a declared node id.
5. The block parses (rule 8 covers all mermaid blocks; rule 17 narrows to skill-id correspondence).

Failure messages:

```
flows/<id>.md: rule 17 — workflow node "s_foo[do-foo]" references skill "do-foo" not in skills_used[]
flows/<id>.md: rule 17 — workflow edge from "s_a" to "s_b" but "s_b" is not declared
flows/<id>.md: rule 17 — workflow field missing from frontmatter
```
````

- [ ] **Step 4: Commit**

```bash
git add bbc-discovery/flow-map-compiler/skills/flow-map-compiler/references/lint-contract.md
git commit -m "feat(flow-map-compiler): add lint rule 17 — workflow well-formedness"
```

---

## Task P0.4: Update SKILL.md with workflow derivation guidance

**Files:**
- Modify: `bbc-discovery/flow-map-compiler/skills/flow-map-compiler/SKILL.md`

- [ ] **Step 1: Locate the `### 5. Render` section**

```bash
grep -n "### 5. Render" bbc-discovery/flow-map-compiler/skills/flow-map-compiler/SKILL.md
```

- [ ] **Step 2: Append a "Deriving the `workflow:` field" subsection**

Inside `### 5. Render`, after the bullet list of generated files (the bullet ending `- tools-proposed.json — bidirectional with capability frontmatter.`), insert:

````markdown
**Deriving the `workflow:` field on each flow:** read the entry file
plus its near transitive imports for control-flow signal. Translate
into a mermaid `flowchart TD` per `references/output-schemas.md`'s
"Workflow notation" subsection. Map call-site sequences to skill nodes
(`id[<skill-id>]`), early-return / guard checks to decision nodes
(`id{<question?>}`), and `Promise.all` / parallel awaits to a parallel
fanout (`id{{parallel}}` with `& joins`). Loops in the source
(while, for-each polling) become back-edges between existing nodes;
do not introduce a dedicated loop node.

When control flow can't be determined with `medium`+ confidence,
fall back to a linear chain through `skills_used` in declared
order, e.g.

```
flowchart TD
  start([start]) --> s_<id1>[<id1>] --> s_<id2>[<id2>] --> e([end])
```

Annotate the flow's `confidence:` field accordingly. Lint rule 17
will check that every skill node's label is a declared
`skills_used[].skill`.
````

- [ ] **Step 3: Commit**

```bash
git add bbc-discovery/flow-map-compiler/skills/flow-map-compiler/SKILL.md
git commit -m "feat(flow-map-compiler): SKILL.md guidance for deriving workflow:"
```

---

## Task P0.5: Re-render canonical fixtures

**Files:**
- Modify: `bbc-discovery/flow-map-compiler/skills/flow-map-compiler/tests/fixtures/sample-react/.flow-map/flows/update-profile.md`
- Modify: `bbc-discovery/flow-map-compiler/skills/flow-map-compiler/tests/fixtures/sample-nextjs/.flow-map/flows/update-profile.md`
- Modify: `bbc-discovery/flow-map-compiler/skills/flow-map-compiler/tests/fixtures/sample-sveltekit/.flow-map/flows/view-home.md`

- [ ] **Step 1: Read sample-react's update-profile to find the frontmatter end**

```bash
sed -n '1,30p' bbc-discovery/flow-map-compiler/skills/flow-map-compiler/tests/fixtures/sample-react/.flow-map/flows/update-profile.md
```

The frontmatter ends with `confidence:` followed by `---` then a blank line.

- [ ] **Step 2: Insert `workflow:` into sample-react's update-profile**

In `tests/fixtures/sample-react/.flow-map/flows/update-profile.md`, find the `confidence: ...` line followed by `---`. Insert before the `---` so the frontmatter ends:

```yaml
confidence: high
workflow: |
  flowchart TD
    start([start]) --> s_read_self_profile[read-self-profile]
    s_read_self_profile --> d{user provided new value?}
    d -- no --> e([end])
    d -- yes --> s_write_self_profile[write-self-profile]
    s_write_self_profile --> e
---
```

(Adjust the `confidence:` value to match what's already in the file.)

- [ ] **Step 3: Same insertion for sample-nextjs's update-profile**

The nextjs fixture has only one skill (`update-user-record`). In `tests/fixtures/sample-nextjs/.flow-map/flows/update-profile.md`, before the closing `---`:

```yaml
workflow: |
  flowchart TD
    start([start]) --> s_update_user_record[update-user-record]
    s_update_user_record --> e([end])
```

- [ ] **Step 4: Same insertion for sample-sveltekit's view-home**

This fixture has only one skill (`list-ping`). In `tests/fixtures/sample-sveltekit/.flow-map/flows/view-home.md`, before the closing `---`:

```yaml
workflow: |
  flowchart TD
    start([start]) --> s_list_ping[list-ping] --> e([end])
```

- [ ] **Step 5: Verify the three files parse as YAML**

```bash
for f in \
  bbc-discovery/flow-map-compiler/skills/flow-map-compiler/tests/fixtures/sample-react/.flow-map/flows/update-profile.md \
  bbc-discovery/flow-map-compiler/skills/flow-map-compiler/tests/fixtures/sample-nextjs/.flow-map/flows/update-profile.md \
  bbc-discovery/flow-map-compiler/skills/flow-map-compiler/tests/fixtures/sample-sveltekit/.flow-map/flows/view-home.md ; do
  python3 -c "
import sys, yaml
with open('$f') as fh:
    txt = fh.read()
fm = txt.split('---', 2)[1]
y = yaml.safe_load(fm)
assert 'workflow' in y, '$f missing workflow'
assert y['workflow'].startswith('flowchart TD'), '$f workflow does not start with flowchart TD'
print('OK:', '$f')
"
done
```

Expected: three `OK: ...` lines.

- [ ] **Step 6: Commit**

```bash
git add bbc-discovery/flow-map-compiler/skills/flow-map-compiler/tests/fixtures/sample-react/.flow-map/flows/update-profile.md \
        bbc-discovery/flow-map-compiler/skills/flow-map-compiler/tests/fixtures/sample-nextjs/.flow-map/flows/update-profile.md \
        bbc-discovery/flow-map-compiler/skills/flow-map-compiler/tests/fixtures/sample-sveltekit/.flow-map/flows/view-home.md
git commit -m "feat(flow-map-compiler): add workflow: to canonical fixtures"
```

---

# Part 1 — open-bbcd PR1: ingest + read-only configurator

All paths in Part 1 are relative to repo root unless noted; commands run from `open-bbcd/` unless specified.

## Task 1: Migration 006 — `flow_map_config` JSONB; drop `wizard_input` / `schema_version`

**Files:**
- Create: `open-bbcd/migrations/006_add_flow_map_config.sql`

- [ ] **Step 1: Pre-flight — confirm pre-prod assumption**

```bash
cd open-bbcd && source .env
psql "$DATABASE_URL" -tAc "SELECT count(*) FROM agents WHERE wizard_input IS NOT NULL"
```

Expected: a small row count from local development, or `0`. **If this is non-trivial real data, STOP and update the migration to backfill before drop.** This plan assumes pre-prod (no production data). If the count is unexpected, surface to the user before continuing.

- [ ] **Step 2: Create the migration**

Create `open-bbcd/migrations/006_add_flow_map_config.sql`:

```sql
-- migrations/006_add_flow_map_config.sql

-- +goose Up
ALTER TABLE agents
  ADD COLUMN flow_map_config       JSONB,
  ADD COLUMN flow_map_parse_error  TEXT;

ALTER TABLE agents
  DROP COLUMN IF EXISTS wizard_input,
  DROP COLUMN IF EXISTS schema_version;

-- +goose Down
ALTER TABLE agents
  ADD COLUMN wizard_input   JSONB,
  ADD COLUMN schema_version VARCHAR(20);

ALTER TABLE agents
  DROP COLUMN IF EXISTS flow_map_config,
  DROP COLUMN IF EXISTS flow_map_parse_error;
```

- [ ] **Step 3: Apply the migration**

```bash
cd open-bbcd && make migrate-up
```

Expected: goose prints `OK 006_add_flow_map_config.sql`.

- [ ] **Step 4: Verify schema**

```bash
psql "$DATABASE_URL" -c "\d agents" | grep -E '(flow_map_config|flow_map_parse_error|wizard_input|schema_version)'
```

Expected: only `flow_map_config | jsonb` and `flow_map_parse_error | text` are present. `wizard_input` and `schema_version` should NOT appear.

- [ ] **Step 5: Commit**

```bash
git add open-bbcd/migrations/006_add_flow_map_config.sql
git commit -m "feat(open-bbcd): migration 006 — add flow_map_config JSONB; drop wizard_input"
```

---

## Task 2: Add `ErrFlowMapInvalid` sentinel and map it to 400

**Files:**
- Modify: `open-bbcd/internal/types/errors.go`
- Modify: `open-bbcd/internal/handler/handler.go`

- [ ] **Step 1: Add the sentinel error**

Append to `open-bbcd/internal/types/errors.go`'s `var (...)` block:

```go
	ErrFlowMapInvalid = errors.New("flow-map archive is invalid")
```

The full block now reads:

```go
var (
	ErrNameRequired   = errors.New("name is required")
	ErrPromptRequired = errors.New("prompt is required")
	ErrAgentRequired  = errors.New("agent_id is required")
	ErrNotFound       = errors.New("not found")

	ErrDiscoveryFileRequired     = errors.New("discovery file is required")
	ErrDiscoveryFileTooLarge     = errors.New("discovery file is too large")
	ErrDiscoveryFileBadExtension = errors.New("discovery file must be a .zip")

	ErrFlowMapInvalid = errors.New("flow-map archive is invalid")
)
```

- [ ] **Step 2: Map it to 400 in `handler/handler.go::Error`**

In `open-bbcd/internal/handler/handler.go`, extend the existing `case errors.Is(...)` for 400-class errors:

```go
case errors.Is(err, types.ErrNameRequired),
	errors.Is(err, types.ErrPromptRequired),
	errors.Is(err, types.ErrAgentRequired),
	errors.Is(err, types.ErrDiscoveryFileRequired),
	errors.Is(err, types.ErrDiscoveryFileTooLarge),
	errors.Is(err, types.ErrDiscoveryFileBadExtension),
	errors.Is(err, types.ErrFlowMapInvalid):
	status = http.StatusBadRequest
```

- [ ] **Step 3: Build to verify**

```bash
cd open-bbcd && go build ./...
```

Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/internal/types/errors.go open-bbcd/internal/handler/handler.go
git commit -m "feat(open-bbcd): ErrFlowMapInvalid sentinel mapped to 400"
```

---

## Task 3: Define `FlowMapConfig` types

**Files:**
- Create: `open-bbcd/internal/types/flow_map.go`
- Create: `open-bbcd/internal/types/flow_map_test.go`

- [ ] **Step 1: Write the failing test (round-trip JSON encode/decode)**

Create `open-bbcd/internal/types/flow_map_test.go`:

```go
package types_test

import (
	"encoding/json"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestFlowMapConfig_JSONRoundTrip(t *testing.T) {
	cfg := types.FlowMapConfig{
		SchemaVersion:   1,
		Name:            "test-agent",
		Scope:           "support",
		ShouldDo:        "answer",
		ShouldNotDo:     "guess",
		BusinessDomain:  "saas",
		Source: types.FlowMapSource{
			CompilerSchemaVersion: 1,
			GeneratedFromSHA:      "deadbeef",
			AppName:               "test-app",
			Stack:                 map[string]string{"framework": "react"},
		},
		Capabilities: []types.Capability{
			{
				Name:    "users",
				Summary: "user resource",
				Tools:   []map[string]any{{"tool": "users.getMe", "method": "GET"}},
				ProseMD: "# Users",
			},
		},
		Skills: []types.Skill{
			{
				ID: "read-self-profile", Origin: "discovered",
				Name: "Read self profile", Role: "read",
				CapabilityRef: "users", External: false,
				ProposedTool: "users.getMe",
				ProseMD:      "# Read self profile",
				UserPhrases:  []string{"who am I"},
			},
		},
		Flows: []types.Flow{
			{
				ID: "update-profile", Origin: "discovered", Included: true,
				Name: "Update profile", Confidence: "high",
				UserPhrases:    []string{"change my email"},
				Preconditions:  []string{"signed in"},
				Postconditions: []string{"profile saved"},
				SideEffects:    []string{"audit-log-entry"},
				Workflow: types.Workflow{
					Mermaid: "flowchart TD\n  start([start]) --> s_x[read-self-profile] --> e([end])",
					Layout:  map[string]types.Position{"start": {X: 40, Y: 40}},
				},
				ProseMD: "# Update profile",
			},
		},
	}

	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded types.FlowMapConfig
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Name != cfg.Name {
		t.Errorf("Name = %q, want %q", decoded.Name, cfg.Name)
	}
	if len(decoded.Flows) != 1 || decoded.Flows[0].Workflow.Mermaid != cfg.Flows[0].Workflow.Mermaid {
		t.Errorf("Workflow mermaid not preserved: %+v", decoded.Flows[0].Workflow)
	}
	if decoded.Flows[0].Workflow.Layout["start"].X != 40 {
		t.Errorf("layout x = %d, want 40", decoded.Flows[0].Workflow.Layout["start"].X)
	}
}
```

- [ ] **Step 2: Run test — expect FAIL (type undefined)**

```bash
cd open-bbcd && go test -v ./internal/types -run TestFlowMapConfig_JSONRoundTrip
```

Expected: build error, `undefined: types.FlowMapConfig`.

- [ ] **Step 3: Implement the types**

Create `open-bbcd/internal/types/flow_map.go`:

```go
package types

// FlowMapConfig is the agent's full configuration: phase-1 wizard fields
// at root, plus the parsed and edited discovery snapshot. Stored in the
// agents.flow_map_config JSONB column; rendered as YAML on demand.
type FlowMapConfig struct {
	SchemaVersion int `json:"schema_version" yaml:"schema_version"`

	// Phase-1 wizard answers.
	Name           string `json:"name" yaml:"name"`
	Scope          string `json:"scope,omitempty" yaml:"scope,omitempty"`
	ShouldDo       string `json:"should_do,omitempty" yaml:"should_do,omitempty"`
	ShouldNotDo    string `json:"should_not_do,omitempty" yaml:"should_not_do,omitempty"`
	BusinessDomain string `json:"business_domain,omitempty" yaml:"business_domain,omitempty"`

	// Phase-2 discovery snapshot.
	Source       FlowMapSource `json:"source" yaml:"source"`
	Capabilities []Capability  `json:"capabilities" yaml:"capabilities"`
	Skills       []Skill       `json:"skills" yaml:"skills"`
	Flows        []Flow        `json:"flows" yaml:"flows"`
}

type FlowMapSource struct {
	CompilerSchemaVersion int               `json:"compiler_schema_version" yaml:"compiler_schema_version"`
	GeneratedFromSHA      string            `json:"generated_from_sha" yaml:"generated_from_sha"`
	AppName               string            `json:"app_name" yaml:"app_name"`
	Stack                 map[string]string `json:"stack,omitempty" yaml:"stack,omitempty"`
}

type Capability struct {
	Name    string           `json:"name" yaml:"name"`
	Summary string           `json:"summary,omitempty" yaml:"summary,omitempty"`
	Tools   []map[string]any `json:"tools,omitempty" yaml:"tools,omitempty"`
	ProseMD string           `json:"prose_md,omitempty" yaml:"prose_md,omitempty"`
}

type Skill struct {
	ID            string   `json:"id" yaml:"id"`
	Origin        string   `json:"origin" yaml:"origin"` // "discovered" | "custom"
	Name          string   `json:"name" yaml:"name"`
	Description   string   `json:"description,omitempty" yaml:"description,omitempty"`
	UserPhrases   []string `json:"user_phrases,omitempty" yaml:"user_phrases,omitempty"`
	Role          string   `json:"role" yaml:"role"` // "read" | "write"
	CapabilityRef string   `json:"capability_ref,omitempty" yaml:"capability_ref,omitempty"`
	External      bool     `json:"external" yaml:"external"`
	ExternalNote  string   `json:"external_note,omitempty" yaml:"external_note,omitempty"`
	ProposedTool  string   `json:"proposed_tool,omitempty" yaml:"proposed_tool,omitempty"`
	ProseMD       string   `json:"prose_md,omitempty" yaml:"prose_md,omitempty"`
}

type Flow struct {
	ID             string   `json:"id" yaml:"id"`
	Origin         string   `json:"origin" yaml:"origin"`
	Included       bool     `json:"included" yaml:"included"`
	Name           string   `json:"name" yaml:"name"`
	Description    string   `json:"description,omitempty" yaml:"description,omitempty"`
	Intent         string   `json:"intent,omitempty" yaml:"intent,omitempty"`
	UserPhrases    []string `json:"user_phrases,omitempty" yaml:"user_phrases,omitempty"`
	Preconditions  []string `json:"preconditions,omitempty" yaml:"preconditions,omitempty"`
	Postconditions []string `json:"postconditions,omitempty" yaml:"postconditions,omitempty"`
	SideEffects    []string `json:"side_effects,omitempty" yaml:"side_effects,omitempty"`
	Confidence     string   `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	Workflow       Workflow `json:"workflow" yaml:"workflow"`
	ProseMD        string   `json:"prose_md,omitempty" yaml:"prose_md,omitempty"`
}

type Workflow struct {
	Mermaid string              `json:"mermaid" yaml:"mermaid"`
	Layout  map[string]Position `json:"layout,omitempty" yaml:"layout,omitempty"`
}

type Position struct {
	X int `json:"x" yaml:"x"`
	Y int `json:"y" yaml:"y"`
}
```

- [ ] **Step 4: Run test — expect PASS**

```bash
cd open-bbcd && go test -v ./internal/types -run TestFlowMapConfig_JSONRoundTrip
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add open-bbcd/internal/types/flow_map.go open-bbcd/internal/types/flow_map_test.go
git commit -m "feat(open-bbcd): types.FlowMapConfig and nested types"
```

---

## Task 4: Update `types.Agent` to drop `WizardInput` / `SchemaVersion`, add `FlowMapConfig`

**Files:**
- Modify: `open-bbcd/internal/types/agent.go`

- [ ] **Step 1: Update the `Agent` struct**

In `open-bbcd/internal/types/agent.go`, replace the `Agent` struct with:

```go
type Agent struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Description       string          `json:"description,omitempty"`
	Prompt            string          `json:"prompt"`
	Status            string          `json:"status"`
	ParentVersionID   *string         `json:"parent_version_id,omitempty"`
	FlowMapConfig     json.RawMessage `json:"flow_map_config,omitempty"`
	FlowMapParseError string          `json:"flow_map_parse_error,omitempty"`
	DiscoveryFilePath string          `json:"discovery_file_path,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}
```

(Removed: `WizardInput`, `SchemaVersion`. Added: `FlowMapConfig`, `FlowMapParseError`.)

- [ ] **Step 2: Update `CreateAgentFromWizardOpts`**

Replace `CreateAgentFromWizardOpts` with:

```go
type CreateAgentFromWizardOpts struct {
	ID                string
	Name              string
	FlowMapConfig     json.RawMessage // pre-marshaled JSONB; nil if parse failed
	FlowMapParseError string          // empty when parse succeeded
	DiscoveryFilePath string
}
```

(Removed: `WizardInput`, `SchemaVersion`. Added: `FlowMapConfig`, `FlowMapParseError`.)

- [ ] **Step 3: Build to verify**

```bash
cd open-bbcd && go build ./...
```

Expected: build errors in `internal/handler/wizard.go` and `internal/repository/agent.go` referencing the removed fields. That's the next two tasks. Don't try to fix here.

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/internal/types/agent.go
git commit -m "feat(open-bbcd): Agent + opts use FlowMapConfig instead of WizardInput"
```

---

## Task 5: Test fixture — minimal `.flow-map/` directory tree

**Files:**
- Create: `open-bbcd/internal/flowmap/testdata/sample-flowmap/AGENTS.md`
- Create: `open-bbcd/internal/flowmap/testdata/sample-flowmap/APP.md`
- Create: `open-bbcd/internal/flowmap/testdata/sample-flowmap/glossary.md`
- Create: `open-bbcd/internal/flowmap/testdata/sample-flowmap/tools-proposed.json`
- Create: `open-bbcd/internal/flowmap/testdata/sample-flowmap/flows/place-order.md`
- Create: `open-bbcd/internal/flowmap/testdata/sample-flowmap/skills/place-order.md`
- Create: `open-bbcd/internal/flowmap/testdata/sample-flowmap/capabilities/orders.md`

The fixture is a tiny but realistic `.flow-map/` tree the parser test will zip on the fly. One flow, one skill, one capability. Includes the new `workflow:` field so the parser's structured path is exercised.

- [ ] **Step 1: Create AGENTS.md (frontmatter only — body is irrelevant for the parser)**

Create `open-bbcd/internal/flowmap/testdata/sample-flowmap/AGENTS.md`:

```markdown
---
schema_version: 1
generated_by: flow-map-compiler
generated_at: 2026-05-07T00:00:00+00:00
generated_from_sha: deadbeef
app_name: sample-flowmap
stack:
  framework: react
  router: react-router-dom
  language: ts
counts:
  skills: 1
  flows: 1
  capabilities: 1
  proposed_tools: 1
freshness:
  last_verified: 2026-05-07T00:00:00+00:00
  staleness_check: weekly
files:
  app_context: APP.md
  glossary: glossary.md
  skills_dir: skills/
  flows_dir: flows/
  capabilities_dir: capabilities/
  proposed_tools: tools-proposed.json
---

# sample-flowmap — flow map

Stub body for parser tests.
```

- [ ] **Step 2: Create APP.md**

Create `open-bbcd/internal/flowmap/testdata/sample-flowmap/APP.md`:

```markdown
---
schema_version: 1
framework:
  name: react
  version: "18"
  router: react-router-dom
api_clients: [fetch]
api_base_url:
  source: hardcoded
  name: null
  default: "/api"
auth:
  type: bearer
  token_source: localStorage.token
  refresh: none
providers: []
---

# App context

Stub.
```

- [ ] **Step 3: Create glossary.md**

Create `open-bbcd/internal/flowmap/testdata/sample-flowmap/glossary.md`:

```markdown
---
schema_version: 1
---

# Glossary

| Skill | User phrases | Capability | Proposed tool |
|---|---|---|---|
| [place-order](skills/place-order.md) | "check out" | [orders-create](capabilities/orders.md#orders-create) | `orders.create` |
```

- [ ] **Step 4: Create tools-proposed.json**

Create `open-bbcd/internal/flowmap/testdata/sample-flowmap/tools-proposed.json`:

```json
{
  "schema_version": 1,
  "generated_by": "flow-map-compiler",
  "generated_at": "2026-05-07T00:00:00+00:00",
  "generated_from_sha": "deadbeef",
  "naming_convention": "dotted-lower-camel",
  "tools": [
    {
      "proposed_name": "orders.create",
      "method": "POST",
      "path": "/api/orders",
      "auth": "bearer",
      "capability_file": "capabilities/orders.md",
      "anchor": "orders-create",
      "used_by_flows": ["place-order"],
      "confidence": "high"
    }
  ],
  "unresolved": []
}
```

- [ ] **Step 5: Create flows/place-order.md (with workflow: field)**

Create `open-bbcd/internal/flowmap/testdata/sample-flowmap/flows/place-order.md`:

```markdown
---
schema_version: 1
id: place-order
name: Place order
description: "Use when the user wants to check out"
intent: "Submit the cart as an order"
user_phrases:
  - "check out"
entry: src/pages/Cart.tsx
trigger: user clicks Place order
preconditions:
  - User is signed in
skills_used:
  - skill: place-order
    role: write
    skill_ref: ../skills/place-order.md
postconditions:
  - The cart is persisted as an order
side_effects: [audit-log-entry]
related_flows: []
confidence: high
workflow: |
  flowchart TD
    start([start]) --> s_place_order[place-order]
    s_place_order --> e([end])
---

# Place order

<!-- AGENT id="prose" -->
The user submits the cart.
<!-- /AGENT -->

## How the agent handles this

1. Confirm signed in.
2. Submit cart via place-order.
```

- [ ] **Step 6: Create skills/place-order.md**

Create `open-bbcd/internal/flowmap/testdata/sample-flowmap/skills/place-order.md`:

```markdown
---
schema_version: 1
id: place-order
name: Place order
description: "Use when the user wants to convert their cart"
user_phrases:
  - "check out"
role: write
capability_ref: capabilities/orders.md#orders-create
proposed_tool: orders.create
flows_using_this: [place-order]
confidence: high
---

# Place order

<!-- AGENT id="overview" -->
Submit the cart as an order.
<!-- /AGENT -->
```

- [ ] **Step 7: Create capabilities/orders.md**

Create `open-bbcd/internal/flowmap/testdata/sample-flowmap/capabilities/orders.md`:

```markdown
---
schema_version: 1
capability: orders
summary: "Create orders"
tools:
  - tool: orders.create
    proposed: true
    does: "Create a new order"
    method: POST
    path: "/api/orders"
    auth: bearer
    confidence: high
    source: src/api/orders.ts:32
flows_using_this: [place-order]
---

# Orders

<!-- AGENT id="overview" -->
The orders capability.
<!-- /AGENT -->
```

- [ ] **Step 8: Commit**

```bash
git add open-bbcd/internal/flowmap/testdata/
git commit -m "test(open-bbcd): minimal .flow-map fixture for flowmap parser tests"
```

---

## Task 6: `internal/flowmap` parser — happy path

**Files:**
- Create: `open-bbcd/internal/flowmap/parser.go`
- Create: `open-bbcd/internal/flowmap/parser_test.go`

- [ ] **Step 1: Write a failing happy-path test**

Create `open-bbcd/internal/flowmap/parser_test.go`:

```go
package flowmap_test

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/flowmap"
)

// zipDir walks dir and returns an in-memory zip with paths relative to dir.
// Mirrors what r.FormFile receives at runtime.
func zipDir(t *testing.T, dir string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		fw, err := w.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		t.Fatalf("zipDir: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zipDir close: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestParse_HappyPath(t *testing.T) {
	r := zipDir(t, "testdata/sample-flowmap")

	cfg, err := flowmap.Parse(r)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if cfg.Source.AppName != "sample-flowmap" {
		t.Errorf("AppName = %q, want sample-flowmap", cfg.Source.AppName)
	}
	if len(cfg.Flows) != 1 {
		t.Fatalf("Flows = %d, want 1", len(cfg.Flows))
	}
	flow := cfg.Flows[0]
	if flow.ID != "place-order" {
		t.Errorf("Flow.ID = %q, want place-order", flow.ID)
	}
	if !flow.Included {
		t.Error("Flow.Included should default to true")
	}
	if flow.Origin != "discovered" {
		t.Errorf("Flow.Origin = %q, want discovered", flow.Origin)
	}
	if !strings.HasPrefix(flow.Workflow.Mermaid, "flowchart TD") {
		t.Errorf("Flow.Workflow.Mermaid does not start with flowchart TD: %q", flow.Workflow.Mermaid)
	}
	if !strings.Contains(flow.ProseMD, "How the agent handles this") {
		t.Errorf("Flow.ProseMD should preserve the body: %q", flow.ProseMD[:80])
	}

	if len(cfg.Skills) != 1 || cfg.Skills[0].ID != "place-order" {
		t.Errorf("Skills = %+v", cfg.Skills)
	}
	if cfg.Skills[0].CapabilityRef != "orders" {
		t.Errorf("Skill.CapabilityRef = %q, want orders (resolved from capabilities/orders.md#orders-create)", cfg.Skills[0].CapabilityRef)
	}

	if len(cfg.Capabilities) != 1 || cfg.Capabilities[0].Name != "orders" {
		t.Errorf("Capabilities = %+v", cfg.Capabilities)
	}
}
```

- [ ] **Step 2: Run test — expect FAIL (package missing)**

```bash
cd open-bbcd && go test -v ./internal/flowmap -run TestParse_HappyPath
```

Expected: build error, `package flowmap is not in std`.

- [ ] **Step 3: Implement the parser**

Create `open-bbcd/internal/flowmap/parser.go`:

```go
// Package flowmap parses a .flow-map zip into a structured types.FlowMapConfig.
package flowmap

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"gopkg.in/yaml.v3"
)

// Parse reads a .flow-map zip and returns a populated FlowMapConfig.
// Phase-1 fields (Name, Scope, etc.) are NOT set here — callers
// (the wizard handler) merge those in from the form values.
func Parse(r io.Reader) (types.FlowMapConfig, error) {
	cfg := types.FlowMapConfig{SchemaVersion: 1}

	body, err := io.ReadAll(r)
	if err != nil {
		return cfg, fmt.Errorf("%w: read upload: %v", types.ErrFlowMapInvalid, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return cfg, fmt.Errorf("%w: not a zip: %v", types.ErrFlowMapInvalid, err)
	}

	files := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// Strip a leading "<root>/" if the zip wrapped contents in a
		// top-level directory (some users zip the parent directory).
		key := stripLeadingDir(f.Name)
		fc, err := f.Open()
		if err != nil {
			return cfg, fmt.Errorf("%w: open %s: %v", types.ErrFlowMapInvalid, f.Name, err)
		}
		b, err := io.ReadAll(fc)
		fc.Close()
		if err != nil {
			return cfg, fmt.Errorf("%w: read %s: %v", types.ErrFlowMapInvalid, f.Name, err)
		}
		files[key] = b
	}

	required := []string{"AGENTS.md", "APP.md", "glossary.md", "tools-proposed.json"}
	for _, rq := range required {
		if _, ok := files[rq]; !ok {
			return cfg, fmt.Errorf("%w: missing %s", types.ErrFlowMapInvalid, rq)
		}
	}

	if err := parseAgentsMD(files["AGENTS.md"], &cfg); err != nil {
		return cfg, err
	}
	if err := parseToolsProposed(files["tools-proposed.json"]); err != nil {
		return cfg, err
	}

	for name, b := range files {
		switch {
		case strings.HasPrefix(name, "capabilities/") && strings.HasSuffix(name, ".md"):
			cap, err := parseCapability(name, b)
			if err != nil {
				return cfg, err
			}
			cfg.Capabilities = append(cfg.Capabilities, cap)
		case strings.HasPrefix(name, "skills/") && strings.HasSuffix(name, ".md"):
			sk, err := parseSkill(name, b)
			if err != nil {
				return cfg, err
			}
			cfg.Skills = append(cfg.Skills, sk)
		case strings.HasPrefix(name, "flows/") && strings.HasSuffix(name, ".md"):
			fl, err := parseFlow(name, b)
			if err != nil {
				return cfg, err
			}
			cfg.Flows = append(cfg.Flows, fl)
		}
	}

	if err := validate(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func stripLeadingDir(p string) string {
	p = path.Clean(p)
	if strings.HasPrefix(p, ".flow-map/") {
		return strings.TrimPrefix(p, ".flow-map/")
	}
	// If the zip's first segment is unique and non-canonical, leave as-is —
	// the required-files check will catch it.
	return p
}

func splitFrontmatter(b []byte) (front []byte, body []byte, err error) {
	s := string(b)
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return nil, nil, fmt.Errorf("missing leading --- frontmatter delimiter")
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(s, "---\r\n"), "---\n")
	idx := strings.Index(rest, "\n---")
	if idx == -1 {
		return nil, nil, fmt.Errorf("missing closing --- frontmatter delimiter")
	}
	frontStr := rest[:idx]
	bodyStr := strings.TrimPrefix(strings.TrimPrefix(rest[idx+len("\n---"):], "\r\n"), "\n")
	return []byte(frontStr), []byte(bodyStr), nil
}

func parseAgentsMD(b []byte, cfg *types.FlowMapConfig) error {
	front, _, err := splitFrontmatter(b)
	if err != nil {
		return fmt.Errorf("%w: AGENTS.md: %v", types.ErrFlowMapInvalid, err)
	}
	var fm struct {
		SchemaVersion    int               `yaml:"schema_version"`
		GeneratedFromSHA string            `yaml:"generated_from_sha"`
		AppName          string            `yaml:"app_name"`
		Stack            map[string]string `yaml:"stack"`
	}
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return fmt.Errorf("%w: AGENTS.md frontmatter: %v", types.ErrFlowMapInvalid, err)
	}
	cfg.Source = types.FlowMapSource{
		CompilerSchemaVersion: fm.SchemaVersion,
		GeneratedFromSHA:      fm.GeneratedFromSHA,
		AppName:               fm.AppName,
		Stack:                 fm.Stack,
	}
	return nil
}

// parseToolsProposed validates the JSON shape minimally; the file is not
// folded into FlowMapConfig (capabilities[].tools carries the same data).
func parseToolsProposed(b []byte) error {
	var v struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("%w: tools-proposed.json: %v", types.ErrFlowMapInvalid, err)
	}
	return nil
}

func parseCapability(name string, b []byte) (types.Capability, error) {
	front, body, err := splitFrontmatter(b)
	if err != nil {
		return types.Capability{}, fmt.Errorf("%w: %s: %v", types.ErrFlowMapInvalid, name, err)
	}
	var fm struct {
		Capability string           `yaml:"capability"`
		Summary    string           `yaml:"summary"`
		Tools      []map[string]any `yaml:"tools"`
	}
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return types.Capability{}, fmt.Errorf("%w: %s frontmatter: %v", types.ErrFlowMapInvalid, name, err)
	}
	return types.Capability{
		Name:    fm.Capability,
		Summary: fm.Summary,
		Tools:   fm.Tools,
		ProseMD: string(body),
	}, nil
}

func parseSkill(name string, b []byte) (types.Skill, error) {
	front, body, err := splitFrontmatter(b)
	if err != nil {
		return types.Skill{}, fmt.Errorf("%w: %s: %v", types.ErrFlowMapInvalid, name, err)
	}
	var fm struct {
		ID            string   `yaml:"id"`
		Name          string   `yaml:"name"`
		Description   string   `yaml:"description"`
		UserPhrases   []string `yaml:"user_phrases"`
		Role          string   `yaml:"role"`
		CapabilityRef string   `yaml:"capability_ref"`
		ProposedTool  string   `yaml:"proposed_tool"`
	}
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return types.Skill{}, fmt.Errorf("%w: %s frontmatter: %v", types.ErrFlowMapInvalid, name, err)
	}
	// capability_ref in skill is "capabilities/<name>.md#<anchor>"; we keep
	// only the <name> part for FlowMapConfig (the anchor is implicit).
	cap := fm.CapabilityRef
	if i := strings.Index(cap, "/"); i >= 0 {
		cap = cap[i+1:]
	}
	if i := strings.Index(cap, "."); i >= 0 {
		cap = cap[:i]
	}
	return types.Skill{
		ID:            fm.ID,
		Origin:        "discovered",
		Name:          fm.Name,
		Description:   fm.Description,
		UserPhrases:   fm.UserPhrases,
		Role:          fm.Role,
		CapabilityRef: cap,
		External:      false,
		ProposedTool:  fm.ProposedTool,
		ProseMD:       string(body),
	}, nil
}

func parseFlow(name string, b []byte) (types.Flow, error) {
	front, body, err := splitFrontmatter(b)
	if err != nil {
		return types.Flow{}, fmt.Errorf("%w: %s: %v", types.ErrFlowMapInvalid, name, err)
	}
	var fm struct {
		ID             string   `yaml:"id"`
		Name           string   `yaml:"name"`
		Description    string   `yaml:"description"`
		Intent         string   `yaml:"intent"`
		UserPhrases    []string `yaml:"user_phrases"`
		Preconditions  []string `yaml:"preconditions"`
		Postconditions []string `yaml:"postconditions"`
		SideEffects    []string `yaml:"side_effects"`
		Confidence     string   `yaml:"confidence"`
		Workflow       string   `yaml:"workflow"`
		SkillsUsed     []struct {
			Skill string `yaml:"skill"`
			Role  string `yaml:"role"`
		} `yaml:"skills_used"`
	}
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return types.Flow{}, fmt.Errorf("%w: %s frontmatter: %v", types.ErrFlowMapInvalid, name, err)
	}

	wf := strings.TrimSpace(fm.Workflow)
	if wf == "" {
		// Fallback: derive a linear chain through skills_used.
		wf = linearFallback(fm.SkillsUsed)
	}

	return types.Flow{
		ID:             fm.ID,
		Origin:         "discovered",
		Included:       true,
		Name:           fm.Name,
		Description:    fm.Description,
		Intent:         fm.Intent,
		UserPhrases:    fm.UserPhrases,
		Preconditions:  fm.Preconditions,
		Postconditions: fm.Postconditions,
		SideEffects:    fm.SideEffects,
		Confidence:     fm.Confidence,
		Workflow: types.Workflow{
			Mermaid: wf,
			Layout:  map[string]types.Position{},
		},
		ProseMD: string(body),
	}, nil
}

// linearFallback emits a deterministic mermaid flowchart connecting all
// skills_used entries in declared order: start → s_<id1> → s_<id2> → ... → end.
func linearFallback(skills []struct {
	Skill string `yaml:"skill"`
	Role  string `yaml:"role"`
}) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	prev := "start"
	b.WriteString("  start([start])")
	for _, s := range skills {
		nodeID := "s_" + strings.ReplaceAll(s.Skill, "-", "_")
		fmt.Fprintf(&b, " --> %s[%s]", nodeID, s.Skill)
		prev = nodeID
		b.WriteString("\n  ")
		b.WriteString(prev)
	}
	b.WriteString(" --> e([end])\n")
	return b.String()
}

// validate runs cross-reference checks: every skill's capability_ref must
// resolve to a discovered capability; every flow's workflow skill nodes
// must resolve to a discovered skill (delegated to mermaid.go).
func validate(cfg *types.FlowMapConfig) error {
	caps := make(map[string]struct{}, len(cfg.Capabilities))
	for _, c := range cfg.Capabilities {
		caps[c.Name] = struct{}{}
	}
	for _, s := range cfg.Skills {
		if s.External || s.CapabilityRef == "" {
			continue
		}
		if _, ok := caps[s.CapabilityRef]; !ok {
			return fmt.Errorf("%w: skill %q references unknown capability %q", types.ErrFlowMapInvalid, s.ID, s.CapabilityRef)
		}
	}

	skillIDs := make(map[string]struct{}, len(cfg.Skills))
	for _, s := range cfg.Skills {
		skillIDs[s.ID] = struct{}{}
	}
	for _, f := range cfg.Flows {
		if err := validateWorkflowSkillRefs(f.Workflow.Mermaid, skillIDs); err != nil {
			return fmt.Errorf("%w: flow %q: %v", types.ErrFlowMapInvalid, f.ID, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test — still failing (mermaid.go undefined)**

```bash
cd open-bbcd && go test -v ./internal/flowmap -run TestParse_HappyPath
```

Expected: build error, `undefined: validateWorkflowSkillRefs`. That's the next task.

- [ ] **Step 5: Commit (will not yet build; we commit at end of next task)**

Skip the commit; we'll commit parser + mermaid validator together at the end of Task 7.

---

## Task 7: `internal/flowmap` mermaid validator (PR1: validation only)

**Files:**
- Create: `open-bbcd/internal/flowmap/mermaid.go`
- Create: `open-bbcd/internal/flowmap/mermaid_test.go`

PR3 will add a full mermaid `flowchart` round-trip parser/serializer. PR1 only needs to validate that every `id[<skill-id>]` skill node's label resolves to a known skill.

- [ ] **Step 1: Write a failing test for the validator**

Create `open-bbcd/internal/flowmap/mermaid_test.go`:

```go
package flowmap

import (
	"errors"
	"testing"
)

func TestValidateWorkflowSkillRefs(t *testing.T) {
	skills := map[string]struct{}{"place-order": {}, "read-self-profile": {}}

	tests := []struct {
		name    string
		mermaid string
		wantErr bool
	}{
		{
			name: "happy path with valid skill nodes",
			mermaid: "flowchart TD\n" +
				"  start([start]) --> s_a[place-order]\n" +
				"  s_a --> e([end])",
			wantErr: false,
		},
		{
			name: "skill node references unknown skill",
			mermaid: "flowchart TD\n" +
				"  start([start]) --> s_a[ghost-skill]\n" +
				"  s_a --> e([end])",
			wantErr: true,
		},
		{
			name: "non-skill nodes are not validated against the skill set",
			mermaid: "flowchart TD\n" +
				"  start([start]) --> d{cart empty?}\n" +
				"  d -- no --> s_a[place-order]\n" +
				"  d -- yes --> e([end])\n" +
				"  s_a --> e",
			wantErr: false,
		},
		{
			name: "linear fallback shape",
			mermaid: "flowchart TD\n" +
				"  start([start]) --> s_place_order[place-order]\n" +
				"  s_place_order --> e([end])",
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkflowSkillRefs(tc.mermaid, skills)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, errUnknownSkill) && tc.wantErr {
				// Allow other validation errors too, but this is the main one.
			}
		})
	}
}
```

- [ ] **Step 2: Run test — expect FAIL (function undefined)**

```bash
cd open-bbcd && go test -v ./internal/flowmap -run TestValidateWorkflowSkillRefs
```

Expected: build error, `undefined: validateWorkflowSkillRefs`.

- [ ] **Step 3: Implement the validator**

Create `open-bbcd/internal/flowmap/mermaid.go`:

```go
package flowmap

import (
	"errors"
	"fmt"
	"regexp"
)

var errUnknownSkill = errors.New("unknown skill")

// skillNodeRe matches mermaid flowchart skill nodes of the form
// "<nodeId>[<skill-id>]". The captured group is the skill-id (the label
// between `[` and `]`). Skill nodes are rectangles by mermaid convention;
// `id([...])` (start/end stadium) and `id{...}` (decision diamond) are
// other shapes that are NOT skill references.
var skillNodeRe = regexp.MustCompile(`(?m)([A-Za-z_][A-Za-z0-9_]*)\[([^\]\[]+)\]`)

// validateWorkflowSkillRefs walks every "id[label]" rectangle node in
// the mermaid string and asserts that label is a key in the provided set.
func validateWorkflowSkillRefs(mermaid string, skills map[string]struct{}) error {
	matches := skillNodeRe.FindAllStringSubmatch(mermaid, -1)
	for _, m := range matches {
		label := m[2]
		if _, ok := skills[label]; !ok {
			return fmt.Errorf("%w: workflow node %q references skill %q which is not declared", errUnknownSkill, m[0], label)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run mermaid_test — expect PASS**

```bash
cd open-bbcd && go test -v ./internal/flowmap -run TestValidateWorkflowSkillRefs
```

Expected: PASS for all four subtests.

- [ ] **Step 5: Run parser_test — expect PASS**

```bash
cd open-bbcd && go test -v ./internal/flowmap -run TestParse_HappyPath
```

Expected: PASS.

- [ ] **Step 6: Add a missing-workflow fallback test**

Append to `open-bbcd/internal/flowmap/parser_test.go`:

```go
func TestParse_MissingWorkflowFallback(t *testing.T) {
	// Build an in-memory fixture variant: same as testdata/sample-flowmap
	// but the flow file's frontmatter has no `workflow:` field. Expect the
	// parser to emit a linear chain through skills_used.
	r := zipDirOverride(t, "testdata/sample-flowmap", map[string]string{
		"flows/place-order.md": `---
schema_version: 1
id: place-order
name: Place order
description: "Use when the user wants to check out"
intent: "Submit the cart as an order"
user_phrases:
  - "check out"
entry: src/pages/Cart.tsx
trigger: user clicks Place order
preconditions:
  - User is signed in
skills_used:
  - skill: place-order
    role: write
    skill_ref: ../skills/place-order.md
postconditions:
  - The cart is persisted as an order
side_effects: [audit-log-entry]
related_flows: []
confidence: high
---

# Place order

stub body
`,
	})

	cfg, err := flowmap.Parse(r)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wf := cfg.Flows[0].Workflow.Mermaid
	if !strings.Contains(wf, "s_place_order[place-order]") {
		t.Errorf("Fallback workflow does not contain expected linear node:\n%s", wf)
	}
}
```

And add the `zipDirOverride` helper at the top of the test file (just below `zipDir`):

```go
// zipDirOverride builds a zip from dir but replaces the bytes of files
// whose relative path matches a key in overrides.
func zipDirOverride(t *testing.T, dir string, overrides map[string]string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		fw, err := w.Create(relSlash)
		if err != nil {
			return err
		}
		if override, ok := overrides[relSlash]; ok {
			_, err = io.WriteString(fw, override)
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		t.Fatalf("zipDirOverride: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zipDirOverride close: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}
```

- [ ] **Step 7: Run the new test — expect PASS**

```bash
cd open-bbcd && go test -v ./internal/flowmap -run TestParse_MissingWorkflowFallback
```

Expected: PASS.

- [ ] **Step 8: Add an invalid-skill-ref test**

Append to `parser_test.go`:

```go
func TestParse_InvalidSkillReference(t *testing.T) {
	r := zipDirOverride(t, "testdata/sample-flowmap", map[string]string{
		"flows/place-order.md": `---
schema_version: 1
id: place-order
name: Place order
description: "Use when the user wants to check out"
intent: "Submit the cart as an order"
user_phrases: ["check out"]
preconditions: []
skills_used:
  - skill: place-order
    role: write
    skill_ref: ../skills/place-order.md
postconditions: []
side_effects: []
related_flows: []
confidence: high
workflow: |
  flowchart TD
    start([start]) --> s_x[ghost-skill]
    s_x --> e([end])
---

# Place order

stub
`,
	})

	_, err := flowmap.Parse(r)
	if err == nil {
		t.Fatal("Parse should fail when workflow references an unknown skill")
	}
	if !errors.Is(err, types.ErrFlowMapInvalid) {
		t.Errorf("err = %v, want errors.Is(types.ErrFlowMapInvalid)", err)
	}
}
```

Add the imports `"errors"` and `"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"` at the top of `parser_test.go` if not already present.

- [ ] **Step 9: Run the new test — expect PASS**

```bash
cd open-bbcd && go test -v ./internal/flowmap -run TestParse_InvalidSkillReference
```

Expected: PASS.

- [ ] **Step 10: Run all flowmap tests with `-race`**

```bash
cd open-bbcd && go test -race -v ./internal/flowmap/...
```

Expected: all PASS.

- [ ] **Step 11: Commit parser + mermaid validator together**

```bash
git add open-bbcd/internal/flowmap/parser.go \
        open-bbcd/internal/flowmap/parser_test.go \
        open-bbcd/internal/flowmap/mermaid.go \
        open-bbcd/internal/flowmap/mermaid_test.go
git commit -m "feat(open-bbcd): internal/flowmap parser + workflow skill-ref validator"
```

---

## Task 8: Update `repository.AgentRepository` for the new columns

**Files:**
- Modify: `open-bbcd/internal/repository/agent.go`
- Modify: `open-bbcd/internal/repository/agent_test.go` (only if it references the dropped columns)

- [ ] **Step 1: Replace `agentColumns`, `scanAgent`, `CreateFromWizard`**

In `open-bbcd/internal/repository/agent.go`:

Update `agentColumns`:

```go
const agentColumns = `id, name, description, prompt, status, parent_version_id, flow_map_config, flow_map_parse_error, discovery_file_path, created_at, updated_at`
```

Update `scanAgent`:

```go
func scanAgent(s scanner) (*types.Agent, error) {
	agent := &types.Agent{}
	var description sql.NullString
	var parentVersionID sql.NullString
	var flowMapConfig []byte
	var flowMapParseError sql.NullString
	var discoveryFilePath sql.NullString
	err := s.Scan(
		&agent.ID,
		&agent.Name,
		&description,
		&agent.Prompt,
		&agent.Status,
		&parentVersionID,
		&flowMapConfig,
		&flowMapParseError,
		&discoveryFilePath,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	agent.Description = description.String
	if parentVersionID.Valid {
		agent.ParentVersionID = &parentVersionID.String
	}
	agent.FlowMapConfig = flowMapConfig
	if flowMapParseError.Valid {
		agent.FlowMapParseError = flowMapParseError.String
	}
	if discoveryFilePath.Valid {
		agent.DiscoveryFilePath = discoveryFilePath.String
	}
	return agent, nil
}
```

Update `CreateFromWizard`:

```go
func (r *AgentRepository) CreateFromWizard(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, error) {
	if opts.Name == "" {
		return nil, types.ErrNameRequired
	}
	if r.db == nil {
		return nil, errors.New("repository: no database connection")
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO agents (id, name, prompt, status, flow_map_config, flow_map_parse_error, discovery_file_path)
		VALUES (COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()), $2, '', 'INITIALIZING', $3, NULLIF($4, ''), NULLIF($5, ''))
		RETURNING `+agentColumns,
		opts.ID, opts.Name, []byte(opts.FlowMapConfig), opts.FlowMapParseError, opts.DiscoveryFilePath,
	)
	return scanAgent(row)
}
```

Drop the unused `encoding/json` import — `json.Marshal` is gone (the handler pre-marshals).

- [ ] **Step 2: Add `UpdateFlowMapConfig` and `GetFlowMapConfig` for the configurator**

Append to `open-bbcd/internal/repository/agent.go`:

```go
// GetFlowMapConfig returns the agent's parsed config (or nil bytes if absent).
// Returns ErrNotFound if no agent has the given id.
func (r *AgentRepository) GetFlowMapConfig(ctx context.Context, agentID string) ([]byte, string, error) {
	var cfg []byte
	var parseErr sql.NullString
	row := r.db.QueryRowContext(ctx, `
		SELECT flow_map_config, flow_map_parse_error FROM agents WHERE id = $1
	`, agentID)
	if err := row.Scan(&cfg, &parseErr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", types.ErrNotFound
		}
		return nil, "", err
	}
	return cfg, parseErr.String, nil
}

// UpdateFlowMapConfig overwrites the agent's flow_map_config and clears any
// prior parse error. Used by configurator edit endpoints (PR2/PR3).
func (r *AgentRepository) UpdateFlowMapConfig(ctx context.Context, agentID string, cfg []byte) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE agents
		SET flow_map_config = $2, flow_map_parse_error = NULL, updated_at = now()
		WHERE id = $1
	`, agentID, cfg)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return types.ErrNotFound
	}
	return nil
}
```

- [ ] **Step 3: Build to verify**

```bash
cd open-bbcd && go build ./...
```

Expected: build errors only in `internal/handler/wizard.go` (still references the old fields). Repository compiles cleanly.

- [ ] **Step 4: Run repository tests**

```bash
cd open-bbcd && go test -race -v ./internal/repository/...
```

Expected: PASS, or — if any existing test references `WizardInput`/`SchemaVersion` — fix those references inline. The pre-existing tests are validation-pre-check style; they likely don't touch the dropped fields. If they do, swap to `FlowMapConfig`/`FlowMapParseError`.

- [ ] **Step 5: Commit**

```bash
git add open-bbcd/internal/repository/agent.go open-bbcd/internal/repository/agent_test.go
git commit -m "feat(open-bbcd): AgentRepository scans flow_map_config; new Update/Get methods"
```

---

## Task 9: Update `WizardHandler.Submit` — parse zip, persist config, redirect to `/configure`

**Files:**
- Modify: `open-bbcd/internal/handler/wizard.go`
- Modify: `open-bbcd/internal/handler/wizard_test.go`

- [ ] **Step 1: Rewrite `Submit` to parse the zip after storage**

In `open-bbcd/internal/handler/wizard.go`, change `Submit` to:

```go
func (h *WizardHandler) Submit(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > h.maxUploadBytes {
		Error(w, types.ErrDiscoveryFileTooLarge)
		return
	}
	if err := r.ParseMultipartForm(h.maxUploadBytes); err != nil {
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}

	fields := h.schema.OrderedFields()
	wizardInput := make(map[string]string, len(fields))
	agentID := uuid.NewString()
	var (
		discoveryKey string
		zipBytes     []byte
	)

	for _, of := range fields {
		if of.Field.Type == "file" {
			file, header, err := r.FormFile(of.Key)
			if err != nil {
				if of.Field.Required {
					Error(w, types.ErrDiscoveryFileRequired)
					return
				}
				continue
			}
			ext := strings.ToLower(filepath.Ext(header.Filename))
			if ext != ".zip" {
				file.Close()
				Error(w, types.ErrDiscoveryFileBadExtension)
				return
			}

			// Buffer the zip so we can both store it and parse it.
			b, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				log.Printf("wizard: read upload: %v", err)
				http.Error(w, "failed to read upload", http.StatusInternalServerError)
				return
			}
			zipBytes = b

			discoveryKey = agentID + ".zip"
			if err := h.store.Put(r.Context(), discoveryKey, bytes.NewReader(zipBytes)); err != nil {
				log.Printf("wizard: storage.Put %s: %v", discoveryKey, err)
				http.Error(w, "failed to save discovery file", http.StatusInternalServerError)
				return
			}
			continue
		}

		val := r.FormValue(of.Key)
		if of.Field.Required && val == "" {
			http.Error(w, of.Key+" is required", http.StatusBadRequest)
			return
		}
		wizardInput[of.Key] = val
	}

	// Parse the zip into a structured config; non-fatal — we record any
	// error on the row so the configurator page can surface it.
	cfg, parseErr := flowmap.Parse(bytes.NewReader(zipBytes))
	cfg.Name = wizardInput["name"]
	cfg.Scope = wizardInput["scope"]
	cfg.ShouldDo = wizardInput["should_do"]
	cfg.ShouldNotDo = wizardInput["should_not_do"]
	cfg.BusinessDomain = wizardInput["business_domain"]
	cfg.SchemaVersion = 1

	var (
		cfgJSON       json.RawMessage
		parseErrText  string
	)
	if parseErr != nil {
		parseErrText = parseErr.Error()
	} else {
		b, err := json.Marshal(cfg)
		if err != nil {
			log.Printf("wizard: marshal flow_map_config: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		cfgJSON = b
	}

	agent, err := h.agentRepo.CreateFromWizard(r.Context(), types.CreateAgentFromWizardOpts{
		ID:                agentID,
		Name:              wizardInput["name"],
		FlowMapConfig:     cfgJSON,
		FlowMapParseError: parseErrText,
		DiscoveryFilePath: discoveryKey,
	})
	if err != nil {
		if discoveryKey != "" {
			log.Printf("wizard: orphan discovery file %s after insert failure: %v", discoveryKey, err)
		} else {
			log.Printf("wizard: CreateFromWizard: %v", err)
		}
		Error(w, err)
		return
	}

	http.Redirect(w, r, "/agents/"+agent.ID+"/configure", http.StatusSeeOther)
}
```

- [ ] **Step 2: Update imports**

Replace the existing `import (...)` block in `wizard.go` with:

```go
import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/flowmap"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/storage"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)
```

- [ ] **Step 3: Build**

```bash
cd open-bbcd && go build ./...
```

Expected: clean build, or test compile errors in `wizard_test.go` referencing the old fields. Fix in next step.

- [ ] **Step 4: Update `wizard_test.go` for the new opts shape**

In `open-bbcd/internal/handler/wizard_test.go`:

In `TestWizardHandler_Submit_HappyPath`, replace the body of the assertion that inspects `capturedOpts`:

```go
	// 303 redirect goes to the configurator now.
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/agents/") || !strings.HasSuffix(loc, "/configure") {
		t.Errorf("Location = %q, want /agents/<id>/configure", loc)
	}
	if store.calls != 1 {
		t.Errorf("store.Put called %d times, want 1", store.calls)
	}
	if !strings.HasSuffix(capturedKey, ".zip") {
		t.Errorf("Put key = %q, want <uuid>.zip", capturedKey)
	}
	if capturedOpts.DiscoveryFilePath != capturedKey {
		t.Errorf("DiscoveryFilePath = %q, want %q", capturedOpts.DiscoveryFilePath, capturedKey)
	}
	// The zip body in the test is "zip body" — not a real zip — so parse fails.
	// We expect the row to be created with a non-empty FlowMapParseError and a nil FlowMapConfig.
	if capturedOpts.FlowMapParseError == "" {
		t.Error("FlowMapParseError should be set when zip body is invalid")
	}
	if capturedOpts.FlowMapConfig != nil {
		t.Errorf("FlowMapConfig should be nil on parse failure, got %s", string(capturedOpts.FlowMapConfig))
	}
```

Remove the test's reference to `capturedOpts.WizardInput[...]` (those fields no longer exist on opts).

- [ ] **Step 5: Add a happy-path test that uses a real zip**

Append to `wizard_test.go`:

```go
func TestWizardHandler_Submit_RealZip_HappyPath(t *testing.T) {
	// Build a real zip from the flowmap testdata.
	zipBytes := buildSampleFlowMapZip(t)

	var capturedOpts types.CreateAgentFromWizardOpts
	repo := &mockWizardRepo{
		createFromWizardFn: func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, error) {
			capturedOpts = opts
			return &types.Agent{ID: opts.ID, Name: opts.Name, Status: "INITIALIZING"}, nil
		},
	}
	store := &mockStorage{}
	h := newTestWizardHandler(t, repo, store)

	body, ct := buildWizardForm(t,
		map[string]string{"name": "agent", "scope": "support"},
		"flow-map.zip", zipBytes,
	)
	req := httptest.NewRequest(http.MethodPost, "/agents/wizard", body)
	req.Header.Set("Content-Type", ct)
	req.ContentLength = int64(body.Len())
	w := httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if capturedOpts.FlowMapParseError != "" {
		t.Errorf("FlowMapParseError = %q, want empty", capturedOpts.FlowMapParseError)
	}
	if len(capturedOpts.FlowMapConfig) == 0 {
		t.Fatal("FlowMapConfig should be populated")
	}
	// Sanity: decode it.
	var cfg types.FlowMapConfig
	if err := json.Unmarshal(capturedOpts.FlowMapConfig, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Name != "agent" || cfg.Scope != "support" {
		t.Errorf("phase-1 fields not folded into FlowMapConfig: %+v", cfg)
	}
	if len(cfg.Flows) != 1 {
		t.Errorf("Flows = %d, want 1", len(cfg.Flows))
	}
}

// buildSampleFlowMapZip returns a zip of internal/flowmap/testdata/sample-flowmap.
func buildSampleFlowMapZip(t *testing.T) []byte {
	t.Helper()
	root := "../flowmap/testdata/sample-flowmap"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		fw, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}
```

Add the imports `"archive/zip"`, `"encoding/json"`, `"os"`, `"path/filepath"` at the top of `wizard_test.go` if not already present.

- [ ] **Step 6: Run tests**

```bash
cd open-bbcd && go test -race -v ./internal/handler -run TestWizardHandler
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add open-bbcd/internal/handler/wizard.go open-bbcd/internal/handler/wizard_test.go
git commit -m "feat(open-bbcd): wizard.Submit parses zip into flow_map_config; redirects to /configure"
```

---

## Task 10: Add goldmark for prose markdown rendering

**Files:**
- Modify: `open-bbcd/go.mod`
- Modify: `open-bbcd/go.sum`

- [ ] **Step 1: Add the dependency**

```bash
cd open-bbcd && go get github.com/yuin/goldmark@v1.7.4
```

- [ ] **Step 2: Verify**

```bash
grep goldmark open-bbcd/go.mod
```

Expected: `	github.com/yuin/goldmark v1.7.4` line present.

- [ ] **Step 3: Commit**

```bash
git add open-bbcd/go.mod open-bbcd/go.sum
git commit -m "chore(open-bbcd): add goldmark for read-only prose rendering"
```

---

## Task 11: Configurator handler — read-only routes

**Files:**
- Create: `open-bbcd/internal/handler/configurator.go`
- Create: `open-bbcd/internal/handler/configurator_test.go`

- [ ] **Step 1: Write a failing route smoke test**

Create `open-bbcd/internal/handler/configurator_test.go`:

```go
package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/handler"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
)

// stubConfigGetter returns a hardcoded FlowMapConfig as raw JSON.
type stubConfigGetter struct {
	cfg     types.FlowMapConfig
	getErr  error
	parseErr string
}

func (s *stubConfigGetter) GetFlowMapConfig(ctx context.Context, agentID string) ([]byte, string, error) {
	if s.getErr != nil {
		return nil, "", s.getErr
	}
	b, _ := json.Marshal(s.cfg)
	return b, s.parseErr, nil
}

func (s *stubConfigGetter) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	return &types.Agent{ID: id, Name: s.cfg.Name, Status: "INITIALIZING"}, nil
}

func sampleConfig() types.FlowMapConfig {
	return types.FlowMapConfig{
		SchemaVersion: 1, Name: "test-agent",
		Source:       types.FlowMapSource{AppName: "sample"},
		Capabilities: []types.Capability{{Name: "orders", Summary: "orders"}},
		Skills: []types.Skill{{
			ID: "place-order", Origin: "discovered", Name: "Place order",
			Role: "write", CapabilityRef: "orders", ProposedTool: "orders.create",
		}},
		Flows: []types.Flow{{
			ID: "place-order", Origin: "discovered", Included: true,
			Name:     "Place order",
			Workflow: types.Workflow{Mermaid: "flowchart TD\n  start([start]) --> e([end])"},
		}},
	}
}

func newConfigHandler(t *testing.T, getter handler.ConfigGetter) *handler.ConfiguratorHandler {
	t.Helper()
	h, err := handler.NewConfiguratorHandler(getter, web.Assets)
	if err != nil {
		t.Fatalf("NewConfiguratorHandler: %v", err)
	}
	return h
}

func TestConfigurator_FlowsTab_RendersFlowsList(t *testing.T) {
	h := newConfigHandler(t, &stubConfigGetter{cfg: sampleConfig()})
	req := httptest.NewRequest(http.MethodGet, "/agents/abc/configure/flows", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.Flows(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "place-order") {
		t.Errorf("body should contain flow id: %s", w.Body.String()[:200])
	}
}

func TestConfigurator_SkillsTab_ShowsSkillRow(t *testing.T) {
	h := newConfigHandler(t, &stubConfigGetter{cfg: sampleConfig()})
	req := httptest.NewRequest(http.MethodGet, "/agents/abc/configure/skills", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.Skills(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "place-order") || !strings.Contains(w.Body.String(), "orders.create") {
		t.Errorf("Skills tab missing expected row content")
	}
}

func TestConfigurator_CapabilitiesTab_IsReadOnly(t *testing.T) {
	h := newConfigHandler(t, &stubConfigGetter{cfg: sampleConfig()})
	req := httptest.NewRequest(http.MethodGet, "/agents/abc/configure/capabilities", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.Capabilities(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Capabilities are derived") {
		t.Errorf("Capabilities tab missing read-only banner")
	}
}

func TestConfigurator_ParseError_ShowsErrorBanner(t *testing.T) {
	h := newConfigHandler(t, &stubConfigGetter{cfg: sampleConfig(), parseErr: "missing tools-proposed.json"})
	req := httptest.NewRequest(http.MethodGet, "/agents/abc/configure", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.Index(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "missing tools-proposed.json") {
		t.Errorf("Parse error not surfaced")
	}
}
```

- [ ] **Step 2: Run test — expect FAIL (handler missing)**

```bash
cd open-bbcd && go test -v ./internal/handler -run TestConfigurator
```

Expected: build error, undefined symbols.

- [ ] **Step 3: Implement the handler**

Create `open-bbcd/internal/handler/configurator.go`:

```go
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/yuin/goldmark"
)

// ConfigGetter is the narrow interface the configurator depends on.
type ConfigGetter interface {
	GetFlowMapConfig(ctx context.Context, agentID string) (cfg []byte, parseErr string, err error)
	GetByID(ctx context.Context, id string) (*types.Agent, error)
}

type ConfiguratorHandler struct {
	repo  ConfigGetter
	flowsTmpl, skillsTmpl, capabilitiesTmpl *template.Template
}

func NewConfiguratorHandler(repo ConfigGetter, webFS fs.FS) (*ConfiguratorHandler, error) {
	funcs := template.FuncMap{
		"renderMarkdown": renderMarkdown,
	}
	parse := func(name string) (*template.Template, error) {
		return template.New("").Funcs(funcs).ParseFS(webFS,
			"templates/layout.html",
			"templates/configurator/layout.html",
			"templates/configurator/_partials.html",
			"templates/configurator/"+name+".html",
		)
	}
	flowsTmpl, err := parse("flows")
	if err != nil {
		return nil, err
	}
	skillsTmpl, err := parse("skills")
	if err != nil {
		return nil, err
	}
	capabilitiesTmpl, err := parse("capabilities")
	if err != nil {
		return nil, err
	}
	return &ConfiguratorHandler{
		repo:             repo,
		flowsTmpl:        flowsTmpl,
		skillsTmpl:       skillsTmpl,
		capabilitiesTmpl: capabilitiesTmpl,
	}, nil
}

// renderMarkdown is a template func that converts markdown prose to HTML.
// Trusted source: prose came from the discovery skill, not user input.
func renderMarkdown(md string) template.HTML {
	if md == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(md), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(md))
	}
	return template.HTML(buf.String())
}

type configPageData struct {
	Active        string
	AgentID       string
	AgentName     string
	Tab           string // "flows" | "skills" | "capabilities"
	Config        types.FlowMapConfig
	ParseError    string
	SelectedFlow  *types.Flow
	SelectedSkill *types.Skill
	SelectedCap   *types.Capability
}

func (h *ConfiguratorHandler) load(r *http.Request) (configPageData, error) {
	agentID := r.PathValue("id")
	agent, err := h.repo.GetByID(r.Context(), agentID)
	if err != nil {
		return configPageData{}, err
	}
	cfgBytes, parseErr, err := h.repo.GetFlowMapConfig(r.Context(), agentID)
	if err != nil {
		return configPageData{}, err
	}
	var cfg types.FlowMapConfig
	if len(cfgBytes) > 0 {
		if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
			return configPageData{}, err
		}
	}
	return configPageData{
		Active:     "agents",
		AgentID:    agentID,
		AgentName:  agent.Name,
		Config:     cfg,
		ParseError: parseErr,
	}, nil
}

// Index redirects /agents/{id}/configure to the default Flows tab.
func (h *ConfiguratorHandler) Index(w http.ResponseWriter, r *http.Request) {
	// Render the Flows tab directly; this keeps the URL stable for share-links.
	h.Flows(w, r)
}

func (h *ConfiguratorHandler) Flows(w http.ResponseWriter, r *http.Request) {
	data, err := h.load(r)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Tab = "flows"
	if flowID := r.PathValue("flowId"); flowID != "" {
		for i := range data.Config.Flows {
			if data.Config.Flows[i].ID == flowID {
				data.SelectedFlow = &data.Config.Flows[i]
				break
			}
		}
		if data.SelectedFlow == nil {
			http.NotFound(w, r)
			return
		}
	}
	renderTemplate(w, h.flowsTmpl, "layout", data)
}

func (h *ConfiguratorHandler) Skills(w http.ResponseWriter, r *http.Request) {
	data, err := h.load(r)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Tab = "skills"
	if skillID := r.PathValue("skillId"); skillID != "" {
		for i := range data.Config.Skills {
			if data.Config.Skills[i].ID == skillID {
				data.SelectedSkill = &data.Config.Skills[i]
				break
			}
		}
		if data.SelectedSkill == nil {
			http.NotFound(w, r)
			return
		}
	}
	renderTemplate(w, h.skillsTmpl, "layout", data)
}

func (h *ConfiguratorHandler) Capabilities(w http.ResponseWriter, r *http.Request) {
	data, err := h.load(r)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Tab = "capabilities"
	if capName := r.PathValue("capName"); capName != "" {
		for i := range data.Config.Capabilities {
			if data.Config.Capabilities[i].Name == capName {
				data.SelectedCap = &data.Config.Capabilities[i]
				break
			}
		}
		if data.SelectedCap == nil {
			http.NotFound(w, r)
			return
		}
	}
	renderTemplate(w, h.capabilitiesTmpl, "layout", data)
}
```

- [ ] **Step 4: Build — expect template-not-found errors at runtime; build itself passes**

```bash
cd open-bbcd && go build ./...
```

Expected: clean build. Templates are loaded at handler-construction time.

- [ ] **Step 5: Don't run tests yet** — templates aren't created. Templates are the next task.

---

## Task 12: Configurator templates — chrome and partials

**Files:**
- Create: `open-bbcd/web/templates/configurator/layout.html`
- Create: `open-bbcd/web/templates/configurator/flows.html`
- Create: `open-bbcd/web/templates/configurator/skills.html`
- Create: `open-bbcd/web/templates/configurator/capabilities.html`
- Create: `open-bbcd/web/templates/configurator/_partials.html`

The repo's existing `web/templates/layout.html` defines the outer page (`{{define "layout"}}`); the configurator's `layout.html` defines the configurator-specific `{{define "content"}}` shell. Per-tab templates override `{{define "tab_content"}}`. Partials are shared across tabs.

- [ ] **Step 1: Configurator layout shell**

Create `open-bbcd/web/templates/configurator/layout.html`:

```html
{{define "content"}}
<div class="config-page">
  <div class="config-header">
    <a href="/agents/ui" class="config-back">← Back to Agents</a>
    <h1>{{.AgentName}}</h1>
  </div>

  <nav class="config-tabs">
    <a href="/agents/{{.AgentID}}/configure/flows"
       class="config-tab {{if eq .Tab "flows"}}active{{end}}">Flows</a>
    <a href="/agents/{{.AgentID}}/configure/skills"
       class="config-tab {{if eq .Tab "skills"}}active{{end}}">Skills</a>
    <a href="/agents/{{.AgentID}}/configure/capabilities"
       class="config-tab {{if eq .Tab "capabilities"}}active{{end}}">Capabilities</a>
    <span class="config-spacer"></span>
    <span class="btn-disabled" title="Available in PR4">Finalize →</span>
  </nav>

  {{if .ParseError}}
  <div class="config-banner config-banner-error">
    <strong>Discovery archive could not be parsed:</strong> {{.ParseError}}
    <p class="hint">Re-run discovery and re-upload the zip via <a href="/agents/new">the wizard</a>.</p>
  </div>
  {{end}}

  <div class="config-body">
    {{template "tab_content" .}}
  </div>
</div>
{{end}}
```

- [ ] **Step 2: Shared partials (list rows + detail panes)**

Create `open-bbcd/web/templates/configurator/_partials.html`:

```html
{{define "flow_row"}}
<a href="/agents/{{.AgentID}}/configure/flows/{{.Flow.ID}}"
   class="config-list-row {{if and .SelectedID (eq .SelectedID .Flow.ID)}}selected{{end}}">
  <span class="config-list-row-icon">{{if .Flow.Included}}☑{{else}}☐{{end}}</span>
  <span class="config-list-row-name">{{.Flow.ID}}</span>
  <span class="config-list-row-meta">{{len .Flow.Workflow.Layout}} nodes</span>
</a>
{{end}}

{{define "flow_detail"}}
<div class="config-detail">
  <h2>{{.Flow.Name}}</h2>
  <p class="config-detail-id"><code>{{.Flow.ID}}</code></p>

  <div class="config-section">
    <h3>Workflow</h3>
    <pre class="config-mermaid">{{.Flow.Workflow.Mermaid}}</pre>
    <p class="hint">Live editor lands in PR3.</p>
  </div>

  <div class="config-section">
    <h3>Metadata</h3>
    <dl class="config-meta">
      <dt>Description</dt>     <dd>{{.Flow.Description}}</dd>
      <dt>Intent</dt>          <dd>{{.Flow.Intent}}</dd>
      <dt>User phrases</dt>    <dd>{{range .Flow.UserPhrases}}<span class="chip">{{.}}</span>{{end}}</dd>
      <dt>Preconditions</dt>   <dd><ul>{{range .Flow.Preconditions}}<li>{{.}}</li>{{end}}</ul></dd>
      <dt>Postconditions</dt>  <dd><ul>{{range .Flow.Postconditions}}<li>{{.}}</li>{{end}}</ul></dd>
      <dt>Side effects</dt>    <dd>{{range .Flow.SideEffects}}<span class="chip">{{.}}</span>{{end}}</dd>
      <dt>Confidence</dt>      <dd>{{.Flow.Confidence}}</dd>
    </dl>
  </div>

  {{if .Flow.ProseMD}}
  <div class="config-section">
    <h3>Description (from discovery)</h3>
    <div class="prose">{{renderMarkdown .Flow.ProseMD}}</div>
  </div>
  {{end}}
</div>
{{end}}

{{define "skill_row"}}
<a href="/agents/{{.AgentID}}/configure/skills/{{.Skill.ID}}"
   class="config-list-row {{if and .SelectedID (eq .SelectedID .Skill.ID)}}selected{{end}}">
  <span class="config-list-row-name">{{.Skill.ID}}</span>
  <span class="config-list-row-meta">
    <span class="badge badge-{{.Skill.Origin}}">{{.Skill.Origin}}</span>
    {{if .Skill.External}}<span class="badge">external</span>{{else}}<span class="muted">{{.Skill.CapabilityRef}}</span>{{end}}
    <code>{{.Skill.ProposedTool}}</code>
  </span>
</a>
{{end}}

{{define "skill_detail"}}
<div class="config-detail">
  <h2>{{.Skill.Name}}</h2>
  <p class="config-detail-id"><code>{{.Skill.ID}}</code> · <span class="badge badge-{{.Skill.Origin}}">{{.Skill.Origin}}</span></p>

  <div class="config-section">
    <h3>Metadata</h3>
    <dl class="config-meta">
      <dt>Description</dt>   <dd>{{.Skill.Description}}</dd>
      <dt>Role</dt>          <dd>{{.Skill.Role}}</dd>
      <dt>User phrases</dt>  <dd>{{range .Skill.UserPhrases}}<span class="chip">{{.}}</span>{{end}}</dd>
      <dt>Capability</dt>    <dd>{{if .Skill.External}}external{{else}}<a href="/agents/{{.AgentID}}/configure/capabilities/{{.Skill.CapabilityRef}}">{{.Skill.CapabilityRef}}</a>{{end}}</dd>
      <dt>Proposed tool</dt> <dd><code>{{.Skill.ProposedTool}}</code></dd>
    </dl>
  </div>

  {{if .Skill.ProseMD}}
  <div class="config-section">
    <h3>Source (from discovery)</h3>
    <div class="prose">{{renderMarkdown .Skill.ProseMD}}</div>
  </div>
  {{end}}
</div>
{{end}}

{{define "capability_row"}}
<a href="/agents/{{.AgentID}}/configure/capabilities/{{.Cap.Name}}"
   class="config-list-row {{if and .SelectedID (eq .SelectedID .Cap.Name)}}selected{{end}}">
  <span class="config-list-row-name">{{.Cap.Name}}</span>
  <span class="config-list-row-meta"><span class="muted">{{len .Cap.Tools}} tools</span></span>
</a>
{{end}}

{{define "capability_detail"}}
<div class="config-detail">
  <h2>{{.Cap.Name}}</h2>
  <p>{{.Cap.Summary}}</p>

  <div class="config-section">
    <h3>Tools</h3>
    <table class="data-table">
      <thead><tr><th>Tool</th><th>Method</th><th>Path</th><th>Auth</th></tr></thead>
      <tbody>
        {{range .Cap.Tools}}
        <tr>
          <td><code>{{index . "tool"}}</code></td>
          <td>{{index . "method"}}</td>
          <td><code>{{index . "path"}}</code></td>
          <td>{{index . "auth"}}</td>
        </tr>
        {{end}}
      </tbody>
    </table>
  </div>

  {{if .Cap.ProseMD}}
  <div class="config-section">
    <h3>Documentation (from discovery)</h3>
    <div class="prose">{{renderMarkdown .Cap.ProseMD}}</div>
  </div>
  {{end}}
</div>
{{end}}
```

- [ ] **Step 3: Flows tab template**

Create `open-bbcd/web/templates/configurator/flows.html`:

```html
{{define "tab_content"}}
<div class="config-two-pane">
  <aside class="config-list">
    <div class="config-list-header">Flows ({{len .Config.Flows}})</div>
    {{range .Config.Flows}}
      {{template "flow_row" (dict "AgentID" $.AgentID "Flow" . "SelectedID" (selectedFlowID $.SelectedFlow))}}
    {{else}}
      <p class="empty-state">No flows in this discovery snapshot.</p>
    {{end}}
  </aside>
  <main class="config-detail-pane">
    {{if .SelectedFlow}}
      {{template "flow_detail" (dict "AgentID" .AgentID "Flow" .SelectedFlow)}}
    {{else}}
      <p class="config-detail-empty">Select a flow on the left.</p>
    {{end}}
  </main>
</div>
{{end}}
```

- [ ] **Step 4: Skills tab template**

Create `open-bbcd/web/templates/configurator/skills.html`:

```html
{{define "tab_content"}}
<div class="config-two-pane">
  <aside class="config-list">
    <div class="config-list-header">Skills ({{len .Config.Skills}})</div>
    {{range .Config.Skills}}
      {{template "skill_row" (dict "AgentID" $.AgentID "Skill" . "SelectedID" (selectedSkillID $.SelectedSkill))}}
    {{else}}
      <p class="empty-state">No skills in this discovery snapshot.</p>
    {{end}}
  </aside>
  <main class="config-detail-pane">
    {{if .SelectedSkill}}
      {{template "skill_detail" (dict "AgentID" .AgentID "Skill" .SelectedSkill)}}
    {{else}}
      <p class="config-detail-empty">Select a skill on the left.</p>
    {{end}}
  </main>
</div>
{{end}}
```

- [ ] **Step 5: Capabilities tab template**

Create `open-bbcd/web/templates/configurator/capabilities.html`:

```html
{{define "tab_content"}}
<div class="config-banner">
  Capabilities are derived from the discovery snapshot and cannot be edited here. To change them, re-run discovery.
</div>
<div class="config-two-pane">
  <aside class="config-list">
    <div class="config-list-header">Capabilities ({{len .Config.Capabilities}})</div>
    {{range .Config.Capabilities}}
      {{template "capability_row" (dict "AgentID" $.AgentID "Cap" . "SelectedID" (selectedCapName $.SelectedCap))}}
    {{else}}
      <p class="empty-state">No capabilities in this discovery snapshot.</p>
    {{end}}
  </aside>
  <main class="config-detail-pane">
    {{if .SelectedCap}}
      {{template "capability_detail" (dict "AgentID" .AgentID "Cap" .SelectedCap)}}
    {{else}}
      <p class="config-detail-empty">Select a capability on the left.</p>
    {{end}}
  </main>
</div>
{{end}}
```

- [ ] **Step 6: Add the helper template funcs**

In `open-bbcd/internal/handler/configurator.go`, replace the `funcs := template.FuncMap{...}` block in `NewConfiguratorHandler` with:

```go
	funcs := template.FuncMap{
		"renderMarkdown": renderMarkdown,
		"dict":           tplDict,
		"selectedFlowID": func(f *types.Flow) string {
			if f == nil {
				return ""
			}
			return f.ID
		},
		"selectedSkillID": func(s *types.Skill) string {
			if s == nil {
				return ""
			}
			return s.ID
		},
		"selectedCapName": func(c *types.Capability) string {
			if c == nil {
				return ""
			}
			return c.Name
		},
	}
```

And add the `tplDict` helper at the bottom of the file:

```go
// tplDict builds a map[string]any from alternating key/value template args.
// Used to pass multiple named values into a sub-template invocation:
//   {{template "flow_row" (dict "AgentID" $.AgentID "Flow" .)}}
func tplDict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("dict: odd number of args")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			return nil, errors.New("dict: keys must be strings")
		}
		m[key] = kv[i+1]
	}
	return m, nil
}
```

- [ ] **Step 7: Build**

```bash
cd open-bbcd && go build ./...
```

Expected: clean build.

- [ ] **Step 8: Run configurator tests**

```bash
cd open-bbcd && go test -race -v ./internal/handler -run TestConfigurator
```

Expected: all four PASS.

- [ ] **Step 9: Commit**

```bash
git add open-bbcd/internal/handler/configurator.go \
        open-bbcd/internal/handler/configurator_test.go \
        open-bbcd/web/templates/configurator/
git commit -m "feat(open-bbcd): read-only configurator handler + templates (Flows/Skills/Capabilities)"
```

---

## Task 13: Configurator CSS

**Files:**
- Create: `open-bbcd/web/static/configurator.css`
- Modify: `open-bbcd/web/templates/layout.html` (add `<link>` to the new stylesheet; relax the `.content` width clamp so the two-pane editor can breathe)

The existing `layout.html` inlines all CSS in a single `<style>` block. The chrome page constrains `.content { max-width: 960px }`, which would clamp the configurator's two-pane layout. We add the configurator stylesheet as an external file and override the width on a body-class-scoped selector for configurator pages.

- [ ] **Step 1: Add `<link>` to the configurator stylesheet in layout.html**

In `open-bbcd/web/templates/layout.html`, inside `<head>` and before the closing `</head>`, after the existing `<style>...</style>` block, add:

```html
  <link rel="stylesheet" href="/static/configurator.css">
```

- [ ] **Step 2: Create the configurator stylesheet**

Create `open-bbcd/web/static/configurator.css`:

```css
/* Page-scoped: the configurator wants more room than the default .content cap. */
.content:has(.config-page) { max-width: 1280px; }

.config-page {
  width: 100%;
  padding: 0;
}

.config-header {
  display: flex;
  align-items: baseline;
  gap: 16px;
  margin-bottom: 16px;
}

.config-header h1 {
  margin: 0;
  font-size: 24px;
}

.config-back {
  font-size: 13px;
  color: #6e7681;
  text-decoration: none;
}

.config-back:hover { text-decoration: underline; }

.config-tabs {
  display: flex;
  gap: 8px;
  border-bottom: 1px solid #30363d;
  align-items: center;
  margin-bottom: 16px;
}

.config-tab {
  padding: 10px 16px;
  text-decoration: none;
  color: #c9d1d9;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
}

.config-tab.active {
  color: #58a6ff;
  border-bottom-color: #58a6ff;
  font-weight: 600;
}

.config-spacer { flex: 1; }

.btn-disabled {
  padding: 8px 14px;
  background: #21262d;
  color: #6e7681;
  border-radius: 6px;
  cursor: not-allowed;
  font-size: 13px;
}

.config-banner {
  padding: 12px 16px;
  background: #161b22;
  border-left: 3px solid #58a6ff;
  border-radius: 4px;
  margin-bottom: 16px;
  font-size: 14px;
}

.config-banner-error {
  border-left-color: #f85149;
  background: #2d1418;
}

.config-banner .hint {
  margin: 4px 0 0;
  font-size: 13px;
  color: #8b949e;
}

.config-two-pane {
  display: grid;
  grid-template-columns: 320px 1fr;
  gap: 16px;
  min-height: 480px;
}

.config-list {
  border: 1px solid #30363d;
  border-radius: 6px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.config-list-header {
  padding: 10px 14px;
  border-bottom: 1px solid #30363d;
  background: #161b22;
  font-weight: 600;
  font-size: 13px;
}

.config-list-row {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 14px;
  border-bottom: 1px solid #21262d;
  text-decoration: none;
  color: #c9d1d9;
}

.config-list-row:hover { background: #1c2128; }
.config-list-row.selected { background: #1f2937; }

.config-list-row-icon { font-size: 14px; }
.config-list-row-name { font-weight: 600; font-size: 14px; }
.config-list-row-meta { font-size: 12px; color: #8b949e; display: flex; gap: 8px; align-items: center; }
.config-list-row-meta code { background: #21262d; padding: 1px 6px; border-radius: 3px; font-size: 11px; }

.config-detail-pane {
  border: 1px solid #30363d;
  border-radius: 6px;
  padding: 24px;
  overflow: auto;
}

.config-detail-empty {
  color: #6e7681;
  font-style: italic;
}

.config-detail h2 { margin: 0 0 6px; }
.config-detail-id { margin: 0 0 16px; color: #8b949e; font-size: 13px; }

.config-section {
  border-top: 1px solid #21262d;
  margin-top: 16px;
  padding-top: 16px;
}

.config-section h3 { margin: 0 0 8px; font-size: 14px; color: #8b949e; text-transform: uppercase; letter-spacing: 0.5px; }

.config-mermaid {
  background: #0d1117;
  border: 1px solid #21262d;
  border-radius: 4px;
  padding: 12px;
  font-size: 12px;
  white-space: pre;
  overflow-x: auto;
}

.config-meta { display: grid; grid-template-columns: 160px 1fr; gap: 6px 16px; margin: 0; }
.config-meta dt { color: #8b949e; font-size: 13px; }
.config-meta dd { margin: 0; }

.chip {
  display: inline-block;
  padding: 2px 8px;
  margin: 2px 4px 2px 0;
  background: #21262d;
  border-radius: 12px;
  font-size: 12px;
  color: #c9d1d9;
}

.muted { color: #8b949e; }

.badge-discovered { background: #1f6feb; }
.badge-custom { background: #d29922; color: #0d1117; }

.prose { line-height: 1.6; }
.prose h1, .prose h2, .prose h3 { margin-top: 16px; }
.prose pre { background: #0d1117; padding: 8px; border-radius: 4px; overflow-x: auto; }
.prose code { background: #21262d; padding: 1px 6px; border-radius: 3px; }
.prose table { border-collapse: collapse; margin: 12px 0; }
.prose th, .prose td { border: 1px solid #30363d; padding: 6px 10px; }
```

- [ ] **Step 3: Verify the stylesheet loads**

The existing `mux.Handle("GET /static/", ...)` in `internal/handler/api.go` serves anything in `web/static/`. No additional wiring needed. The Go embed in `web/assets.go` (`//go:embed static`) already includes new files at compile time.

```bash
cd open-bbcd && go build ./... && ls web/static/configurator.css
```

Expected: file exists; build clean.

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/web/static/configurator.css open-bbcd/web/templates/layout.html
git commit -m "feat(open-bbcd): configurator stylesheet"
```

---

## Task 14: Wire configurator routes in `api.go`

**Files:**
- Modify: `open-bbcd/internal/handler/api.go`

- [ ] **Step 1: Construct the configurator handler**

In `open-bbcd/internal/handler/api.go`, after `wizardHandler := NewWizardHandler(...)`, add:

```go
	configuratorHandler, err := NewConfiguratorHandler(agentRepo, web.Assets)
	if err != nil {
		log.Fatalf("init configurator handler: %v", err)
	}
```

- [ ] **Step 2: Register the routes**

After `mux.HandleFunc("POST /agents/wizard", wizardHandler.Submit)`, add:

```go
	mux.HandleFunc("GET /agents/{id}/configure", configuratorHandler.Index)
	mux.HandleFunc("GET /agents/{id}/configure/flows", configuratorHandler.Flows)
	mux.HandleFunc("GET /agents/{id}/configure/flows/{flowId}", configuratorHandler.Flows)
	mux.HandleFunc("GET /agents/{id}/configure/skills", configuratorHandler.Skills)
	mux.HandleFunc("GET /agents/{id}/configure/skills/{skillId}", configuratorHandler.Skills)
	mux.HandleFunc("GET /agents/{id}/configure/capabilities", configuratorHandler.Capabilities)
	mux.HandleFunc("GET /agents/{id}/configure/capabilities/{capName}", configuratorHandler.Capabilities)
```

The fixed `/agents/{id}/configure*` patterns must register **before** the existing `GET /agents/{id}` pattern (used by `agentHandler.Get`) — which already happens in `api.go` since wizard routes register first. Confirm by reading the file:

```bash
grep -n 'mux.HandleFunc' open-bbcd/internal/handler/api.go
```

Expected: configure-prefixed routes appear above `GET /agents/{id}`. (If `GET /agents/{id}` is registered above, move the configure block before it — Go's `http.ServeMux` rejects overlapping patterns at register time, but pattern specificity has changed in 1.22+; verify with `go run ./cmd/open-bbcd` and a curl in a moment.)

- [ ] **Step 3: Build**

```bash
cd open-bbcd && go build ./...
```

Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/internal/handler/api.go
git commit -m "feat(open-bbcd): register configurator routes in api.go"
```

---

## Task 15: End-to-end smoke test

This validates Part 1 against a real DB and a real .flow-map zip.

- [ ] **Step 1: Start Postgres and apply migrations**

```bash
cd open-bbcd && docker-compose up -d
source .env
make migrate-up
```

- [ ] **Step 2: Build a sample zip from the test fixture**

```bash
cd open-bbcd/internal/flowmap/testdata
python3 -c "
import zipfile, os
with zipfile.ZipFile('/tmp/sample-flowmap.zip', 'w') as z:
    for root, _, files in os.walk('sample-flowmap'):
        for f in files:
            p = os.path.join(root, f)
            z.write(p, os.path.relpath(p, 'sample-flowmap'))
print('wrote /tmp/sample-flowmap.zip')
"
cd ../../..
```

Expected: `/tmp/sample-flowmap.zip` printed.

- [ ] **Step 3: Run the server**

```bash
cd open-bbcd && make run
```

Leave it running in one terminal; open a second.

- [ ] **Step 4: Walk the wizard via curl**

```bash
# Submit the wizard form with all phase-1 fields + the zip
curl -i -X POST http://localhost:8080/agents/wizard \
  -F "name=smoke-test" \
  -F "scope=smoke" \
  -F "should_do=test" \
  -F "should_not_do=fail" \
  -F "business_domain=internal" \
  -F "discovery_file=@/tmp/sample-flowmap.zip"
```

Expected:
- HTTP 303 See Other.
- `Location: /agents/<uuid>/configure`.

Record the redirect target (the agent UUID).

- [ ] **Step 5: Open the configurator in the browser**

Navigate to `http://localhost:8080/agents/<uuid>/configure`. Verify:

- The page loads with the agent name "smoke-test" in the header.
- Three tabs visible: Flows / Skills / Capabilities.
- Flows tab is active by default; left pane lists `place-order`.
- Click `place-order` → detail pane shows the metadata, the workflow (mermaid as raw text), and the prose body rendered.
- Click Skills tab → list shows `place-order` skill with `discovered` badge and `orders.create` proposed tool.
- Click the skill → detail shows metadata + prose; capability link points to `/agents/<uuid>/configure/capabilities/orders`.
- Click Capabilities tab → banner: "Capabilities are derived…". List shows `orders` (1 tool). Click it → detail shows the tool table and the prose.

- [ ] **Step 6: Verify the DB row**

```bash
psql "$DATABASE_URL" -c "SELECT id, name, status, length(flow_map_config::text) AS cfg_size, flow_map_parse_error FROM agents ORDER BY created_at DESC LIMIT 1;"
```

Expected:
- `status = INITIALIZING`
- `cfg_size > 1000` (rough; means JSONB is populated)
- `flow_map_parse_error` is NULL

- [ ] **Step 7: Run the full test suite once more**

```bash
cd open-bbcd && make test
```

Expected: all packages PASS with `-race`.

- [ ] **Step 8: Stop the server and Postgres**

```bash
docker-compose down
```

- [ ] **Step 9: Commit any final adjustments**

If Step 5's browser walk surfaced template/CSS bugs, fix and commit:

```bash
git add ...
git commit -m "fix(open-bbcd): smoke-test fixes for configurator"
```

If everything was clean, no commit needed for this task.

---

## Done criteria for PR1

- ✅ Migration 006 applies cleanly; `agents.flow_map_config` and `agents.flow_map_parse_error` exist; `wizard_input` and `schema_version` are gone.
- ✅ Uploading a valid zip via the wizard creates an `INITIALIZING` agent with a populated `flow_map_config`.
- ✅ Uploading an invalid zip creates an `INITIALIZING` agent with `flow_map_parse_error` set.
- ✅ `/agents/{id}/configure[/flows|/skills|/capabilities[/{id}]]` renders read-only content matching the JSONB.
- ✅ Capabilities are visibly read-only (banner + no edit affordances).
- ✅ All `*_test.go` pass with `-race`.

PR2 (skills/capabilities edit paths + flow include toggle), PR3 (Drawflow workflow editor), and PR4 (finalize + config.yaml) get their own plans after this lands.
