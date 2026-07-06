package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// setupEvalAPI seeds a minimal (agent, version, dataset, closed_dv) and
// returns a bound handler, the ids, and the *sql.DB for direct seeding.
func setupEvalAPI(t *testing.T) (*EvalHandler, string, string) {
	t.Helper()
	db := openTestDBForHandlers(t)
	var agentID, versionID, datasetID, dvID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('h-a') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	_ = db.QueryRow(`INSERT INTO datasets (name) VALUES ('h-ds') RETURNING id::text`).Scan(&datasetID)
	_ = db.QueryRow(`INSERT INTO dataset_versions (dataset_id, status, version_num, closed_at) VALUES ($1::uuid, 'CLOSED', 1, now()) RETURNING id::text`, datasetID).Scan(&dvID)

	evalRepo := repository.NewEvalRepository(db)
	datasetRepo := repository.NewDatasetRepository(db)
	chatRepo := repository.NewChatRepository(db)
	feedbackRepo := repository.NewFeedbackRepository(db)
	tsRepo := repository.NewTrainingSessionRepository(db)
	adapter := &evalStoreAdapter{db: db, evalRepo: evalRepo, dataset: datasetRepo, chat: chatRepo, feedback: feedbackRepo, trainingSessions: tsRepo}
	h, err := NewEvalHandler(evalRepo, datasetRepo, adapter, adapter, testWebFS())
	if err != nil {
		t.Fatalf("NewEvalHandler: %v", err)
	}
	return h, versionID, dvID
}

// getEvalTestDB returns the *sql.DB underlying an EvalHandler's adapter,
// allowing tests to seed rows directly.
func getEvalTestDB(t *testing.T, h *EvalHandler) *sql.DB {
	t.Helper()
	db := unwrapDB(h)
	if db == nil {
		t.Fatal("getEvalTestDB: could not unwrap DB from EvalHandler")
	}
	return db
}

// insertDoneEval creates an eval with status=DONE and given score directly
// via SQL, bypassing the handler's create path.
func insertDoneEval(t *testing.T, db *sql.DB, versionID, datasetVersionID string, score float64) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO evals (agent_version_id, dataset_version_id, status, score, completed_at)
		VALUES ($1::uuid, $2::uuid, 'DONE', $3, now())
		RETURNING id::text
	`, versionID, datasetVersionID, score).Scan(&id); err != nil {
		t.Fatalf("insertDoneEval: %v", err)
	}
	return id
}

// insertTrainingSession creates a PENDING training session for the given eval.
func insertTrainingSession(t *testing.T, db *sql.DB, evalID, parentVersionID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO training_sessions (source_eval_id, parent_version_id)
		VALUES ($1::uuid, $2::uuid)
		RETURNING id::text
	`, evalID, parentVersionID).Scan(&id); err != nil {
		t.Fatalf("insertTrainingSession: %v", err)
	}
	return id
}

func TestEval_Create_RefusesNonClosedDatasetVersion(t *testing.T) {
	h, versionID, _ := setupEvalAPI(t)
	db := unwrapDB(h)
	var dsID, dvID string
	_ = db.QueryRow(`INSERT INTO datasets (name) VALUES ('nc-ds') RETURNING id::text`).Scan(&dsID)
	_ = db.QueryRow(`INSERT INTO dataset_versions (dataset_id, status, version_num) VALUES ($1::uuid, 'DRAFT', 1) RETURNING id::text`, dsID).Scan(&dvID)

	body := map[string]string{"dataset_version_id": dvID}
	bodyJSON, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/evals", bytes.NewReader(bodyJSON))
	r.SetPathValue("version_id", versionID)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400. Body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "must be closed") {
		t.Errorf("body missing expected message: %s", w.Body.String())
	}
}

