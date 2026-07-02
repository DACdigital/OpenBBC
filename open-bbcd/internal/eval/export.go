// Package eval builds the eval-input.yaml payload aikdm consumes.
// It is intentionally isolated from handler concerns so the payload
// shape is easy to test in isolation.
package eval

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"gopkg.in/yaml.v3"
)

// InputPayload is the shape emitted to aikdm.
type InputPayload struct {
	SchemaVersion  string              `yaml:"schema_version"`
	EvalID         string              `yaml:"eval_id"`
	AgentVersion   InputAgentVersion   `yaml:"agent_version"`
	DatasetVersion InputDatasetVersion `yaml:"dataset_version"`
}

type InputAgentVersion struct {
	ID     string                 `yaml:"id"`
	Bundle map[string]interface{} `yaml:"bundle"`
}

type InputDatasetVersion struct {
	ID       string         `yaml:"id"`
	Sessions []InputSession `yaml:"sessions"`
}

type InputSession struct {
	SessionID  string           `yaml:"session_id"`
	Title      string           `yaml:"title"`
	Transcript []InputMessage   `yaml:"transcript"`
	Criteria   []InputCriterion `yaml:"criteria"`
}

type InputMessage struct {
	MessageID string          `yaml:"message_id"`
	Role      string          `yaml:"role"`
	Content   json.RawMessage `yaml:"content"`
}

type InputCriterion struct {
	MessageID string   `yaml:"message_id"`
	Rating    string   `yaml:"rating"`
	Items     []string `yaml:"items"`
}

// Store is the minimum set of reads the export builder needs.
type Store interface {
	GetEval(ctx context.Context, evalID string) (*types.Eval, error)
	GetBundle(ctx context.Context, agentVersionID string) ([]byte, error)
	GetSessionRefs(ctx context.Context, datasetVersionID string) ([]*types.DatasetSessionRef, error)
	GetMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error)
	GetFeedbackForSession(ctx context.Context, sessionID string) (map[string]*types.ChatMessageFeedback, error)
}

// Build assembles the full payload for the given eval id.
func Build(ctx context.Context, s Store, evalID string) (*InputPayload, error) {
	e, err := s.GetEval(ctx, evalID)
	if err != nil {
		return nil, err
	}
	bundleBytes, err := s.GetBundle(ctx, e.AgentVersionID)
	if err != nil {
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	var bundle map[string]interface{}
	if len(bundleBytes) > 0 {
		if err := yaml.Unmarshal(bundleBytes, &bundle); err != nil {
			// Bundles are stored as JSONB — try JSON.
			if err2 := json.Unmarshal(bundleBytes, &bundle); err2 != nil {
				return nil, fmt.Errorf("parse bundle: %w", err)
			}
		}
	}
	refs, err := s.GetSessionRefs(ctx, e.DatasetVersionID)
	if err != nil {
		return nil, err
	}
	sessions := make([]InputSession, 0, len(refs))
	for _, ref := range refs {
		msgs, err := s.GetMessages(ctx, ref.SessionID)
		if err != nil {
			return nil, err
		}
		fbMap, err := s.GetFeedbackForSession(ctx, ref.SessionID)
		if err != nil {
			return nil, err
		}
		transcript := make([]InputMessage, 0, len(msgs))
		for _, m := range msgs {
			transcript = append(transcript, InputMessage{
				MessageID: m.ID,
				Role:      string(m.Role),
				Content:   m.Content,
			})
		}
		criteria := make([]InputCriterion, 0, len(fbMap))
		for msgID, fb := range fbMap {
			if len(fb.JudgeCriteria) == 0 {
				continue // filter empty — close-draft guarantees this stays empty for closed versions
			}
			criteria = append(criteria, InputCriterion{
				MessageID: msgID,
				Rating:    string(fb.Rating),
				Items:     fb.JudgeCriteria,
			})
		}
		sessions = append(sessions, InputSession{
			SessionID:  ref.SessionID,
			Title:      ref.SessionTitle,
			Transcript: transcript,
			Criteria:   criteria,
		})
	}
	return &InputPayload{
		SchemaVersion: "eval-input-v1",
		EvalID:        e.ID,
		AgentVersion: InputAgentVersion{
			ID:     e.AgentVersionID,
			Bundle: bundle,
		},
		DatasetVersion: InputDatasetVersion{
			ID:       e.DatasetVersionID,
			Sessions: sessions,
		},
	}, nil
}
