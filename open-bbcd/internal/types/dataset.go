package types

import "time"

// Dataset is a cross-agent collection of feedbacked sessions. Versioned via
// dataset_versions; consumers should almost always reach for versions, not
// datasets directly, when reading session lists.
type Dataset struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// DatasetVersionStatus is DRAFT while mutable, CLOSED once published.
type DatasetVersionStatus string

const (
	DatasetVersionDraft  DatasetVersionStatus = "DRAFT"
	DatasetVersionClosed DatasetVersionStatus = "CLOSED"
)

// DatasetVersion is either the current DRAFT (mutable) or a CLOSED
// (immutable) snapshot. Immutability is enforced by locking every session
// in the version at close time (see chat_sessions.locked_at).
type DatasetVersion struct {
	ID         string               `json:"id"`
	DatasetID  string               `json:"dataset_id"`
	Status     DatasetVersionStatus `json:"status"`
	VersionNum int                  `json:"version_num"`
	CloseNote  string               `json:"close_note"`
	CreatedAt  time.Time            `json:"created_at"`
	ClosedAt   *time.Time           `json:"closed_at,omitempty"`
}

// DatasetSessionRef is one row in the "sessions in this dataset version"
// join, enriched with the source agent/version for cross-agent display.
type DatasetSessionRef struct {
	SessionID       string    `json:"session_id"`
	SessionTitle    string    `json:"session_title"`
	AgentID         string    `json:"agent_id"`
	AgentName       string    `json:"agent_name"`
	AgentVersionID  string    `json:"agent_version_id"`
	AgentVersionNum int       `json:"agent_version_num"`
	ThumbsUpCount   int       `json:"thumbs_up_count"`
	ThumbsDownCount int       `json:"thumbs_down_count"`
	AddedAt         time.Time `json:"added_at"`
}