func TestEval_Start_TransitionsThenRefusesSecond(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	body, _ := json.Marshal(map[string]string{"dataset_version_id": dvID})
	rc := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/evals", bytes.NewReader(body))
	rc.SetPathValue("version_id", versionID)
	rc.Header.Set("Content-Type", "application/json")
	wc := httptest.NewRecorder()
	h.Create(wc, rc)
	if wc.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", wc.Code, wc.Body.String())
	}
	var e types.Eval
	_ = json.Unmarshal(wc.Body.Bytes(), &e)

	rs := httptest.NewRequest(http.MethodPost, "/evals/"+e.ID+"/start", nil)
	rs.SetPathValue("eval_id", e.ID)
	ws := httptest.NewRecorder()
	h.Start(ws, rs)
	if ws.Code != http.StatusNoContent {
		t.Errorf("first Start = %d, want 204. Body: %s", ws.Code, ws.Body.String())
	}
	rs2 := httptest.NewRequest(http.MethodPost, "/evals/"+e.ID+"/start", nil)
	rs2.SetPathValue("eval_id", e.ID)
	ws2 := httptest.NewRecorder()
	h.Start(ws2, rs2)
	if ws2.Code != http.StatusConflict {
		t.Errorf("second Start = %d, want 409", ws2.Code)
	}
}

// TestEval_Create_HappyPath — JSON body creates an eval in PENDING.
func TestEval_Create_HappyPath(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	body := map[string]string{"dataset_version_id": dvID}
	bodyJSON, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/evals", bytes.NewReader(bodyJSON))
	r.SetPathValue("version_id", versionID)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201. Body: %s", w.Code, w.Body.String())
	}
	var e types.Eval
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Status != types.EvalStatusPending {
		t.Errorf("status = %q, want PENDING", e.Status)
	}
	if e.ID == "" {
		t.Errorf("empty ID in response")
	}
}

// TestEval_Create_RefusesMissingCriteria — closed dataset version with a
// feedback row that has empty judge_criteria must reject the create.
func TestEval_Create_RefusesMissingCriteria(t *testing.T) {
	h, versionID, _ := setupEvalAPI(t)
	db := unwrapDB(h)

	// Seed a fresh dataset+CLOSED version that contains a session with
	// feedback lacking criteria. We bypass CloseDraft's guard by inserting
	// the dataset_version straight in CLOSED state so we can test the
	// belt-and-braces check on Create.
	var dsID, dvID, sessionID, msgID string
	_ = db.QueryRow(`INSERT INTO datasets (name) VALUES ('mc-ds-'||md5(random()::text)) RETURNING id::text`).Scan(&dsID)
	_ = db.QueryRow(`INSERT INTO dataset_versions (dataset_id, status, version_num, closed_at) VALUES ($1::uuid, 'CLOSED', 1, now()) RETURNING id::text`, dsID).Scan(&dvID)
	_ = db.QueryRow(`INSERT INTO chat_sessions (agent_version_id, locked_at) VALUES ($1::uuid, now()) RETURNING id::text`, versionID).Scan(&sessionID)
	_ = db.QueryRow(`INSERT INTO chat_messages (session_id, role, content, seq) VALUES ($1::uuid, 'assistant', '[]'::jsonb, 1) RETURNING id::text`, sessionID).Scan(&msgID)
	_, _ = db.Exec(`INSERT INTO chat_message_feedback (message_id, rating) VALUES ($1::uuid, 'up')`, msgID)
	_, _ = db.Exec(`INSERT INTO dataset_version_sessions (dataset_version_id, session_id) VALUES ($1::uuid, $2::uuid)`, dvID, sessionID)

	body, _ := json.Marshal(map[string]string{"dataset_version_id": dvID})
	r := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/evals", bytes.NewReader(body))
	r.SetPathValue("version_id", versionID)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. Body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "judge_criteria") {
		t.Errorf("body missing expected criteria message: %s", w.Body.String())
	}
}

// TestEval_Export_YAMLShape — export.yaml returns the expected top-level keys.
func TestEval_Export_YAMLShape(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	// Create an eval via the handler.
	body, _ := json.Marshal(map[string]string{"dataset_version_id": dvID})
	rc := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/evals", bytes.NewReader(body))
	rc.SetPathValue("version_id", versionID)
	rc.Header.Set("Content-Type", "application/json")
	wc := httptest.NewRecorder()
	h.Create(wc, rc)
	var e types.Eval
	_ = json.Unmarshal(wc.Body.Bytes(), &e)

	re := httptest.NewRequest(http.MethodGet, "/evals/"+e.ID+"/export.yaml", nil)
	re.SetPathValue("eval_id", e.ID)
	we := httptest.NewRecorder()
	h.Export(we, re)
	if we.Code != http.StatusOK {
		t.Fatalf("Export status = %d, want 200. Body: %s", we.Code, we.Body.String())
	}
	if !strings.Contains(we.Header().Get("Content-Type"), "yaml") {
		t.Errorf("Content-Type = %q, want yaml", we.Header().Get("Content-Type"))
	}
	body2 := we.Body.String()
	for _, want := range []string{"schema_version: eval-input-v1", "eval_id:", "agent_version:", "dataset_version:"} {
		if !strings.Contains(body2, want) {
			t.Errorf("export body missing %q. Full body:\n%s", want, body2)
		}
	}
}

