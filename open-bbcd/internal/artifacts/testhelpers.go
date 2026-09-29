package artifacts

import "time"

// RegisterKindForTest registers an adapter-factory under a kind name so
// that tests in OTHER packages (e.g. internal/handler) can build a
// Registry via Load without needing a real S3 endpoint. Not intended
// for production callers — the name signals its purpose. There is no
// deregistration counterpart; test callers should use a kind slug
// unique to their test (e.g. "test-fake") to avoid stepping on
// production kinds.
//
// Kept in a non-_test.go file (rather than testhelpers_test.go) because
// Go does NOT expose *_test.go declarations across package boundaries.
// The helper is small and imported only by tests, so the runtime cost
// is negligible.
func RegisterKindForTest(kind string, factory func(options map[string]string, ttl time.Duration) (ArtifactStore, error)) {
	adapterFactories[kind] = factory
}
