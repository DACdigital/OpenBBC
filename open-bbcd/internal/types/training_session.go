package types

import (
	"encoding/json"
	"time"
)

type TrainingSessionStatus string

const (
	TrainingSessionStatusPending    TrainingSessionStatus = "PENDING"
	TrainingSessionStatusInProgress TrainingSessionStatus = "IN_PROGRESS"
	TrainingSessionStatusDone       TrainingSessionStatus = "DONE"
	TrainingSessionStatusFailed     TrainingSessionStatus = "FAILED"
)

// TrainingSession mirrors the training_sessions row.
type TrainingSession struct {
	ID              string                `json:"id"`
	SourceEvalID    string                `json:"source_eval_id"`
	ParentVersionID string                `json:"parent_version_id"`
	NewVersionID    *string               `json:"new_version_id,omitempty"`
	Status          TrainingSessionStatus `json:"status"`
	RequestedAt     time.Time             `json:"requested_at"`
	StartedAt       *time.Time            `json:"started_at,omitempty"`
	CompletedAt     *time.Time            `json:"completed_at,omitempty"`
	ErrorMessage    string                `json:"error_message"`
	Epochs          *int                  `json:"epochs,omitempty"`
	Patience        *int                  `json:"patience,omitempty"`
	InitialScore    *float64              `json:"initial_score,omitempty"`
	FinalScore      *float64              `json:"final_score,omitempty"`
	TotalEpochsRun  *int                  `json:"total_epochs_run,omitempty"`
	StoppedReason   *string               `json:"stopped_reason,omitempty"`
	TrainingReport  json.RawMessage       `json:"training_report,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
}
