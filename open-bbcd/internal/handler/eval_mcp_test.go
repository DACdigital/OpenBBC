package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// attachBackend inserts a tool_backends row and attaches it to versionID via
// agent_version_mcp_backend. Returns the backend id.
func attachBackend(t *testing.T, db *sql.DB, versionID, name, kind, config string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`INSERT INTO tool_backends (name, kind, config) VALUES ($1, $2, $3::jsonb) RETURNING id::text`,
		name, kind, config).Scan(&id); err != nil {
		t.Fatalf("insert backend: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_version_mcp_backend (agent_version_id, backend_id) VALUES ($1::uuid, $2::uuid)`,
		versionID, id); err != nil {
		t.Fatalf("attach backend: %v", err)
	}
	return id
}

func createEvalJSON(t *testing.T, h *EvalHandler, versionID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/evals", bytes.NewReader(raw))
	r.SetPathValue("version_id", versionID)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, r)
	return w
}

func TestEval_Create_MCPHeaderOverrides_Persisted(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	db := getEvalTestDB(t, h)
	attachBackend(t, db, versionID, "kb", "mcp_client", `{"url":"http://x/mcp","transport":"streamable_http"}`)

	w := createEvalJSON(t, h, versionID, map[string]any{
		"dataset_version_id":   dvID,
		"mcp_header_overrides": map[string]any{"kb": map[string]any{"X-Identity": "tester"}},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var e types.Eval
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	var raw string
	if err := db.QueryRow(`SELECT mcp_header_overrides::text FROM evals WHERE id = $1::uuid`, e.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]string
	_ = json.Unmarshal([]byte(raw), &got)
	if got["kb"]["X-Identity"] != "tester" {
		t.Fatalf("stored = %s", raw)
	}
}

func TestEval_Create_MCPHeaderOverrides_DefaultsEmpty(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	db := getEvalTestDB(t, h)
	w := createEvalJSON(t, h, versionID, map[string]any{"dataset_version_id": dvID})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var e types.Eval
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	var raw string
	_ = db.QueryRow(`SELECT mcp_header_overrides::text FROM evals WHERE id = $1::uuid`, e.ID).Scan(&raw)
	if raw != "{}" {
		t.Fatalf("stored = %s, want {}", raw)
	}
}

func TestEval_Create_MCPHeaderOverrides_Rejects(t *testing.T) {
	cases := map[string]any{
		"unattached backend": map[string]any{"other": map[string]any{"X": "y"}},
		"non mcp_client":     map[string]any{"http": map[string]any{"X": "y"}},
		"non-string value":   map[string]any{"kb": map[string]any{"X": 1}},
		"non-object block":   map[string]any{"kb": "X: y"},
		"null block":         map[string]any{"kb": nil},
		"not an object":      []string{"kb"},
	}
	for name, ovr := range cases {
		t.Run(name, func(t *testing.T) {
			h, versionID, dvID := setupEvalAPI(t)
			db := getEvalTestDB(t, h)
			attachBackend(t, db, versionID, "kb", "mcp_client", `{"url":"http://x/mcp"}`)
			attachBackend(t, db, versionID, "http", "http_endpoint", `{"base_url":"http://x"}`)
			w := createEvalJSON(t, h, versionID, map[string]any{"dataset_version_id": dvID, "mcp_header_overrides": ovr})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", w.Code, w.Body.String())
			}
			var n int
			_ = db.QueryRow(`SELECT count(*) FROM evals`).Scan(&n)
			if n != 0 {
				t.Fatalf("eval rows = %d, want 0", n)
			}
		})
	}
}

func TestEval_Create_MCPHeaderOverrides_Form(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	db := getEvalTestDB(t, h)
	attachBackend(t, db, versionID, "kb", "mcp_client", `{"url":"http://x/mcp"}`)
	form := url.Values{
		"dataset_version_id":        {dvID},
		"mcp_header_overrides_json": {`{"kb":{"X-Identity":"form"}}`},
	}
	r := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/evals", strings.NewReader(form.Encode()))
	r.SetPathValue("version_id", versionID)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.Create(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var raw string
	_ = db.QueryRow(`SELECT mcp_header_overrides::text FROM evals`).Scan(&raw)
	if !strings.Contains(raw, `"form"`) {
		t.Fatalf("stored = %s", raw)
	}

	form.Set("mcp_header_overrides_json", `{"nope":{"X":"y"}}`)
	r = httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/evals", strings.NewReader(form.Encode()))
	r.SetPathValue("version_id", versionID)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.Create(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unattached via form: status = %d, want 400", w.Code)
	}
}

// fakeMCPServer answers initialize / tools/list / tools/call and records the
// headers of every tools/list request.
func fakeMCPServer(t *testing.T, listHeaders *[]http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "kb", "version": "0.1"},
			}
		case "tools/list":
			*listHeaders = append(*listHeaders, r.Header.Clone())
			result = map[string]any{"tools": []map[string]any{
				{"name": "search", "description": "Search the KB",
					"inputSchema":  map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
					"outputSchema": map[string]any{"type": "object", "properties": map[string]any{"hits": map[string]any{"type": "integer"}}}},
				{"name": "no_schema"},
			}}
		default:
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func exportEval(t *testing.T, h *EvalHandler, evalID string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/evals/"+evalID+"/export.yaml", nil)
	r.SetPathValue("eval_id", evalID)
	w := httptest.NewRecorder()
	h.Export(w, r)
	return w
}

func TestEval_Export_MCPBackends_MergedHeaders(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	db := getEvalTestDB(t, h)
	var seen []http.Header
	srv := fakeMCPServer(t, &seen)
	attachBackend(t, db, versionID, "kb", "mcp_client",
		`{"url":"`+srv.URL+`","transport":"streamable_http","default_headers":{"X-Identity":"default","X-Default":"d"}}`)

	w := createEvalJSON(t, h, versionID, map[string]any{
		"dataset_version_id":   dvID,
		"mock_mcp_tools":       false,
		"mcp_header_overrides": map[string]any{"kb": map[string]any{"X-Identity": "override"}},
	})
	var e types.Eval
	_ = json.Unmarshal(w.Body.Bytes(), &e)

	we := exportEval(t, h, e.ID)
	if we.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", we.Code, we.Body.String())
	}
	body := we.Body.String()
	for _, want := range []string{
		"mcp_backends:", "kb:", "url: " + srv.URL, "transport: streamable_http",
		"X-Identity: override", "X-Default: d",
		"name: kb__search", "description: Search the KB", "output_schema:",
		"name: kb__no_schema", "additionalProperties: true",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("export missing %q:\n%s", want, body)
		}
	}
	if len(seen) == 0 || seen[0].Get("X-Identity") != "override" || seen[0].Get("X-Default") != "d" {
		t.Errorf("tools/list headers = %v", seen)
	}
}