// TestEval_Result_HappyPathThenRefusesSecond — Submit DONE once, refuse a second Submit.
func TestEval_Result_HappyPathThenRefusesSecond(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	body, _ := json.Marshal(map[string]string{"dataset_version_id": dvID})
	rc := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/evals", bytes.NewReader(body))
	rc.SetPathValue("version_id", versionID)
	rc.Header.Set("Content-Type", "application/json")
	wc := httptest.NewRecorder()
	h.Create(wc, rc)
	var e types.Eval
	_ = json.Unmarshal(wc.Body.Bytes(), &e)

	// Move to IN_PROGRESS (Submit doesn't require this — but let's mirror
	// the realistic path).
	rs := httptest.NewRequest(http.MethodPost, "/evals/"+e.ID+"/start", nil)
	rs.SetPathValue("eval_id", e.ID)
	ws := httptest.NewRecorder()
	h.Start(ws, rs)

	// Submit a DONE result with no session rows (empty dataset).
	result := types.EvalResult{
		SchemaVersion:  "eval-result-v1",
		Status:         types.EvalStatusDone,
		Score:          0,
		TotalCriteria:  0,
		PassedCriteria: 0,
		AikdmMeta:      json.RawMessage(`{"judge_model":"claude-haiku-4-5"}`),
	}
	resultJSON, _ := json.Marshal(result)
	rr := httptest.NewRequest(http.MethodPost, "/evals/"+e.ID+"/result", bytes.NewReader(resultJSON))
	rr.SetPathValue("eval_id", e.ID)
	rr.Header.Set("Content-Type", "application/json")
	wr := httptest.NewRecorder()
	h.Result(wr, rr)
	if wr.Code != http.StatusNoContent {
		t.Fatalf("first Result = %d, want 204. Body: %s", wr.Code, wr.Body.String())
	}

	// Second Result must refuse.
	rr2 := httptest.NewRequest(http.MethodPost, "/evals/"+e.ID+"/result", bytes.NewReader(resultJSON))
	rr2.SetPathValue("eval_id", e.ID)
	rr2.Header.Set("Content-Type", "application/json")
	wr2 := httptest.NewRecorder()
	h.Result(wr2, rr2)
	if wr2.Code != http.StatusConflict {
		t.Errorf("second Result = %d, want 409", wr2.Code)
	}
}

// TestEval_Fail_TransitionsToFailed — /fail flips status to FAILED with the error message.
func TestEval_Fail_TransitionsToFailed(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	body, _ := json.Marshal(map[string]string{"dataset_version_id": dvID})
	rc := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/evals", bytes.NewReader(body))
	rc.SetPathValue("version_id", versionID)
	rc.Header.Set("Content-Type", "application/json")
	wc := httptest.NewRecorder()
	h.Create(wc, rc)
	var e types.Eval
	_ = json.Unmarshal(wc.Body.Bytes(), &e)

	failBody, _ := json.Marshal(map[string]string{"error_message": "aikdm crashed"})
	rf := httptest.NewRequest(http.MethodPost, "/evals/"+e.ID+"/fail", bytes.NewReader(failBody))
	rf.SetPathValue("eval_id", e.ID)
	rf.Header.Set("Content-Type", "application/json")
	wf := httptest.NewRecorder()
	h.Fail(wf, rf)
	if wf.Code != http.StatusNoContent {
		t.Fatalf("Fail = %d, want 204. Body: %s", wf.Code, wf.Body.String())
	}

	// Reload via GetByID indirectly through the repo — use unwrapDB for a raw query.
	db := unwrapDB(h)
	var status, errMsg string
	_ = db.QueryRow(`SELECT status, error_message FROM evals WHERE id=$1::uuid`, e.ID).Scan(&status, &errMsg)
	if status != "FAILED" || errMsg != "aikdm crashed" {
		t.Errorf("post-Fail state: status=%q, err=%q; want FAILED / 'aikdm crashed'", status, errMsg)
	}
}

