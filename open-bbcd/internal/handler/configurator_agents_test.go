package handler_test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/handler"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// --- fixtures ---

// seedAgentsVersion creates an agent named agentName with one root version in
// status and returns (agentID, versionID).
func seedAgentsVersion(t *testing.T, db *sql.DB, agentName string, status types.AgentStatus) (string, string) {
	t.Helper()
	var agentID, versionID string
	if err := db.QueryRow(`
		WITH a AS (INSERT INTO agents (name) VALUES ($1) RETURNING id)
		INSERT INTO agent_versions (agent_id, status, flow_map_config)
		SELECT id, $2, '{}'::jsonb FROM a
		RETURNING agent_id::text, id::text`, agentName, string(status)).Scan(&agentID, &versionID); err != nil {
		t.Fatalf("seedAgentsVersion: %v", err)
	}
	return agentID, versionID
}

func agentsBind(t *testing.T, db *sql.DB, caller, name, target, note string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO agent_version_subagent (caller_version_id, target_version_id, name, note)
		VALUES ($1::uuid, $2::uuid, $3, $4)`, caller, target, name, note); err != nil {
		t.Fatalf("agentsBind: %v", err)
	}
}

// agentsSeedEval inserts an eval in status on versionID and returns its id.
func agentsSeedEval(t *testing.T, db *sql.DB, versionID, status string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		WITH d AS (INSERT INTO datasets (name) VALUES ('ds-' || gen_random_uuid()) RETURNING id),
		     dv AS (INSERT INTO dataset_versions (dataset_id, status, version_num) SELECT id, 'CLOSED', 1 FROM d RETURNING id)
		INSERT INTO evals (agent_version_id, dataset_version_id, status)
		SELECT $1::uuid, dv.id, $2 FROM dv RETURNING id::text`, versionID, status).Scan(&id); err != nil {
		t.Fatalf("agentsSeedEval: %v", err)
	}
	return id
}

type agentsState struct {
	enabled bool
	rows    string // "name=target:note;…" ordered by name
}

func agentsSnapshot(t *testing.T, db *sql.DB, versionID string) agentsState {
	t.Helper()
	var s agentsState
	if err := db.QueryRow(`SELECT agent_tool_enabled FROM agent_versions WHERE id = $1::uuid`, versionID).Scan(&s.enabled); err != nil {
		t.Fatalf("snapshot enabled: %v", err)
	}
	if err := db.QueryRow(`SELECT coalesce(string_agg(name || '=' || target_version_id || ':' || note, ';' ORDER BY name), '')
		FROM agent_version_subagent WHERE caller_version_id = $1::uuid`, versionID).Scan(&s.rows); err != nil {
		t.Fatalf("snapshot rows: %v", err)
	}
	return s
}

// --- request helpers ---

func agentsGET(t *testing.T, h *handler.ConfiguratorHandler, versionID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/"+versionID+"/configure/agents", nil)
	req.SetPathValue("version_id", versionID)
	rec := httptest.NewRecorder()
	h.AgentsTab(rec, req)
	return rec
}

type agentsWrite struct {
	name string
	path string
	fn   func(*handler.ConfiguratorHandler, http.ResponseWriter, *http.Request)
	form url.Values
	bind string // {name} path value for delete
}

func agentsPOST(t *testing.T, h *handler.ConfiguratorHandler, versionID string, wr agentsWrite) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+wr.path, strings.NewReader(wr.form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("version_id", versionID)
	if wr.bind != "" {
		req.SetPathValue("name", wr.bind)
	}
	rec := httptest.NewRecorder()
	wr.fn(h, rec, req)
	return rec
}

func toggleWrite(enabled string) agentsWrite {
	return agentsWrite{name: "toggle", path: "/architecture/agents/toggle",
		fn: (*handler.ConfiguratorHandler).ToggleAgentTool, form: url.Values{"enabled": {enabled}}}
}

func addWrite(name, target, note string) agentsWrite {
	return agentsWrite{name: "add", path: "/architecture/agents",
		fn:   (*handler.ConfiguratorHandler).AddSubAgentBinding,
		form: url.Values{"name": {name}, "target_version_id": {target}, "note": {note}}}
}

func notesWrite(notes map[string]string) agentsWrite {
	f := url.Values{}
	for k, v := range notes {
		f.Set("note["+k+"]", v)
	}
	return agentsWrite{name: "notes", path: "/architecture/agents/notes",
		fn: (*handler.ConfiguratorHandler).UpdateSubAgentNotes, form: f}
}

