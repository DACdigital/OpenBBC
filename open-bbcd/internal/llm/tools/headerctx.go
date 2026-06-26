package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type forwardedHeadersKey struct{}

// WithForwardedHeaders stashes the live FE request headers on ctx so backends
// can replay them (allowlisted) to downstream services. Used by the chat
// handler at the deployed-runtime entry point.
func WithForwardedHeaders(ctx context.Context, h http.Header) context.Context {
	return context.WithValue(ctx, forwardedHeadersKey{}, h)
}

func forwardedHeadersFromContext(ctx context.Context) http.Header {
	v, _ := ctx.Value(forwardedHeadersKey{}).(http.Header)
	return v
}

type sessionHeaderOverridesKey struct{}

// SessionHeaderOverrides is the per-session map: backend_id → {header → value}.
// Used by BO chat to inject test credentials per backend without modifying
// the backend's persisted default_headers. Deployed runtime ignores this.
type SessionHeaderOverrides map[string]map[string]string

func WithSessionHeaderOverrides(ctx context.Context, ovr SessionHeaderOverrides) context.Context {
	if ovr == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionHeaderOverridesKey{}, ovr)
}

// sessionHeaderOverridesFromContext returns the per-backend override map for
// the current chat session, or nil if none was stashed.
func sessionHeaderOverridesFromContext(ctx context.Context) SessionHeaderOverrides {
	v, _ := ctx.Value(sessionHeaderOverridesKey{}).(SessionHeaderOverrides)
	return v
}

// RoutingEnvelopeHeader is the wire name of the routing envelope header.
// Exported so the chat handler can pull it off r.Header and exclude it from
// pass-through.
const RoutingEnvelopeHeader = "X-Openbbcd-Mcp-Headers"

// BackendHeaderRouting is the parsed form of the X-Openbbcd-Mcp-Headers
// envelope. Keys are backend names (case-insensitive lookup).
type BackendHeaderRouting struct {
	ByName map[string]BackendRoutingBlock
}

// BackendRoutingBlock is the routing instructions for a single backend.
type BackendRoutingBlock struct {
	// All, when true, forwards every header from the original FE request
	// (minus hop-by-hop and the routing header itself) to this backend
	// before applying the explicit Headers map.
	All bool
	// Headers explicitly mapped onto outgoing requests. Overrides All-forwarded
	// headers on key conflict.
	Headers map[string]string
}

// ParseBackendHeaderRouting decodes the base64url-encoded JSON envelope.
// Returns an empty (zero-value) routing struct when the header value is
// empty. Returns an error on malformed input — the caller should log + ignore
// (fail safe: no FE headers reach any backend).
func ParseBackendHeaderRouting(headerValue string) (BackendHeaderRouting, error) {
	out := BackendHeaderRouting{ByName: map[string]BackendRoutingBlock{}}
	if headerValue == "" {
		return out, nil
	}
	raw, err := base64.URLEncoding.DecodeString(headerValue)
	if err != nil {
		// Tolerate stdEncoding too, for FEs that mis-spec the variant.
		raw2, err2 := base64.StdEncoding.DecodeString(headerValue)
		if err2 != nil {
			return out, fmt.Errorf("decode base64: %w", err)
		}
		raw = raw2
	}
	// Decode JSON into a raw map first so we can extract _all separately.
	var rawMap map[string]map[string]any
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return out, fmt.Errorf("parse json: %w", err)
	}
	for name, block := range rawMap {
		b := BackendRoutingBlock{Headers: map[string]string{}}
		for k, v := range block {
			if k == "_all" {
				if vb, ok := v.(bool); ok {
					b.All = vb
				}
				continue
			}
			if vs, ok := v.(string); ok {
				b.Headers[k] = vs
			}
		}
		out.ByName[strings.ToLower(name)] = b
	}
	return out, nil
}

// LookupByBackendName returns the routing block for the named backend
// (case-insensitive). The second return value is false when the backend is
// not in the envelope, in which case NO live FE headers are forwarded.
func (r BackendHeaderRouting) LookupByBackendName(name string) (BackendRoutingBlock, bool) {
	if r.ByName == nil {
		return BackendRoutingBlock{}, false
	}
	b, ok := r.ByName[strings.ToLower(name)]
	return b, ok
}

type backendHeaderRoutingKey struct{}

// WithBackendHeaderRouting stashes the parsed envelope on ctx for backends
// to consult when building outgoing requests.
func WithBackendHeaderRouting(ctx context.Context, r BackendHeaderRouting) context.Context {
	return context.WithValue(ctx, backendHeaderRoutingKey{}, r)
}

func backendHeaderRoutingFromContext(ctx context.Context) (BackendHeaderRouting, bool) {
	v, ok := ctx.Value(backendHeaderRoutingKey{}).(BackendHeaderRouting)
	return v, ok
}
