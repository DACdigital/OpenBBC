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
