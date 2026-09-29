package artifacts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
)

// Registry is the in-memory map of store_id → ArtifactStore constructed
// at open-bbcd boot from env vars. There is no runtime CRUD: adding,
// removing, or reconfiguring a store requires a redeploy. Callers
// resolve reads via Get(store_id-from-ref); writes go to Default().
//
// Callers must not mutate the map after Load returns.
type Registry struct {
	stores    map[string]ArtifactStore
	defaultID string
}

// ErrRegistryDisabled indicates the caller asked for a registry from a
// config that has no configured stores. Callers use it to gate whether
// artifact routes are wired at all — an empty registry is a feature flag,
// not an error state.
var ErrRegistryDisabled = errors.New("artifacts: registry disabled (no stores configured)")

// adapterFactory is the kind → constructor mapping. New kinds register
// here as they land (each new kind is a code change per the arch — no
// runtime plug-in surface).
type adapterFactory func(options map[string]string, ttl time.Duration) (ArtifactStore, error)

var adapterFactories = map[string]adapterFactory{
	s3CompatibleKind: NewS3Compatible,
}

// Load constructs a Registry from parsed configuration. Callers should
// check errors.Is(err, ErrRegistryDisabled) before treating a non-nil
// error as a boot failure — that specific sentinel means "artifact
// feature is off; skip route wiring", not "config is broken".
//
// Load does NOT probe any store. Boot code that wants to fail fast on
// unreachable stores should call ProbeAll(ctx) immediately after Load
// and treat probe failure as a boot error.
func Load(cfg config.ArtifactsConfig) (*Registry, error) {
	if !cfg.Enabled() {
		return nil, ErrRegistryDisabled
	}

	reg := &Registry{
		stores:    make(map[string]ArtifactStore, len(cfg.Stores)),
		defaultID: cfg.DefaultID,
	}

	for id, sc := range cfg.Stores {
		factory, ok := adapterFactories[sc.Kind]
		if !ok {
			return nil, fmt.Errorf("artifact store %q: unknown kind %q (supported: %s)", id, sc.Kind, supportedKindsList())
		}
		adapter, err := factory(sc.Options, cfg.SignedURLTTL)
		if err != nil {
			return nil, fmt.Errorf("artifact store %q: %w", id, err)
		}
		reg.stores[id] = adapter
	}

	// The config parser already validated that DefaultID resolves to a
	// configured store, so this is a defensive re-check rather than an
	// expected code path.
	if _, ok := reg.stores[reg.defaultID]; !ok {
		return nil, fmt.Errorf("artifact store default %q is not in the registry (this is a config invariant bug — report it)", reg.defaultID)
	}
	return reg, nil
}

// Get returns the store registered under id, or nil if none. Callers
// treating nil as "unknown store" should return HTTP 404 to the client:
// a nonexistent store_id is the same shape of failure as a nonexistent
// blob URI, and both should stay indistinguishable to avoid leaking
// registry membership.
func (r *Registry) Get(id string) ArtifactStore {
	if r == nil {
		return nil
	}
	return r.stores[id]
}

// Default returns the store nominated by ARTIFACT_STORE_DEFAULT.
// Guaranteed non-nil after a successful Load (both the config parser
// and Load itself enforce that invariant).
func (r *Registry) Default() ArtifactStore {
	if r == nil {
		return nil
	}
	return r.stores[r.defaultID]
}

// DefaultID returns the env-declared slug for the default store. This
// value is what the framework writes into store_id on every new
// artifact_ref block, so it must be stable across deploys.
func (r *Registry) DefaultID() string {
	if r == nil {
		return ""
	}
	return r.defaultID
}

// IDs returns every configured store slug. Order is not stable across
// calls; callers wanting a stable order sort the result.
func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.stores))
	for id := range r.stores {
		out = append(out, id)
	}
	return out
}