func TestEval_Export_MCPDiscoveryFailure_502AndFailsPending(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	db := getEvalTestDB(t, h)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	attachBackend(t, db, versionID, "kb", "mcp_client", `{"url":"`+deadURL+`","transport":"streamable_http"}`)

	w := createEvalJSON(t, h, versionID, map[string]any{"dataset_version_id": dvID})
	var e types.Eval
	_ = json.Unmarshal(w.Body.Bytes(), &e)

	we := exportEval(t, h, e.ID)
	if we.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", we.Code, we.Body.String())
	}
	if ct := we.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var got map[string]string
	if err := json.Unmarshal(we.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %s", we.Body.String())
	}
	if got["error"] != "mcp_discovery_failed" || got["backend"] != "kb" || got["details"] == "" {
		t.Errorf("body = %v", got)
	}
	var status, msg string
	_ = db.QueryRow(`SELECT status, error_message FROM evals WHERE id = $1::uuid`, e.ID).Scan(&status, &msg)
	if status != "FAILED" || msg != "mcp_discovery_failed: kb: "+got["details"] {
		t.Errorf("eval = %s %q", status, msg)
	}
}

// A DONE source eval (training) is not touched, and neither is its PENDING
// training session: train_from_session.sh fails the session itself.
func TestEval_Export_MCPDiscoveryFailure_DoneEvalUntouched(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	db := getEvalTestDB(t, h)
	attachBackend(t, db, versionID, "kb", "mcp_client", `{"url":"http://127.0.0.1:1/mcp","transport":"streamable_http"}`)
	evalID := insertDoneEval(t, db, versionID, dvID, 0.5)
	tsID := insertTrainingSession(t, db, evalID, versionID)

	we := exportEval(t, h, evalID)
	if we.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", we.Code)
	}
	var evalStatus, tsStatus string
	_ = db.QueryRow(`SELECT status FROM evals WHERE id = $1::uuid`, evalID).Scan(&evalStatus)
	_ = db.QueryRow(`SELECT status FROM training_sessions WHERE id = $1::uuid`, tsID).Scan(&tsStatus)
	if evalStatus != "DONE" || tsStatus != "PENDING" {
		t.Errorf("eval=%s training=%s, want DONE/PENDING", evalStatus, tsStatus)
	}
}

// Backends attached with kind http_endpoint are not MCP servers: no
// mcp_backends key, no discovery.
func TestEval_Export_NoMCPClientBackend_NoKey(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	db := getEvalTestDB(t, h)
	attachBackend(t, db, versionID, "http", "http_endpoint", `{"base_url":"http://127.0.0.1:1"}`)
	w := createEvalJSON(t, h, versionID, map[string]any{"dataset_version_id": dvID})
	var e types.Eval
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	we := exportEval(t, h, e.ID)
	if we.Code != http.StatusOK || strings.Contains(we.Body.String(), "mcp_backends") {
		t.Fatalf("status = %d body:\n%s", we.Code, we.Body.String())
	}
}
