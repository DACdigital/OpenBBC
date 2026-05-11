# Flow-Map Configurator — PR4 (finalize + YAML download)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close out the configurator. The "Finalize →" pill in the configurator chrome (currently disabled in PR1) becomes a real link to a small confirmation page; confirming flips the agent's `status` `INITIALIZING → DRAFT` and redirects to `/agents/ui`. The agent's full config is downloadable as YAML at `GET /agents/{id}/config.yaml` for any agent in `DRAFT` or later.

**Architecture:**
- `Finalize` is a hard-coded two-step flow: `GET /configure/finalize` renders a small confirmation page; `POST /finalize` does the state transition and redirects. Same handler file (`internal/handler/configurator.go`).
- `DownloadYAML` is a single `GET /agents/{id}/config.yaml`. Marshals `flow_map_config` JSONB through `gopkg.in/yaml.v3` so the on-disk shape exactly mirrors the JSONB shape from PR1. Round-trip property test guards against drift.
- One new sentinel error (`ErrInvalidAgentStatus` → 409) covers the case where the user tries to finalize an already-DRAFT agent.
- One new repository method (`UpdateStatus`) handles the status flip. The version-chain machinery from `migrations/003_add_agent_versioning.sql` is unchanged.

**Tech stack:** Go 1.26, `gopkg.in/yaml.v3` (existing dep — used by the wizard schema loader), no new vendored libs.

**Spec:** `docs/superpowers/specs/2026-05-07-flow-map-configurator-design.md` (PR4 section).

**Branching:** Branch off updated `main`. Branch name: `feat/flow-map-configurator-pr4`. (Already set up.)

---

## File Structure

```
open-bbcd/
├── internal/
│   ├── types/
│   │   └── errors.go                    # MODIFY: add ErrInvalidAgentStatus
│   ├── handler/
│   │   ├── handler.go                   # MODIFY: map ErrInvalidAgentStatus → 409
│   │   ├── configurator.go              # MODIFY: add FinalizeConfirm + Finalize + DownloadYAML + 1 helper
│   │   ├── configurator_test.go         # MODIFY: 7 tests (finalize confirm/post + yaml + round-trip)
│   │   └── api.go                       # MODIFY: register 3 new routes
│   └── repository/
│       ├── agent.go                     # MODIFY: add UpdateStatus
│       └── agent_test.go                # MODIFY: 1 test (or skip — DB-dependent)
└── web/
    └── templates/
        ├── configurator/
        │   ├── layout.html              # MODIFY: replace disabled span with active finalize link
        │   └── finalize.html            # CREATE: confirmation page
        └── agent-versions.html          # MODIFY: add "Download config" column for DRAFT+
```

---

## Task 1: Sentinel error + 409 mapping + UpdateStatus repository method

**Files:**
- Modify: `open-bbcd/internal/types/errors.go`
- Modify: `open-bbcd/internal/handler/handler.go`
- Modify: `open-bbcd/internal/repository/agent.go`

### Step 1: Add the sentinel

Append to the existing `var (...)` block in `open-bbcd/internal/types/errors.go`:

```go
	ErrInvalidAgentStatus = errors.New("agent is not in a valid status for this transition")
```

### Step 2: Map to 409 in handler.go

In `open-bbcd/internal/handler/handler.go::Error`, find the existing 409 case (added in PR2 for `ErrSkillReferenced`):

```go
case errors.Is(err, types.ErrSkillReferenced):
    status = http.StatusConflict
```

Extend it to include the new sentinel:

```go
case errors.Is(err, types.ErrSkillReferenced),
    errors.Is(err, types.ErrInvalidAgentStatus):
    status = http.StatusConflict
```

### Step 3: Add `UpdateStatus` to AgentRepository

Append to `open-bbcd/internal/repository/agent.go`:

```go
// UpdateStatus transitions the agent's status. Used by Finalize
// (INITIALIZING → DRAFT). Returns ErrInvalidAgentStatus if the current
// status doesn't match expectedFrom — preventing accidental re-finalize.
func (r *AgentRepository) UpdateStatus(ctx context.Context, agentID, expectedFrom, to string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE agents
		SET status = $3, updated_at = now()
		WHERE id = $1 AND status = $2
	`, agentID, expectedFrom, to)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Either the agent doesn't exist, or its status isn't expectedFrom.
		// Distinguish: re-fetch to choose the right sentinel.
		var cur string
		row := r.db.QueryRowContext(ctx, `SELECT status FROM agents WHERE id = $1`, agentID)
		if err := row.Scan(&cur); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return types.ErrNotFound
			}
			return err
		}
		return fmt.Errorf("%w: have %q, want %q", types.ErrInvalidAgentStatus, cur, expectedFrom)
	}
	return nil
}
```

Add `"fmt"` to the imports if it isn't already there (other functions in this file use it; likely fine).

### Step 4: Build

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4/open-bbcd
go build ./...
go test -race ./internal/repository/... ./internal/types/...
```