// ProbeAll runs each store's Probe(ctx) with bounded retry. Retries
// per store: up to 3 attempts with exponential backoff (250ms, 1s, 4s
// between attempts). Returns the first error encountered so boot fails
// fast; on success, every registered store has been successfully round-
// tripped.
//
// Timing: the default store is probed first, so a healthy default plus
// one broken secondary still surfaces the broken one — but the daemon
// won't have wasted boot time on a broken default's retries if the
// primary is also broken.
func (r *Registry) ProbeAll(ctx context.Context) error {
	if r == nil {
		return ErrRegistryDisabled
	}
	// Probe the default first — it's the store the daemon must have
	// reachable to accept the first upload, so failing on it early is
	// the highest-signal boot failure.
	if err := probeWithRetry(ctx, r.defaultID, r.stores[r.defaultID]); err != nil {
		return err
	}
	for id, s := range r.stores {
		if id == r.defaultID {
			continue
		}
		if err := probeWithRetry(ctx, id, s); err != nil {
			return err
		}
	}
	return nil
}

// probeWithRetry runs one store's Probe with a bounded retry ladder.
// Delays are chosen so a transient network hiccup gets three chances
// across ~5s but a genuinely misconfigured store still fails boot
// within seconds rather than minutes.
func probeWithRetry(ctx context.Context, id string, s ArtifactStore) error {
	const attempts = 3
	backoffs := []time.Duration{250 * time.Millisecond, 1 * time.Second, 4 * time.Second}

	var last error
	for i := 0; i < attempts; i++ {
		if err := s.Probe(ctx); err != nil {
			last = err
			if i < attempts-1 {
				select {
				case <-time.After(backoffs[i]):
				case <-ctx.Done():
					return fmt.Errorf("%w: store %q: context cancelled during probe retry: %w", ErrProbeFailed, id, ctx.Err())
				}
				continue
			}
			return fmt.Errorf("%w: store %q: %w", ErrProbeFailed, id, last)
		}
		return nil
	}
	// Defensive — the loop above always returns.
	return fmt.Errorf("%w: store %q: %w", ErrProbeFailed, id, last)
}

func supportedKindsList() string {
	// Enumerated statically because iterating adapterFactories has
	// nondeterministic order and the error message benefits from a
	// stable presentation.
	return "s3_compatible"
}

// LockedSessionChecker reports whether a given (store_id, uri) pair is
// referenced by any message in a dataset-locked chat session. The
// implementation lives in the repository layer where the JSONB SQL is;
// this package holds the invariant contract.
//
// Contract: returns (true, nil) if any chat_messages row for a session
// with locked_at != NULL contains an artifact_ref block matching
// (storeID, uri). Returns (false, nil) if not referenced or referenced
// only by unlocked sessions. Wraps SQL errors verbatim on the second
// return value.
type LockedSessionChecker func(ctx context.Context, storeID, uri string) (bool, error)

// DeleteWithGuard removes a blob only when no locked session references
// the ref. Upholds the arch invariant "refs stay resolvable while any
// locked session references them" — see
// docs/architecture/current/ddd/contexts/artifacts.md § Invariants.
//
// Phase 1 note: this method is **not called by any REST route in the
// chat-artifacts feature**. It is dead code held for chat-artifacts-gc
// (a future spec) so the invariant contract is expressed in Go rather
// than relying on the arch document alone. Tests exercise the guard
// against a stub LockedSessionChecker to verify the branch, but no
// production caller reaches it yet.
//
// Errors:
//   - ErrRegistryDisabled if the registry is nil.
//   - Wrapped ErrDeleteRefused with an unknown-store message if
//     storeID is not in the registry.
//   - ErrDeleteRefused (bare) if check reports the ref is locked.
//   - Wrapped check error if the SQL scan fails.
//   - Whatever the adapter's Delete returns on the actual delete call.
func (r *Registry) DeleteWithGuard(ctx context.Context, check LockedSessionChecker, storeID, uri string) error {
	if r == nil {
		return ErrRegistryDisabled
	}
	if check == nil {
		return fmt.Errorf("%w: nil LockedSessionChecker (programmer error)", ErrDeleteRefused)
	}
	store := r.Get(storeID)
	if store == nil {
		return fmt.Errorf("%w: unknown store %q", ErrDeleteRefused, storeID)
	}
	locked, err := check(ctx, storeID, uri)
	if err != nil {
		return fmt.Errorf("locked-session check: %w", err)
	}
	if locked {
		return ErrDeleteRefused
	}
	return store.Delete(ctx, uri)
}
