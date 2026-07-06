package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// stubTrainingStore captures calls for handler-layer assertions without a real DB.
type stubTrainingStore struct {
	// Recorded call inputs
	lastCreateEvalID    string
	lastCreateParentID  string
	lastStartID         string
	lastStartEpochs     int
	lastStartPatience   int
	lastFailID          string
	lastFailMessage     string
	lastCompleteID      string
	lastCompletePrompts []byte
	lastCompleteReport  json.RawMessage
	lastCompleteSummary types.CompleteSummary

	// Preset returns
	createErr     error
	createID      string
	getSession    *types.TrainingSession
	getErr        error
	getActive     *types.TrainingSession
	getActiveErr  error
	completeNewID string
	completeErr   error
	startErr      error
	failErr       error
	eval          *types.Eval
	evalErr       error
}

func (s *stubTrainingStore) Create(_ context.Context, sourceEvalID, parentVersionID string) (string, error) {
	s.lastCreateEvalID = sourceEvalID
	s.lastCreateParentID = parentVersionID
	return s.createID, s.createErr
}
func (s *stubTrainingStore) GetByID(_ context.Context, _ string) (*types.TrainingSession, error) {
	return s.getSession, s.getErr
}
func (s *stubTrainingStore) GetActiveByEval(_ context.Context, _ string) (*types.TrainingSession, error) {
	return s.getActive, s.getActiveErr
}
func (s *stubTrainingStore) List(_ context.Context, _, _ int) ([]*types.TrainingSession, error) {
	return nil, nil
}
func (s *stubTrainingStore) EnrichRows(_ context.Context, _ []*types.TrainingSession) ([]repository.TrainingSessionRowView, error) {
	return nil, nil
}
func (s *stubTrainingStore) Start(_ context.Context, id string, epochs, patience int) error {
	s.lastStartID = id
	s.lastStartEpochs = epochs
	s.lastStartPatience = patience
	return s.startErr
}
func (s *stubTrainingStore) Complete(_ context.Context, id string, promptsJSON []byte, trainingReport json.RawMessage, summary types.CompleteSummary) (string, error) {
	s.lastCompleteID = id
	s.lastCompletePrompts = append([]byte(nil), promptsJSON...)
	s.lastCompleteReport = append(json.RawMessage(nil), trainingReport...)
	s.lastCompleteSummary = summary
	return s.completeNewID, s.completeErr
}
func (s *stubTrainingStore) Fail(_ context.Context, id, msg string) error {
	s.lastFailID = id
	s.lastFailMessage = msg
	return s.failErr
}
func (s *stubTrainingStore) EvalForTraining(_ context.Context, _ string) (*types.Eval, error) {
	return s.eval, s.evalErr
}

func newTrainingHandler(t *testing.T, s TrainingSessionStore) *TrainingSessionHandler {
	t.Helper()
	h, err := NewTrainingSessionHandler(s)
	if err != nil {
		t.Fatalf("NewTrainingSessionHandler: %v", err)
	}
	return h
}

func score(f float64) *float64 { return &f }

func TestCreate_HappyPath(t *testing.T) {
	stub := &stubTrainingStore{
		eval: &types.Eval{
			ID: "e-1", AgentVersionID: "av-1", Status: types.EvalStatusDone,
			Score: score(0.4),
		},
		getActive: nil,
		createID:  "sess-new",
	}
	h := newTrainingHandler(t, stub)

	form := url.Values{"source_eval_id": {"e-1"}}
	req := httptest.NewRequest(http.MethodPost, "/training-sessions", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d, want 303 (body=%s)", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc != "/training-sessions/sess-new" {
		t.Errorf("Location = %q, want /training-sessions/sess-new", loc)
	}
	if stub.lastCreateEvalID != "e-1" || stub.lastCreateParentID != "av-1" {
		t.Errorf("wrong args: eval=%q parent=%q", stub.lastCreateEvalID, stub.lastCreateParentID)
	}
}

