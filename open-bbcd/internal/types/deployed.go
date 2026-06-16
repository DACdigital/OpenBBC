package types

import (
	"encoding/json"
	"time"
)

// DeployedSession is one chat thread against a deployed agent chain. Scoped
// by (chain_root_id, user_id). UserID is opaque integrator-defined text.
type DeployedSession struct {
	ID          string    `json:"id"`
	ChainRootID string    `json:"chain_root_id"`
	UserID      string    `json:"user_id"`
	Title       string    `json:"title,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DeployedMessage is one row in a deployed session's message log. AgentVersionID
// stamps the specific version that handled the turn this row is part of.
type DeployedMessage struct {
	ID             string          `json:"id"`
	SessionID      string          `json:"session_id"`
	AgentVersionID string          `json:"agent_version_id"`
	Role           ChatRole        `json:"role"`
	Content        json.RawMessage `json:"content"`
	Seq            int             `json:"seq"`
	CreatedAt      time.Time       `json:"created_at"`
}
