# Flow-Map Configurator — PR3 (workflow editor)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the read-only `<pre>` mermaid block in the flow detail pane with a live Drawflow canvas. Users drag nodes, add skills/decisions/ends, attach edges (including back-edge loops), and every change persists to `flow_map_config.flows[].workflow` via a debounced htmx-style POST. On first open the canvas auto-lays-out via Dagre.

**Architecture:**
- **Drawflow** (vendored, ~50 KB JS + CSS, MIT) is the canvas library — it owns drag/drop, ports, connections, the DOM.
- **Dagre** (vendored, ~30 KB JS, MIT) runs once when a flow has no saved positions — produces a layered layout that respects edge direction.
- **Vanilla JS glue** (`web/static/openbbc-flow.js`, ~300 LoC) bridges:
  - `mermaid: string` ⇄ Drawflow JSON state
  - Custom Drawflow node-type templates (start, end, skill, decision)
  - Toolbar (`+ skill`, `+ decision`, `+ end`, `Auto-layout`, `Reset to source`)
  - Debounced save (~500 ms) — POSTs `{ mermaid, layout }` to the new endpoint
- **Server-side parser/serializer** (`internal/flowmap/mermaid_parser.go`, `mermaid_serializer.go`) — Go implementations of the same dialect, used for save-time validation. Two parsers (Go + JS) is a known cost; mitigated by shared canonical fixtures.
- **Save endpoint** — `POST /agents/{id}/configure/flows/{flowId}/workflow` accepts `{ mermaid, layout }`, validates mermaid, persists to JSONB.

**Tech stack:** Go 1.26, Drawflow 0.0.59 (latest), Dagre 0.8.5 (latest), vanilla JS (no framework), htmx 1.x (existing).

**Spec:** `docs/superpowers/specs/2026-05-07-flow-map-configurator-design.md` (PR3 section).

**Branching:** Branch off updated `main` (after PR2 merges). Branch name: `feat/flow-map-configurator-pr3`.

