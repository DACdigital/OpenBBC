package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
)

// openHandlerTestDB opens a Postgres connection and truncates backend-related
// tables. Skips if DATABASE_URL is unset.
func openHandlerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`TRUNCATE
		deployed_messages, deployed_sessions, chat_messages, chat_sessions,
		resources, agent_versions, agents,
		tool_backends, agent_endpoint_backend, agent_version_mcp_backend
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return db
}

func newTestBackendsHandler(t *testing.T, db *sql.DB) *BackendsHandler {
	t.Helper()
	backendRepo := repository.NewToolBackendRepository(db)
	wiringRepo := repository.NewVersionWiringRepository(db)
	h, err := NewBackendsHandler(backendRepo, wiringRepo, web.Assets)
	if err != nil {
		t.Fatalf("NewBackendsHandler: %v", err)
	}
	return h
}

// --- Non-DB tests (no skip) ---

func TestBackendsHandler_New_RendersHTTPForm(t *testing.T) {
	// Use an in-memory stub DB approach: we just need non-nil repos.
	// Since New() never touches the DB, we can use a nil-DB repos and
	// rely on the template rendering path only.
	// To avoid nil panics on the repos, open a real connection if available;
	// otherwise build the handler directly from web.Assets with nil db repos.
	// The cleanest approach: construct with a nil *sql.DB — repos don't call
	// the DB in New(), so this is safe.
	var nilDB *sql.DB
	backendRepo := repository.NewToolBackendRepository(nilDB)
	wiringRepo := repository.NewVersionWiringRepository(nilDB)
	h, err := NewBackendsHandler(backendRepo, wiringRepo, web.Assets)
	if err != nil {
		t.Fatalf("NewBackendsHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/mcp/new?kind=http_endpoint", nil)
	w := httptest.NewRecorder()
	h.New(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "New HTTP backend") {
		t.Errorf("body missing 'New HTTP backend': %s", body)
	}
	if !strings.Contains(body, `name="base_url"`) {
		t.Errorf("body missing base_url input: %s", body)
	}
}

func TestBackendsHandler_New_RendersMCPForm(t *testing.T) {
	var nilDB *sql.DB
	h, err := NewBackendsHandler(
		repository.NewToolBackendRepository(nilDB),
		repository.NewVersionWiringRepository(nilDB),
		web.Assets,
	)
	if err != nil {
		t.Fatalf("NewBackendsHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/mcp/new?kind=mcp_client", nil)
	w := httptest.NewRecorder()
	h.New(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "New MCP backend") {
		t.Errorf("body missing 'New MCP backend': %s", body)
	}
	if !strings.Contains(body, `name="url"`) {
		t.Errorf("body missing url input: %s", body)
	}
}

func TestBackendsHandler_New_UnknownKind_400(t *testing.T) {
	var nilDB *sql.DB
	h, err := NewBackendsHandler(
		repository.NewToolBackendRepository(nilDB),
		repository.NewVersionWiringRepository(nilDB),
		web.Assets,
	)
	if err != nil {
		t.Fatalf("NewBackendsHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/mcp/new?kind=foobar", nil)
	w := httptest.NewRecorder()
	h.New(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestBackendsHandler_Create_MissingName_400(t *testing.T) {
	var nilDB *sql.DB
	h, err := NewBackendsHandler(
		repository.NewToolBackendRepository(nilDB),
		repository.NewVersionWiringRepository(nilDB),
		web.Assets,
	)
	if err != nil {
		t.Fatalf("NewBackendsHandler: %v", err)
	}

	form := url.Values{}
	form.Set("kind", "http_endpoint")
	form.Set("name", "") // missing name
	form.Set("base_url", "https://api.example.com")

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

// --- DB-gated tests ---

func TestBackendsHandler_List_Empty(t *testing.T) {
	db := openHandlerTestDB(t)
	h := newTestBackendsHandler(t, db)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "No backends configured yet") {
		t.Errorf("expected empty-state message in body: %s", w.Body.String())
	}
}

func TestBackendsHandler_Create_HTTPHappyPath(t *testing.T) {
	db := openHandlerTestDB(t)
	h := newTestBackendsHandler(t, db)

	form := url.Values{}
	form.Set("kind", "http_endpoint")
	form.Set("name", "payments")
	form.Set("base_url", "https://payments.example.com")
	form.Add("forwarded_headers", "Authorization")
	form.Add("forwarded_headers", "Cookie")

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/mcp" {
		t.Errorf("Location = %q, want /mcp", loc)
	}

	// Verify row exists in DB.
	backendRepo := repository.NewToolBackendRepository(db)
	all, err := backendRepo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].Name != "payments" {
		t.Fatalf("expected 1 backend named 'payments', got %d rows", len(all))
	}
	var cfg struct {
		BaseURL          string   `json:"base_url"`
		ForwardedHeaders []string `json:"forwarded_headers"`
	}
	if err := json.Unmarshal(all[0].Config, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.BaseURL != "https://payments.example.com" {
		t.Errorf("BaseURL = %q, want https://payments.example.com", cfg.BaseURL)
	}
}

func TestBackendsHandler_Edit_Renders(t *testing.T) {
	db := openHandlerTestDB(t)
	h := newTestBackendsHandler(t, db)

	// Seed a backend via the repo directly.
	backendRepo := repository.NewToolBackendRepository(db)
	be := &struct {
		ID string
	}{}
	_ = be
	cfgJSON, _ := json.Marshal(map[string]any{
		"base_url":          "https://api.example.com",
		"forwarded_headers": []string{"Authorization"},
	})
	seedBE := &struct {
		Name   string
		Kind   string
		Config json.RawMessage
	}{Name: "myapi", Kind: "http_endpoint", Config: cfgJSON}
	_ = seedBE

	// Use Create to seed
	form := url.Values{}
	form.Set("kind", "http_endpoint")
	form.Set("name", "myapi")
	form.Set("base_url", "https://api.example.com")
	form.Add("forwarded_headers", "Authorization")
	createReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(form.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createW := httptest.NewRecorder()
	h.Create(createW, createReq)
	if createW.Code != http.StatusSeeOther {
		t.Fatalf("seed Create status = %d", createW.Code)
	}

	all, err := backendRepo.List(context.Background())
	if err != nil || len(all) == 0 {
		t.Fatalf("expected seeded backend, got err=%v, len=%d", err, len(all))
	}
	id := all[0].ID

	req := httptest.NewRequest(http.MethodGet, "/mcp/"+id, nil)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.Edit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "myapi") {
		t.Errorf("body missing backend name 'myapi': %s", body)
	}
	if !strings.Contains(body, "https://api.example.com") {
		t.Errorf("body missing base_url: %s", body)
	}
}

func TestBackendsHandler_Update_HappyPath(t *testing.T) {
	db := openHandlerTestDB(t)
	h := newTestBackendsHandler(t, db)
	backendRepo := repository.NewToolBackendRepository(db)

	// Seed via Create.
	form := url.Values{}
	form.Set("kind", "http_endpoint")
	form.Set("name", "original")
	form.Set("base_url", "https://original.example.com")
	createReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(form.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createW := httptest.NewRecorder()
	h.Create(createW, createReq)
	if createW.Code != http.StatusSeeOther {
		t.Fatalf("seed Create status = %d", createW.Code)
	}

	all, _ := backendRepo.List(context.Background())
	id := all[0].ID

	// Now update.
	updateForm := url.Values{}
	updateForm.Set("name", "updated")
	updateForm.Set("base_url", "https://updated.example.com")
	updateReq := httptest.NewRequest(http.MethodPost, "/mcp/"+id, strings.NewReader(updateForm.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateReq.SetPathValue("id", id)
	updateW := httptest.NewRecorder()
	h.Update(updateW, updateReq)

	if updateW.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want 303; body = %s", updateW.Code, updateW.Body.String())
	}

	updated, err := backendRepo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if updated.Name != "updated" {
		t.Errorf("Name = %q, want 'updated'", updated.Name)
	}
}

func TestBackendsHandler_Delete_HappyPath(t *testing.T) {
	db := openHandlerTestDB(t)
	h := newTestBackendsHandler(t, db)
	backendRepo := repository.NewToolBackendRepository(db)

	// Seed.
	form := url.Values{}
	form.Set("kind", "http_endpoint")
	form.Set("name", "tobedeleted")
	form.Set("base_url", "https://delete.example.com")
	createReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(form.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createW := httptest.NewRecorder()
	h.Create(createW, createReq)
	if createW.Code != http.StatusSeeOther {
		t.Fatalf("seed Create status = %d", createW.Code)
	}
	all, _ := backendRepo.List(context.Background())
	id := all[0].ID

	delReq := httptest.NewRequest(http.MethodPost, "/mcp/"+id+"/delete", nil)
	delReq.SetPathValue("id", id)
	delW := httptest.NewRecorder()
	h.Delete(delW, delReq)

	if delW.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303; body = %s", delW.Code, delW.Body.String())
	}

	remaining, _ := backendRepo.List(context.Background())
	if len(remaining) != 0 {
		t.Errorf("expected 0 backends after delete, got %d", len(remaining))
	}
}

// newBackendsHandlerForTest builds a BackendsHandler with nil-safe repos.
// TestConnection does not touch the DB, so nil repos are fine for those tests.
func newBackendsHandlerForTest(t *testing.T) *BackendsHandler {
	t.Helper()
	var nilDB *sql.DB
	h, err := NewBackendsHandler(
		repository.NewToolBackendRepository(nilDB),
		repository.NewVersionWiringRepository(nilDB),
		web.Assets,
	)
	if err != nil {
		t.Fatalf("NewBackendsHandler: %v", err)
	}
	return h
}

// stubMCPJSONRPCRequest is the inbound shape parsed from MCP wire traffic.
type stubMCPJSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
}

func stubMCPRespond(w http.ResponseWriter, id any, result any) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// newStubMCPServer returns an httptest.Server that responds to the MCP
// initialize / tools/list handshake with a single canned tool.
func newStubMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req stubMCPJSONRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			stubMCPRespond(w, req.ID, map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "stub", "version": "0.1"},
			})
		case "notifications/initialized":
			// no response for notifications
		case "tools/list":
			stubMCPRespond(w, req.ID, map[string]any{
				"tools": []map[string]any{
					{
						"name":        "do_thing",
						"description": "Does a thing",
						"inputSchema": map[string]any{"type": "object"},
					},
				},
			})
		default:
			http.Error(w, "unknown method "+req.Method, 400)
		}
	}))
}

func TestBackends_TestConnection_Success(t *testing.T) {
	srv := newStubMCPServer(t)
	defer srv.Close()

	h := newBackendsHandlerForTest(t)
	form := url.Values{}
	form.Set("url", srv.URL)
	form.Set("transport", "streamable_http")

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.TestConnection(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Connection OK") {
		t.Fatalf("expected success banner: %s", w.Body.String())
	}
}

func TestBackends_TestConnection_UnreachableURL(t *testing.T) {
	h := newBackendsHandlerForTest(t)
	form := url.Values{}
	form.Set("url", "http://127.0.0.1:1")
	form.Set("transport", "streamable_http")

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.TestConnection(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with error fragment, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Test failed") {
		t.Fatalf("expected error banner: %s", w.Body.String())
	}
}

func TestBackends_TestConnection_MissingURL(t *testing.T) {
	h := newBackendsHandlerForTest(t)
	form := url.Values{}
	form.Set("transport", "streamable_http")
	// no url

	req := httptest.NewRequest(http.MethodPost, "/mcp/test", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.TestConnection(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Test failed") {
		t.Fatalf("expected error banner: %s", w.Body.String())
	}
}

func TestBackendsHandler_Delete_InUse_409(t *testing.T) {
	db := openHandlerTestDB(t)
	h := newTestBackendsHandler(t, db)
	backendRepo := repository.NewToolBackendRepository(db)

	// Seed a backend.
	form := url.Values{}
	form.Set("kind", "http_endpoint")
	form.Set("name", "wired")
	form.Set("base_url", "https://wired.example.com")
	createReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(form.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createW := httptest.NewRecorder()
	h.Create(createW, createReq)
	if createW.Code != http.StatusSeeOther {
		t.Fatalf("seed Create status = %d", createW.Code)
	}
	all, _ := backendRepo.List(context.Background())
	backendID := all[0].ID

	// Seed an agent + version and wire the backend to the agent.
	var agentID string
	err := db.QueryRow(`
		WITH a AS (
			INSERT INTO agents (name) VALUES ('test-wired-' || gen_random_uuid())
			RETURNING id
		), v AS (
			INSERT INTO agent_versions (agent_id, status, flow_map_config)
			SELECT id, 'INITIALIZING', '{}'::jsonb FROM a
			RETURNING agent_id
		)
		SELECT agent_id::text FROM v
	`).Scan(&agentID)
	if err != nil {
		t.Fatalf("seed agent_version: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO agent_endpoint_backend (agent_id, endpoint_id, backend_id)
		VALUES ($1::uuid, 'ep-1', $2)
	`, agentID, backendID); err != nil {
		t.Fatalf("wire backend: %v", err)
	}

	// Attempt to delete — should get 409.
	delReq := httptest.NewRequest(http.MethodPost, "/mcp/"+backendID+"/delete", nil)
	delReq.SetPathValue("id", backendID)
	delW := httptest.NewRecorder()
	h.Delete(delW, delReq)

	if delW.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", delW.Code, delW.Body.String())
	}
}
