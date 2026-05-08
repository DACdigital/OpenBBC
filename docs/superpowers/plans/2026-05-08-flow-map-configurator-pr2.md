# Flow-Map Configurator — PR2 (include/exclude + skills edit paths)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make everything except the workflow editor interactive. Add the `included` toggle on flows; full edit form for existing skills; create a custom skill from scratch; delete a custom skill (gated by referential integrity). Capabilities remain read-only. The Drawflow workflow editor is PR3; finalize + YAML download is PR4.

**Architecture:** New POST/DELETE handlers on the existing `ConfiguratorHandler`. Each handler reads the agent row's `flow_map_config` JSONB, mutates the in-memory `FlowMapConfig`, marshals back, calls `repo.UpdateFlowMapConfig` (already added in PR1 T8). Last-write-wins concurrency — accepted per the spec. The PR1 `ConfigGetter` interface widens to `ConfigStore` (adds `UpdateFlowMapConfig`). htmx swaps the affected row or detail panel in place.

**Tech Stack:** Go 1.26, htmx 1.x (already vendored), `html/template`, vanilla JS for the chip-input widget (~50 LoC, no framework).

**Spec:** `docs/superpowers/specs/2026-05-07-flow-map-configurator-design.md` (PR2 section, "include/exclude + skills/capabilities edit paths").

**Branching:** Create a fresh worktree off the current `main` (after PR1 merges) — `feat/flow-map-configurator-pr2`. While PR1 is unmerged, branch off `feat/flow-map-configurator-pr1` instead and rebase onto `main` once PR1 lands.

---

## File Structure

```
open-bbcd/
├── internal/
│   ├── types/
│   │   └── errors.go                                 # MODIFY: add ErrSkillReferenced, ErrCapabilityReadOnly,
│   │                                                 #         ErrInvalidSkillRole, ErrCustomSkillNameRequired
│   ├── handler/
│   │   ├── handler.go                                # MODIFY: map new sentinels (ErrSkillReferenced → 409,
│   │   │                                             #         the rest → 400)
│   │   ├── configurator.go                           # MODIFY: rename ConfigGetter → ConfigStore (add Update);
│   │   │                                             #         add 4 new handlers (FlowToggle, SkillUpdate,
│   │   │                                             #         SkillCreate, SkillDelete); shared loadAndSave
│   │   │                                             #         helper for read-modify-write
│   │   ├── configurator_test.go                      # MODIFY: extend stub, add edit-path tests
│   │   └── api.go                                    # MODIFY: register 4 new routes
│   └── flowmap/
│       └── id.go                                     # CREATE: SlugifySkillName + collision discriminator
│       └── id_test.go                                # CREATE
└── web/
    ├── static/
    │   ├── chip-input.js                             # CREATE: ~50 LoC chip-list widget
    │   └── configurator.css                          # MODIFY: chip styles + form styles
    ├── templates/
    │   ├── layout.html                               # MODIFY: <script src="/static/chip-input.js">
    │   └── configurator/
    │       ├── partials.html                         # MODIFY: rewrite skill_detail as a form;
    │       │                                         #         flow_row uses an htmx checkbox toggle;
    │       │                                         #         add `skill_form_fields` partial reused
    │       │                                         #         by edit + create flows
    │       └── skills.html                           # MODIFY: "+ Add skill" button → opens blank form
```

The `internal/flowmap/id.go` split keeps slug/discriminator logic out of the handler. Everything else extends existing files.

---

## Task 1: Sentinel errors + 400/409 mapping

**Files:**
- Modify: `open-bbcd/internal/types/errors.go`
- Modify: `open-bbcd/internal/handler/handler.go`

- [ ] **Step 1: Add four sentinels**

Append to the existing `var (...)` block in `open-bbcd/internal/types/errors.go`:

```go
	ErrSkillReferenced         = errors.New("skill is referenced by a flow's workflow and cannot be deleted")
	ErrCapabilityReadOnly      = errors.New("capabilities are read-only")
	ErrInvalidSkillRole        = errors.New("skill role must be 'read' or 'write'")
	ErrCustomSkillNameRequired = errors.New("custom skill name is required")
```

The full block is now:

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

	ErrSkillReferenced         = errors.New("skill is referenced by a flow's workflow and cannot be deleted")
	ErrCapabilityReadOnly      = errors.New("capabilities are read-only")
	ErrInvalidSkillRole        = errors.New("skill role must be 'read' or 'write'")
	ErrCustomSkillNameRequired = errors.New("custom skill name is required")
)
```

- [ ] **Step 2: Map sentinels in handler.go::Error**

In `open-bbcd/internal/handler/handler.go`, extend the existing `switch`:

- Add a new `case` for 409 (the only one in the file so far):

```go
case errors.Is(err, types.ErrSkillReferenced):
    status = http.StatusConflict
```

- Extend the existing 400-class case with the three remaining new sentinels:

```go
case errors.Is(err, types.ErrNameRequired),
    errors.Is(err, types.ErrPromptRequired),
    errors.Is(err, types.ErrAgentRequired),
    errors.Is(err, types.ErrDiscoveryFileRequired),
    errors.Is(err, types.ErrDiscoveryFileTooLarge),
    errors.Is(err, types.ErrDiscoveryFileBadExtension),
    errors.Is(err, types.ErrFlowMapInvalid),
    errors.Is(err, types.ErrCapabilityReadOnly),
    errors.Is(err, types.ErrInvalidSkillRole),
    errors.Is(err, types.ErrCustomSkillNameRequired):
    status = http.StatusBadRequest
```

- [ ] **Step 3: Build**

```bash
cd open-bbcd && go build ./...
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/internal/types/errors.go open-bbcd/internal/handler/handler.go
git commit -m "feat(open-bbcd): edit-path sentinels (skill referenced 409, validation 400)"
```

---

## Task 2: Slug + collision discriminator for custom skill IDs

**Files:**
- Create: `open-bbcd/internal/flowmap/id.go`
- Create: `open-bbcd/internal/flowmap/id_test.go`

- [ ] **Step 1: Write the failing test**

Create `open-bbcd/internal/flowmap/id_test.go`:

```go
package flowmap

import (
	"strings"
	"testing"
)

