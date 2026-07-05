package types

import (
	"encoding/json"
	"time"
)

type EvalStatus string

const (
	EvalStatusPending    EvalStatus = "PENDING"
	EvalStatusInProgress EvalStatus = "IN_PROGRESS"
	EvalStatusDone       EvalStatus = "DONE"
	EvalStatusFailed     EvalStatus = "FAILED"
)

// Eval is one run: (agent_version, dataset_version) with a status and a
// score. Score/total/passed are nullable until DONE. AikdmMeta captures
// the models used and timing so we can trace how a number was produced.
type Eval struct {
	ID               string          `json:"id"`
	AgentVersionID   string          `json:"agent_version_id"`
	DatasetVersionID string          `json:"dataset_version_id"`
	Status           EvalStatus      `json:"status"`
	Score            *float64        `json:"score,omitempty"`
	TotalCriteria    *int            `json:"total_criteria,omitempty"`
	PassedCriteria   *int            `json:"passed_criteria,omitempty"`
	ErrorMessage     string          `json:"error_message,omitempty"`
	AikdmMeta        json.RawMessage `json:"aikdm_meta,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	CompletedAt      *time.Time      `json:"completed_at,omitempty"`
}

// EvalSession is one row per simulated session inside an eval.
// Transcript and Judgments are opaque JSON — their shape is owned by aikdm
// and re-used by the UI's read-only rendering.
type EvalSession struct {
	ID             string          `json:"id"`
	EvalID         string          `json:"eval_id"`
	SessionID      string          `json:"session_id"`
	SessionTitle   string          `json:"session_title,omitempty"`
	AgentVersionID string          `json:"agent_version_id,omitempty"`
	Score          float64         `json:"score"`
	TotalCriteria  int             `json:"total_criteria"`
	PassedCriteria int             `json:"passed_criteria"`
	Transcript     json.RawMessage `json:"transcript"`
	Judgments      json.RawMessage `json:"judgments"`
}

// EvalResult is the JSON payload aikdm uploads to POST /evals/{id}/result.
// Fields mirror internal/repository/eval.go's Submit contract.
type EvalResult struct {
	SchemaVersion  string              `json:"schema_version"`
	Status         EvalStatus          `json:"status"`
	ErrorMessage   string              `json:"error_message,omitempty"`
	Score          float64             `json:"score"`
	TotalCriteria  int                 `json:"total_criteria"`
	PassedCriteria int                 `json:"passed_criteria"`
	AikdmMeta      json.RawMessage     `json:"aikdm_meta"`
	Sessions       []EvalResultSession `json:"sessions"`
}

// EvalResultSession is one entry in EvalResult.Sessions.
type EvalResultSession struct {
	SessionID      string          `json:"session_id"`
	Score          float64         `json:"score"`
	TotalCriteria  int             `json:"total_criteria"`
	PassedCriteria int             `json:"passed_criteria"`
	Transcript     json.RawMessage `json:"transcript"`
	Judgments      json.RawMessage `json:"judgments"`
}
