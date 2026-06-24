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
