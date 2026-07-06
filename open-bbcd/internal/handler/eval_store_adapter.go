package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/eval"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// evalStoreAdapter satisfies eval.Store and EvalDetailStore by fanning out to
// the concrete repositories. Keeps eval.export ignorant of repo types.
type evalStoreAdapter struct {
	db               *sql.DB
	evalRepo         *repository.EvalRepository
	dataset          *repository.DatasetRepository
	chat             *repository.ChatRepository
	feedback         *repository.FeedbackRepository
	trainingSessions *repository.TrainingSessionRepository
}

var _ eval.Store = (*evalStoreAdapter)(nil)
var _ EvalDetailStore = (*evalStoreAdapter)(nil)

func (a *evalStoreAdapter) GetEval(ctx context.Context, id string) (*types.Eval, error) {
	return a.evalRepo.GetByID(ctx, id)
}

// GetBundle reconstructs the aikdm bundle JSON blob for the given agent
// version by re-joining the split payloads: agents.architecture holds the
// frozen structural pieces (tools/flows/external_actions/skills_meta) and
// agent_versions.prompts holds the editable main_prompt + per-skill prompts.
// This is the inverse of types.SplitBundle. The eval builder needs the
// combined shape because aikdm consumes bundles, not the split.
func (a *evalStoreAdapter) GetBundle(ctx context.Context, agentVersionID string) ([]byte, error) {
	var archRaw, promptsRaw []byte
	err := a.db.QueryRowContext(ctx, `
		SELECT COALESCE(a.architecture, '{}'::jsonb),
		       COALESCE(av.prompts, '{}'::jsonb)
		FROM agent_versions av
		JOIN agents a ON a.id = av.agent_id
		WHERE av.id = $1::uuid
	`, agentVersionID).Scan(&archRaw, &promptsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var arch types.Architecture
	if len(archRaw) > 0 {
		if err := json.Unmarshal(archRaw, &arch); err != nil {
			return nil, fmt.Errorf("parse architecture: %w", err)
		}
	}
	var prompts types.Prompts
	if len(promptsRaw) > 0 {
		if err := json.Unmarshal(promptsRaw, &prompts); err != nil {
			return nil, fmt.Errorf("parse prompts: %w", err)
		}
	}

	bundle := types.Bundle{
		Metadata:        arch.Metadata,
		MainPrompt:      prompts.MainPrompt,
		Tools:           arch.Tools,
		Flows:           arch.Flows,
		ExternalActions: arch.ExternalActions,
	}
	for _, sm := range arch.SkillsMeta {
		bundle.Skills = append(bundle.Skills, types.BundleSkill{
			Name:        sm.Name,
			Description: sm.Description,
			Prompt:      prompts.SkillPrompts[sm.Name],
		})
	}
	out, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("marshal bundle: %w", err)
	}
	return out, nil
}

func (a *evalStoreAdapter) GetSessionRefs(ctx context.Context, datasetVersionID string) ([]*types.DatasetSessionRef, error) {
	return a.dataset.GetVersionSessions(ctx, datasetVersionID)
}

func (a *evalStoreAdapter) GetMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error) {
	return a.chat.LoadMessages(ctx, sessionID)
}

func (a *evalStoreAdapter) GetFeedbackForSession(ctx context.Context, sessionID string) (map[string]*types.ChatMessageFeedback, error) {
	return a.feedback.GetForSession(ctx, sessionID)
}

// GetToolBackends resolves each wired endpoint on the agent version's
// agent to its HTTP-endpoint backend config (base_url + default_headers).
// MCP-client backends are filtered out. Rows with malformed config JSON
// are skipped rather than aborting the whole eval.
func (a *evalStoreAdapter) GetToolBackends(ctx context.Context, agentVersionID string) (map[string]eval.InputToolBackend, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT aeb.endpoint_id, tb.config
		FROM agent_versions av
		JOIN agent_endpoint_backend aeb ON aeb.agent_id = av.agent_id
		JOIN tool_backends tb ON tb.id = aeb.backend_id
		WHERE av.id = $1::uuid AND tb.kind = 'http_endpoint'
	`, agentVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]eval.InputToolBackend{}
	for rows.Next() {
		var endpointID string
		var raw []byte
		if err := rows.Scan(&endpointID, &raw); err != nil {
			return nil, err
		}
		var cfg types.HTTPBackendConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			continue
		}
		out[endpointID] = eval.InputToolBackend{
			BaseURL:        cfg.BaseURL,
			DefaultHeaders: cfg.DefaultHeaders,
		}
	}
	return out, rows.Err()
}

func (a *evalStoreAdapter) GetActiveTrainingSessionForEval(ctx context.Context, evalID string) (*types.TrainingSession, error) {
	return a.trainingSessions.GetActiveByEval(ctx, evalID)
}
