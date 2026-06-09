// Package chat provides the per-turn orchestrator for the run-agent
// feature. Stateless: each Turn call loads bundle + history, drives one
// LLM round (text streaming + tool-use loop in B21), persists user +
// assistant messages, streams events through the transport.
package chat

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// AgentReader is the narrow agent-side interface the orchestrator needs.
type AgentReader interface {
	GetByID(ctx context.Context, id string) (*types.Agent, error)
}

// ChatStore is the narrow chat-repo interface the orchestrator needs.
type ChatStore interface {
	EnsureSession(ctx context.Context, sessionID, agentID string) error
	LoadMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error)
	AppendMessages(ctx context.Context, msgs []types.ChatMessage) error
	NextSeq(ctx context.Context, sessionID string) (int, error)
}

type Orchestrator struct {
	agents AgentReader
	chats  ChatStore
	llm    llm.LLM
	tools  tools.Handler
	logger *slog.Logger

	// Tunables; set by NewAPI from config. Sensible defaults baked in.
	Model         string
	MaxTokens     int
	MaxToolRounds int
}

func NewOrchestrator(agents AgentReader, chats ChatStore, l llm.LLM, t tools.Handler, logger *slog.Logger) *Orchestrator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Orchestrator{
		agents:        agents,
		chats:         chats,
		llm:           l,
		tools:         t,
		logger:        logger,
		Model:         "claude-sonnet-4-6",
		MaxTokens:     4096,
		MaxToolRounds: 10,
	}
}