Expected: clean build; existing repository tests still pass.

### Step 5: Commit

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4
git add open-bbcd/internal/types/errors.go \
        open-bbcd/internal/handler/handler.go \
        open-bbcd/internal/repository/agent.go
git commit -m "feat(open-bbcd): ErrInvalidAgentStatus sentinel + UpdateStatus repo method"
```

---

## Task 2: Widen `ConfigStore` interface with `UpdateStatus`

**Files:**
- Modify: `open-bbcd/internal/handler/configurator.go` — add `UpdateStatus` to the `ConfigStore` interface
- Modify: `open-bbcd/internal/handler/configurator_test.go` — add `UpdateStatus` method to `stubConfigStore`

### Step 1: Extend the interface

In `open-bbcd/internal/handler/configurator.go`, find the `ConfigStore` interface (added in PR2). Add one method:

```go
type ConfigStore interface {
	GetFlowMapConfig(ctx context.Context, agentID string) (cfg []byte, parseErr string, err error)
	GetByID(ctx context.Context, id string) (*types.Agent, error)
	UpdateFlowMapConfig(ctx context.Context, agentID string, cfg []byte) error
	UpdateStatus(ctx context.Context, agentID, expectedFrom, to string) error
}
```

### Step 2: Extend the test stub

In `open-bbcd/internal/handler/configurator_test.go`, find `stubConfigStore`. Add fields + method:

```go
type stubConfigStore struct {
	cfg          types.FlowMapConfig
	getErr       error
	parseErr     string
	updates      int
	updateFn     func(cfg []byte) error
	statusFn     func(agentID, expectedFrom, to string) error
	currentStatus string // optional override; defaults to "INITIALIZING"
}
```

Update the existing `GetByID` method to use `currentStatus` (with default):

```go
func (s *stubConfigStore) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	status := s.currentStatus
	if status == "" {
		status = "INITIALIZING"
	}
	return &types.Agent{ID: id, Name: s.cfg.Name, Status: status}, nil
}
```

Add the new method:

```go
func (s *stubConfigStore) UpdateStatus(ctx context.Context, agentID, expectedFrom, to string) error {
	if s.statusFn != nil {
		return s.statusFn(agentID, expectedFrom, to)
	}
	cur := s.currentStatus
	if cur == "" {
		cur = "INITIALIZING"
	}
	if cur != expectedFrom {
		return fmt.Errorf("%w: have %q, want %q", types.ErrInvalidAgentStatus, cur, expectedFrom)
	}
	s.currentStatus = to
	return nil
}
```

Add the `"fmt"` import to the test file if not already present.

### Step 3: Build + run existing handler tests

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4/open-bbcd
go build ./...
go test -race -v ./internal/handler -run TestConfigurator
```

Expected: clean build; all existing tests still PASS (the wider interface is satisfied by both the real repo and the stub).

### Step 4: Commit

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4
git add open-bbcd/internal/handler/configurator.go open-bbcd/internal/handler/configurator_test.go
git commit -m "feat(open-bbcd): widen ConfigStore with UpdateStatus"
```

---

## Task 3: Finalize handlers (`FinalizeConfirm` + `Finalize`) with TDD

**Files:**
- Modify: `open-bbcd/internal/handler/configurator.go` — add `FinalizeConfirm` (GET) + `Finalize` (POST)
- Modify: `open-bbcd/internal/handler/configurator_test.go` — add 3 tests
- Create: `open-bbcd/web/templates/configurator/finalize.html`

### Step 1: Write the failing tests

Append to `open-bbcd/internal/handler/configurator_test.go`:

```go
func TestConfigurator_FinalizeConfirm_RendersPage(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodGet,
		"/agents/abc/configure/finalize", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.FinalizeConfirm(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Finalize") {
		t.Errorf("response should contain a Finalize heading or button: %s", body[:min(200, len(body))])
	}
	// The page must POST to /agents/abc/finalize.
	if !strings.Contains(body, "/agents/abc/finalize") {
		t.Errorf("response should include the POST target: %s", body)
	}
}

