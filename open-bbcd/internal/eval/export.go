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
//
// Note: HeaderOverrides is a flat map (differs from chat-session's
// per-backend layout intentionally — an eval targets a single agent
// version, so a flat set of overrides is sufficient).
type InputPayload struct {
	SchemaVersion   string              `yaml:"schema_version"`
	EvalID          string              `yaml:"eval_id"`
	MockMCPTools    bool                `yaml:"mock_mcp_tools"`
	HeaderOverrides map[string]string   `yaml:"header_overrides,omitempty"`
	AgentVersion    InputAgentVersion   `yaml:"agent_version"`
	DatasetVersion  InputDatasetVersion `yaml:"dataset_version"`
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

// InputMessage carries the decoded JSON content so YAML round-trips as
// native structures (maps, arrays, strings) — not byte-arrays as would
// happen if Content were json.RawMessage.
type InputMessage struct {
	MessageID string          `yaml:"message_id"`
	Role      string          `yaml:"role"`
	Content   any             `yaml:"content"`
	ToolCalls []InputToolCall `yaml:"tool_calls,omitempty"`
}

type InputToolCall struct {
	Name   string `yaml:"name"`
	Args   any    `yaml:"args"`
	Result any    `yaml:"result"`
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
		// Keep raw bytes for the tool_use / tool_result scan (needs sub-tree
		// selection from Anthropic-style content blocks); decode to native
		// structures once at the end for YAML.
		rawContent := make([]json.RawMessage, len(msgs))
		transcript := make([]InputMessage, 0, len(msgs))
		for i, m := range msgs {
			rawContent[i] = m.Content
			transcript = append(transcript, InputMessage{
				MessageID: m.ID,
				Role:      string(m.Role),
			})
		}
		// First pass: index tool_use_id → decoded tool_result payload from tool-role messages.
		toolResults := map[string]any{}
		for i, m := range transcript {
			if m.Role != string(types.ChatRoleTool) {
				continue
			}
			var blocks []map[string]json.RawMessage
			if err := json.Unmarshal(rawContent[i], &blocks); err != nil {
				continue
			}
			for _, blk := range blocks {
				var t string
				_ = json.Unmarshal(blk["type"], &t)
				if t != "tool_result" {
					continue
				}
				var id string
				_ = json.Unmarshal(blk["tool_use_id"], &id)
				if id == "" {
					continue
				}
				if raw, ok := blk["content"]; ok {
					var decoded any
					if err := json.Unmarshal(raw, &decoded); err == nil {
						toolResults[id] = decoded
					}
				}
			}
		}
		// Second pass: for each assistant turn, hoist tool_use blocks into ToolCalls.
		for i := range transcript {
			if transcript[i].Role != string(types.ChatRoleAssistant) {
				continue
			}
			var blocks []map[string]json.RawMessage
			if err := json.Unmarshal(rawContent[i], &blocks); err != nil {
				continue
			}
			for _, blk := range blocks {
				var t string
				_ = json.Unmarshal(blk["type"], &t)
				if t != "tool_use" {
					continue
				}
				var name, id string
				_ = json.Unmarshal(blk["name"], &name)
				_ = json.Unmarshal(blk["id"], &id)
				var args any = map[string]any{}
				if raw := blk["input"]; len(raw) > 0 {
					_ = json.Unmarshal(raw, &args)
				}
				result := toolResults[id] // nil if the paired tool_result is missing
				transcript[i].ToolCalls = append(transcript[i].ToolCalls, InputToolCall{
					Name:   name,
					Args:   args,
					Result: result,
				})
			}
		}
		// Decode content bytes to native structures for YAML.
		for i := range transcript {
			raw := rawContent[i]
			if len(raw) == 0 {
				continue
			}
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err == nil {
				transcript[i].Content = decoded
			} else {
				transcript[i].Content = string(raw)
			}
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
		SchemaVersion:   "eval-input-v1",
		EvalID:          e.ID,
		MockMCPTools:    e.MockMCPTools,
		HeaderOverrides: e.HeaderOverrides,
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
