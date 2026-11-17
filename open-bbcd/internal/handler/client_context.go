package handler

import (
	"bytes"
	"encoding/json"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// defaultClientContextMaxBytes matches config's OPENBBC_CLIENT_CONTEXT_MAX_BYTES
// default; handlers built without WithClientContextMaxBytes (tests) use it.
const defaultClientContextMaxBytes = 65536

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