func deleteWrite(name string) agentsWrite {
	return agentsWrite{name: "delete", path: "/architecture/agents/" + name + "/delete",
		fn: (*handler.ConfiguratorHandler).DeleteSubAgentBinding, form: url.Values{}, bind: name}
}

var emptyAgentsError = regexp.MustCompile(`<div id="agents-error"[^>]*></div>`)

// assertAgentsError checks a write failed with status, as an HTML error
// fragment (never JSON) carrying the sentinel text.
func assertAgentsError(t *testing.T, rec *httptest.ResponseRecorder, status int, text string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Fatalf("error body is JSON: %s", body)
	}
	if !strings.Contains(body, `id="agents-error"`) || !strings.Contains(body, text) {
		t.Fatalf("error fragment missing slot or %q: %s", text, body)
	}
}

// --- GET ---

func TestAgentsTab_GetDraftEditable(t *testing.T) {
	db := openConfiguratorTestDB(t)
	_, caller := seedAgentsVersion(t, db, "boss", types.AgentStatusDraft)
	_, helper := seedAgentsVersion(t, db, "helper", types.AgentStatusReady)
	_, _ = seedAgentsVersion(t, db, "other", types.AgentStatusDeployed)
	_, _ = seedAgentsVersion(t, db, "wip", types.AgentStatusDraft)
	agentsBind(t, db, caller, "researcher", helper, "finds facts")

	rec := agentsGET(t, newConfigHandlerWithDB(t, db), caller)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`hx-ext="response-targets"`,
		`/static/htmx-response-targets.min.js`,
		`href="/agent_versions/` + caller + `/configure/agents"`,
		`type="checkbox" name="enabled"`,
		`id="agents-table"`,
		`researcher`,
		`finds facts`,
		`name="note[researcher]"`,
		`<optgroup label="helper">`,
		`helper · v1 · READY`,
		`<optgroup label="other">`,
		`other · v1 · DEPLOYED`,
		// "-" escaped: pattern compiles with the v flag, where an
		// unescaped trailing "-" in a class is a SyntaxError and the
		// browser silently drops the constraint.
		`pattern="[a-z][a-z0-9_\-]{0,39}"`,
		`/architecture/agents/researcher/delete`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	if strings.Contains(body, `label="wip"`) || strings.Contains(body, `label="boss"`) {
		t.Error("picker lists a non-runnable agent")
	}
	if !emptyAgentsError.MatchString(body) {
		t.Error("missing empty #agents-error slot")
	}
	posts := strings.Count(body, "hx-post=")
	targets := strings.Count(body, `hx-target-error="#agents-error"`)
	if posts < 4 || posts != targets {
		t.Errorf("hx-post=%d, hx-target-error=%d; want ≥4 and equal", posts, targets)
	}
}

func TestAgentsTab_GetReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status types.AgentStatus
		eval   bool
	}{
		{"READY", types.AgentStatusReady, false},
		{"DEPLOYED", types.AgentStatusDeployed, false},
		{"DRAFT+pending eval", types.AgentStatusDraft, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openConfiguratorTestDB(t)
			_, caller := seedAgentsVersion(t, db, "boss", tc.status)
			_, helper := seedAgentsVersion(t, db, "helper", types.AgentStatusReady)
			agentsBind(t, db, caller, "researcher", helper, "n")
			if tc.eval {
				agentsSeedEval(t, db, caller, "PENDING")
			}
			rec := agentsGET(t, newConfigHandlerWithDB(t, db), caller)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if strings.Contains(body, "hx-post=") {
				t.Error("read-only tab has write controls (hx-post)")
			}
			if strings.Contains(body, `id="agents-add-form"`) {
				t.Error("read-only tab has the add form")
			}
			if !strings.Contains(body, "disabled") {
				t.Error("checkbox not disabled")
			}
			if !strings.Contains(body, "researcher") || !strings.Contains(body, "helper · v1 · READY") {
				t.Error("bindings table missing in read-only view")
			}
			hasBanner := strings.Contains(body, "eval or training")
			if hasBanner != tc.eval {
				t.Errorf("eval banner shown = %v, want %v", hasBanner, tc.eval)
			}
		})
	}
}