func TestConfigurator_Finalize_HappyPath_RedirectsToAgentsUI(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()} // currentStatus defaults to "INITIALIZING"
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/finalize", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.Finalize(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/agents/ui" {
		t.Errorf("Location = %q, want /agents/ui", loc)
	}
	if store.currentStatus != "DRAFT" {
		t.Errorf("status = %q, want DRAFT", store.currentStatus)
	}
}

func TestConfigurator_Finalize_WrongStatus_409(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig(), currentStatus: "DRAFT"}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/finalize", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.Finalize(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}
```

(`min` is the helper already in the file from PR3 (`minInt`) — rename to `min` or use Go's built-in `min` (Go 1.21+). The existing tests likely use the helper; check and reuse.)

### Step 2: Run test — expect FAIL

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4/open-bbcd
go test -v ./internal/handler -run TestConfigurator_Finalize 2>&1 || true
```

Expected: undefined `h.FinalizeConfirm`, `h.Finalize`.

### Step 3: Implement the handlers

Append to `open-bbcd/internal/handler/configurator.go`:

```go
// FinalizeConfirm renders the small confirmation page shown when the user
// clicks "Finalize →" in the configurator. Submitting the page's form
// POSTs to /agents/{id}/finalize.
func (h *ConfiguratorHandler) FinalizeConfirm(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	agent, err := h.repo.GetByID(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cfgBytes, parseErr, err := h.repo.GetFlowMapConfig(r.Context(), agentID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := configPageData{
		Active:     "agents",
		AgentID:    agentID,
		AgentName:  agent.Name,
		Tab:        "finalize",
		ParseError: parseErr,
	}
	if len(cfgBytes) > 0 {
		_ = json.Unmarshal(cfgBytes, &data.Config)
	}
	renderTemplate(w, h.finalizeTmpl, "layout", data)
}

// Finalize flips status INITIALIZING → DRAFT and redirects to /agents/ui.
// 409 (ErrInvalidAgentStatus) if the agent isn't in INITIALIZING.
func (h *ConfiguratorHandler) Finalize(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if err := h.repo.UpdateStatus(r.Context(), agentID, "INITIALIZING", "DRAFT"); err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/agents/ui", http.StatusSeeOther)
}
```

### Step 4: Add `finalizeTmpl` to `ConfiguratorHandler` + parse it

In `open-bbcd/internal/handler/configurator.go`, find the `ConfiguratorHandler` struct (declared in PR1). Add a field:

```go
type ConfiguratorHandler struct {
	repo                                                   ConfigStore
	flowsTmpl, skillsTmpl, capabilitiesTmpl, finalizeTmpl  *template.Template
}
```

In `NewConfiguratorHandler`, find the existing `parse` closure that loads tabs. Add one more parse call:

```go
	finalizeTmpl, err := parse("finalize")
	if err != nil {
		return nil, err
	}
```

And include the new field in the struct literal returned at the bottom of `NewConfiguratorHandler`:

```go
	return &ConfiguratorHandler{
		repo:             repo,
		flowsTmpl:        flowsTmpl,
		skillsTmpl:       skillsTmpl,
		capabilitiesTmpl: capabilitiesTmpl,
		finalizeTmpl:     finalizeTmpl,
	}, nil
```

### Step 5: Create the finalize.html template

Create `open-bbcd/web/templates/configurator/finalize.html`:

```html
{{define "tab_content"}}
<div class="config-finalize">
  <h2>Finalize this agent?</h2>
  <p>
    The agent is currently in <span class="badge badge-initializing">INITIALIZING</span> state.
    Finalizing flips it to <span class="badge badge-draft">DRAFT</span> and locks the configurator —
    re-running the wizard against this agent will create a new version.
  </p>

  {{if .ParseError}}
  <div class="config-banner config-banner-error">
    <strong>Discovery archive could not be parsed:</strong> {{.ParseError}}
    <p class="hint">Finalize is allowed, but downstream alpha generation will see an empty config.</p>
  </div>
  {{end}}

  <ul class="config-finalize-summary">
    <li><strong>{{len .Config.Flows}}</strong> flows ({{len .Config.Flows}} declared, this snapshot)</li>
    <li><strong>{{len .Config.Skills}}</strong> skills</li>
    <li><strong>{{len .Config.Capabilities}}</strong> capabilities</li>
  </ul>

  <form method="POST" action="/agents/{{.AgentID}}/finalize" class="config-finalize-form">
    <button type="submit" class="btn-primary">Yes, finalize →</button>
    <a href="/agents/{{.AgentID}}/configure/flows" class="btn-secondary">Cancel</a>
  </form>
</div>
{{end}}
```