**Out of scope (deferred to a future PR):**
- Parallel-fanout nodes (`id{{label}}` with `&` joins). Current fixtures don't use them. Mermaid parser will reject `{{...}}` shapes with a clear error so future work knows what to add.
- Edge labels beyond decision `yes`/`no` (the spec doesn't require richer labels).
- Drag-to-reconnect existing edges (Drawflow supports it natively; the JS glue just doesn't expose extra UI for it).
- Multi-select / box-delete in the canvas. One node at a time.

---

## File Structure

```
open-bbcd/
├── internal/
│   ├── flowmap/
│   │   ├── mermaid_parser.go             # CREATE: parse flowchart TD → structured nodes/edges
│   │   ├── mermaid_parser_test.go        # CREATE
│   │   ├── mermaid_serializer.go         # CREATE: structured nodes/edges → flowchart TD
│   │   ├── mermaid_serializer_test.go    # CREATE
│   │   ├── mermaid_roundtrip_test.go     # CREATE: parse→serialize→parse on canonical fixtures
│   │   ├── mermaid.go                    # MODIFY: keep WorkflowReferencesSkill + skill-ref check;
│   │   │                                 #         add ParsedWorkflow type used by parser/serializer
│   │   └── testdata/
│   │       └── mermaid/                  # CREATE: canonical .mermaid fixtures shared with JS tests
│   │           ├── linear.mermaid
│   │           ├── decision.mermaid
│   │           ├── back-edge-loop.mermaid
│   │           └── start-end-only.mermaid
│   ├── handler/
│   │   ├── configurator.go               # MODIFY: add WorkflowUpdate handler
│   │   ├── configurator_test.go          # MODIFY: add 4 workflow-save tests
│   │   └── api.go                        # MODIFY: register POST /workflow route
│   └── types/
│       └── flow_map.go                   # (no change — Workflow type is already there)
└── web/
    ├── static/
    │   ├── drawflow.min.js               # CREATE (vendored)
    │   ├── drawflow.min.css              # CREATE (vendored)
    │   ├── dagre.min.js                  # CREATE (vendored)
    │   ├── openbbc-flow.js               # CREATE: ~300 LoC glue (mermaid<->Drawflow, toolbar, save)
    │   ├── configurator.css              # MODIFY: workflow-editor styles
    │   └── VENDORED.md                   # CREATE: provenance + license for the three new files
    └── templates/
        ├── layout.html                   # MODIFY: <link> drawflow.css, <script> drawflow + dagre + openbbc-flow
        └── configurator/
            └── partials.html             # MODIFY: replace flow_detail's <pre> with Drawflow canvas + state JSON
```

---

## Task 1: Vendor Drawflow + Dagre with provenance

**Files:**
- Create: `open-bbcd/web/static/drawflow.min.js`
- Create: `open-bbcd/web/static/drawflow.min.css`
- Create: `open-bbcd/web/static/dagre.min.js`
- Create: `open-bbcd/web/static/VENDORED.md`

- [ ] **Step 1: Fetch Drawflow 0.0.59**

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd/web/static
curl -sL -o drawflow.min.js   https://cdn.jsdelivr.net/npm/drawflow@0.0.59/dist/drawflow.min.js
curl -sL -o drawflow.min.css  https://cdn.jsdelivr.net/npm/drawflow@0.0.59/dist/drawflow.min.css
ls -la drawflow.min.js drawflow.min.css
```

Expected: both files present, `drawflow.min.js` is ~50 KB, `drawflow.min.css` is ~6 KB.

Sanity-check the JS file is the actual minified source (first 80 chars should look like minified code):

```bash
head -c 80 drawflow.min.js && echo
```

Expected: something starting with `class Drawflow{` or `var Drawflow=...`. If it looks like an HTML error page, retry the download.

- [ ] **Step 2: Fetch Dagre 0.8.5**

```bash
curl -sL -o dagre.min.js https://cdn.jsdelivr.net/npm/dagre@0.8.5/dist/dagre.min.js
ls -la dagre.min.js
head -c 80 dagre.min.js && echo
```

Expected: `dagre.min.js` is ~150 KB, content starts with the minified Dagre source.

(Dagre's size is bigger than the spec's earlier estimate because it bundles lodash — accepted tradeoff for the layout quality.)

- [ ] **Step 3: Document provenance**

Create `open-bbcd/web/static/VENDORED.md`:

```markdown
# Vendored static assets

Third-party JS/CSS pinned to specific versions, checked into the repo so the
binary is self-contained and offline-deployable. Update by fetching the new
release into this directory and bumping the version + checksum here.

## drawflow

- Version: 0.0.59
- License: MIT
- Source: https://github.com/jerosoler/Drawflow
- CDN used for fetch: https://cdn.jsdelivr.net/npm/drawflow@0.0.59/dist/

## dagre

- Version: 0.8.5
- License: MIT
- Source: https://github.com/dagrejs/dagre
- CDN used for fetch: https://cdn.jsdelivr.net/npm/dagre@0.8.5/dist/

## htmx

- Version: see `htmx.min.js` (existing — vendored prior to this PR)

## openbbc-flow.js

- First-party module. Bridges Drawflow ⇄ mermaid `flowchart TD` and runs Dagre
  for auto-layout when no saved positions exist. See its file header for
  details.
```

- [ ] **Step 4: Verify embed picks the new files up**

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd
go build ./...
```

The `//go:embed static` directive in `web/assets.go` includes the directory recursively — no code change needed.

- [ ] **Step 5: Wire stylesheet + scripts into layout.html**

In `open-bbcd/web/templates/layout.html`, locate the existing `<link rel="stylesheet" href="/static/configurator.css">` line. Immediately before it (so configurator overrides win), add:

```html
  <link rel="stylesheet" href="/static/drawflow.min.css">
```

Locate the existing `<script defer src="/static/chip-input.js"></script>` line. Immediately before it (so the chip script can stay last), add:

```html
  <script src="/static/drawflow.min.js"></script>
  <script src="/static/dagre.min.js"></script>
  <script defer src="/static/openbbc-flow.js"></script>
```

(`drawflow.min.js` and `dagre.min.js` are NOT `defer` — they must be available when `openbbc-flow.js` runs. `openbbc-flow.js` IS `defer` so it runs after DOM is ready.)

- [ ] **Step 6: Commit**

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3
git add open-bbcd/web/static/drawflow.min.js \
        open-bbcd/web/static/drawflow.min.css \
        open-bbcd/web/static/dagre.min.js \
        open-bbcd/web/static/VENDORED.md \
        open-bbcd/web/templates/layout.html
git commit -m "chore(open-bbcd): vendor Drawflow 0.0.59 + Dagre 0.8.5; wire layout.html"
```

---

## Task 2: Canonical mermaid fixtures

**Files:**
- Create: `open-bbcd/internal/flowmap/testdata/mermaid/linear.mermaid`
- Create: `open-bbcd/internal/flowmap/testdata/mermaid/decision.mermaid`
- Create: `open-bbcd/internal/flowmap/testdata/mermaid/back-edge-loop.mermaid`
- Create: `open-bbcd/internal/flowmap/testdata/mermaid/start-end-only.mermaid`

Four small `.mermaid` files used by both the Go parser/serializer tests AND (in T8) the JS round-trip test. Keeping them in source tree lets future contributors hand-edit and re-verify both languages.

- [ ] **Step 1: Linear chain**

Create `linear.mermaid`:

```
flowchart TD
  start([start]) --> s_place_order[place-order]
  s_place_order --> e([end])
```

- [ ] **Step 2: Decision with two branches**

Create `decision.mermaid`:

```
flowchart TD
  start([start]) --> s_read[read-product-catalog]
  s_read --> d{cart empty?}
  d -- yes --> e([end])
  d -- no --> s_place[place-order]
  s_place --> e
```

- [ ] **Step 3: Back-edge loop**

Create `back-edge-loop.mermaid`:

```
flowchart TD
  start([start]) --> s_a[poll-status]
  s_a --> d{ready?}
  d -- no --> s_a
  d -- yes --> e([end])
```

- [ ] **Step 4: Start-end only (degenerate)**

Create `start-end-only.mermaid`:

```
flowchart TD
  start([start]) --> e([end])
```

- [ ] **Step 5: Commit**

```bash
git add open-bbcd/internal/flowmap/testdata/mermaid/
git commit -m "test(open-bbcd): canonical mermaid fixtures (linear, decision, loop, degenerate)"
```

---

## Task 3: Mermaid parser in Go (TDD)

**Files:**
- Modify: `open-bbcd/internal/flowmap/mermaid.go` — add the `ParsedWorkflow` type
- Create: `open-bbcd/internal/flowmap/mermaid_parser.go`
- Create: `open-bbcd/internal/flowmap/mermaid_parser_test.go`

### Step 1: Add the shared types

In `open-bbcd/internal/flowmap/mermaid.go`, append (do NOT modify the existing `skillNodeRe`, `validateWorkflowSkillRefs`, or `WorkflowReferencesSkill`):

```go
// NodeKind enumerates the shapes the editor produces.
type NodeKind string

const (
	NodeStart    NodeKind = "start"
	NodeEnd      NodeKind = "end"
	NodeSkill    NodeKind = "skill"
	NodeDecision NodeKind = "decision"
)

// ParsedNode is one node in a parsed mermaid flowchart.
type ParsedNode struct {
	ID    string   // mermaid node id (e.g. "s_place_order")
	Kind  NodeKind // start | end | skill | decision
	Label string   // for skill: skill-id; for decision: question text; for start/end: literal "start"/"end"
}

// ParsedEdge is one edge in a parsed mermaid flowchart.
type ParsedEdge struct {
	From  string // source node id
	To    string // target node id
	Label string // empty for `-->`; non-empty for `-- yes -->` / `-- no -->`
}

// ParsedWorkflow is the structured form of a mermaid flowchart TD block.
// Round-trip property: serialize(parse(s)) preserves node ids, kinds, labels,
// edges (set-equal), and edge labels — though edge ORDER may differ since
// the serializer emits a deterministic order.
type ParsedWorkflow struct {
	Nodes []ParsedNode
	Edges []ParsedEdge
}
```

### Step 2: Write the failing parser tests

Create `open-bbcd/internal/flowmap/mermaid_parser_test.go`:

```go
package flowmap

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "mermaid", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func TestParseWorkflow_Linear(t *testing.T) {
	wf, err := ParseWorkflow(readFixture(t, "linear.mermaid"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(wf.Nodes) != 3 {
		t.Fatalf("Nodes = %d, want 3 (start, s_place_order, end): %+v", len(wf.Nodes), wf.Nodes)
	}

	byID := indexByID(wf.Nodes)
	if byID["start"].Kind != NodeStart {
		t.Errorf("start kind = %q, want start", byID["start"].Kind)
	}
	if byID["s_place_order"].Kind != NodeSkill || byID["s_place_order"].Label != "place-order" {
		t.Errorf("s_place_order = %+v", byID["s_place_order"])
	}
	if byID["e"].Kind != NodeEnd {
		t.Errorf("e kind = %q, want end", byID["e"].Kind)
	}
	if len(wf.Edges) != 2 {
		t.Errorf("Edges = %d, want 2: %+v", len(wf.Edges), wf.Edges)
	}
}

func TestParseWorkflow_Decision(t *testing.T) {
	wf, err := ParseWorkflow(readFixture(t, "decision.mermaid"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	byID := indexByID(wf.Nodes)
	if byID["d"].Kind != NodeDecision || byID["d"].Label != "cart empty?" {
		t.Errorf("d = %+v, want decision with label 'cart empty?'", byID["d"])
	}

	// Edge with "yes" label
	var yesEdge *ParsedEdge
	for i := range wf.Edges {
		if wf.Edges[i].Label == "yes" {
			yesEdge = &wf.Edges[i]
			break
		}
	}
	if yesEdge == nil || yesEdge.From != "d" || yesEdge.To != "e" {
		t.Errorf("expected `d -- yes --> e` edge, edges = %+v", wf.Edges)
	}
}

func TestParseWorkflow_BackEdge(t *testing.T) {
	wf, err := ParseWorkflow(readFixture(t, "back-edge-loop.mermaid"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// d -- no --> s_a is the back-edge.
	var back *ParsedEdge
	for i := range wf.Edges {
		if wf.Edges[i].From == "d" && wf.Edges[i].To == "s_a" {
			back = &wf.Edges[i]
			break
		}
	}
	if back == nil || back.Label != "no" {
		t.Errorf("expected back-edge `d -- no --> s_a`, edges = %+v", wf.Edges)
	}
}

func TestParseWorkflow_StartEndOnly(t *testing.T) {
	wf, err := ParseWorkflow(readFixture(t, "start-end-only.mermaid"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(wf.Nodes) != 2 || len(wf.Edges) != 1 {
		t.Errorf("Nodes=%d Edges=%d, want 2 and 1", len(wf.Nodes), len(wf.Edges))
	}
}

func TestParseWorkflow_RejectsParallelFanout(t *testing.T) {
	// {{label}} is the parallel-fanout shape — explicitly out of scope for PR3.
	src := "flowchart TD\n  start([start]) --> p{{parallel}}\n  p --> e([end])"
	_, err := ParseWorkflow(src)
	if err == nil {
		t.Fatal("Parse should reject {{...}} (parallel fanout) in PR3 scope")
	}
}

func TestParseWorkflow_MissingHeader(t *testing.T) {
	src := "  start([start]) --> e([end])"
	_, err := ParseWorkflow(src)
	if err == nil || !errors.Is(err, ErrMermaidInvalid) {
		t.Errorf("expected ErrMermaidInvalid, got %v", err)
	}
}

func TestParseWorkflow_UnknownNodeRef(t *testing.T) {
	src := "flowchart TD\n  start([start]) --> phantom"
	_, err := ParseWorkflow(src)
	if err == nil || !errors.Is(err, ErrMermaidInvalid) {
		t.Errorf("expected ErrMermaidInvalid for edge referencing undeclared node, got %v", err)
	}
}

// indexByID is a small test helper.
func indexByID(nodes []ParsedNode) map[string]ParsedNode {
	m := make(map[string]ParsedNode, len(nodes))
	for _, n := range nodes {
		m[n.ID] = n
	}
	return m
}
```

### Step 3: Run test — expect FAIL

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd
go test -v ./internal/flowmap -run TestParseWorkflow 2>&1 || true
```

Expected: undefined symbols (`ParseWorkflow`, `ErrMermaidInvalid`).

### Step 4: Implement the parser

Create `open-bbcd/internal/flowmap/mermaid_parser.go`:

```go
package flowmap

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrMermaidInvalid wraps every parser failure so callers can use errors.Is.
var ErrMermaidInvalid = errors.New("mermaid flowchart is invalid")

// Recognised node shapes:
//   start([label]), end([label])  — stadium
//   id[label]                     — skill
//   id{label}                     — decision
//
// Recognised edges:
//   a --> b           (unlabeled)
//   a -- label --> b  (labeled, label is text — typically "yes" or "no")
//
// Out of scope (PR3):
//   id{{label}}       — parallel fanout (explicitly rejected)
//   a & b --> c       — fanout joins
//   a --|label|--> b  — alternate label syntax
var (
	// Node shapes. Order matters: stadium first (more specific), then decision, then skill rectangle.
	stadiumRe  = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*\(\[\s*([^\]\[]+?)\s*\]\)$`)
	decisionRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*\{\s*([^{}]+?)\s*\}$`)
	skillRe    = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*\[\s*([^\]\[]+?)\s*\]$`)

	parallelRe = regexp.MustCompile(`\{\{[^{}]*\}\}`) // for explicit rejection
)

// ParseWorkflow reads a mermaid `flowchart TD/LR` string and returns
// structured nodes and edges. Errors wrap ErrMermaidInvalid.
//
// Strategy: two passes.
//   Pass 1 — collect declared nodes from any token that matches one of the
//            node-shape patterns (declaration can happen on either side of
//            an edge, e.g. `start([start]) --> s_a[place-order]`).
//   Pass 2 — collect edges, requiring both endpoints to be declared.
func ParseWorkflow(src string) (ParsedWorkflow, error) {
	if parallelRe.MatchString(src) {
		return ParsedWorkflow{}, fmt.Errorf("%w: parallel-fanout shape {{...}} is not supported in PR3", ErrMermaidInvalid)
	}

	lines := strings.Split(src, "\n")
	var header bool
	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "flowchart ") {
			rest := strings.TrimSpace(strings.TrimPrefix(l, "flowchart"))
			if rest != "TD" && rest != "LR" {
				return ParsedWorkflow{}, fmt.Errorf("%w: unsupported orientation %q (want TD or LR)", ErrMermaidInvalid, rest)
			}
			header = true
			break
		}
		return ParsedWorkflow{}, fmt.Errorf("%w: first non-blank line must be `flowchart TD` or `flowchart LR`", ErrMermaidInvalid)
	}
	if !header {
		return ParsedWorkflow{}, fmt.Errorf("%w: no flowchart header found", ErrMermaidInvalid)
	}

	nodes := make(map[string]ParsedNode)
	type pendingEdge struct {
		from, to, label string
	}
	var pending []pendingEdge

	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		if l == "" || strings.HasPrefix(l, "flowchart ") || strings.HasPrefix(l, "%%") {
			continue
		}

		// Split on edge arrows to extract LHS and RHS tokens plus optional label.
		// We support: "a --> b", "a -- label --> b".
		from, label, to, ok := splitEdgeOrDecl(l)
		if !ok {
			// Single-token declaration (no arrow), e.g. `start([start])` on its own line.
			if n, err := parseNodeToken(l); err == nil {
				nodes[n.ID] = n
				continue
			}
			return ParsedWorkflow{}, fmt.Errorf("%w: cannot parse line %q", ErrMermaidInvalid, l)
		}

		// Either side may be a declaration ("id[label]"), a bare reference ("id"),
		// or a stadium ("start([start])"). Declarations register the node.
		fID, err := absorbToken(from, nodes)
		if err != nil {
			return ParsedWorkflow{}, fmt.Errorf("%w: %v", ErrMermaidInvalid, err)
		}
		tID, err := absorbToken(to, nodes)
		if err != nil {
			return ParsedWorkflow{}, fmt.Errorf("%w: %v", ErrMermaidInvalid, err)
		}
		pending = append(pending, pendingEdge{from: fID, to: tID, label: label})
	}

	// Resolve pending edges; require both endpoints to be declared.
	var edges []ParsedEdge
	for _, p := range pending {
		if _, ok := nodes[p.from]; !ok {
			return ParsedWorkflow{}, fmt.Errorf("%w: edge endpoint %q not declared", ErrMermaidInvalid, p.from)
		}
		if _, ok := nodes[p.to]; !ok {
			return ParsedWorkflow{}, fmt.Errorf("%w: edge endpoint %q not declared", ErrMermaidInvalid, p.to)
		}
		edges = append(edges, ParsedEdge{From: p.from, To: p.to, Label: p.label})
	}

	// Materialise nodes in declaration order: re-walk lines and emit each
	// node the first time it appears (stable output for the round-trip test).
	seen := make(map[string]struct{}, len(nodes))
	var ordered []ParsedNode
	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		if l == "" || strings.HasPrefix(l, "flowchart ") || strings.HasPrefix(l, "%%") {
			continue
		}
		var tokens []string
		if from, _, to, ok := splitEdgeOrDecl(l); ok {
			tokens = []string{from, to}
		} else {
			tokens = []string{l}
		}
		for _, tok := range tokens {
			id, _ := absorbToken(tok, map[string]ParsedNode{}) // re-extract id only
			if _, dup := seen[id]; dup {
				continue
			}
			if n, ok := nodes[id]; ok {
				seen[id] = struct{}{}
				ordered = append(ordered, n)
			}
		}
	}

	return ParsedWorkflow{Nodes: ordered, Edges: edges}, nil
}

// splitEdgeOrDecl returns from, label, to and ok=true if the line is an
// edge; ok=false means the whole line is a single-token declaration.
//
// Supported forms (after trimming):
//   "a --> b"
//   "a -- label --> b"
//   "a([start]) --> s_x[place-order]"
//
// The function splits greedily on " -- ... --> " first, then on " --> ".
var (
	labeledArrowRe = regexp.MustCompile(`^(.+?)\s+--\s+(.+?)\s+-->\s+(.+)$`)
	plainArrowRe   = regexp.MustCompile(`^(.+?)\s+-->\s+(.+)$`)
)

func splitEdgeOrDecl(l string) (from, label, to string, ok bool) {
	if m := labeledArrowRe.FindStringSubmatch(l); m != nil {
		return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), strings.TrimSpace(m[3]), true
	}
	if m := plainArrowRe.FindStringSubmatch(l); m != nil {
		return strings.TrimSpace(m[1]), "", strings.TrimSpace(m[2]), true
	}
	return "", "", "", false
}

// absorbToken parses a node token. If the token includes a shape declaration
// (`id[label]`, `id{label}`, `id([label])`), the node is registered in the
// nodes map. Returns the node id either way. Returns an error if the token
// is malformed (e.g. unmatched braces).
func absorbToken(token string, nodes map[string]ParsedNode) (string, error) {
	if n, err := parseNodeToken(token); err == nil {
		nodes[n.ID] = n
		return n.ID, nil
	}
	// Plain id reference.
	bare := strings.TrimSpace(token)
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(bare) {
		return "", fmt.Errorf("malformed node token %q", token)
	}
	return bare, nil
}

func parseNodeToken(token string) (ParsedNode, error) {
	t := strings.TrimSpace(token)
	if m := stadiumRe.FindStringSubmatch(t); m != nil {
		kind := NodeStart
		if strings.EqualFold(m[2], "end") {
			kind = NodeEnd
		}
		return ParsedNode{ID: m[1], Kind: kind, Label: m[2]}, nil
	}
	if m := decisionRe.FindStringSubmatch(t); m != nil {
		return ParsedNode{ID: m[1], Kind: NodeDecision, Label: m[2]}, nil
	}
	if m := skillRe.FindStringSubmatch(t); m != nil {
		return ParsedNode{ID: m[1], Kind: NodeSkill, Label: m[2]}, nil
	}
	return ParsedNode{}, fmt.Errorf("not a node shape: %q", token)
}
```

### Step 5: Run tests — expect PASS

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd
go test -race -v ./internal/flowmap -run TestParseWorkflow
```

Expected: 7 tests PASS (linear, decision, back-edge, start-end-only, parallel-rejection, missing-header, unknown-ref).

### Step 6: Commit

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3
git add open-bbcd/internal/flowmap/mermaid.go \
        open-bbcd/internal/flowmap/mermaid_parser.go \
        open-bbcd/internal/flowmap/mermaid_parser_test.go
git commit -m "feat(open-bbcd): mermaid flowchart parser (start/end/skill/decision + labeled edges)"
```

---

## Task 4: Mermaid serializer in Go (TDD)

**Files:**
- Create: `open-bbcd/internal/flowmap/mermaid_serializer.go`
- Create: `open-bbcd/internal/flowmap/mermaid_serializer_test.go`
- Create: `open-bbcd/internal/flowmap/mermaid_roundtrip_test.go`

### Step 1: Write the failing serializer test

Create `open-bbcd/internal/flowmap/mermaid_serializer_test.go`:

```go
package flowmap

import (
	"strings"
	"testing"
)

func TestSerializeWorkflow_Linear(t *testing.T) {
	wf := ParsedWorkflow{
		Nodes: []ParsedNode{
			{ID: "start", Kind: NodeStart, Label: "start"},
			{ID: "s_place_order", Kind: NodeSkill, Label: "place-order"},
			{ID: "e", Kind: NodeEnd, Label: "end"},
		},
		Edges: []ParsedEdge{
			{From: "start", To: "s_place_order"},
			{From: "s_place_order", To: "e"},
		},
	}
	got := SerializeWorkflow(wf)
	if !strings.HasPrefix(got, "flowchart TD\n") {
		t.Errorf("output should start with `flowchart TD\\n`, got %q", got[:min(40, len(got))])
	}
	if !strings.Contains(got, "start([start])") {
		t.Errorf("missing start node: %s", got)
	}
	if !strings.Contains(got, "s_place_order[place-order]") {
		t.Errorf("missing skill node: %s", got)
	}
	if !strings.Contains(got, "e([end])") {
		t.Errorf("missing end node: %s", got)
	}
	if !strings.Contains(got, "start --> s_place_order") || !strings.Contains(got, "s_place_order --> e") {
		t.Errorf("missing expected edges: %s", got)
	}
}

func TestSerializeWorkflow_Decision_LabeledEdges(t *testing.T) {
	wf := ParsedWorkflow{
		Nodes: []ParsedNode{
			{ID: "start", Kind: NodeStart, Label: "start"},
			{ID: "d", Kind: NodeDecision, Label: "ready?"},
			{ID: "e", Kind: NodeEnd, Label: "end"},
		},
		Edges: []ParsedEdge{
			{From: "start", To: "d"},
			{From: "d", To: "e", Label: "yes"},
			{From: "d", To: "start", Label: "no"}, // back-edge
		},
	}
	got := SerializeWorkflow(wf)
	if !strings.Contains(got, "d{ready?}") {
		t.Errorf("decision node should serialize as `d{ready?}`: %s", got)
	}
	if !strings.Contains(got, "d -- yes --> e") {
		t.Errorf("labeled edge should serialize as `d -- yes --> e`: %s", got)
	}
	if !strings.Contains(got, "d -- no --> start") {
		t.Errorf("back-edge should serialize: %s", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

### Step 2: Run test — expect FAIL

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd
go test -v ./internal/flowmap -run TestSerializeWorkflow 2>&1 || true
```

Expected: undefined `SerializeWorkflow`.

### Step 3: Implement the serializer

Create `open-bbcd/internal/flowmap/mermaid_serializer.go`:

```go
package flowmap

import (
	"fmt"
	"strings"
)

// SerializeWorkflow emits a `flowchart TD` mermaid block for the given nodes
// and edges. Output is deterministic:
//   - one node-declaration line per node in input order, with shape syntax
//     based on Kind (stadium for start/end, brackets for skill, braces for
//     decision)
//   - one edge line per edge, edges in input order
//
// Round-trip property with ParseWorkflow holds for canonical fixtures.
func SerializeWorkflow(wf ParsedWorkflow) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	for _, n := range wf.Nodes {
		b.WriteString("  ")
		b.WriteString(formatNode(n))
		b.WriteByte('\n')
	}
	for _, e := range wf.Edges {
		b.WriteString("  ")
		if e.Label == "" {
			fmt.Fprintf(&b, "%s --> %s", e.From, e.To)
		} else {
			fmt.Fprintf(&b, "%s -- %s --> %s", e.From, e.Label, e.To)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func formatNode(n ParsedNode) string {
	switch n.Kind {
	case NodeStart, NodeEnd:
		return fmt.Sprintf("%s([%s])", n.ID, n.Label)
	case NodeDecision:
		return fmt.Sprintf("%s{%s}", n.ID, n.Label)
	case NodeSkill:
		return fmt.Sprintf("%s[%s]", n.ID, n.Label)
	default:
		// Defensive: fall back to skill rectangle.
		return fmt.Sprintf("%s[%s]", n.ID, n.Label)
	}
}
```

### Step 4: Run serializer tests — expect PASS

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd
go test -race -v ./internal/flowmap -run TestSerializeWorkflow
```

Expected: 2 tests PASS.

### Step 5: Add a round-trip property test

Create `open-bbcd/internal/flowmap/mermaid_roundtrip_test.go`:

```go
package flowmap

import (
	"reflect"
	"sort"
	"testing"
)

// TestParseSerializeRoundTrip ensures that for every canonical fixture,
// parse → serialize → parse produces the same structural result. We don't
// assert byte-equality (whitespace and edge ordering can differ); we assert
// the parsed structures match.
func TestParseSerializeRoundTrip(t *testing.T) {
	fixtures := []string{"linear.mermaid", "decision.mermaid", "back-edge-loop.mermaid", "start-end-only.mermaid"}
	for _, f := range fixtures {
		t.Run(f, func(t *testing.T) {
			src := readFixture(t, f)
			parsed1, err := ParseWorkflow(src)
			if err != nil {
				t.Fatalf("first parse: %v", err)
			}
			emitted := SerializeWorkflow(parsed1)
			parsed2, err := ParseWorkflow(emitted)
			if err != nil {
				t.Fatalf("second parse of emitted output:\n%s\nerr=%v", emitted, err)
			}

			// Compare node sets (id, kind, label).
			n1 := nodeSet(parsed1.Nodes)
			n2 := nodeSet(parsed2.Nodes)
			if !reflect.DeepEqual(n1, n2) {
				t.Errorf("node set mismatch:\nfirst:  %+v\nsecond: %+v\nemitted:\n%s", n1, n2, emitted)
			}

			// Compare edge sets (from, to, label).
			e1 := edgeSet(parsed1.Edges)
			e2 := edgeSet(parsed2.Edges)
			if !reflect.DeepEqual(e1, e2) {
				t.Errorf("edge set mismatch:\nfirst:  %+v\nsecond: %+v\nemitted:\n%s", e1, e2, emitted)
			}
		})
	}
}

func nodeSet(ns []ParsedNode) []ParsedNode {
	out := make([]ParsedNode, len(ns))
	copy(out, ns)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func edgeSet(es []ParsedEdge) []ParsedEdge {
	out := make([]ParsedEdge, len(es))
	copy(out, es)
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Label < out[j].Label
	})
	return out
}
```

### Step 6: Run round-trip test — expect PASS

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd
go test -race -v ./internal/flowmap -run TestParseSerializeRoundTrip
```

Expected: 4 subtests PASS (one per fixture).

### Step 7: Commit

```bash
git add open-bbcd/internal/flowmap/mermaid_serializer.go \
        open-bbcd/internal/flowmap/mermaid_serializer_test.go \
        open-bbcd/internal/flowmap/mermaid_roundtrip_test.go
git commit -m "feat(open-bbcd): mermaid flowchart serializer + round-trip test"
```

---

## Task 5: `POST /flows/{flowId}/workflow` handler

**Files:**
- Modify: `open-bbcd/internal/handler/configurator.go` — add `WorkflowUpdate` method
- Modify: `open-bbcd/internal/handler/configurator_test.go` — add 4 tests

### Step 1: Write the failing tests

Append to `open-bbcd/internal/handler/configurator_test.go`:

```go
func TestConfigurator_WorkflowUpdate_HappyPath(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	body := `{
		"mermaid": "flowchart TD\n  start([start]) --> s_x[place-order]\n  s_x --> e([end])",
		"layout": {"start": {"x": 40, "y": 40}, "s_x": {"x": 40, "y": 140}, "e": {"x": 40, "y": 240}}
	}`
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/flows/place-order/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.WorkflowUpdate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := store.cfg.Flows[0].Workflow
	if !strings.Contains(got.Mermaid, "place-order") {
		t.Errorf("mermaid not saved: %q", got.Mermaid)
	}
	if got.Layout["s_x"].X != 40 || got.Layout["s_x"].Y != 140 {
		t.Errorf("layout not saved: %+v", got.Layout)
	}
}

func TestConfigurator_WorkflowUpdate_RejectsUnknownSkill(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	body := `{
		"mermaid": "flowchart TD\n  start([start]) --> s_x[ghost-skill]\n  s_x --> e([end])",
		"layout": {}
	}`
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/flows/place-order/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.WorkflowUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown skill)", w.Code)
	}
}

func TestConfigurator_WorkflowUpdate_RejectsMalformedMermaid(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	body := `{"mermaid": "this is not mermaid", "layout": {}}`
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/flows/place-order/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.WorkflowUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (malformed)", w.Code)
	}
}

func TestConfigurator_WorkflowUpdate_UnknownFlow_404(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	body := `{"mermaid": "flowchart TD\n  start([start]) --> e([end])", "layout": {}}`
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/flows/ghost/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "abc")
	req.SetPathValue("flowId", "ghost")
	w := httptest.NewRecorder()
	h.WorkflowUpdate(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
```

### Step 2: Run test — expect FAIL (undefined h.WorkflowUpdate)

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd
go test -v ./internal/handler -run TestConfigurator_WorkflowUpdate 2>&1 || true
```

### Step 3: Implement the handler

Append to `open-bbcd/internal/handler/configurator.go`:

```go
// WorkflowUpdate accepts a JSON body { "mermaid": "...", "layout": { nodeId: {x, y} } }
// and persists it to the named flow's Workflow struct. Validates the mermaid:
//   - structural parse via flowmap.ParseWorkflow
//   - every id[<skill-id>] rectangle resolves to a discovered or custom skill
// Returns 200 with empty body on success (the editor doesn't need a re-render).
func (h *ConfiguratorHandler) WorkflowUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mermaid string                    `json:"mermaid"`
		Layout  map[string]types.Position `json:"layout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	agentID := r.PathValue("id")
	flowID := r.PathValue("flowId")
	cfg, err := h.loadConfig(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	idx := -1
	for i := range cfg.Flows {
		if cfg.Flows[i].ID == flowID {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.NotFound(w, r)
		return
	}

	// Structural parse.
	if _, err := flowmap.ParseWorkflow(body.Mermaid); err != nil {
		Error(w, fmt.Errorf("%w: %v", types.ErrFlowMapInvalid, err))
		return
	}
	// Skill-ref check.
	skillIDs := make(map[string]struct{}, len(cfg.Skills))
	for _, s := range cfg.Skills {
		skillIDs[s.ID] = struct{}{}
	}
	// Use the existing PR1 validator — same regex.
	if err := validateWorkflowSkillRefsExt(body.Mermaid, skillIDs); err != nil {
		Error(w, fmt.Errorf("%w: %v", types.ErrFlowMapInvalid, err))
		return
	}

	cfg.Flows[idx].Workflow = types.Workflow{
		Mermaid: body.Mermaid,
		Layout:  body.Layout,
	}

	if err := h.saveConfig(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// validateWorkflowSkillRefsExt is the package-external entry point for the
// PR1 validator (which lives in internal/flowmap with a lowercase name).
// Tiny wrapper to avoid exporting the original. Kept in this file because
// the handler is the only caller; if more callers appear, promote to a
// package function in internal/flowmap.
func validateWorkflowSkillRefsExt(mermaid string, skills map[string]struct{}) error {
	return flowmap.ValidateWorkflowSkillRefs(mermaid, skills)
}
```

Note: this requires exporting the PR1 helper. In `open-bbcd/internal/flowmap/mermaid.go`, rename:

```go
func validateWorkflowSkillRefs(mermaid string, skills map[string]struct{}) error {
```

to:

```go
func ValidateWorkflowSkillRefs(mermaid string, skills map[string]struct{}) error {
```

And update its existing internal caller in `validate()` (same file) to `ValidateWorkflowSkillRefs`. The `mermaid_test.go` test that referenced `validateWorkflowSkillRefs` directly must be updated to the new name.

Drop the `validateWorkflowSkillRefsExt` wrapper above — call `flowmap.ValidateWorkflowSkillRefs` directly from the handler. Updated handler body:

```go
	if err := flowmap.ValidateWorkflowSkillRefs(body.Mermaid, skillIDs); err != nil {
		Error(w, fmt.Errorf("%w: %v", types.ErrFlowMapInvalid, err))
		return
	}
```

### Step 4: Run tests — expect PASS

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd
go test -race -v ./internal/handler -run TestConfigurator_WorkflowUpdate
go test -race -v ./internal/flowmap -run TestValidateWorkflowSkillRefs
```

Expected: all PASS (4 handler tests + 4 PR1 validator subtests still pass after the rename).

### Step 5: Run full suite

```bash
go test -race ./...
```

Expected: every package PASS.

### Step 6: Commit

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3
git add open-bbcd/internal/flowmap/mermaid.go \
        open-bbcd/internal/flowmap/mermaid_test.go \
        open-bbcd/internal/handler/configurator.go \
        open-bbcd/internal/handler/configurator_test.go
git commit -m "feat(open-bbcd): POST /flows/{id}/workflow — structural + skill-ref validation"
```

---

## Task 6: Workflow editor — JS mermaid ↔ Drawflow bridge

**Files:**
- Create: `open-bbcd/web/static/openbbc-flow.js` (first half: parser + serializer + bridge)

The single JS file in this task implements the mermaid ↔ Drawflow conversion **only**. Task 7 adds the editor wiring, toolbar, and debounced save.

- [ ] **Step 1: Create the file with header + mermaid parser**

Create `open-bbcd/web/static/openbbc-flow.js`:

```js
// openbbc-flow.js — workflow editor for the configurator.
//
// Bridges:
//   - mermaid `flowchart TD` string (server-side source of truth)
//   - Drawflow JSON state (in-memory)
//
// Mirrors the Go parser/serializer in internal/flowmap/. The supported
// dialect is intentionally small:
//   - flowchart TD or flowchart LR header
//   - node shapes: id([label]) start/end, id[label] skill, id{label} decision
//   - edges:       a --> b, a -- label --> b
//
// Out of scope: {{label}} parallel-fanout, & joins, --|label|-- syntax.

(function (window) {
  "use strict";

  const OpenBBCFlow = {};

  // ---- Mermaid parser ----
  // Returns { nodes: [{id, kind, label}], edges: [{from, to, label}] }
  // Throws Error("...") on parse failure.

  const stadiumRe  = /^([A-Za-z_][A-Za-z0-9_]*)\s*\(\[\s*([^\]\[]+?)\s*\]\)$/;
  const decisionRe = /^([A-Za-z_][A-Za-z0-9_]*)\s*\{\s*([^{}]+?)\s*\}$/;
  const skillRe    = /^([A-Za-z_][A-Za-z0-9_]*)\s*\[\s*([^\]\[]+?)\s*\]$/;
  const idRe       = /^[A-Za-z_][A-Za-z0-9_]*$/;
  const labeledArrowRe = /^(.+?)\s+--\s+(.+?)\s+-->\s+(.+)$/;
  const plainArrowRe   = /^(.+?)\s+-->\s+(.+)$/;

  function parseNodeToken(token) {
    const t = token.trim();
    let m;
    if ((m = t.match(stadiumRe))) {
      const kind = m[2].toLowerCase() === "end" ? "end" : "start";
      return { id: m[1], kind, label: m[2] };
    }
    if ((m = t.match(decisionRe)))  return { id: m[1], kind: "decision", label: m[2] };
    if ((m = t.match(skillRe)))     return { id: m[1], kind: "skill",    label: m[2] };
    return null;
  }

  function absorbToken(token, nodes) {
    const parsed = parseNodeToken(token);
    if (parsed) {
      nodes.set(parsed.id, parsed);
      return parsed.id;
    }
    const bare = token.trim();
    if (!idRe.test(bare)) throw new Error(`malformed node token: ${token}`);
    return bare;
  }

  function splitEdge(line) {
    let m = line.match(labeledArrowRe);
    if (m) return { from: m[1].trim(), label: m[2].trim(), to: m[3].trim() };
    m = line.match(plainArrowRe);
    if (m) return { from: m[1].trim(), label: "", to: m[2].trim() };
    return null;
  }

  OpenBBCFlow.parseMermaid = function (src) {
    if (/\{\{[^{}]*\}\}/.test(src)) {
      throw new Error("parallel-fanout {{...}} is not supported");
    }
    const lines = src.split("\n");
    let header = false;
    for (const raw of lines) {
      const l = raw.trim();
      if (!l) continue;
      if (l.startsWith("flowchart ")) {
        const rest = l.substring("flowchart".length).trim();
        if (rest !== "TD" && rest !== "LR") throw new Error(`unsupported orientation: ${rest}`);
        header = true;
        break;
      }
      throw new Error("first non-blank line must be `flowchart TD` or `flowchart LR`");
    }
    if (!header) throw new Error("no flowchart header found");

    const nodes = new Map();
    const pending = [];
    for (const raw of lines) {
      const l = raw.trim();
      if (!l || l.startsWith("flowchart ") || l.startsWith("%%")) continue;
      const e = splitEdge(l);
      if (e) {
        const f = absorbToken(e.from, nodes);
        const t = absorbToken(e.to, nodes);
        pending.push({ from: f, to: t, label: e.label });
      } else if (parseNodeToken(l)) {
        const n = parseNodeToken(l);
        nodes.set(n.id, n);
      } else {
        throw new Error(`cannot parse line: ${l}`);
      }
    }
    for (const p of pending) {
      if (!nodes.has(p.from)) throw new Error(`edge endpoint ${p.from} not declared`);
      if (!nodes.has(p.to))   throw new Error(`edge endpoint ${p.to} not declared`);
    }
    return { nodes: Array.from(nodes.values()), edges: pending };
  };

  // ---- Mermaid serializer ----
  // Inverse of parseMermaid. Deterministic output: nodes in declaration
  // order, edges in input order.

  function formatNode(n) {
    if (n.kind === "start" || n.kind === "end")   return `${n.id}([${n.label}])`;
    if (n.kind === "decision")                    return `${n.id}{${n.label}}`;
    return `${n.id}[${n.label}]`;
  }

  OpenBBCFlow.serializeMermaid = function (wf) {
    const out = ["flowchart TD"];
    for (const n of wf.nodes) out.push("  " + formatNode(n));
    for (const e of wf.edges) {
      out.push(e.label ? `  ${e.from} -- ${e.label} --> ${e.to}` : `  ${e.from} --> ${e.to}`);
    }
    return out.join("\n") + "\n";
  };

  // ---- Drawflow JSON ↔ ParsedWorkflow bridge ----
  // The Drawflow library uses its own state shape:
  //   { drawflow: { Home: { data: { <num>: { id, name, data: {...}, class, pos_x, pos_y, ... } } } } }
  // We encode each node's domain payload in `data` and expose `name` as the
  // Drawflow node-type identifier registered via `editor.registerNode`.

  OpenBBCFlow.drawflowFromWorkflow = function (wf, layout) {
    layout = layout || {};
    const data = {};
    // Map our string id → Drawflow numeric id.
    const idToNum = new Map();
    let next = 1;
    for (const n of wf.nodes) {
      idToNum.set(n.id, next++);
    }
    for (const n of wf.nodes) {
      const num = idToNum.get(n.id);
      const pos = layout[n.id] || { x: 40 + (num - 1) * 200, y: 40 };
      data[num] = {
        id: num,
        name: n.kind,            // matches the type registered in editor.registerNode(...)
        data: { mermaidId: n.id, label: n.label },
        class: `obf-node obf-${n.kind}`,
        html: n.kind,            // template name (see registerNode wiring in T7)
        typenode: false,
        inputs: { input_1: { connections: [] } },
        outputs: { output_1: { connections: [] } },
        pos_x: pos.x,
        pos_y: pos.y,
      };
    }
    for (const e of wf.edges) {
      const fromNum = idToNum.get(e.from);
      const toNum = idToNum.get(e.to);
      if (fromNum == null || toNum == null) continue;
      data[fromNum].outputs.output_1.connections.push({ node: String(toNum), output: "input_1" });
      data[toNum].inputs.input_1.connections.push({ node: String(fromNum), input: "output_1" });
      // Edge label: stash on the Drawflow node payload (Drawflow doesn't
      // model edge labels directly). We attach the label to the source
      // node's outgoing edge metadata.
      if (e.label) {
        data[fromNum].data.edgeLabels = data[fromNum].data.edgeLabels || {};
        data[fromNum].data.edgeLabels[toNum] = e.label;
      }
    }
    return { drawflow: { Home: { data } } };
  };

  OpenBBCFlow.workflowFromDrawflow = function (df) {
    const data = df.drawflow.Home.data;
    const nodes = [];
    const edges = [];
    // First pass: nodes, in id order (numeric ascending — Drawflow's natural order).
    const keys = Object.keys(data).sort((a, b) => Number(a) - Number(b));
    for (const k of keys) {
      const dfn = data[k];
      nodes.push({
        id: dfn.data.mermaidId || `n${k}`,
        kind: dfn.name,
        label: dfn.data.label || dfn.name,
      });
    }
    const idForKey = (k) => data[k].data.mermaidId || `n${k}`;
    // Second pass: edges.
    for (const k of keys) {
      const dfn = data[k];
      const labels = (dfn.data && dfn.data.edgeLabels) || {};
      for (const conn of dfn.outputs.output_1.connections) {
        edges.push({
          from: idForKey(k),
          to: idForKey(conn.node),
          label: labels[conn.node] || "",
        });
      }
    }
    return { nodes, edges };
  };

  window.OpenBBCFlow = OpenBBCFlow;
})(window);
```

- [ ] **Step 2: Smoke-test the JS bridge in a tiny HTML harness (optional but recommended)**

Create a temporary `/tmp/obf-test.html` (will not be committed):

```html
<!doctype html><html><body>
<script src="/home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd/web/static/openbbc-flow.js"></script>
<script>
  const src = "flowchart TD\n  start([start]) --> s_x[place-order]\n  s_x --> d{ok?}\n  d -- yes --> e([end])\n  d -- no --> s_x";
  const wf = OpenBBCFlow.parseMermaid(src);
  console.log("parsed:", JSON.stringify(wf, null, 2));
  const back = OpenBBCFlow.serializeMermaid(wf);
  console.log("serialized:", back);
  const df = OpenBBCFlow.drawflowFromWorkflow(wf, {});
  console.log("drawflow:", JSON.stringify(df, null, 2));
  const wf2 = OpenBBCFlow.workflowFromDrawflow(df);
  console.log("round-trip:", JSON.stringify(wf2, null, 2));
</script>
</body></html>
```

Open in a browser console (or use `node -e` with a stub `window` if you prefer). Confirm:
- `parsed.nodes` has 4 entries (start, s_x, d, e).
- `parsed.edges` has 4 entries; one has `label: "yes"`, one has `label: "no"`.
- `serialized` starts with `flowchart TD` and contains all four nodes.
- `drawflow.drawflow.Home.data` has 4 entries keyed by `1, 2, 3, 4`.
- `wf2` matches `wf` after sort (node order + edge order may differ since the Drawflow side doesn't preserve declaration order; sort by id before comparing).

Delete the temp file after verifying.

- [ ] **Step 3: Commit**

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3
git add open-bbcd/web/static/openbbc-flow.js
git commit -m "feat(open-bbcd): openbbc-flow.js — mermaid ↔ Drawflow bridge (no editor yet)"
```

---

## Task 7: Workflow editor — Drawflow init, toolbar, debounced save

**Files:**
- Modify: `open-bbcd/web/static/openbbc-flow.js` — append the editor wiring

### Step 1: Append editor wiring

Append to the end of `open-bbcd/web/static/openbbc-flow.js`, **inside** the outer IIFE (so it lands before `window.OpenBBCFlow = OpenBBCFlow`):

```js
  // ---- Editor wiring ----
  //
  // Markup contract for a workflow-editor container:
  //   <div class="obf-editor"
  //        data-obf-editor
  //        data-agent-id="<uuid>"
  //        data-flow-id="<flow-id>"
  //        data-skills='["skill-id-1","skill-id-2",...]'>
  //     <div class="obf-toolbar">
  //       <button data-obf-add="skill">+ skill</button>
  //       <button data-obf-add="decision">+ decision</button>
  //       <button data-obf-add="end">+ end</button>
  //       <button data-obf-action="auto-layout">Auto-layout</button>
  //       <button data-obf-action="reset">Reset to source</button>
  //       <span class="obf-saved-indicator"></span>
  //     </div>
  //     <div class="obf-canvas"></div>
  //     <script type="application/json" data-obf-state>
  //       {"mermaid":"...","layout":{...}}
  //     </script>
  //   </div>

  const NODE_TEMPLATES = {
    start:    '<div class="obf-pill obf-start">start</div>',
    end:      '<div class="obf-pill obf-end">end</div>',
    skill:    '<div class="obf-rect obf-skill"><span class="obf-label">SKILL</span></div>',
    decision: '<div class="obf-diamond"><span class="obf-label">DECISION</span></div>',
  };

  function ensureUniqueId(base, taken) {
    if (!taken.has(base)) { taken.add(base); return base; }
    let i = 2;
    while (taken.has(`${base}_${i}`)) i++;
    const id = `${base}_${i}`;
    taken.add(id);
    return id;
  }

  function applyLayout(wf, layout) {
    // Greedy fallback when Dagre isn't loaded or layout is empty.
    // Dagre is preferred; called explicitly below.
    if (window.dagre && (!layout || Object.keys(layout).length === 0)) {
      const g = new window.dagre.graphlib.Graph();
      g.setGraph({ rankdir: "TB", nodesep: 50, ranksep: 60 });
      g.setDefaultEdgeLabel(() => ({}));
      for (const n of wf.nodes) {
        const size = n.kind === "decision" ? { width: 140, height: 60 } : { width: 140, height: 44 };
        g.setNode(n.id, size);
      }
      for (const e of wf.edges) g.setEdge(e.from, e.to);
      window.dagre.layout(g);
      layout = {};
      for (const n of wf.nodes) {
        const node = g.node(n.id);
        layout[n.id] = { x: Math.round(node.x - node.width / 2), y: Math.round(node.y - node.height / 2) };
      }
      return layout;
    }
    return layout || {};
  }

  OpenBBCFlow.initEditor = function (root) {
    if (!root || root.dataset.obfReady === "1") return;
    root.dataset.obfReady = "1";

    const stateEl = root.querySelector("[data-obf-state]");
    if (!stateEl) { console.warn("[obf] no state element"); return; }
    const state = JSON.parse(stateEl.textContent);
    const skills = JSON.parse(root.dataset.skills || "[]");

    let wf;
    try { wf = OpenBBCFlow.parseMermaid(state.mermaid); }
    catch (err) { console.error("[obf] parse failed", err); return; }

    const layout = applyLayout(wf, state.layout);
    const dfState = OpenBBCFlow.drawflowFromWorkflow(wf, layout);

    const canvas = root.querySelector(".obf-canvas");
    const editor = new window.Drawflow(canvas);
    editor.start();
    for (const [k, tpl] of Object.entries(NODE_TEMPLATES)) {
      // Each "type" is registered with a unique template string. Drawflow
      // substitutes them when rendering nodes whose `name` matches.
      editor.registerNode(k, tpl, {}, {});
    }
    editor.import(dfState);

    // Decorate skill nodes with their label text (Drawflow's `html` template
    // doesn't interpolate from `data`, so we walk the DOM after import).
    refreshLabels(canvas);

    // Track an in-memory authority for the workflow.
    const taken = new Set(wf.nodes.map((n) => n.id));

    function currentWorkflow() {
      const cur = editor.export();
      return OpenBBCFlow.workflowFromDrawflow(cur);
    }

    let saveTimer = null;
    const savedEl = root.querySelector(".obf-saved-indicator");

    function scheduleSave() {
      if (saveTimer) clearTimeout(saveTimer);
      saveTimer = setTimeout(doSave, 500);
    }

    async function doSave() {
      const next = currentWorkflow();
      const mermaid = OpenBBCFlow.serializeMermaid(next);
      const layout = {};
      const cur = editor.export().drawflow.Home.data;
      for (const k of Object.keys(cur)) {
        const dfn = cur[k];
        layout[dfn.data.mermaidId || `n${k}`] = { x: Math.round(dfn.pos_x), y: Math.round(dfn.pos_y) };
      }
      const url = `/agents/${root.dataset.agentId}/configure/flows/${root.dataset.flowId}/workflow`;
      try {
        const res = await fetch(url, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ mermaid, layout }),
        });
        if (!res.ok) throw new Error(`save failed: ${res.status}`);
        if (savedEl) savedEl.textContent = `Saved · ${new Date().toLocaleTimeString()}`;
      } catch (err) {
        console.error("[obf] save error", err);
        if (savedEl) savedEl.textContent = `Save error: ${err.message}`;
      }
    }

    // Drawflow events that should trigger a save.
    ["nodeMoved", "nodeRemoved", "nodeDataChanged", "connectionCreated", "connectionRemoved"].forEach((evt) => {
      editor.on(evt, scheduleSave);
    });

    // Toolbar — add nodes.
    root.querySelectorAll("[data-obf-add]").forEach((btn) => {
      btn.addEventListener("click", () => {
        const kind = btn.dataset.obfAdd;
        let mermaidId, label;
        if (kind === "skill") {
          // Prompt the user for which skill (cheap: window.prompt with a
          // newline-separated list). The form-driven affordance can come later.
          const choice = window.prompt(`Skill id (one of):\n${skills.join("\n")}`);
          if (!choice || !skills.includes(choice)) return;
          mermaidId = ensureUniqueId("s_" + choice.replaceAll("-", "_"), taken);
          label = choice;
        } else if (kind === "decision") {
          const q = window.prompt("Decision question (e.g. 'cart empty?')");
          if (!q) return;
          mermaidId = ensureUniqueId("d", taken);
          label = q;
        } else if (kind === "end") {
          mermaidId = ensureUniqueId("e", taken);
          label = "end";
        } else {
          return;
        }
        // Drawflow needs pos_x, pos_y. Plop near the canvas centre.
        const rect = canvas.getBoundingClientRect();
        const x = Math.round(rect.width / 2 - 70);
        const y = Math.round(rect.height / 2 - 22);
        const numId = editor.addNode(
          kind,                   // name (template)
          1, 1,                   // inputs, outputs (single port each)
          x, y,                   // pos
          `obf-node obf-${kind}`, // class
          { mermaidId, label, edgeLabels: {} },
          kind                    // html (template ref)
        );
        refreshLabels(canvas);
        scheduleSave();
      });
    });

    // Toolbar — actions.
    root.querySelector('[data-obf-action="auto-layout"]')?.addEventListener("click", () => {
      const cur = currentWorkflow();
      const newLayout = applyLayout(cur, {});
      // Apply positions to the live editor.
      const data = editor.export().drawflow.Home.data;
      for (const k of Object.keys(data)) {
        const mid = data[k].data.mermaidId;
        if (newLayout[mid]) {
          editor.updateNodeDataFromId(Number(k), { ...data[k].data });
          // Drawflow's API for moving: drawflow.drawflow.Home.data[k].pos_x = ...
          data[k].pos_x = newLayout[mid].x;
          data[k].pos_y = newLayout[mid].y;
        }
      }
      // Re-import so the DOM rerenders at new positions.
      editor.import({ drawflow: { Home: { data } } });
      refreshLabels(canvas);
      scheduleSave();
    });

    root.querySelector('[data-obf-action="reset"]')?.addEventListener("click", () => {
      if (!window.confirm("Discard layout and recompute from the original mermaid?")) return;
      const original = OpenBBCFlow.parseMermaid(state.mermaid);
      const newLayout = applyLayout(original, {});
      const fresh = OpenBBCFlow.drawflowFromWorkflow(original, newLayout);
      editor.clear();
      editor.import(fresh);
      refreshLabels(canvas);
      scheduleSave();
    });
  };

  function refreshLabels(canvas) {
    canvas.querySelectorAll(".drawflow-node").forEach((nodeEl) => {
      // Drawflow stores per-node data on the element via dataset.id; pull
      // it through the global drawflow instance is tricky. Simpler: look at
      // .data attribute or the registered label via the node's title.
      const label = nodeEl.querySelector(".obf-label");
      if (!label) return;
      const id = nodeEl.getAttribute("id"); // "node-<num>"
      // editor isn't visible here; use the drawflow-data-attr the lib sets:
      const dataNum = id?.replace("node-", "");
      const drawflowData = window.__obfEditor?.drawflow?.drawflow?.Home?.data?.[dataNum];
      if (drawflowData?.data?.label) label.textContent = drawflowData.data.label;
    });
  }

  // Init on DOM ready + after htmx swaps.
  function scan(root) {
    (root || document).querySelectorAll("[data-obf-editor]").forEach(OpenBBCFlow.initEditor);
  }
  document.addEventListener("DOMContentLoaded", () => scan());
  document.body.addEventListener("htmx:afterSwap", (e) => scan(e.target));
```

### Step 2: Commit

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3
git add open-bbcd/web/static/openbbc-flow.js
git commit -m "feat(open-bbcd): openbbc-flow.js — Drawflow editor, toolbar, debounced save, Dagre auto-layout"
```

---

## Task 8: Workflow-editor CSS

**Files:**
- Modify: `open-bbcd/web/static/configurator.css` — append editor styles

- [ ] **Step 1: Append CSS**

Append to `open-bbcd/web/static/configurator.css`:

```css

/* Workflow editor (Drawflow canvas + toolbar) */
.obf-editor {
  display: flex;
  flex-direction: column;
  gap: 8px;
  border: 1px solid #30363d;
  border-radius: 6px;
  background: #0d1117;
  height: 520px;
}

.obf-toolbar {
  display: flex;
  gap: 6px;
  align-items: center;
  padding: 6px 8px;
  border-bottom: 1px solid #21262d;
  background: #161b22;
}

.obf-toolbar button {
  background: #21262d;
  color: #c9d1d9;
  border: 1px solid #30363d;
  border-radius: 4px;
  padding: 4px 10px;
  font-size: 12px;
  cursor: pointer;
}
.obf-toolbar button:hover { background: #1c2128; }

.obf-saved-indicator {
  margin-left: auto;
  font-size: 11px;
  color: #8b949e;
}

.obf-canvas {
  flex: 1;
  position: relative;
  overflow: hidden;
}

/* Override Drawflow's default light theme for the dark configurator. */
.obf-canvas .drawflow {
  background: #0d1117;
}
.obf-canvas .drawflow .drawflow-node {
  background: transparent;
  border: none;
  padding: 0;
  box-shadow: none;
}

/* Custom node shapes. */
.obf-pill {
  display: inline-block;
  padding: 6px 14px;
  border-radius: 999px;
  background: #161b22;
  border: 2px solid #58a6ff;
  color: #e6edf3;
  font-size: 12px;
}
.obf-pill.obf-end { border-color: #58a6ff; }

.obf-rect {
  display: inline-block;
  padding: 8px 14px;
  border-radius: 4px;
  background: #161b22;
  border: 1px solid #6e7681;
  color: #e6edf3;
  font-size: 12px;
}

.obf-diamond {
  position: relative;
  width: 140px;
  height: 60px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #e6edf3;
  font-size: 12px;
}
.obf-diamond::before {
  content: "";
  position: absolute;
  inset: 0;
  background: #161b22;
  border: 2px solid #d29922;
  transform: rotate(45deg) scale(0.7);
  border-radius: 4px;
  z-index: -1;
}
```

### Step 2: Commit

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3
git add open-bbcd/web/static/configurator.css
git commit -m "feat(open-bbcd): workflow-editor CSS (toolbar + node shapes)"
```

---

## Task 9: Update `flow_detail` to render the Drawflow canvas

**Files:**
- Modify: `open-bbcd/web/templates/configurator/partials.html` — replace the `<pre>` workflow block

### Step 1: Replace the workflow section of `flow_detail`

In `open-bbcd/web/templates/configurator/partials.html`, find the existing `flow_detail` `Workflow` section:

```html
  <div class="config-section">
    <h3>Workflow</h3>
    <pre class="config-mermaid">{{.Flow.Workflow.Mermaid}}</pre>
    <p class="hint">Live editor lands in PR3.</p>
  </div>
```

REPLACE it with:

```html
  <div class="config-section">
    <h3>Workflow</h3>
    <div class="obf-editor"
         data-obf-editor
         data-agent-id="{{.AgentID}}"
         data-flow-id="{{.Flow.ID}}"
         data-skills='{{json (skillIds .Skills)}}'>
      <div class="obf-toolbar">
        <button type="button" data-obf-add="skill">+ skill</button>
        <button type="button" data-obf-add="decision">+ decision</button>
        <button type="button" data-obf-add="end">+ end</button>
        <button type="button" data-obf-action="auto-layout">Auto-layout</button>
        <button type="button" data-obf-action="reset">Reset to source</button>
        <span class="obf-saved-indicator"></span>
      </div>
      <div class="obf-canvas"></div>
      <script type="application/json" data-obf-state>{{workflowState .Flow.Workflow}}</script>
    </div>
  </div>
```

### Step 2: Add two new template helpers in `configurator.go`

In `open-bbcd/internal/handler/configurator.go`'s `NewConfiguratorHandler`, extend `funcs`:

```go
	funcs := template.FuncMap{
		"renderMarkdown":  renderMarkdown,
		"dict":            tplDict,
		"selectedFlowID":  func(f *types.Flow) string { if f == nil { return "" }; return f.ID },
		"selectedSkillID": func(s *types.Skill) string { if s == nil { return "" }; return s.ID },
		"selectedCapName": func(c *types.Capability) string { if c == nil { return "" }; return c.Name },
		"json":            tplJSON,
		"skillIds":        tplSkillIDs,
		"workflowState":   tplWorkflowState,
	}
```

And add the helper functions at the bottom of `configurator.go`:

```go
// tplJSON marshals v to a JSON string suitable for embedding in a single-quoted
// HTML attribute. Used by the workflow editor's data-skills attribute.
func tplJSON(v any) (template.JS, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return template.JS(b), nil
}

// tplSkillIDs extracts the id slice from a []types.Skill.
func tplSkillIDs(skills []types.Skill) []string {
	out := make([]string, len(skills))
	for i, s := range skills {
		out[i] = s.ID
	}
	return out
}

// tplWorkflowState marshals a Workflow as JSON for the inline state element
// the editor reads.
func tplWorkflowState(wf types.Workflow) (template.JS, error) {
	b, err := json.Marshal(wf)
	if err != nil {
		return "", err
	}
	return template.JS(b), nil
}
```

### Step 3: Thread `.Skills` into the flow_detail dict invocation

In `open-bbcd/web/templates/configurator/flows.html`, find the existing flow_detail template call. Replace:

```html
{{template "flow_detail" (dict "AgentID" .AgentID "Flow" .SelectedFlow)}}
```

with:

```html
{{template "flow_detail" (dict "AgentID" .AgentID "Flow" .SelectedFlow "Skills" .Config.Skills)}}
```

### Step 4: Build and run all tests

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd
go build ./...
go test -race ./...
```

Expected: clean build, every package PASS. The PR1 flow-detail test that checked for `flowchart TD` string in the rendered output still passes because the mermaid is now embedded in the `data-obf-state` JSON, and the test does substring search.

If a pre-existing test breaks (e.g. it relied on the literal `<pre class="config-mermaid">` markup), update the assertion to look for `data-obf-editor` instead.

### Step 5: Commit

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3
git add open-bbcd/web/templates/configurator/partials.html \
        open-bbcd/web/templates/configurator/flows.html \
        open-bbcd/internal/handler/configurator.go
git commit -m "feat(open-bbcd): flow_detail renders Drawflow canvas + initial state JSON"
```

---

## Task 10: Register the workflow save route + smoke test

**Files:**
- Modify: `open-bbcd/internal/handler/api.go`

### Step 1: Register the route

In `open-bbcd/internal/handler/api.go`, after the existing line:

```go
mux.HandleFunc("DELETE /agents/{id}/configure/skills/{skillId}", configuratorHandler.SkillDelete)
```

Add:

```go
	mux.HandleFunc("POST /agents/{id}/configure/flows/{flowId}/workflow", configuratorHandler.WorkflowUpdate)
```

### Step 2: Build + run all tests

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd
go build ./...
go test -race ./...
```

Expected: clean, all green.

### Step 3: Smoke test against a real DB

Start Postgres + apply migrations + start server:

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3/open-bbcd
set -a; source .env; set +a
docker-compose up -d
make migrate-up
export SERVER_PORT=8083
nohup go run ./cmd/open-bbcd > /tmp/openbbc-pr3.log 2>&1 &
sleep 4
```

Build the sample zip + post wizard:

```bash
cd internal/flowmap/testdata && python3 -c "
import zipfile, os
with zipfile.ZipFile('/tmp/sample-flowmap.zip', 'w') as z:
    for root, _, files in os.walk('sample-flowmap'):
        for f in files:
            p = os.path.join(root, f)
            z.write(p, os.path.relpath(p, 'sample-flowmap'))
"
RESPONSE=$(curl -i -s -X POST http://localhost:8083/agents/wizard \
  -F "name=pr3-smoke" -F "scope=smoke" -F "should_do=test" \
  -F "should_not_do=fail" -F "business_domain=internal" \
  -F "discovery_file=@/tmp/sample-flowmap.zip")
AID=$(echo "$RESPONSE" | grep -i '^Location:' | sed 's|.*/agents/\([^/]*\)/configure.*|\1|' | tr -d '\r')
echo "AID=$AID"
```

POST a workflow update:

```bash
curl -i -s -X POST "http://localhost:8083/agents/$AID/configure/flows/place-order/workflow" \
  -H "Content-Type: application/json" \
  --data '{
    "mermaid": "flowchart TD\n  start([start]) --> s_place_order[place-order]\n  s_place_order --> d{retry?}\n  d -- yes --> s_place_order\n  d -- no --> e([end])",
    "layout": {"start":{"x":40,"y":40},"s_place_order":{"x":40,"y":140},"d":{"x":40,"y":240},"e":{"x":40,"y":340}}
  }' | head -5

psql "$DATABASE_URL" -tAc "SELECT flow_map_config->'flows'->0->'workflow'->>'mermaid' FROM agents WHERE id='$AID'" | head -10
```

Expected: HTTP 200, then the new mermaid (with the back-edge loop) printed.

POST an invalid workflow (unknown skill):

```bash
curl -i -s -X POST "http://localhost:8083/agents/$AID/configure/flows/place-order/workflow" \
  -H "Content-Type: application/json" \
  --data '{
    "mermaid": "flowchart TD\n  start([start]) --> s_x[ghost-skill]\n  s_x --> e([end])",
    "layout": {}
  }' | head -3
```

Expected: HTTP 400.

### Step 4: Browser walk

Open `http://localhost:8083/agents/$AID/configure/flows/place-order` in a browser. Verify:

- The flow detail's Workflow section shows the Drawflow canvas (not the old `<pre>` block).
- Nodes are auto-laid-out (Dagre) on first visit.
- Drag a node — within 500ms, "Saved · HH:MM:SS" appears in the toolbar.
- Click "+ decision", enter a question, hit OK — a diamond appears on the canvas; save indicator updates.
- Click "Reset to source" — confirm dialog; nodes return to auto-laid-out positions.

### Step 5: Stop server

```bash
pkill -f open-bbcd
docker-compose down
```

### Step 6: Commit any browser-walk fixes

If the browser walk surfaced template/CSS/JS bugs, fix and commit. Otherwise:

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr3
git add open-bbcd/internal/handler/api.go
git commit -m "feat(open-bbcd): register POST /flows/{id}/workflow route"
```

---

## Done criteria for PR3

- ✅ Drawflow + Dagre vendored with VENDORED.md provenance.
- ✅ Mermaid parser + serializer in Go with full round-trip property on 4 canonical fixtures.
- ✅ `openbbc-flow.js` implements mermaid ⇄ Drawflow conversion + editor wiring.
- ✅ `POST /flows/{flowId}/workflow` endpoint validates and persists.
- ✅ Drawflow canvas replaces the `<pre>` in flow detail; auto-layout on first visit via Dagre.
- ✅ Toolbar adds skill / decision / end; auto-layout button; reset button.
- ✅ Debounced (~500 ms) save on every Drawflow change.
- ✅ `go test -race ./...` green; `go vet ./...` clean.
- ✅ Smoke test passes (real-DB POST + browser walk).

PR4 (finalize + YAML download) is the last piece.