// Turn runs one chat turn end-to-end. Caller owns the Sink + HTTP
// connection. Stream-level errors are emitted as ErrorEvent and don't
// abort the function; only unrecoverable errors (bundle missing, session
// mismatch, persistence failures) return non-nil error.
//
// B20 implements TEXT-ONLY turns (no tool-use loop). If the LLM emits a
// tool_use stop_reason, we currently let it through to the client — B21
// adds the tool loop.
func (o *Orchestrator) Turn(
	ctx context.Context,
	agentID, sessionID string,
	userInput []llm.Block,
	sink transport.Sink,
) error {
	// 1. Load agent + verify bundle exists.
	agent, err := o.agents.GetByID(ctx, agentID)
	if err != nil {
		return err
	}
	if len(agent.Bundle) == 0 {
		_ = sink.Send(ctx, transport.ErrorEvent{Code: "agent_not_runnable", Message: "agent has no bundle"})
		return types.ErrAgentNotRunnable
	}

	// 2. Ensure session row exists (lazy-create).
	if err := o.chats.EnsureSession(ctx, sessionID, agentID); err != nil {
		_ = sink.Send(ctx, transport.ErrorEvent{Code: "session_error", Message: err.Error()})
		return err
	}

	// 3. Load history.
	history, err := o.chats.LoadMessages(ctx, sessionID)
	if err != nil {
		return err
	}

	// 4. Build LLM request.
	var bundleHead struct {
		MainPrompt string `json:"main_prompt"`
	}
	if err := json.Unmarshal(agent.Bundle, &bundleHead); err != nil {
		return err
	}

	toolDefs, err := o.tools.Tools(agent.Bundle)
	if err != nil {
		return err
	}

	msgs := historyToLLM(history)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: userInput})

	// 5. Persist the user message NOW (before the LLM call). A failed
	// turn still captures what the user asked.
	userMsgID := uuid.NewString()
	userContent, err := blocksToJSON(userInput)
	if err != nil {
		return err
	}
	userSeq, err := o.chats.NextSeq(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := o.chats.AppendMessages(ctx, []types.ChatMessage{{
		ID:        userMsgID,
		SessionID: sessionID,
		Role:      types.ChatRoleUser,
		Content:   userContent,
		Seq:       userSeq,
	}}); err != nil {
		return err
	}

	// 6. Send session-start event.
	_ = sink.Send(ctx, transport.SessionStartEvent{SessionID: sessionID, AgentID: agentID})

	// 7. Drive the LLM (text-only for now — tool loop in B21).
	req := llm.Request{
		Model:     o.Model,
		System:    bundleHead.MainPrompt,
		Messages:  msgs,
		Tools:     toolDefs,
		MaxTokens: o.MaxTokens,
	}

	assistantMsgID := uuid.NewString()
	_ = sink.Send(ctx, transport.TextStartEvent{MessageID: assistantMsgID})

	var assistantBlocks []llm.Block
	var stopReason string
	var usageIn, usageOut int

	for ev, err := range o.llm.Generate(ctx, req) {
		if err != nil {
			_ = sink.Send(ctx, transport.ErrorEvent{Code: "llm_error", Message: err.Error()})
			return err
		}
		switch e := ev.(type) {
		case llm.TextDeltaEvent:
			_ = sink.Send(ctx, transport.TextDeltaEvent{MessageID: assistantMsgID, Delta: e.Delta})
			// accumulate into the last TextBlock (or open a new one)
			n := len(assistantBlocks)
			if n > 0 {
				if tb, ok := assistantBlocks[n-1].(llm.TextBlock); ok {
					assistantBlocks[n-1] = llm.TextBlock{Text: tb.Text + e.Delta}
					continue
				}
			}
			assistantBlocks = append(assistantBlocks, llm.TextBlock{Text: e.Delta})
		case llm.MessageStopEvent:
			stopReason = e.StopReason
		case llm.UsageEvent:
			if e.InputTokens > 0 {
				usageIn = e.InputTokens
			}
			if e.OutputTokens > 0 {
				usageOut = e.OutputTokens
			}
		// ToolUseStart/Input/End: ignored in B20 — B21 wires the loop.
		}
	}

	_ = sink.Send(ctx, transport.TextEndEvent{MessageID: assistantMsgID})

	// 8. Persist the assistant message.
	assistantContent, err := blocksToJSON(assistantBlocks)
	if err != nil {
		return err
	}
	assistantSeq, err := o.chats.NextSeq(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := o.chats.AppendMessages(ctx, []types.ChatMessage{{
		ID:        assistantMsgID,
		SessionID: sessionID,
		Role:      types.ChatRoleAssistant,
		Content:   assistantContent,
		Seq:       assistantSeq,
	}}); err != nil {
		return err
	}

	// 9. Turn-end + close sink.
	_ = sink.Send(ctx, transport.TurnEndEvent{
		StopReason: stopReason,
		UsageIn:    usageIn,
		UsageOut:   usageOut,
	})
	_ = sink.Close()

	o.logger.Info("turn completed",
		slog.String("agent_id", agentID),
		slog.String("session_id", sessionID),
		slog.String("stop_reason", stopReason),
		slog.Int("tokens_in", usageIn),
		slog.Int("tokens_out", usageOut),
	)
	return nil
}

// historyToLLM converts persisted ChatMessage rows to llm.Message values.
// Content is a JSONB array of content blocks (matches Anthropic's shape);
// parse each block by its "type" field.
func historyToLLM(rows []*types.ChatMessage) []llm.Message {
	out := make([]llm.Message, 0, len(rows))
	for _, m := range rows {
		var rawBlocks []json.RawMessage
		if err := json.Unmarshal(m.Content, &rawBlocks); err != nil {
			// Skip malformed rows rather than fail the whole turn.
			continue
		}
		out = append(out, llm.Message{
			Role:    llm.Role(m.Role),
			Content: parseBlocks(rawBlocks),
		})
	}
	return out
}

func parseBlocks(raw []json.RawMessage) []llm.Block {
	out := make([]llm.Block, 0, len(raw))
	for _, r := range raw {
		var head struct{ Type string `json:"type"` }
		_ = json.Unmarshal(r, &head)
		switch head.Type {
		case "text":
			var b struct{ Text string `json:"text"` }
			_ = json.Unmarshal(r, &b)
			out = append(out, llm.TextBlock{Text: b.Text})
		case "tool_use":
			var b struct {
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(r, &b)
			out = append(out, llm.ToolUseBlock{ID: b.ID, Name: b.Name, Input: b.Input})
		case "tool_result":
			var b struct {
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
				IsError   bool            `json:"is_error"`
			}
			_ = json.Unmarshal(r, &b)
			out = append(out, llm.ToolResultBlock{ToolUseID: b.ToolUseID, Result: b.Content, IsError: b.IsError})
		}
	}
	return out
}

func blocksToJSON(blocks []llm.Block) (json.RawMessage, error) {
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		switch x := b.(type) {
		case llm.TextBlock:
			out = append(out, map[string]any{"type": "text", "text": x.Text})
		case llm.ToolUseBlock:
			out = append(out, map[string]any{
				"type":  "tool_use",
				"id":    x.ID,
				"name":  x.Name,
				"input": json.RawMessage(x.Input),
			})
		case llm.ToolResultBlock:
			out = append(out, map[string]any{
				"type":        "tool_result",
				"tool_use_id": x.ToolUseID,
				"content":     json.RawMessage(x.Result),
				"is_error":    x.IsError,
			})
		}
	}
	return json.Marshal(out)
}
