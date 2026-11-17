package handler

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// defaultClientContextMaxBytes is used by handlers built without
// WithClientContextMaxBytes (tests).
const defaultClientContextMaxBytes = config.DefaultClientContextMaxBytes

// clampClientContextMaxBytes backs both WithClientContextMaxBytes setters:
// n < 1 keeps cur.
func clampClientContextMaxBytes(cur, n int) int {
	if n > 0 {
		return n
	}
	return cur
}

// clientContextBlock validates and canonicalizes a turn request's
// client_context (spec client-context Scope 1). Absent or null → (nil, nil).
// Errors are types.ErrClientContextInvalid (wrapped) or
// types.ErrClientContextTooLarge; their text is the plain-text 400 body.
func clientContextBlock(raw json.RawMessage, maxBytes int) (llm.Block, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	canonical, err := chat.CanonicalizeClientContext(trimmed)
	if err != nil {
		return nil, err
	}
	if len(canonical) > maxBytes {
		return nil, types.ErrClientContextTooLarge
	}
	return llm.ClientContextBlock{Canonical: canonical}, nil
}

// requestClientContext runs clientContextBlock for a turn handler. On error
// it writes the plain-text 400 (before any stream header) and returns false.
func requestClientContext(w http.ResponseWriter, raw json.RawMessage, maxBytes int) (llm.Block, bool) {
	cc, err := clientContextBlock(raw, maxBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return cc, true
}

// prependClientContext puts cc (if any) first in input. Call it after the
// empty-turn check: the block never counts toward that rule.
func prependClientContext(cc llm.Block, input []llm.Block) []llm.Block {
	if cc == nil {
		return input
	}
	return append([]llm.Block{cc}, input...)
}