// --- writes: success ---

func TestAgentsTab_ToggleOnOff(t *testing.T) {
	db := openConfiguratorTestDB(t)
	_, caller := seedAgentsVersion(t, db, "boss", types.AgentStatusDraft)
	h := newConfigHandlerWithDB(t, db)

	rec := agentsPOST(t, h, caller, toggleWrite("on"))
	if rec.Code != http.StatusOK {
		t.Fatalf("on: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="agents-toggle"`) || !strings.Contains(body, "checked") {
		t.Fatalf("on fragment: %s", body)
	}
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Error("success fragment does not clear #agents-error out of band")
	}
	if !agentsSnapshot(t, db, caller).enabled {
		t.Fatal("flag not set")
	}

	rec = agentsPOST(t, h, caller, toggleWrite("off"))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "checked") {
		t.Fatalf("off: %d %s", rec.Code, rec.Body.String())
	}
	if agentsSnapshot(t, db, caller).enabled {
		t.Fatal("flag not cleared")
	}

	rec = agentsPOST(t, h, caller, toggleWrite("maybe"))
	assertAgentsError(t, rec, http.StatusBadRequest, "enabled")
}

// The checkbox form posts a hidden enabled=off before the checkbox's
// enabled=on, so a checked box sends both values.
func TestAgentsTab_ToggleCheckedSendsBoth(t *testing.T) {
	db := openConfiguratorTestDB(t)
	_, caller := seedAgentsVersion(t, db, "boss", types.AgentStatusDraft)
	h := newConfigHandlerWithDB(t, db)
	wr := toggleWrite("off")
	wr.form.Add("enabled", "on")
	if rec := agentsPOST(t, h, caller, wr); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !agentsSnapshot(t, db, caller).enabled {
		t.Fatal("off+on should enable")
	}
}