func TestSlugifySkillName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Place Order", "place-order"},
		{"  Trim me  ", "trim-me"},
		{"Order #42!", "order-42"},
		{"Send_email_alert", "send-email-alert"},
		{"camelCase ID", "camelcase-id"},
		{"Multiple   spaces", "multiple-spaces"},
		{"---hyphens---", "hyphens"},
		{"żółć", ""}, // non-ASCII collapses; caller guards empty
	}
	for _, tc := range tests {
		got := SlugifySkillName(tc.in)
		if got != tc.want {
			t.Errorf("SlugifySkillName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUniqueSkillID_NoCollision(t *testing.T) {
	taken := map[string]struct{}{"foo": {}, "bar": {}}
	got := UniqueSkillID("baz", taken)
	if got != "baz" {
		t.Errorf("UniqueSkillID = %q, want baz", got)
	}
}

func TestUniqueSkillID_CollisionAppendsDiscriminator(t *testing.T) {
	taken := map[string]struct{}{"foo": {}}
	got := UniqueSkillID("foo", taken)
	if !strings.HasPrefix(got, "foo-") || len(got) != len("foo-")+4 {
		t.Errorf("UniqueSkillID = %q, want foo-<4-hex-chars>", got)
	}
	if _, exists := taken[got]; exists {
		t.Errorf("UniqueSkillID returned an already-taken id: %q", got)
	}
}
```

- [ ] **Step 2: Run test — expect FAIL**

```bash
cd open-bbcd && go test -v ./internal/flowmap -run "TestSlugify|TestUniqueSkillID" 2>&1 || true
```

Expected: build error, undefined symbols.

- [ ] **Step 3: Implement**

Create `open-bbcd/internal/flowmap/id.go`:

```go
package flowmap

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"unicode"
)

// SlugifySkillName lowercases s, replaces runs of non-ASCII-alphanumeric
// characters with single hyphens, and trims leading/trailing hyphens.
// Returns "" if no usable characters remain — callers must reject that case.
func SlugifySkillName(s string) string {
	var b strings.Builder
	prevHyphen := true // suppresses leading hyphens
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
		} else if unicode.IsSpace(r) || r == '-' || r == '_' {
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
		// other runes are dropped
	}
	return strings.TrimRight(b.String(), "-")
}

// UniqueSkillID returns base if it is not in taken; otherwise appends "-<4-hex>"
// using crypto/rand. Re-rolls on the (cosmically unlikely) chance of collision.
func UniqueSkillID(base string, taken map[string]struct{}) string {
	if _, exists := taken[base]; !exists {
		return base
	}
	for attempts := 0; attempts < 8; attempts++ {
		var buf [2]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand should never fail on Linux; if it does, fall back
			// to a deterministic suffix derived from base length.
			return base + "-" + hex.EncodeToString([]byte{byte(len(base)), 0xa5})
		}
		candidate := base + "-" + hex.EncodeToString(buf[:])
		if _, exists := taken[candidate]; !exists {
			return candidate
		}
	}
	// 8 collisions on a 16-bit suffix is extraordinary; surface a deterministic id.
	return base + "-conflict"
}
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
cd open-bbcd && go test -race -v ./internal/flowmap -run "TestSlugify|TestUniqueSkillID"
```

Expected: PASS for all cases.

- [ ] **Step 5: Commit**

```bash
git add open-bbcd/internal/flowmap/id.go open-bbcd/internal/flowmap/id_test.go
git commit -m "feat(open-bbcd): SlugifySkillName + UniqueSkillID for custom skills"
```

---

## Task 3: Widen `ConfigGetter` to `ConfigStore`; add `loadAndSave` helper

**Files:**
- Modify: `open-bbcd/internal/handler/configurator.go`
- Modify: `open-bbcd/internal/handler/configurator_test.go`

- [ ] **Step 1: Widen the interface**

In `configurator.go`, rename `ConfigGetter` to `ConfigStore` and add the write method:

```go
// ConfigStore is the narrow interface the configurator depends on.
type ConfigStore interface {
	GetFlowMapConfig(ctx context.Context, agentID string) (cfg []byte, parseErr string, err error)
	GetByID(ctx context.Context, id string) (*types.Agent, error)
	UpdateFlowMapConfig(ctx context.Context, agentID string, cfg []byte) error
}
```

Update `ConfiguratorHandler.repo` to use `ConfigStore`. Update `NewConfiguratorHandler` parameter type. Replace any other `ConfigGetter` reference with `ConfigStore`.

- [ ] **Step 2: Add `loadAndSave` helper**

Append to `configurator.go`:

```go
// loadConfig fetches the agent's flow_map_config and unmarshals into a
// FlowMapConfig. Returns ErrNotFound if the agent does not exist or has no
// config persisted (the configurator pages assume the wizard already ran).
func (h *ConfiguratorHandler) loadConfig(ctx context.Context, agentID string) (types.FlowMapConfig, error) {
	cfgBytes, _, err := h.repo.GetFlowMapConfig(ctx, agentID)
	if err != nil {
		return types.FlowMapConfig{}, err
	}
	if len(cfgBytes) == 0 {
		return types.FlowMapConfig{}, types.ErrNotFound
	}
	var cfg types.FlowMapConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		return types.FlowMapConfig{}, err
	}
	return cfg, nil
}

// saveConfig marshals cfg to JSON and writes it via the repository.
func (h *ConfiguratorHandler) saveConfig(ctx context.Context, agentID string, cfg types.FlowMapConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return h.repo.UpdateFlowMapConfig(ctx, agentID, b)
}
```

- [ ] **Step 3: Update the test stub**

In `configurator_test.go`, rename `stubConfigGetter` to `stubConfigStore` and add a write method:

```go
type stubConfigStore struct {
	cfg      types.FlowMapConfig
	getErr   error
	parseErr string
	updates  int
	updateFn func(cfg []byte) error
}

func (s *stubConfigStore) GetFlowMapConfig(ctx context.Context, agentID string) ([]byte, string, error) {
	if s.getErr != nil {
		return nil, "", s.getErr
	}
	b, _ := json.Marshal(s.cfg)
	return b, s.parseErr, nil
}

func (s *stubConfigStore) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	return &types.Agent{ID: id, Name: s.cfg.Name, Status: "INITIALIZING"}, nil
}

func (s *stubConfigStore) UpdateFlowMapConfig(ctx context.Context, agentID string, cfg []byte) error {
	s.updates++
	if s.updateFn != nil {
		return s.updateFn(cfg)
	}
	var decoded types.FlowMapConfig
	if err := json.Unmarshal(cfg, &decoded); err != nil {
		return err
	}
	s.cfg = decoded
	return nil
}
```

Update `newConfigHandler` parameter type accordingly. Replace `&stubConfigGetter{...}` with `&stubConfigStore{...}` everywhere.

- [ ] **Step 4: Build + run existing PR1 tests**

```bash
cd open-bbcd && go build ./... && go test -race -v ./internal/handler -run TestConfigurator
```

Expected: clean build, all four PR1 configurator tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add open-bbcd/internal/handler/configurator.go open-bbcd/internal/handler/configurator_test.go
git commit -m "refactor(open-bbcd): ConfigGetter → ConfigStore (read+write); add loadConfig/saveConfig helpers"
```

---

## Task 4: `POST /agents/{id}/configure/flows/{flowId}/included` — toggle inclusion