// TestEvalHandler_UIDetail_ShowsSourceAgentLink — GET /evals/{id} renders
// the enriched agent name + version number in a link to the prompts tab.
func TestEvalHandler_UIDetail_ShowsSourceAgentLink(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)

	// Create an eval so we have a real eval ID.
	body, _ := json.Marshal(map[string]string{"dataset_version_id": dvID})
	rc := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/evals", bytes.NewReader(body))
	rc.SetPathValue("version_id", versionID)
	rc.Header.Set("Content-Type", "application/json")
	wc := httptest.NewRecorder()
	h.Create(wc, rc)
	if wc.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", wc.Code, wc.Body.String())
	}
	var e types.Eval
	_ = json.Unmarshal(wc.Body.Bytes(), &e)

	// Render the detail page.
	rd := httptest.NewRequest(http.MethodGet, "/evals/"+e.ID, nil)
	rd.SetPathValue("eval_id", e.ID)
	wd := httptest.NewRecorder()
	h.UIDetail(wd, rd)
	if wd.Code != http.StatusOK {
		t.Fatalf("UIDetail = %d, want 200. Body: %s", wd.Code, wd.Body.String())
	}

	body2 := wd.Body.String()
	// The enriched agent name (seeded as "h-a" in setupEvalAPI) must appear.
	if !strings.Contains(body2, "h-a") {
		t.Errorf("detail page missing agent name 'h-a'. Body snippet: %s", body2[:min(500, len(body2))])
	}
	// The link href must point to the source agent version's prompts tab.
	wantHref := "/agent_versions/" + versionID + "/configure/prompts"
	if !strings.Contains(body2, wantHref) {
		t.Errorf("detail page missing href %q. Body snippet: %s", wantHref, body2[:min(500, len(body2))])
	}
	// The "Source agent version" label must be present.
	if !strings.Contains(body2, "Source agent version") {
		t.Errorf("detail page missing 'Source agent version' label")
	}
}

func TestEvalHandler_UIDetail_TrainButtonAppearsWhenEligible(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	db := getEvalTestDB(t, h)
	evalID := insertDoneEval(t, db, versionID, dvID, 0.5)

	req := httptest.NewRequest(http.MethodGet, "/evals/"+evalID, nil)
	req.SetPathValue("eval_id", evalID)
	rec := httptest.NewRecorder()
	h.UIDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `action="/training-sessions"`) {
		t.Error("expected Train form action='/training-sessions'")
	}
	if !strings.Contains(body, `name="source_eval_id"`) {
		t.Error("expected hidden source_eval_id input")
	}
}

func TestEvalHandler_UIDetail_TrainButtonHiddenWhenActiveSession(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	db := getEvalTestDB(t, h)
	evalID := insertDoneEval(t, db, versionID, dvID, 0.5)
	_ = insertTrainingSession(t, db, evalID, versionID)

	req := httptest.NewRequest(http.MethodGet, "/evals/"+evalID, nil)
	req.SetPathValue("eval_id", evalID)
	rec := httptest.NewRecorder()
	h.UIDetail(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `action="/training-sessions"`) {
		t.Error("Train form should NOT be present when active session exists")
	}
	if !strings.Contains(body, "Training pending") {
		t.Error("expected 'Training pending' link/badge in place of Train button")
	}
}

func TestEvalHandler_UIDetail_TrainButtonHiddenForPerfectScore(t *testing.T) {
	h, versionID, dvID := setupEvalAPI(t)
	db := getEvalTestDB(t, h)
	evalID := insertDoneEval(t, db, versionID, dvID, 1.0)

	req := httptest.NewRequest(http.MethodGet, "/evals/"+evalID, nil)
	req.SetPathValue("eval_id", evalID)
	rec := httptest.NewRecorder()
	h.UIDetail(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `action="/training-sessions"`) {
		t.Error("Train form should NOT be present for perfect-score evals")
	}
}
