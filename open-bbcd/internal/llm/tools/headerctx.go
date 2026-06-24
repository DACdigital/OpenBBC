package tools

import (
	"context"
	"net/http"
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