func TestAgentsTab_AddNotesDelete(t *testing.T) {
	db := openConfiguratorTestDB(t)
	_, caller := seedAgentsVersion(t, db, "boss", types.AgentStatusDraft)
	_, helper := seedAgentsVersion(t, db, "helper", types.AgentStatusReady)
	_, other := seedAgentsVersion(t, db, "other", types.AgentStatusDeployed)
	h := newConfigHandlerWithDB(t, db)

	rec := agentsPOST(t, h, caller, addWrite("researcher", helper, "finds facts"))
	if rec.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`id="agents-table"`, "researcher", "helper · v1 · READY", `hx-swap-oob="true"`} {
		if !strings.Contains(body, want) {
			t.Errorf("add fragment missing %q: %s", want, body)
		}
	}
	if rec := agentsPOST(t, h, caller, addWrite("writer", other, "")); rec.Code != http.StatusOK {
		t.Fatalf("add 2: %d %s", rec.Code, rec.Body.String())
	}

	rec = agentsPOST(t, h, caller, notesWrite(map[string]string{"researcher": "new note", "ghost": "x"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("notes: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "Saved ✓") || !strings.Contains(body, `id="agents-tab"`) {
		t.Fatalf("notes fragment: %s", body)
	}
	if got := agentsSnapshot(t, db, caller).rows; got != "researcher="+helper+":new note;writer="+other+":" {
		t.Fatalf("rows after notes = %q", got)
	}

	rec = agentsPOST(t, h, caller, deleteWrite("researcher"))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "researcher") || !strings.Contains(body, "writer") {
		t.Fatalf("delete fragment: %s", body)
	}

	rec = agentsPOST(t, h, caller, deleteWrite("ghost"))
	assertAgentsError(t, rec, http.StatusNotFound, types.ErrNotFound.Error())
}

// --- writes: errors ---

func TestAgentsTab_LockedStatuses409(t *testing.T) {
	for _, st := range []types.AgentStatus{types.AgentStatusReady, types.AgentStatusDeployed} {
		t.Run(string(st), func(t *testing.T) {
			db := openConfiguratorTestDB(t)
			_, caller := seedAgentsVersion(t, db, "boss", st)
			_, helper := seedAgentsVersion(t, db, "helper", types.AgentStatusReady)
			_, other := seedAgentsVersion(t, db, "other", types.AgentStatusReady)
			agentsBind(t, db, caller, "existing", helper, "keep")
			before := agentsSnapshot(t, db, caller)
			h := newConfigHandlerWithDB(t, db)
			for _, wr := range []agentsWrite{
				toggleWrite("on"), addWrite("newone", other, ""),
				notesWrite(map[string]string{"existing": "changed"}), deleteWrite("existing"),
			} {
				rec := agentsPOST(t, h, caller, wr)
				assertAgentsError(t, rec, http.StatusConflict, "can only change while the version is INITIALIZING or DRAFT")
			}
			if after := agentsSnapshot(t, db, caller); after != before {
				t.Fatalf("DB changed: %+v -> %+v", before, after)
			}
		})
	}
}

func TestAgentsTab_ActiveEval409ThenEditable(t *testing.T) {
	db := openConfiguratorTestDB(t)
	_, caller := seedAgentsVersion(t, db, "boss", types.AgentStatusDraft)
	_, helper := seedAgentsVersion(t, db, "helper", types.AgentStatusReady)
	_, other := seedAgentsVersion(t, db, "other", types.AgentStatusReady)
	agentsBind(t, db, caller, "existing", helper, "keep")
	evalID := agentsSeedEval(t, db, caller, "IN_PROGRESS")
	before := agentsSnapshot(t, db, caller)
	h := newConfigHandlerWithDB(t, db)
	writes := []agentsWrite{
		toggleWrite("on"), addWrite("newone", other, ""),
		notesWrite(map[string]string{"existing": "changed"}), deleteWrite("existing"),
	}
	for _, wr := range writes {
		rec := agentsPOST(t, h, caller, wr)
		assertAgentsError(t, rec, http.StatusConflict, "eval or training session is pending or in progress")
	}
	if after := agentsSnapshot(t, db, caller); after != before {
		t.Fatalf("DB changed: %+v -> %+v", before, after)
	}

	if _, err := db.Exec(`UPDATE evals SET status = 'DONE' WHERE id = $1::uuid`, evalID); err != nil {
		t.Fatal(err)
	}
	for _, wr := range writes {
		if rec := agentsPOST(t, h, caller, wr); rec.Code != http.StatusOK {
			t.Fatalf("%s after DONE: %d %s", wr.name, rec.Code, rec.Body.String())
		}
	}
}

func TestAgentsTab_AddErrors(t *testing.T) {
	db := openConfiguratorTestDB(t)
	bossAgent, caller := seedAgentsVersion(t, db, "boss", types.AgentStatusDraft)
	_, helper := seedAgentsVersion(t, db, "helper", types.AgentStatusReady)
	_, other := seedAgentsVersion(t, db, "other", types.AgentStatusReady)
	agentsBind(t, db, caller, "existing", helper, "")
	h := newConfigHandlerWithDB(t, db)

	for _, st := range []types.AgentStatus{
		types.AgentStatusInitializing, types.AgentStatusPending, types.AgentStatusDraft, types.AgentStatusTraining,
	} {
		_, bad := seedAgentsVersion(t, db, "bad-"+string(st), st)
		before := agentsSnapshot(t, db, caller)
		rec := agentsPOST(t, h, caller, addWrite("newone", bad, ""))
		assertAgentsError(t, rec, http.StatusBadRequest, "target version must be READY or DEPLOYED")
		if after := agentsSnapshot(t, db, caller); after != before {
			t.Fatalf("%s: DB changed", st)
		}
	}

	before := agentsSnapshot(t, db, caller)
	// Duplicate name, duplicate target.
	assertAgentsError(t, agentsPOST(t, h, caller, addWrite("existing", other, "")), http.StatusConflict, "already bound")
	assertAgentsError(t, agentsPOST(t, h, caller, addWrite("again", helper, "")), http.StatusConflict, "already bound")
	// Invalid name, missing/invalid target.
	assertAgentsError(t, agentsPOST(t, h, caller, addWrite("Bad Name", other, "")), http.StatusBadRequest, "sub-agent name")
	assertAgentsError(t, agentsPOST(t, h, caller, addWrite("ok", "", "")), http.StatusBadRequest, "target")
	assertAgentsError(t, agentsPOST(t, h, caller, addWrite("ok", "not-a-uuid", "")), http.StatusBadRequest, "target")
	if after := agentsSnapshot(t, db, caller); after != before {
		t.Fatalf("DB changed: %+v -> %+v", before, after)
	}

	// Name collision: the caller's agent has an endpoint tool named "agent".
	if _, err := db.Exec(`UPDATE agents SET architecture = '{"tools":[{"id":"t1","name":"agent"}]}'::jsonb WHERE id = $1::uuid`, bossAgent); err != nil {
		t.Fatal(err)
	}
	assertAgentsError(t, agentsPOST(t, h, caller, addWrite("newone", other, "")), http.StatusConflict, "already has an endpoint tool named")
	assertAgentsError(t, agentsPOST(t, h, caller, toggleWrite("on")), http.StatusConflict, "already has an endpoint tool named")
	if after := agentsSnapshot(t, db, caller); after != before {
		t.Fatalf("DB changed after collision: %+v -> %+v", before, after)
	}
}

func TestAgentsTab_UnknownVersion(t *testing.T) {
	db := openConfiguratorTestDB(t)
	h := newConfigHandlerWithDB(t, db)
	if rec := agentsGET(t, h, "00000000-0000-0000-0000-000000000000"); rec.Code != http.StatusNotFound {
		t.Fatalf("GET missing: %d", rec.Code)
	}
	if rec := agentsGET(t, h, "nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("GET bad id: %d", rec.Code)
	}
	assertAgentsError(t, agentsPOST(t, h, "nope", toggleWrite("on")), http.StatusNotFound, types.ErrNotFound.Error())
	assertAgentsError(t, agentsPOST(t, h, "00000000-0000-0000-0000-000000000000", toggleWrite("on")), http.StatusNotFound, types.ErrNotFound.Error())
}

// A refused toggle answers with the error fragment plus an out-of-band
// #agents-toggle carrying the persisted state, so the flipped box snaps back.
func TestAgentsTab_RefusedToggleSnapsBack(t *testing.T) {
	db := openConfiguratorTestDB(t)
	bossAgent, caller := seedAgentsVersion(t, db, "boss", types.AgentStatusDraft)
	if _, err := db.Exec(`UPDATE agents SET architecture = '{"tools":[{"id":"t1","name":"agent"}]}'::jsonb WHERE id = $1::uuid`, bossAgent); err != nil {
		t.Fatal(err)
	}
	h := newConfigHandlerWithDB(t, db)

	rec := agentsPOST(t, h, caller, toggleWrite("on"))
	assertAgentsError(t, rec, http.StatusConflict, "already has an endpoint tool named")
	body := rec.Body.String()
	oob := regexp.MustCompile(`(?s)<form id="agents-toggle"[^>]*hx-swap-oob="true".*?<input type="checkbox" name="enabled" value="on" ?>`)
	if !oob.MatchString(body) {
		t.Fatalf("refused toggle lacks OOB unchecked checkbox: %s", body)
	}
	if agentsSnapshot(t, db, caller).enabled {
		t.Fatal("agent tool enabled despite refusal")
	}

	// Locked version: the OOB fragment is the disabled, read-only box.
	_, locked := seedAgentsVersion(t, db, "locked", types.AgentStatusReady)
	rec = agentsPOST(t, h, locked, toggleWrite("on"))
	assertAgentsError(t, rec, http.StatusConflict, "can only change while the version is INITIALIZING or DRAFT")
	if !regexp.MustCompile(`<div id="agents-toggle"[^>]*hx-swap-oob="true"`).MatchString(rec.Body.String()) ||
		!strings.Contains(rec.Body.String(), "disabled") {
		t.Fatalf("locked refusal lacks OOB disabled toggle: %s", rec.Body.String())
	}

	// Form-shape errors (not refusals by the store) stay plain.
	rec = agentsPOST(t, h, caller, toggleWrite("maybe"))
	assertAgentsError(t, rec, http.StatusBadRequest, "enabled must be on or off")
	if strings.Contains(rec.Body.String(), `id="agents-toggle"`) {
		t.Fatalf("form error carries toggle: %s", rec.Body.String())
	}

	// Success responses never carry the OOB attribute on the toggle.
	if _, err := db.Exec(`UPDATE agents SET architecture = '{}'::jsonb WHERE id = $1::uuid`, bossAgent); err != nil {
		t.Fatal(err)
	}
	rec = agentsPOST(t, h, caller, toggleWrite("on"))
	if rec.Code != http.StatusOK || regexp.MustCompile(`id="agents-toggle"[^>]*hx-swap-oob`).MatchString(rec.Body.String()) {
		t.Fatalf("success toggle: %d %s", rec.Code, rec.Body.String())
	}
}
