package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// setupEvalAPI seeds a minimal (agent, version, dataset, closed_dv) and
// returns a bound handler + the ids.
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
	adapter := &evalStoreAdapter{db: db, evalRepo: evalRepo, dataset: datasetRepo, chat: chatRepo, feedback: feedbackRepo}
	h, err := NewEvalHandler(evalRepo, datasetRepo, adapter, testWebFS())
	if err != nil {
		t.Fatalf("NewEvalHandler: %v", err)
	}
	return h, versionID, dvID
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
