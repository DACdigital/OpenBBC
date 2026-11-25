package handler

import (
	"context"
	"net/http"
)

// idResolver maps a path/body reference — a UUID or a static ID — to the
// stored canonical lower-case UUID, or types.ErrNotFound. Implemented by
// *repository.AgentRepository and *repository.AgentVersionRepository.
type idResolver interface {
	ResolveID(ctx context.Context, ref string) (string, error)
}

// resolvePathID resolves the named path value through res. On a miss (or any
// resolver error) it writes the mapped error response — 404 for an unknown
// ref — and returns ok=false. Handlers must use the returned UUID, never the
// raw path value, for everything downstream.
func resolvePathID(w http.ResponseWriter, r *http.Request, res idResolver, param string) (string, bool) {
	id, err := res.ResolveID(r.Context(), r.PathValue(param))
	if err != nil {
		Error(w, err)
		return "", false
	}
	return id, true
}