**Files:**
- Modify: `open-bbcd/internal/handler/configurator.go`
- Modify: `open-bbcd/internal/handler/configurator_test.go`
- Modify: `open-bbcd/internal/handler/api.go` (route registration in T9 — but the handler method is added here)

- [ ] **Step 1: Write the failing test**

Append to `configurator_test.go`:

```go
func TestConfigurator_FlowIncluded_Toggle(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	// Toggle off
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/flows/place-order/included",
		strings.NewReader("included=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.FlowIncluded(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if store.cfg.Flows[0].Included {
		t.Error("Flows[0].Included should be false after toggle")
	}
	// The response is an htmx-friendly fragment containing the updated row.
	if !strings.Contains(w.Body.String(), "place-order") {
		t.Errorf("response should re-render the flow row")
	}

	// Toggle back on.
	req2 := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/flows/place-order/included",
		strings.NewReader("included=true"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.SetPathValue("id", "abc")
	req2.SetPathValue("flowId", "place-order")
	w2 := httptest.NewRecorder()
	h.FlowIncluded(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d", w2.Code)
	}
	if !store.cfg.Flows[0].Included {
		t.Error("Flows[0].Included should be true after second toggle")
	}
}

func TestConfigurator_FlowIncluded_UnknownFlow_404(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/flows/ghost/included",
		strings.NewReader("included=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")
	req.SetPathValue("flowId", "ghost")
	w := httptest.NewRecorder()
	h.FlowIncluded(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
```

- [ ] **Step 2: Run test — expect FAIL**

```bash
cd open-bbcd && go test -v ./internal/handler -run TestConfigurator_FlowIncluded 2>&1 || true
```

Expected: undefined `h.FlowIncluded`.

- [ ] **Step 3: Implement the handler**

Append to `configurator.go`:

```go
// FlowIncluded toggles a flow's `included` boolean. Body: "included=true"
// or "included=false". Responds with the updated flow_row HTML fragment so
// htmx can swap the list row in place.
func (h *ConfiguratorHandler) FlowIncluded(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	included := r.FormValue("included") == "true"

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
	cfg.Flows[idx].Included = included

	if err := h.saveConfig(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	// Re-render the flow row so htmx can swap it in place.
	renderTemplate(w, h.flowsTmpl, "flow_row", map[string]any{
		"AgentID":    agentID,
		"Flow":       cfg.Flows[idx],
		"SelectedID": "",
	})
}
```

- [ ] **Step 4: Run test — expect PASS**

```bash
cd open-bbcd && go test -race -v ./internal/handler -run TestConfigurator_FlowIncluded
```

Expected: both PASS.

- [ ] **Step 5: Update `flow_row` partial to use htmx for the toggle**

In `web/templates/configurator/partials.html`, replace `flow_row`:

```html
{{define "flow_row"}}
<div id="flow-row-{{.Flow.ID}}" class="config-list-row {{if and .SelectedID (eq .SelectedID .Flow.ID)}}selected{{end}}">
  <input type="checkbox" class="config-list-row-toggle"
         {{if .Flow.Included}}checked{{end}}
         hx-post="/agents/{{.AgentID}}/configure/flows/{{.Flow.ID}}/included"
         hx-trigger="change"
         hx-vals='js:{included: event.target.checked}'
         hx-target="#flow-row-{{.Flow.ID}}"
         hx-swap="outerHTML">
  <a href="/agents/{{.AgentID}}/configure/flows/{{.Flow.ID}}" class="config-list-row-link">
    <span class="config-list-row-name">{{.Flow.ID}}</span>
    <span class="config-list-row-meta">{{len .Flow.Workflow.Layout}} nodes</span>
  </a>
</div>
{{end}}
```

(The htmx checkbox sends `included` based on the box's `.checked` state; the response replaces the whole row, so the checkbox state stays consistent with the server even if the user double-clicks.)

- [ ] **Step 6: Commit**

```bash
git add open-bbcd/internal/handler/configurator.go open-bbcd/internal/handler/configurator_test.go open-bbcd/web/templates/configurator/partials.html
git commit -m "feat(open-bbcd): POST /flows/{id}/included toggle (htmx swap)"
```

---

## Task 5: `POST /agents/{id}/configure/skills/{skillId}` — update skill

**Files:**
- Modify: `open-bbcd/internal/handler/configurator.go`
- Modify: `open-bbcd/internal/handler/configurator_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `configurator_test.go`:

```go
func TestConfigurator_SkillUpdate_HappyPath(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name":          {"Place an order"},
		"description":   {"Updated description"},
		"role":          {"write"},
		"capability":    {"orders"},
		"proposed_tool": {"orders.create"},
		"user_phrases":  {"check out\nplace order\nbuy"},
		"external":      {"false"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/skills/place-order",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")
	req.SetPathValue("skillId", "place-order")
	w := httptest.NewRecorder()
	h.SkillUpdate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := store.cfg.Skills[0]
	if got.Name != "Place an order" || got.Description != "Updated description" {
		t.Errorf("metadata not saved: %+v", got)
	}
	if len(got.UserPhrases) != 3 || got.UserPhrases[0] != "check out" {
		t.Errorf("user_phrases not split correctly: %+v", got.UserPhrases)
	}
	if got.External {
		t.Error("External should be false")
	}
}

func TestConfigurator_SkillUpdate_External(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name":          {"Send notification"},
		"role":          {"write"},
		"external":      {"true"},
		"external_note": {"sends to webhook"},
		"user_phrases":  {""},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/skills/place-order",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")
	req.SetPathValue("skillId", "place-order")
	w := httptest.NewRecorder()
	h.SkillUpdate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	got := store.cfg.Skills[0]
	if !got.External || got.ExternalNote != "sends to webhook" {
		t.Errorf("External/Note not saved: %+v", got)
	}
	if got.CapabilityRef != "" {
		t.Errorf("CapabilityRef should be cleared when external=true, got %q", got.CapabilityRef)
	}
}

func TestConfigurator_SkillUpdate_InvalidRole(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name": {"Some skill"},
		"role": {"banana"}, // invalid
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/skills/place-order",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")
	req.SetPathValue("skillId", "place-order")
	w := httptest.NewRecorder()
	h.SkillUpdate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