func TestCreate_RejectsNonDoneEval(t *testing.T) {
	stub := &stubTrainingStore{
		eval: &types.Eval{ID: "e-1", AgentVersionID: "av-1", Status: types.EvalStatusPending},
	}
	h := newTrainingHandler(t, stub)

	form := url.Values{"source_eval_id": {"e-1"}}
	req := httptest.NewRequest(http.MethodPost, "/training-sessions", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Create(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestCreate_RejectsPerfectScore(t *testing.T) {
	stub := &stubTrainingStore{
		eval: &types.Eval{ID: "e-1", AgentVersionID: "av-1", Status: types.EvalStatusDone, Score: score(1.0)},
	}
	h := newTrainingHandler(t, stub)

	form := url.Values{"source_eval_id": {"e-1"}}
	req := httptest.NewRequest(http.MethodPost, "/training-sessions", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Create(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestCreate_RejectsWhenActiveExists(t *testing.T) {
	stub := &stubTrainingStore{
		eval: &types.Eval{ID: "e-1", AgentVersionID: "av-1", Status: types.EvalStatusDone, Score: score(0.4)},
		getActive: &types.TrainingSession{
			ID: "sess-active", SourceEvalID: "e-1", Status: types.TrainingSessionStatusPending,
		},
	}
	h := newTrainingHandler(t, stub)

	form := url.Values{"source_eval_id": {"e-1"}}
	req := httptest.NewRequest(http.MethodPost, "/training-sessions", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Create(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("code = %d, want 409", rec.Code)
	}
}

func TestStart_HappyPath(t *testing.T) {
	stub := &stubTrainingStore{}
	h := newTrainingHandler(t, stub)

	body := `{"epochs":5,"patience":3}`
	req := httptest.NewRequest(http.MethodPost, "/training-sessions/sess-1/start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("session_id", "sess-1")
	rec := httptest.NewRecorder()
	h.Start(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if stub.lastStartID != "sess-1" {
		t.Errorf("start id = %q", stub.lastStartID)
	}
	if stub.lastStartEpochs != 5 || stub.lastStartPatience != 3 {
		t.Errorf("epochs=%d patience=%d, want 5/3", stub.lastStartEpochs, stub.lastStartPatience)
	}
}

func TestStart_WrongStatus_409(t *testing.T) {
	stub := &stubTrainingStore{startErr: types.ErrTrainingSessionConflict}
	h := newTrainingHandler(t, stub)

	body := `{"epochs":5,"patience":3}`
	req := httptest.NewRequest(http.MethodPost, "/training-sessions/sess-1/start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("session_id", "sess-1")
	rec := httptest.NewRecorder()
	h.Start(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("code = %d, want 409", rec.Code)
	}
}

func TestFail_HappyPath(t *testing.T) {
	stub := &stubTrainingStore{}
	h := newTrainingHandler(t, stub)

	body := `{"error_message":"crashed"}`
	req := httptest.NewRequest(http.MethodPost, "/training-sessions/sess-1/fail", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("session_id", "sess-1")
	rec := httptest.NewRecorder()
	h.Fail(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if stub.lastFailID != "sess-1" || stub.lastFailMessage != "crashed" {
		t.Errorf("fail args: id=%q msg=%q", stub.lastFailID, stub.lastFailMessage)
	}
}

func TestJSONFetch_ReturnsSessionJSON(t *testing.T) {
	stub := &stubTrainingStore{getSession: &types.TrainingSession{
		ID: "sess-1", SourceEvalID: "e-1", ParentVersionID: "av-1",
		Status: types.TrainingSessionStatusPending,
	}}
	h := newTrainingHandler(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/training-sessions/sess-1.json", nil)
	req.SetPathValue("session_id", "sess-1")
	rec := httptest.NewRecorder()
	h.JSONFetch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var got types.TrainingSession
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "sess-1" || got.Status != types.TrainingSessionStatusPending {
		t.Errorf("unexpected shape: %+v", got)
	}
}

func TestReportJSON_ServesRawReport(t *testing.T) {
	rawReport := json.RawMessage(`{"schema_version":"training-report-v1","final_score":0.7}`)
	stub := &stubTrainingStore{getSession: &types.TrainingSession{
		ID: "sess-1", TrainingReport: rawReport,
	}}
	h := newTrainingHandler(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/training-sessions/sess-1/report.json", nil)
	req.SetPathValue("session_id", "sess-1")
	rec := httptest.NewRecorder()
	h.ReportJSON(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Body)
	if !bytes.Contains(body, []byte("training-report-v1")) {
		t.Errorf("expected training-report-v1 in body, got %s", body)
	}
}

// Sanity check on the sentinel type check.
func TestErrConflictIsRecognised(t *testing.T) {
	if !errors.Is(types.ErrTrainingSessionConflict, types.ErrTrainingSessionConflict) {
		t.Fatal("errors.Is broken")
	}
}
