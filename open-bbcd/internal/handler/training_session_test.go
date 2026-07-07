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
	listStatus          string
	listLimit           int

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
	listResult    []*types.TrainingSession
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
func (s *stubTrainingStore) List(_ context.Context, status string, limit int) ([]*types.TrainingSession, error) {
	s.listStatus = status
	s.listLimit = limit
	return s.listResult, nil
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

	req := httptest.NewRequest(http.MethodGet, "/training-sessions/sess-1/json", nil)
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

func TestComplete_HappyPath(t *testing.T) {
	stub := &stubTrainingStore{completeNewID: "av-new"}
	h := newTrainingHandler(t, stub)

	body := `{
		"bundle": {
			"main_prompt": "trained",
			"skills": [{"name":"greet","prompt":"say hi politely"}]
		},
		"training_report": {
			"schema_version": "training-report-v1",
			"initial_score": 0.4,
			"final_score": 0.7,
			"total_epochs_run": 3,
			"stopped_reason": "max_epochs",
			"epochs": []
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/training-sessions/sess-1/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("session_id", "sess-1")
	rec := httptest.NewRecorder()
	h.Complete(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		NewVersionID string `json:"new_version_id"`
		SessionURL   string `json:"session_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.NewVersionID != "av-new" {
		t.Errorf("new_version_id = %q", resp.NewVersionID)
	}
	if resp.SessionURL != "/training-sessions/sess-1" {
		t.Errorf("session_url = %q", resp.SessionURL)
	}

	// Verify the extracted prompts match the sent bundle.
	var prompts types.Prompts
	if err := json.Unmarshal(stub.lastCompletePrompts, &prompts); err != nil {
		t.Fatalf("prompts unmarshal: %v", err)
	}
	if prompts.MainPrompt != "trained" {
		t.Errorf("MainPrompt = %q", prompts.MainPrompt)
	}
	if prompts.SkillPrompts["greet"] != "say hi politely" {
		t.Errorf("skill 'greet' prompt = %q", prompts.SkillPrompts["greet"])
	}

	// Verify the summary extraction.
	if stub.lastCompleteSummary.InitialScore != 0.4 {
		t.Errorf("initial_score = %v", stub.lastCompleteSummary.InitialScore)
	}
	if stub.lastCompleteSummary.FinalScore != 0.7 {
		t.Errorf("final_score = %v", stub.lastCompleteSummary.FinalScore)
	}
	if stub.lastCompleteSummary.TotalEpochsRun != 3 {
		t.Errorf("total_epochs_run = %d", stub.lastCompleteSummary.TotalEpochsRun)
	}
	if stub.lastCompleteSummary.StoppedReason != "max_epochs" {
		t.Errorf("stopped_reason = %q", stub.lastCompleteSummary.StoppedReason)
	}
}

