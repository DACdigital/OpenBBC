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
