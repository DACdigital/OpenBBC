package types

import "time"

// SubAgentBinding is one row of agent_version_subagent: the caller version
// may delegate to TargetVersionID under the tool-facing Name; Note is shown
// to the caller LLM in the agent tool description.
type SubAgentBinding struct {
	CallerVersionID string    `json:"caller_version_id"`
	TargetVersionID string    `json:"target_version_id"`
	Name            string    `json:"name"`
	Note            string    `json:"note"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