func TestComplete_MalformedBundle(t *testing.T) {
	stub := &stubTrainingStore{}
	h := newTrainingHandler(t, stub)

	// bundle is missing main_prompt.
	body := `{"bundle": {"skills": []}, "training_report": {"final_score": 0.7}}`
	req := httptest.NewRequest(http.MethodPost, "/training-sessions/sess-1/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("session_id", "sess-1")
	rec := httptest.NewRecorder()
	h.Complete(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	if stub.lastCompleteID != "" {
		t.Error("store.Complete should NOT be called on malformed bundle")
	}
}

func TestComplete_WrongStatus_409(t *testing.T) {
	stub := &stubTrainingStore{completeErr: types.ErrTrainingSessionConflict}
	h := newTrainingHandler(t, stub)

	body := `{"bundle": {"main_prompt":"x","skills":[]}, "training_report": {"final_score":0.7}}`
	req := httptest.NewRequest(http.MethodPost, "/training-sessions/sess-1/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("session_id", "sess-1")
	rec := httptest.NewRecorder()
	h.Complete(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("code = %d, want 409", rec.Code)
	}
}

// listStub extends stubTrainingStore with per-test list/get returns.
type listStub struct {
	stubTrainingStore
	sessions   []*types.TrainingSession
	views      []repository.TrainingSessionRowView
	getSession *types.TrainingSession
}

func (s *listStub) List(_ context.Context, _ string, _ int) ([]*types.TrainingSession, error) {
	return s.sessions, nil
}
func (s *listStub) EnrichRows(_ context.Context, _ []*types.TrainingSession) ([]repository.TrainingSessionRowView, error) {
	return s.views, nil
}
func (s *listStub) GetByID(_ context.Context, _ string) (*types.TrainingSession, error) {
	return s.getSession, nil
}

func iptr(i int) *int         { return &i }
func strptr(s string) *string { return &s }

func TestUIList_RendersRows(t *testing.T) {
	sess := &types.TrainingSession{
		ID: "s-1", SourceEvalID: "e-1", Status: types.TrainingSessionStatusDone,
		InitialScore: score(0.4), FinalScore: score(0.7),
	}
	stub := &listStub{
		sessions: []*types.TrainingSession{sess},
		views: []repository.TrainingSessionRowView{
			{Session: sess, AgentName: "AcmeAgent", ParentVersionNum: 3, NewVersionNum: 4, SourceEvalScore: score(0.4)},
		},
	}
	h := newTrainingHandler(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/training-sessions", nil)
	rec := httptest.NewRecorder()
	h.UIList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "AcmeAgent") {
		t.Errorf("body should mention agent name")
	}
	if !strings.Contains(body, "0.40") || !strings.Contains(body, "0.70") {
		t.Errorf("body should show both scores")
	}
	if !strings.Contains(body, "/training-sessions/s-1") {
		t.Errorf("body should link to session detail")
	}
}

func TestUIDetail_ShowsFieldsForDone(t *testing.T) {
	newVer := "av-new"
	report := json.RawMessage(`{
		"schema_version":"training-report-v1",
		"initial_score":0.4,"final_score":0.7,"total_epochs_run":2,"stopped_reason":"max_epochs",
		"epochs":[
			{"epoch":1,"baseline_score":0.4,"candidate_score":0.5,"promoted":true,"patches":[],"teacher_notes":"tightened","duration_seconds":1.0,"tokens_in":10,"tokens_out":2,"error":""},
			{"epoch":2,"baseline_score":0.5,"candidate_score":0.7,"promoted":true,"patches":[],"teacher_notes":"added example","duration_seconds":2.0,"tokens_in":20,"tokens_out":4,"error":""}
		]
	}`)
	stub := &listStub{
		getSession: &types.TrainingSession{
			ID: "s-1", SourceEvalID: "e-1", ParentVersionID: "av-1", NewVersionID: &newVer,
			Status: types.TrainingSessionStatusDone,
			InitialScore: score(0.4), FinalScore: score(0.7),
			TotalEpochsRun: iptr(2),
			StoppedReason:  strptr("max_epochs"),
			TrainingReport: report,
		},
		views: []repository.TrainingSessionRowView{{
			AgentName: "AcmeAgent", ParentVersionNum: 3, NewVersionNum: 4, SourceEvalScore: score(0.4),
		}},
	}
	h := newTrainingHandler(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/training-sessions/s-1", nil)
	req.SetPathValue("session_id", "s-1")
	rec := httptest.NewRecorder()
	h.UIDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"AcmeAgent", "v3", "v4", "0.4000", "0.7000", "max_epochs", "tightened", "added example"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	// Trained version link
	if !strings.Contains(body, "/agent_versions/av-new/configure/prompts") {
		t.Errorf("body should link to trained version prompts tab")
	}
}

func TestTrainingSessionHandler_ListJSON_FiltersByStatus(t *testing.T) {
	stub := &stubTrainingStore{listResult: []*types.TrainingSession{
		{ID: "s1", Status: types.TrainingSessionStatusPending, SourceEvalID: "e1", ParentVersionID: "av1"},
	}}
	h := newTrainingHandler(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/training-sessions.json?status=PENDING&limit=50", nil)
	rec := httptest.NewRecorder()
	h.ListJSON(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var out []types.TrainingSession
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(out) != 1 || out[0].ID != "s1" {
		t.Errorf("unexpected result: %+v", out)
	}
	if stub.listStatus != "PENDING" || stub.listLimit != 50 {
		t.Errorf("stub not called with expected args: status=%q limit=%d", stub.listStatus, stub.listLimit)
	}
}

func TestTrainingSessionHandler_ListJSON_InvalidStatus_400(t *testing.T) {
	stub := &stubTrainingStore{}
	h := newTrainingHandler(t, stub)
	req := httptest.NewRequest(http.MethodGet, "/training-sessions.json?status=BOGUS", nil)
	rec := httptest.NewRecorder()
	h.ListJSON(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400. body=%s", rec.Code, rec.Body.String())
	}
}

func TestTrainingSessionHandler_ListJSON_EmptyResult_ReturnsEmptyArray(t *testing.T) {
	stub := &stubTrainingStore{listResult: nil}
	h := newTrainingHandler(t, stub)
	req := httptest.NewRequest(http.MethodGet, "/training-sessions.json?status=PENDING", nil)
	rec := httptest.NewRecorder()
	h.ListJSON(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("body = %q, want []", body)
	}
}

func TestTrainingSessionHandler_ListJSON_InvalidLimit_400(t *testing.T) {
	stub := &stubTrainingStore{}
	h := newTrainingHandler(t, stub)
	req := httptest.NewRequest(http.MethodGet, "/training-sessions.json?limit=abc", nil)
	rec := httptest.NewRecorder()
	h.ListJSON(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400. body=%s", rec.Code, rec.Body.String())
	}
}

func TestUIDetail_ShowsErrorForFailed(t *testing.T) {
	stub := &listStub{
		getSession: &types.TrainingSession{
			ID: "s-1", SourceEvalID: "e-1", ParentVersionID: "av-1",
			Status: types.TrainingSessionStatusFailed, ErrorMessage: "boom",
		},
		views: []repository.TrainingSessionRowView{{
			AgentName: "AcmeAgent", ParentVersionNum: 3,
		}},
	}
	h := newTrainingHandler(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/training-sessions/s-1", nil)
	req.SetPathValue("session_id", "s-1")
	rec := httptest.NewRecorder()
	h.UIDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("body should render error message")
	}
}