### Step 6: Add styles for the finalize page

Append to `open-bbcd/web/static/configurator.css`:

```css

.config-finalize { max-width: 640px; }
.config-finalize h2 { margin: 0 0 12px; }
.config-finalize p { margin: 0 0 16px; line-height: 1.5; color: #c9d1d9; }

.config-finalize-summary {
  list-style: none;
  padding: 12px 16px;
  margin: 0 0 16px;
  border: 1px solid #30363d;
  border-radius: 6px;
  background: #161b22;
}
.config-finalize-summary li { margin: 4px 0; color: #c9d1d9; }

.config-finalize-form { display: flex; gap: 8px; }
```

### Step 7: Run tests — expect PASS

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4/open-bbcd
go build ./...
go test -race -v ./internal/handler -run TestConfigurator_Finalize
```

Expected: all 3 new tests PASS. Full configurator suite still green:

```bash
go test -race -v ./internal/handler -run TestConfigurator
```

### Step 8: Commit

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4
git add open-bbcd/internal/handler/configurator.go \
        open-bbcd/internal/handler/configurator_test.go \
        open-bbcd/web/templates/configurator/finalize.html \
        open-bbcd/web/static/configurator.css
git commit -m "feat(open-bbcd): GET /configure/finalize confirmation + POST /finalize transition"
```

---

## Task 4: `DownloadYAML` handler with round-trip property test

**Files:**
- Modify: `open-bbcd/internal/handler/configurator.go` — add `DownloadYAML` method + template-free YAML rendering
- Modify: `open-bbcd/internal/handler/configurator_test.go` — add 2 tests (basic + round-trip property)

### Step 1: Write the failing tests

Append to `open-bbcd/internal/handler/configurator_test.go`:

```go
func TestConfigurator_DownloadYAML_HappyPath(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig(), currentStatus: "DRAFT"}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodGet, "/agents/abc/config.yaml", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.DownloadYAML(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/yaml") {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want to contain 'attachment'", cd)
	}

	body := w.Body.String()
	if !strings.Contains(body, "schema_version") {
		t.Errorf("yaml should contain schema_version: %s", body[:min(200, len(body))])
	}
	if !strings.Contains(body, "test-agent") {
		t.Errorf("yaml should contain the agent name: %s", body[:min(200, len(body))])
	}
}

func TestConfigurator_DownloadYAML_RoundTrip(t *testing.T) {
	// Write the JSONB → render YAML → parse YAML → compare to the original.
	cfg := sampleConfig()
	store := &stubConfigStore{cfg: cfg, currentStatus: "DRAFT"}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodGet, "/agents/abc/config.yaml", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.DownloadYAML(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var decoded types.FlowMapConfig
	if err := yaml.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}

	if decoded.Name != cfg.Name {
		t.Errorf("Name mismatch: %q vs %q", decoded.Name, cfg.Name)
	}
	if len(decoded.Flows) != len(cfg.Flows) {
		t.Fatalf("Flows len mismatch: %d vs %d", len(decoded.Flows), len(cfg.Flows))
	}
	if decoded.Flows[0].Workflow.Mermaid != cfg.Flows[0].Workflow.Mermaid {
		t.Errorf("Workflow.Mermaid not preserved")
	}
	if len(decoded.Skills) != len(cfg.Skills) {
		t.Errorf("Skills len mismatch: %d vs %d", len(decoded.Skills), len(cfg.Skills))
	}
	if len(decoded.Capabilities) != len(cfg.Capabilities) {
		t.Errorf("Capabilities len mismatch: %d vs %d", len(decoded.Capabilities), len(cfg.Capabilities))
	}
}
```

Add `"gopkg.in/yaml.v3"` to the test file's imports.

