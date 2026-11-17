package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// CanonicalizeClientContext re-encodes a client_context JSON object
// deterministically. It runs once, at ingest (spec client-context, Scope 3):
//   - keys sorted at every depth (Go map marshalling);
//   - no insignificant whitespace, no HTML escaping, no trailing newline;
//   - number literals kept as sent (UseNumber + json.Number);
//   - strings re-encoded by Go: U+2028/U+2029 escaped, invalid UTF-8 and
//     lone surrogates become U+FFFD;
//   - duplicate keys: the last value wins, at every depth.
//
// Errors wrap types.ErrClientContextInvalid: the value is not an object, or
// a key or string value contains U+0000 (Postgres JSONB rejects it).
func CanonicalizeClientContext(raw json.RawMessage) (string, error) {
	notObject := fmt.Errorf("%w: must be a JSON object", types.ErrClientContextInvalid)
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", notObject
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return "", notObject
	}
	if containsNUL(obj) {
		return "", fmt.Errorf("%w: contains \\u0000", types.ErrClientContextInvalid)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// RenderClientContext is the only renderer of a client_context block: the
// exact text the model sees, on the live turn, every replay and the export.
func RenderClientContext(canonical string) string {
	return "<client_context>" + canonical + "</client_context>"
}

// containsNUL reports whether any key or string value holds U+0000.
func containsNUL(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.ContainsRune(x, 0)
	case map[string]any:
		for k, e := range x {
			if strings.ContainsRune(k, 0) || containsNUL(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if containsNUL(e) {
				return true
			}
		}
	}
	return false
}