```

Add `"net/url"` to imports if not already present.

- [ ] **Step 2: Run test — expect FAIL**

```bash
cd open-bbcd && go test -v ./internal/handler -run TestConfigurator_SkillUpdate 2>&1 || true
```

- [ ] **Step 3: Implement the handler + a small parser helper**

Append to `configurator.go`:

```go
// parseSkillForm reads form values shared by SkillUpdate and SkillCreate.
// Validates role; clears capability_ref/external_note when external is the
// other state. Splits user_phrases on newlines or commas.
func parseSkillForm(r *http.Request, capabilities []types.Capability) (types.Skill, error) {
	role := strings.TrimSpace(r.FormValue("role"))
	if role != "read" && role != "write" {
		return types.Skill{}, types.ErrInvalidSkillRole
	}
	external := r.FormValue("external") == "true"
	cap := strings.TrimSpace(r.FormValue("capability"))
	note := strings.TrimSpace(r.FormValue("external_note"))
	if external {
		cap = ""
	} else {
		note = ""
		if cap != "" {
			ok := false
			for _, c := range capabilities {
				if c.Name == cap {
					ok = true
					break
				}
			}
			if !ok {
				return types.Skill{}, fmt.Errorf("%w: capability %q not present in this agent's discovery snapshot",
					types.ErrFlowMapInvalid, cap)
			}
		}
	}

	rawPhrases := r.FormValue("user_phrases")
	var phrases []string
	for _, line := range strings.FieldsFunc(rawPhrases, func(r rune) bool { return r == '\n' || r == ',' }) {
		if s := strings.TrimSpace(line); s != "" {
			phrases = append(phrases, s)
		}
	}

	return types.Skill{
		Name:          strings.TrimSpace(r.FormValue("name")),
		Description:   strings.TrimSpace(r.FormValue("description")),
		Role:          role,
		CapabilityRef: cap,
		External:      external,
		ExternalNote:  note,
		ProposedTool:  strings.TrimSpace(r.FormValue("proposed_tool")),
		UserPhrases:   phrases,
	}, nil
}