### Step 2: Run test — expect FAIL

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4/open-bbcd
go test -v ./internal/handler -run TestConfigurator_DownloadYAML 2>&1 || true
```

Expected: undefined `h.DownloadYAML`.

### Step 3: Implement the handler

Append to `open-bbcd/internal/handler/configurator.go`:

```go
// DownloadYAML renders the agent's flow_map_config as YAML and serves it
// as a file attachment named "<agent-name>.yaml".
//
// Available for any agent (no status gate at the handler level — the link
// in /agents/ui only appears for DRAFT+ agents). Returns 404 if the agent
// doesn't exist or has no config persisted yet.
func (h *ConfiguratorHandler) DownloadYAML(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	agent, err := h.repo.GetByID(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cfg, err := h.loadConfig(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	filename := sanitiseFilename(agent.Name) + ".yaml"
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(yamlBytes)
}

// sanitiseFilename produces a safe basename for the Content-Disposition header.
// Strips path separators and quotes; falls back to "agent" if the input is empty
// or has no allowed characters.
func sanitiseFilename(name string) string {
	if name == "" {
		return "agent"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		return "agent"
	}
	return out
}
```

Add `"gopkg.in/yaml.v3"` to the imports in `configurator.go`.

### Step 4: Run tests — expect PASS

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4/open-bbcd
go test -race -v ./internal/handler -run TestConfigurator_DownloadYAML
```

Expected: 2 PASS (basic + round-trip).

### Step 5: Run full suite

```bash
go test -race ./...
```

Expected: every package PASS.

### Step 6: Commit

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4
git add open-bbcd/internal/handler/configurator.go open-bbcd/internal/handler/configurator_test.go
git commit -m "feat(open-bbcd): GET /agents/{id}/config.yaml (YAML download + round-trip test)"
```

---

## Task 5: Activate the finalize link + add download link to agents-versions page

**Files:**
- Modify: `open-bbcd/web/templates/configurator/layout.html` — replace the disabled finalize span with an active link
- Modify: `open-bbcd/web/templates/agent-versions.html` — add "Download config" column

### Step 1: Make the finalize link active

In `open-bbcd/web/templates/configurator/layout.html`, find the line:

```html
<span class="btn-disabled" title="Available in PR4">Finalize →</span>
```

Replace it with:

```html
<a href="/agents/{{.AgentID}}/configure/finalize"
   class="config-tab {{if eq .Tab "finalize"}}active{{end}}">Finalize →</a>
```

(Using the same `.config-tab` class as the other tabs keeps the styling consistent; the active state lights up when the user is on the confirmation page.)

### Step 2: Add a "Download config" column to the versions page

In `open-bbcd/web/templates/agent-versions.html`, replace the entire `<table>` block with:

```html
<table class="data-table">
  <thead>
    <tr>
      <th class="col-version">Version</th>
      <th class="col-status">Status</th>
      <th class="col-date">Created</th>
      <th>Config</th>
    </tr>
  </thead>
  <tbody>
    {{range .Versions}}
    <tr>
      <td class="col-version {{if gt .VersionNum 1}}old{{end}}">v{{.VersionNum}}</td>
      <td class="col-status"><span class="badge badge-{{.Agent.Status | statusClass}}">{{.Agent.Status}}</span></td>
      <td class="col-date">{{.Agent.CreatedAt.Format "Jan 2, 2006"}}</td>
      <td>
        {{if ne .Agent.Status "INITIALIZING"}}
          <a href="/agents/{{.Agent.ID}}/config.yaml" class="agent-link">Download YAML</a>
        {{else}}
          <a href="/agents/{{.Agent.ID}}/configure" class="agent-link">Resume setup →</a>
        {{end}}
      </td>
    </tr>
    {{end}}
  </tbody>
</table>
```

(For INITIALIZING agents the link is "Resume setup →" pointing back to the configurator — a free affordance the data model has supported since PR1 but wasn't surfaced.)

### Step 3: Build

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4/open-bbcd
go build ./...
go test -race ./...
```

Expected: all green.

### Step 4: Commit

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4
git add open-bbcd/web/templates/configurator/layout.html open-bbcd/web/templates/agent-versions.html
git commit -m "feat(open-bbcd): activate finalize link; add Download YAML + Resume setup affordances"
```

---

## Task 6: Wire routes + end-to-end smoke test

**Files:**
- Modify: `open-bbcd/internal/handler/api.go` — register 3 new routes

### Step 1: Register routes

In `open-bbcd/internal/handler/api.go`, after the existing line:

```go
mux.HandleFunc("POST /agents/{id}/configure/flows/{flowId}/workflow", configuratorHandler.WorkflowUpdate)
```

Add:

```go
	mux.HandleFunc("GET /agents/{id}/configure/finalize", configuratorHandler.FinalizeConfirm)
	mux.HandleFunc("POST /agents/{id}/finalize", configuratorHandler.Finalize)
	mux.HandleFunc("GET /agents/{id}/config.yaml", configuratorHandler.DownloadYAML)
```

### Step 2: Build + test

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4/open-bbcd
go build ./...
go test -race ./...
go vet ./...
```

Expected: all green.

### Step 3: Real-DB smoke test

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4/open-bbcd
cp .env.example .env 2>/dev/null
set -a; source .env; set +a
docker-compose up -d
sleep 2
make migrate-up

export SERVER_PORT=8085
nohup go run ./cmd/open-bbcd > /tmp/openbbc-pr4.log 2>&1 &
sleep 5

# Re-use PR3's sample-flowmap zip (or rebuild from testdata if missing)
if [ ! -f /tmp/sample-flowmap.zip ]; then
  (cd internal/flowmap/testdata && python3 -c "
import zipfile, os
with zipfile.ZipFile('/tmp/sample-flowmap.zip', 'w') as z:
    for root, _, files in os.walk('sample-flowmap'):
        for f in files:
            p = os.path.join(root, f)
            z.write(p, os.path.relpath(p, 'sample-flowmap'))
")
fi

# Walk wizard.
RESPONSE=$(curl -i -s -X POST http://localhost:8085/agents/wizard \
  -F "name=pr4-smoke" -F "scope=smoke" -F "should_do=test" \
  -F "should_not_do=fail" -F "business_domain=internal" \
  -F "discovery_file=@/tmp/sample-flowmap.zip")
AID=$(echo "$RESPONSE" | grep -i '^Location:' | sed 's|.*/agents/\([^/]*\)/configure.*|\1|' | tr -d '\r')
echo "AID=$AID"

echo "=== GET finalize confirmation page ==="
curl -s -o /dev/null -w "status=%{http_code}\n" "http://localhost:8085/agents/$AID/configure/finalize"

echo "=== POST finalize ==="
curl -i -s -X POST "http://localhost:8085/agents/$AID/finalize" | head -3
psql "$DATABASE_URL" -tAc "SELECT status FROM agents WHERE id='$AID'"

echo "=== POST finalize again (wrong status → 409) ==="
curl -s -o /dev/null -w "status=%{http_code}\n" -X POST "http://localhost:8085/agents/$AID/finalize"

echo "=== GET config.yaml ==="
curl -i -s "http://localhost:8085/agents/$AID/config.yaml" | head -20

pkill -f open-bbcd
docker-compose down
```

Expected:
- `GET /configure/finalize` → 200
- `POST /finalize` → 303 with `Location: /agents/ui`; DB row's status is now `DRAFT`
- Second `POST /finalize` → 409
- `GET /config.yaml` → 200, `Content-Type: application/yaml`, body starts with `schema_version: 1` and contains the agent's name + flows

### Step 4: Browser walk (optional but recommended)

Open `http://localhost:8085/agents/<some-INITIALIZING-agent-id>/configure` in a browser. Verify:
- The "Finalize →" link is now active (not greyed out).
- Clicking it loads the confirmation page with a summary of flows/skills/capabilities.
- "Yes, finalize →" submits, redirects to `/agents/ui`, and the agent's row shows status `DRAFT`.
- On the agent's versions page (`/agents/ui?agent=<name>`), the row now shows "Download YAML" — clicking it downloads `<agent-name>.yaml`.
- For agents still in `INITIALIZING`, the same column shows "Resume setup →" linking back to the configurator.

### Step 5: Commit any browser-walk fixes

If browser walk surfaced bugs, fix and commit. Otherwise:

```bash
cd /home/john/dev/OpenBBC/.worktrees/flow-map-configurator-pr4
git add open-bbcd/internal/handler/api.go
git commit -m "feat(open-bbcd): register finalize + config.yaml routes"
```

---

## Done criteria for PR4

- ✅ `POST /agents/{id}/finalize` flips status `INITIALIZING → DRAFT`, redirects to `/agents/ui`. 409 when status isn't INITIALIZING.
- ✅ `GET /agents/{id}/configure/finalize` renders a confirmation page; "Finalize →" link in the configurator chrome is no longer disabled.
- ✅ `GET /agents/{id}/config.yaml` renders the full agent config as YAML (round-trip property test on the canonical fixture).
- ✅ `/agents/ui?agent=<name>` shows "Download YAML" for non-INITIALIZING agents and "Resume setup →" for INITIALIZING.
- ✅ All `go test -race ./...` green; `go vet ./...` clean.
- ✅ Smoke test passes against a real DB.

After PR4 merges, the configurator feature is complete: ingest (PR1) → edit (PR2) → workflow editor (PR3) → finalize + YAML (PR4). The downstream alpha-generation phase (referenced in `docs/ARCHITECTURE.md`) becomes unblocked.
