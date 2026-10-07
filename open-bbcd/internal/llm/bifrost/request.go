package bifrost

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// buildChatRequest maps an llm.Request onto Bifrost's OpenAI-shaped
// chat-completions request. model is used when req.Model is empty.
func buildChatRequest(req llm.Request, provider schemas.ModelProvider, model string) *schemas.BifrostChatRequest {
	if req.Model != "" {
		model = req.Model
	}
	var input []schemas.ChatMessage
	if req.System != "" {
		input = append(input, schemas.ChatMessage{Role: schemas.ChatMessageRoleSystem, Content: textContent(req.System)})
	}
	for _, m := range req.Messages {
		input = append(input, convertMessage(m)...)
	}
	params := &schemas.ChatParameters{}
	if req.MaxTokens > 0 {
		n := req.MaxTokens
		params.MaxCompletionTokens = &n
	}
	if req.Temperature > 0 {
		t := req.Temperature
		params.Temperature = &t
	}
	for _, t := range req.Tools {
		params.Tools = append(params.Tools, convertTool(t))
	}
	return &schemas.BifrostChatRequest{Provider: provider, Model: model, Input: input, Params: params}
}

func textContent(s string) *schemas.ChatMessageContent {
	return &schemas.ChatMessageContent{ContentStr: &s}
}

func textPart(s string) schemas.ChatContentBlock {
	return schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: &s}
}

func imagePart(b llm.InlineMediaBlock) schemas.ChatContentBlock {
	url := "data:" + b.MIME + ";base64," + base64.StdEncoding.EncodeToString(b.Data)
	return schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: url}}
}

// convertMessage returns one chat message for user and assistant messages.
// A tool-role message expands to one "tool" message per tool result plus, when
// the framework appended media or surrogate text after the results, one
// synthetic "user" message carrying them: chat-completions tool messages are
// text-only.
func convertMessage(m llm.Message) []schemas.ChatMessage {
	switch m.Role {
	case llm.RoleAssistant:
		return []schemas.ChatMessage{convertAssistant(m)}
	case llm.RoleTool:
		return convertToolRole(m)
	default:
		return []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentBlocks: contentParts(m.Content)},
		}}
	}
}

// contentParts maps text and image blocks to chat content parts. Artifact refs
// never reach an adapter: the framework renders them to InlineMediaBlock or a
// text surrogate first, and only images are rendered natively here.
func contentParts(blocks []llm.Block) []schemas.ChatContentBlock {
	var parts []schemas.ChatContentBlock
	for _, b := range blocks {
		switch x := b.(type) {
		case llm.TextBlock:
			parts = append(parts, textPart(x.Text))
		case llm.InlineMediaBlock:
			if strings.HasPrefix(x.MIME, "image/") {
				parts = append(parts, imagePart(x))
			}
		}
	}
	return parts
}

func convertAssistant(m llm.Message) schemas.ChatMessage {
	var text strings.Builder
	var calls []schemas.ChatAssistantMessageToolCall
	for _, b := range m.Content {
		switch x := b.(type) {
		case llm.TextBlock:
			text.WriteString(x.Text)
		case llm.ToolUseBlock:
			args := string(x.Input)
			if len(bytes.TrimSpace(x.Input)) == 0 {
				args = "{}"
			}
			id, name, typ := x.ID, x.Name, "function"
			calls = append(calls, schemas.ChatAssistantMessageToolCall{
				Index:    uint16(len(calls)),
				Type:     &typ,
				ID:       &id,
				Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name, Arguments: args},
			})
		}
	}
	msg := schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant}
	if text.Len() > 0 {
		msg.Content = textContent(text.String())
	}
	if len(calls) > 0 {
		msg.ChatAssistantMessage = &schemas.ChatAssistantMessage{ToolCalls: calls}
	}
	return msg
}

func convertToolRole(m llm.Message) []schemas.ChatMessage {
	var out []schemas.ChatMessage
	var ids []string
	var extra []llm.Block
	for _, b := range m.Content {
		r, ok := b.(llm.ToolResultBlock)
		if !ok {
			extra = append(extra, b)
			continue
		}
		content := toolResultText(r.Result)
		id := r.ToolUseID
		tm := &schemas.ChatToolMessage{ToolCallID: &id}
		if r.IsError {
			// Chat completions has no error flag on the wire: the prefix tells
			// the model. IsError is also set; Bifrost maps it for providers
			// that support one (Anthropic is_error) and strips it otherwise.
			content = "Error: " + content
			isErr := true
			tm.IsError = &isErr
		}
		out = append(out, schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: textContent(content), ChatToolMessage: tm})
		ids = append(ids, r.ToolUseID)
	}
	if parts := contentParts(extra); len(parts) > 0 {
		lead := textPart("Attachments returned by tool call(s) " + strings.Join(ids, ", ") + ":")
		out = append(out, schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentBlocks: append([]schemas.ChatContentBlock{lead}, parts...)},
		})
	}
	return out
}

// toolResultText sends a JSON-string result as its plain-text value (e.g. the
// agent tool's child text) so the model never sees JSON quoting; anything else
// passes through as raw JSON. Same rule as the anthropic adapter.
func toolResultText(raw json.RawMessage) string {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
	}
	return string(raw)
}

func convertTool(t llm.ToolDef) schemas.ChatTool {
	params := &schemas.ToolFunctionParameters{Type: "object"}
	if len(bytes.TrimSpace(t.InputSchema)) > 0 {
		var p schemas.ToolFunctionParameters
		if err := json.Unmarshal(t.InputSchema, &p); err == nil {
			if p.Type == "" {
				p.Type = "object"
			}
			params = &p
		}
	}
	fn := &schemas.ChatToolFunction{Name: t.Name, Parameters: params}
	if t.Description != "" {
		d := t.Description
		fn.Description = &d
	}
	return schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: fn}
}
