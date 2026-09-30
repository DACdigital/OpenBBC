package types

import "time"

// Session-artifact origins; mirror the CHECK on chat_session_artifacts /
// deployed_session_artifacts.
const (
	ArtifactOriginUpload     = "upload"
	ArtifactOriginToolResult = "tool_result"
)

// SessionArtifact is one row of a session's artifact table: an artifact in
// that session's read scope. MessageID == "" means pending (an upload not
// yet claimed by a turn).
type SessionArtifact struct {
	ID        string
	SessionID string
	Origin    string
	StoreID   string
	URI       string
	MIME      string
	SizeBytes int64
	Sha256    string
	Filename  string
	MessageID string
	CreatedAt time.Time
}

// ArtifactRefContent is the persisted JSON shape of an artifact_ref content
// block in chat_messages.content / deployed_messages.content. It is the
// single definition of that shape: the chat codec and the repositories
// (which append claimed refs inside the claim transaction) both use it.
type ArtifactRefContent struct {
	Type      string `json:"type"` // always "artifact_ref"
	StoreID   string `json:"store_id"`
	URI       string `json:"uri"`
	MIME      string `json:"mime"`
	SizeBytes int64  `json:"size_bytes"`
	Sha256    string `json:"sha256"`
	Filename  string `json:"filename,omitempty"`
}