// SkillUpdate applies form values to an existing skill in place.
func (h *ConfiguratorHandler) SkillUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	agentID := r.PathValue("id")
	skillID := r.PathValue("skillId")
	cfg, err := h.loadConfig(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	idx := -1
	for i := range cfg.Skills {
		if cfg.Skills[i].ID == skillID {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.NotFound(w, r)
		return
	}

	parsed, err := parseSkillForm(r, cfg.Capabilities)
	if err != nil {
		Error(w, err)
		return
	}
	if parsed.Name == "" {
		Error(w, types.ErrCustomSkillNameRequired)
		return
	}

	// Preserve immutable fields: id, origin, prose_md.
	cur := &cfg.Skills[idx]
	cur.Name = parsed.Name
	cur.Description = parsed.Description
	cur.Role = parsed.Role
	cur.CapabilityRef = parsed.CapabilityRef
	cur.External = parsed.External
	cur.ExternalNote = parsed.ExternalNote
	cur.ProposedTool = parsed.ProposedTool
	cur.UserPhrases = parsed.UserPhrases

	if err := h.saveConfig(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	// Re-render skill_detail so the htmx swap shows the saved state.
	renderTemplate(w, h.skillsTmpl, "skill_detail", map[string]any{
		"AgentID": agentID,
		"Skill":   *cur,
	})
}
```

Add `"fmt"` to imports if not already present.

- [ ] **Step 4: Run test — expect PASS**

```bash
cd open-bbcd && go test -race -v ./internal/handler -run TestConfigurator_SkillUpdate
```

Expected: all three PASS.

- [ ] **Step 5: Commit**

```bash
git add open-bbcd/internal/handler/configurator.go open-bbcd/internal/handler/configurator_test.go
git commit -m "feat(open-bbcd): POST /skills/{id} update with role/capability validation"
```

---

## Task 6: `POST /agents/{id}/configure/skills` — create custom skill

**Files:**
- Modify: `open-bbcd/internal/handler/configurator.go`
- Modify: `open-bbcd/internal/handler/configurator_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `configurator_test.go`:

```go
func TestConfigurator_SkillCreate_HappyPath(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name":         {"Send Email Alert"},
		"description":  {"Notify the user via email"},
		"role":         {"write"},
		"external":     {"true"},
		"external_note": {"sends through SMTP relay"},
		"user_phrases": {"send email\nemail me"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/skills",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.SkillCreate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	if len(store.cfg.Skills) != 2 {
		t.Fatalf("Skills len = %d, want 2", len(store.cfg.Skills))
	}
	created := store.cfg.Skills[1]
	if created.ID != "send-email-alert" {
		t.Errorf("created.ID = %q, want send-email-alert", created.ID)
	}
	if created.Origin != "custom" {
		t.Errorf("created.Origin = %q, want custom", created.Origin)
	}
	if !created.External {
		t.Error("External should be true")
	}
}

func TestConfigurator_SkillCreate_NameCollision_GetsDiscriminator(t *testing.T) {
	cfg := sampleConfig()
	// Existing skill is "place-order"; force a collision by using the same name.
	store := &stubConfigStore{cfg: cfg}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name":     {"Place Order"},
		"role":     {"write"},
		"external": {"true"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/skills",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.SkillCreate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	created := store.cfg.Skills[len(store.cfg.Skills)-1]
	if !strings.HasPrefix(created.ID, "place-order-") || created.ID == "place-order" {
		t.Errorf("collision id = %q, want place-order-<hex>", created.ID)
	}
}

func TestConfigurator_SkillCreate_NameRequired(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{"role": {"write"}, "external": {"true"}}
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/skills",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.SkillCreate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
```

- [ ] **Step 2: Implement the handler**

Append to `configurator.go`:

```go
// SkillCreate adds a new custom skill from form values. The id is server-
// assigned via SlugifySkillName + UniqueSkillID. Returns the rendered
// skill_row partial so htmx can append it to the list.
func (h *ConfiguratorHandler) SkillCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	agentID := r.PathValue("id")
	cfg, err := h.loadConfig(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	parsed, err := parseSkillForm(r, cfg.Capabilities)
	if err != nil {
		Error(w, err)
		return
	}
	if parsed.Name == "" {
		Error(w, types.ErrCustomSkillNameRequired)
		return
	}

	slug := flowmap.SlugifySkillName(parsed.Name)
	if slug == "" {
		Error(w, types.ErrCustomSkillNameRequired)
		return
	}
	taken := make(map[string]struct{}, len(cfg.Skills))
	for _, s := range cfg.Skills {
		taken[s.ID] = struct{}{}
	}
	parsed.ID = flowmap.UniqueSkillID(slug, taken)
	parsed.Origin = "custom"

	cfg.Skills = append(cfg.Skills, parsed)
	if err := h.saveConfig(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	renderTemplate(w, h.skillsTmpl, "skill_row", map[string]any{
		"AgentID":    agentID,
		"Skill":      parsed,
		"SelectedID": "",
	})
}
```

Add `"github.com/DACdigital/OpenBBC/open-bbcd/internal/flowmap"` to imports.

- [ ] **Step 3: Run tests — expect PASS**

```bash
cd open-bbcd && go test -race -v ./internal/handler -run TestConfigurator_SkillCreate
```

Expected: all three PASS.

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/internal/handler/configurator.go open-bbcd/internal/handler/configurator_test.go
git commit -m "feat(open-bbcd): POST /skills create custom skill (slugified id + collision discriminator)"
```

---

## Task 7: `DELETE /agents/{id}/configure/skills/{skillId}` — delete custom skill (gated)

**Files:**
- Modify: `open-bbcd/internal/handler/configurator.go`
- Modify: `open-bbcd/internal/handler/configurator_test.go`

The deletion is allowed only when the skill is `custom` AND no flow's workflow references the skill ID. Reuse the existing `skillNodeRe` regex from `internal/flowmap/mermaid.go` for the reference check.

- [ ] **Step 1: Export a helper from `internal/flowmap` for reference checks**

In `open-bbcd/internal/flowmap/mermaid.go`, add a small exported helper:

```go
// WorkflowReferencesSkill returns true if the mermaid string declares any
// id[<skill-id>] rectangle node whose label equals the given skill id.
func WorkflowReferencesSkill(mermaid, skillID string) bool {
	for _, m := range skillNodeRe.FindAllStringSubmatch(mermaid, -1) {
		if m[2] == skillID {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Write the failing tests**

Append to `configurator_test.go`:

```go
func TestConfigurator_SkillDelete_Custom_OK(t *testing.T) {
	cfg := sampleConfig()
	cfg.Skills = append(cfg.Skills, types.Skill{
		ID: "custom-thing", Origin: "custom", Name: "Custom thing", Role: "write",
		External: true,
	})
	store := &stubConfigStore{cfg: cfg}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodDelete,
		"/agents/abc/configure/skills/custom-thing", nil)
	req.SetPathValue("id", "abc")
	req.SetPathValue("skillId", "custom-thing")
	w := httptest.NewRecorder()
	h.SkillDelete(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	for _, s := range store.cfg.Skills {
		if s.ID == "custom-thing" {
			t.Error("custom-thing should have been removed")
		}
	}
}

func TestConfigurator_SkillDelete_Discovered_409(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodDelete,
		"/agents/abc/configure/skills/place-order", nil)
	req.SetPathValue("id", "abc")
	req.SetPathValue("skillId", "place-order")
	w := httptest.NewRecorder()
	h.SkillDelete(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (cannot delete discovered skill)", w.Code)
	}
}

func TestConfigurator_SkillDelete_Referenced_409(t *testing.T) {
	cfg := sampleConfig()
	// Add a custom skill that is referenced by the existing flow's workflow.
	cfg.Skills = append(cfg.Skills, types.Skill{
		ID: "needed-by-flow", Origin: "custom", Name: "Needed", Role: "write",
		External: true,
	})
	cfg.Flows[0].Workflow.Mermaid = "flowchart TD\n" +
		"  start([start]) --> a[needed-by-flow]\n" +
		"  a --> e([end])"
	store := &stubConfigStore{cfg: cfg}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodDelete,
		"/agents/abc/configure/skills/needed-by-flow", nil)
	req.SetPathValue("id", "abc")
	req.SetPathValue("skillId", "needed-by-flow")
	w := httptest.NewRecorder()
	h.SkillDelete(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (skill referenced by flow workflow)", w.Code)
	}
}
```

- [ ] **Step 3: Implement the handler**

Append to `configurator.go`:

```go
// SkillDelete removes a custom skill. Discovered skills cannot be deleted
// (409). Custom skills cannot be deleted while referenced by any flow's
// workflow (409). On success returns 200 with an empty body so htmx can
// remove the row in place.
func (h *ConfiguratorHandler) SkillDelete(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	skillID := r.PathValue("skillId")
	cfg, err := h.loadConfig(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	idx := -1
	for i := range cfg.Skills {
		if cfg.Skills[i].ID == skillID {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.NotFound(w, r)
		return
	}
	if cfg.Skills[idx].Origin != "custom" {
		Error(w, types.ErrSkillReferenced)
		return
	}
	for _, f := range cfg.Flows {
		if flowmap.WorkflowReferencesSkill(f.Workflow.Mermaid, skillID) {
			Error(w, types.ErrSkillReferenced)
			return
		}
	}

	cfg.Skills = append(cfg.Skills[:idx], cfg.Skills[idx+1:]...)
	if err := h.saveConfig(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
cd open-bbcd && go test -race -v ./internal/handler -run TestConfigurator_SkillDelete
go test -race -v ./internal/flowmap -run "TestValidate|TestParse|TestSlugify|TestUniqueSkillID|TestWorkflowReferencesSkill" 2>&1 || true
```

Expected: 3 handler tests PASS.

- [ ] **Step 5: Commit**

```bash
git add open-bbcd/internal/flowmap/mermaid.go open-bbcd/internal/handler/configurator.go open-bbcd/internal/handler/configurator_test.go
git commit -m "feat(open-bbcd): DELETE /skills/{id} (custom-only, unreferenced; 409 otherwise)"
```

---

## Task 8: chip-input.js + chip CSS

**Files:**
- Create: `open-bbcd/web/static/chip-input.js`
- Modify: `open-bbcd/web/static/configurator.css`
- Modify: `open-bbcd/web/templates/layout.html`

A small vanilla JS widget that turns a `<div data-chip-input>` into a multi-tag input. Each chip is added on Enter or comma; Backspace removes the last chip when the input is empty; the underlying `<input type="hidden" name="user_phrases">` carries the comma-joined value for form submission.

- [ ] **Step 1: Create the widget script**

Create `open-bbcd/web/static/chip-input.js`:

```js
// chip-input.js — turns <div data-chip-input> into a tag input.
// Markup contract:
//   <div class="chip-input" data-chip-input data-name="user_phrases" data-value="a,b,c"></div>
// Renders chips, an inline text field, and a hidden input named data-name carrying
// the current comma-joined value. Triggers a 'change' on the hidden input on every
// add/remove so htmx form serialization picks up the latest value.

(function () {
  function init(el) {
    if (el.dataset.chipReady === "1") return;
    el.dataset.chipReady = "1";

    const name = el.dataset.name || "phrases";
    const initial = (el.dataset.value || "")
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);

    const hidden = document.createElement("input");
    hidden.type = "hidden";
    hidden.name = name;
    el.appendChild(hidden);

    const input = document.createElement("input");
    input.type = "text";
    input.className = "chip-input-field";
    input.placeholder = "type a phrase, press Enter";
    el.appendChild(input);

    let chips = [];

    function sync() {
      hidden.value = chips.join(",");
      hidden.dispatchEvent(new Event("change", { bubbles: true }));
      el.querySelectorAll(".chip").forEach((c) => c.remove());
      chips.forEach((value, idx) => {
        const span = document.createElement("span");
        span.className = "chip";
        span.textContent = value;
        const x = document.createElement("button");
        x.type = "button";
        x.className = "chip-x";
        x.textContent = "×";
        x.onclick = () => {
          chips.splice(idx, 1);
          sync();
        };
        span.appendChild(x);
        el.insertBefore(span, input);
      });
    }

    function addFromInput() {
      const v = input.value.trim();
      if (v && !chips.includes(v)) {
        chips.push(v);
      }
      input.value = "";
      sync();
    }

    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === ",") {
        e.preventDefault();
        addFromInput();
      } else if (e.key === "Backspace" && input.value === "" && chips.length) {
        chips.pop();
        sync();
      }
    });
    input.addEventListener("blur", addFromInput);

    chips = initial;
    sync();
  }

  function scan(root) {
    (root || document).querySelectorAll("[data-chip-input]").forEach(init);
  }

  document.addEventListener("DOMContentLoaded", () => scan());
  // Re-scan after htmx swaps so newly-rendered detail panes get wired.
  document.body.addEventListener("htmx:afterSwap", (e) => scan(e.target));
})();
```

- [ ] **Step 2: Add chip CSS**

Append to `open-bbcd/web/static/configurator.css`:

```css
.chip-input {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
  padding: 6px;
  background: #0d1117;
  border: 1px solid #30363d;
  border-radius: 6px;
  min-height: 36px;
}

.chip-input-field {
  flex: 1;
  min-width: 120px;
  background: transparent;
  color: #e6edf3;
  border: none;
  outline: none;
  font-size: 13px;
}

.chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 4px 2px 8px;
  background: #21262d;
  border-radius: 12px;
  font-size: 12px;
  color: #c9d1d9;
}

.chip-x {
  background: transparent;
  border: none;
  color: #8b949e;
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  padding: 0 2px;
}

.chip-x:hover { color: #f85149; }

/* Form-specific styles for the skill detail edit form. */
.config-form { display: flex; flex-direction: column; gap: 12px; max-width: 640px; }
.config-form label { display: flex; flex-direction: column; gap: 4px; font-size: 13px; color: #8b949e; }
.config-form input[type=text],
.config-form textarea,
.config-form select {
  background: #0d1117;
  border: 1px solid #30363d;
  color: #e6edf3;
  border-radius: 6px;
  padding: 8px 10px;
  font-size: 13px;
  font-family: inherit;
}
.config-form textarea { resize: vertical; min-height: 80px; }
.config-form .form-row { display: flex; gap: 12px; align-items: flex-end; }
.config-form .form-row > * { flex: 1; }
.config-form .form-actions { display: flex; gap: 8px; margin-top: 8px; }
.config-form-saved { color: #2ea043; font-size: 12px; opacity: 0; transition: opacity .2s; }
.config-form-saved.show { opacity: 1; }

.skill-add-trigger {
  margin: 12px;
  padding: 8px 12px;
  background: #1f6feb;
  color: #fff;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  font-size: 13px;
}

.skill-add-trigger:hover { background: #388bfd; }

.danger-button {
  background: #21262d;
  color: #f85149;
  border: 1px solid #30363d;
}
.danger-button:hover { background: #2d1418; border-color: #f85149; }
```

- [ ] **Step 3: Add the script tag to layout.html**

In `open-bbcd/web/templates/layout.html`, after the existing `<script src="/static/htmx.min.js"></script>` line, add:

```html
  <script defer src="/static/chip-input.js"></script>
```

- [ ] **Step 4: Confirm embed picks up the new file**

```bash
cd open-bbcd && go build ./... && ls web/static/chip-input.js
```

Expected: file exists; build clean.

- [ ] **Step 5: Commit**

```bash
git add open-bbcd/web/static/chip-input.js open-bbcd/web/static/configurator.css open-bbcd/web/templates/layout.html
git commit -m "feat(open-bbcd): chip-input widget + form CSS for configurator"
```

---

## Task 9: Skill detail form (edit) + create form

**Files:**
- Modify: `open-bbcd/web/templates/configurator/partials.html`
- Modify: `open-bbcd/web/templates/configurator/skills.html`

The current PR1 `skill_detail` is read-only. Replace it with a form. Add a `skill_form_fields` shared partial used by both the edit form and the new "+ Add skill" form.

- [ ] **Step 1: Replace `skill_detail` and add `skill_form_fields`**

In `open-bbcd/web/templates/configurator/partials.html`, replace the existing `{{define "skill_detail"}}...{{end}}` block with:

```html
{{define "skill_form_fields"}}
<input type="hidden" name="agent_id" value="{{.AgentID}}">

<label>Name
  <input type="text" name="name" value="{{.Skill.Name}}" required>
</label>

<label>Description
  <textarea name="description" rows="2">{{.Skill.Description}}</textarea>
</label>

<div class="form-row">
  <label>Role
    <select name="role">
      <option value="read"  {{if eq .Skill.Role "read"}}selected{{end}}>read</option>
      <option value="write" {{if eq .Skill.Role "write"}}selected{{end}}>write</option>
    </select>
  </label>
  <label>Proposed tool
    <input type="text" name="proposed_tool" value="{{.Skill.ProposedTool}}" placeholder="e.g. orders.create">
  </label>
</div>

<label>User phrases
  <div class="chip-input"
       data-chip-input
       data-name="user_phrases"
       data-value="{{range $i, $p := .Skill.UserPhrases}}{{if $i}},{{end}}{{$p}}{{end}}"></div>
</label>

<label>Capability
  <select name="capability"
          hx-on:change="
            const isExt = this.value === '__external__';
            this.form.querySelector('[name=external]').value = isExt ? 'true' : 'false';
            this.form.querySelector('.external-note-row').style.display = isExt ? '' : 'none';
          ">
    {{range .Capabilities}}
    <option value="{{.Name}}" {{if and (not $.Skill.External) (eq .Name $.Skill.CapabilityRef)}}selected{{end}}>{{.Name}}</option>
    {{end}}
    <option value="__external__" {{if .Skill.External}}selected{{end}}>External (no discovered capability)</option>
  </select>
</label>

<input type="hidden" name="external" value="{{if .Skill.External}}true{{else}}false{{end}}">

<div class="external-note-row" style="{{if not .Skill.External}}display:none{{end}}">
  <label>External note
    <textarea name="external_note" rows="2" placeholder="why this skill has no discovered capability">{{.Skill.ExternalNote}}</textarea>
  </label>
</div>
{{end}}

{{define "skill_detail"}}
<div class="config-detail">
  <h2>{{.Skill.Name}}</h2>
  <p class="config-detail-id"><code>{{.Skill.ID}}</code> · <span class="badge badge-{{.Skill.Origin}}">{{.Skill.Origin}}</span></p>

  <form class="config-form"
        hx-post="/agents/{{.AgentID}}/configure/skills/{{.Skill.ID}}"
        hx-target="closest .config-detail"
        hx-swap="outerHTML"
        hx-indicator=".config-form-saved">
    {{template "skill_form_fields" (dict
      "AgentID"     .AgentID
      "Skill"       .Skill
      "Capabilities" .Capabilities)}}

    <div class="form-actions">
      <button type="submit" class="btn-primary">Save</button>
      {{if eq .Skill.Origin "custom"}}
      <button type="button"
              class="btn-secondary danger-button"
              hx-delete="/agents/{{.AgentID}}/configure/skills/{{.Skill.ID}}"
              hx-target="#skill-row-{{.Skill.ID}}"
              hx-swap="delete"
              hx-confirm="Delete this custom skill?">Delete</button>
      {{end}}
      <span class="config-form-saved">Saved</span>
    </div>
  </form>

  {{if .Skill.ProseMD}}
  <div class="config-section">
    <h3>Source (from discovery)</h3>
    <div class="prose">{{renderMarkdown .Skill.ProseMD}}</div>
  </div>
  {{end}}
</div>
{{end}}
```

(Note: the form passes `Capabilities` so the dropdown can list them. The handler must include `Capabilities` in the data passed when rendering this template.)

- [ ] **Step 2: Update `skill_row` to use a stable id for htmx delete-swap**

In `partials.html`, change `skill_row` to wrap the row in a div with id:

```html
{{define "skill_row"}}
<div id="skill-row-{{.Skill.ID}}">
  <a href="/agents/{{.AgentID}}/configure/skills/{{.Skill.ID}}"
     class="config-list-row {{if and .SelectedID (eq .SelectedID .Skill.ID)}}selected{{end}}">
    <span class="config-list-row-name">{{.Skill.ID}}</span>
    <span class="config-list-row-meta">
      <span class="badge badge-{{.Skill.Origin}}">{{.Skill.Origin}}</span>
      {{if .Skill.External}}<span class="badge">external</span>{{else}}<span class="muted">{{.Skill.CapabilityRef}}</span>{{end}}
      <code>{{.Skill.ProposedTool}}</code>
    </span>
  </a>
</div>
{{end}}
```

- [ ] **Step 3: Add the "+ Add skill" affordance to skills.html**

In `open-bbcd/web/templates/configurator/skills.html`, replace the file content with:

```html
{{define "tab_content"}}
<div class="config-two-pane">
  <aside class="config-list">
    <div class="config-list-header">Skills ({{len .Config.Skills}})</div>
    <div id="skill-list-rows">
      {{range .Config.Skills}}
        {{template "skill_row" (dict "AgentID" $.AgentID "Skill" . "SelectedID" (selectedSkillID $.SelectedSkill))}}
      {{else}}
        <p class="empty-state">No skills in this discovery snapshot.</p>
      {{end}}
    </div>
    <button class="skill-add-trigger"
            hx-get="/agents/{{.AgentID}}/configure/skills/new"
            hx-target=".config-detail-pane"
            hx-swap="innerHTML">+ Add skill</button>
  </aside>
  <main class="config-detail-pane">
    {{if .SelectedSkill}}
      {{template "skill_detail" (dict "AgentID" .AgentID "Skill" .SelectedSkill "Capabilities" .Config.Capabilities)}}
    {{else}}
      <p class="config-detail-empty">Select a skill on the left or click + Add skill.</p>
    {{end}}
  </main>
</div>
{{end}}
```

- [ ] **Step 4: Update existing `Skills` handler to pass `Capabilities`**

In `configurator.go`'s `Skills` method, where the data is composed for selected-skill rendering, the existing render targets the layout template with `data` (a `configPageData`). The template now needs `.Config.Capabilities` available — which it already is. No handler change needed for the Skills tab itself.

But the `skill_detail` partial expects `Capabilities` as a top-level key in its `dict`. The `skills.html` template invocation already passes `.Config.Capabilities`. Confirmed by inspecting the template above.

- [ ] **Step 5: Add the "new skill" GET route**

The "+ Add skill" button issues `GET /agents/{id}/configure/skills/new`. Add a handler that renders the same `skill_detail` partial with a blank `types.Skill{Origin: "custom", Role: "read"}` and the existing capabilities list.

Append to `configurator.go`:

```go
// SkillNew renders an empty skill_detail form for creating a custom skill.
// The form's submit URL is rewritten to POST /skills (no skillId), creating
// a new row instead of updating an existing one.
func (h *ConfiguratorHandler) SkillNew(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	cfg, err := h.loadConfig(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	blank := types.Skill{Origin: "custom", Role: "read"}
	renderTemplate(w, h.skillsTmpl, "skill_new_form", map[string]any{
		"AgentID":      agentID,
		"Skill":        blank,
		"Capabilities": cfg.Capabilities,
	})
}
```

And add a `skill_new_form` partial in `partials.html` (separate from `skill_detail` because the form action differs):

```html
{{define "skill_new_form"}}
<div class="config-detail">
  <h2>New custom skill</h2>
  <p class="config-detail-id">A new id will be generated from the name.</p>

  <form class="config-form"
        hx-post="/agents/{{.AgentID}}/configure/skills"
        hx-target="#skill-list-rows"
        hx-swap="beforeend"
        hx-indicator=".config-form-saved">
    {{template "skill_form_fields" (dict
      "AgentID"     .AgentID
      "Skill"       .Skill
      "Capabilities" .Capabilities)}}

    <div class="form-actions">
      <button type="submit" class="btn-primary">Create</button>
      <span class="config-form-saved">Created</span>
    </div>
  </form>
</div>
{{end}}
```

- [ ] **Step 6: Update existing tests for new template requirements**

The PR1 `TestConfigurator_SkillsTab_ShowsSkillRow` test checked for `place-order` and `orders.create` in the Skills tab body. With the row wrapped in `<div id="skill-row-...">`, the assertions still hold (substring search). No change needed.

The PR1 `TestConfigurator_FlowsTab_RendersFlowsList` similarly continues to work since `flow_row` was extended, not narrowed.

Verify by running:

```bash
cd open-bbcd && go test -race -v ./internal/handler -run TestConfigurator
```

Expected: all pre-existing PR1 tests + all new PR2 tests PASS.

- [ ] **Step 7: Commit**

```bash
git add open-bbcd/web/templates/configurator/partials.html \
        open-bbcd/web/templates/configurator/skills.html \
        open-bbcd/internal/handler/configurator.go
git commit -m "feat(open-bbcd): editable skill detail form + add-skill flow"
```

---

## Task 10: Wire the four new routes in api.go

**Files:**
- Modify: `open-bbcd/internal/handler/api.go`

- [ ] **Step 1: Register the routes**

In `open-bbcd/internal/handler/api.go`, after the existing `mux.HandleFunc("GET /agents/{id}/configure/capabilities/{capName}", configuratorHandler.Capabilities)` line, add:

```go
	mux.HandleFunc("POST /agents/{id}/configure/flows/{flowId}/included", configuratorHandler.FlowIncluded)
	mux.HandleFunc("GET /agents/{id}/configure/skills/new", configuratorHandler.SkillNew)
	mux.HandleFunc("POST /agents/{id}/configure/skills", configuratorHandler.SkillCreate)
	mux.HandleFunc("POST /agents/{id}/configure/skills/{skillId}", configuratorHandler.SkillUpdate)
	mux.HandleFunc("DELETE /agents/{id}/configure/skills/{skillId}", configuratorHandler.SkillDelete)
```

Note: `GET /skills/new` must register **before** `GET /skills/{skillId}` so the literal `/new` segment doesn't get captured as a `{skillId}`. Go 1.22+'s mux uses pattern specificity, so this is automatic — confirm with a quick test.

- [ ] **Step 2: Build + run all tests**

```bash
cd open-bbcd && go build ./... && go test -race ./...
```

Expected: clean build, every package PASS.

- [ ] **Step 3: Commit**

```bash
git add open-bbcd/internal/handler/api.go
git commit -m "feat(open-bbcd): register configurator edit routes"
```

---

## Task 11: End-to-end smoke test

This validates PR2 against a real DB and the configurator UI.

- [ ] **Step 1: Start Postgres and apply migrations**

```bash
cd open-bbcd && docker-compose up -d
source .env
make migrate-up
```

Expected: schema is up to date (migration 006 already applied during PR1).

- [ ] **Step 2: Build the sample zip (or reuse from PR1)**

```bash
cd open-bbcd/internal/flowmap/testdata
python3 -c "
import zipfile, os
with zipfile.ZipFile('/tmp/sample-flowmap.zip', 'w') as z:
    for root, _, files in os.walk('sample-flowmap'):
        for f in files:
            p = os.path.join(root, f)
            z.write(p, os.path.relpath(p, 'sample-flowmap'))
"
cd ../../..
```

- [ ] **Step 3: Run the server (different port if 8080 is busy)**

```bash
cd open-bbcd && SERVER_PORT=8082 make run &
sleep 3
```

- [ ] **Step 4: Submit the wizard and capture the agent id**

```bash
RESPONSE=$(curl -i -s -X POST http://localhost:8082/agents/wizard \
  -F "name=pr2-smoke" \
  -F "scope=smoke" \
  -F "should_do=test" \
  -F "should_not_do=fail" \
  -F "business_domain=internal" \
  -F "discovery_file=@/tmp/sample-flowmap.zip")
AID=$(echo "$RESPONSE" | grep -i '^Location:' | sed 's|.*/agents/\([^/]*\)/configure.*|\1|' | tr -d '\r')
echo "Agent: $AID"
```

- [ ] **Step 5: Toggle the flow off, verify**

```bash
curl -i -s -X POST "http://localhost:8082/agents/$AID/configure/flows/place-order/included" \
  -d "included=false" | head -5

psql "$DATABASE_URL" -tAc "SELECT flow_map_config->'flows'->0->>'included' FROM agents WHERE id='$AID'"
```

Expected: HTTP 200 with row HTML; psql returns `false`.

Toggle back on:

```bash
curl -s -X POST "http://localhost:8082/agents/$AID/configure/flows/place-order/included" \
  -d "included=true" > /dev/null

psql "$DATABASE_URL" -tAc "SELECT flow_map_config->'flows'->0->>'included' FROM agents WHERE id='$AID'"
```

Expected: `true`.

- [ ] **Step 6: Update the existing skill**

```bash
curl -i -s -X POST "http://localhost:8082/agents/$AID/configure/skills/place-order" \
  -d "name=Place an order&description=Updated&role=write&capability=orders&proposed_tool=orders.create&user_phrases=check%20out%2Cbuy&external=false" | head -5

psql "$DATABASE_URL" -tAc "SELECT flow_map_config->'skills'->0->>'description' FROM agents WHERE id='$AID'"
```

Expected: HTTP 200; psql returns `Updated`.

- [ ] **Step 7: Create a custom skill**

```bash
curl -i -s -X POST "http://localhost:8082/agents/$AID/configure/skills" \
  -d "name=Send%20Email&role=write&external=true&external_note=via%20SMTP&user_phrases=email" | head -5

psql "$DATABASE_URL" -tAc "SELECT jsonb_array_length(flow_map_config->'skills') FROM agents WHERE id='$AID'"
psql "$DATABASE_URL" -tAc "SELECT flow_map_config->'skills'->1->>'id' FROM agents WHERE id='$AID'"
```

Expected: skills length = 2; new skill id = `send-email`.

- [ ] **Step 8: Delete-discovered should fail with 409**

```bash
curl -i -s -X DELETE "http://localhost:8082/agents/$AID/configure/skills/place-order" | head -3
```

Expected: HTTP 409.

- [ ] **Step 9: Delete the custom skill (succeeds)**

```bash
curl -i -s -X DELETE "http://localhost:8082/agents/$AID/configure/skills/send-email" | head -3
psql "$DATABASE_URL" -tAc "SELECT jsonb_array_length(flow_map_config->'skills') FROM agents WHERE id='$AID'"
```

Expected: HTTP 200; skills length back to 1.

- [ ] **Step 10: Browser walk**

Open `http://localhost:8082/agents/$AID/configure/skills` in a browser. Verify:

- The Skills tab list shows `place-order` with the updated description carried into the detail form.
- Click the row — the detail pane shows the skill form with prefilled fields, including the user-phrases as chips.
- Add a chip ("test phrase"), click Save — the form refreshes with the new chip persisted.
- Click "+ Add skill" — the detail pane shows a blank form. Type a name, switch capability dropdown to "External (no discovered capability)", note appears, fill it, click Create — the new row appears at the bottom of the list.
- Click the new custom row, click Delete, confirm — the row disappears.

- [ ] **Step 11: Stop server**

```bash
pkill -f open-bbcd
docker-compose down
```

- [ ] **Step 12: Final test sweep**

```bash
cd open-bbcd && go test -race ./...
```

Expected: every package PASS.

- [ ] **Step 13: Commit any browser-walk fixes**

If Step 10 surfaced template/CSS issues, fix and commit. Otherwise no commit.

---

## Done criteria for PR2

- ✅ Four new sentinel errors defined and mapped (3 to 400, 1 to 409).
- ✅ `ConfigStore` interface (read+write) replaces `ConfigGetter`; PR1 tests still pass.
- ✅ Custom-skill ID generation deterministic with collision discriminator (TDD-tested).
- ✅ Flow include/exclude toggle works end-to-end with htmx.
- ✅ Skill update form persists every editable field; role validation enforced.
- ✅ Custom skill creation + deletion works; deletion is gated by origin and reference checks.
- ✅ Capabilities tab remains read-only (unchanged).
- ✅ Smoke test passes against a real DB.
- ✅ All `go test -race ./...` packages green.

PR3 (Drawflow workflow editor) and PR4 (finalize + YAML download) get their own plans after this lands.
