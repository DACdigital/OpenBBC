package artifacts

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
)

// fakeStore is a hand-rolled ArtifactStore stub. Registry tests need
// control over Probe outcomes (pass, fail forever, fail-then-succeed)
// without needing a live S3.
type fakeStore struct {
	kind          string
	delivery      DeliveryMode
	probeCalls    int32
	probeFailFor  int32  // number of initial failures before Probe succeeds; -1 = fail forever
	probeErrValue string // error string returned during failure
}

func (f *fakeStore) Kind() string                    { return f.kind }
func (f *fakeStore) PreferredDelivery() DeliveryMode { return f.delivery }
func (f *fakeStore) Put(ctx context.Context, uri, mime string, r io.Reader, size int64) (PutResult, error) {
	return PutResult{URI: uri, SizeBytes: size}, nil
}
func (f *fakeStore) Get(ctx context.Context, uri string) (io.ReadCloser, error) { return nil, nil }
func (f *fakeStore) Sign(ctx context.Context, uri string, ttl time.Duration, opts SignOptions) (string, error) {
	return "https://example.com/signed/" + uri, nil
}
func (f *fakeStore) Stat(ctx context.Context, uri string) (StatResult, error) {
	return StatResult{Exists: true}, nil
}
func (f *fakeStore) Delete(ctx context.Context, uri string) error { return nil }
func (f *fakeStore) Probe(ctx context.Context) error {
	n := atomic.AddInt32(&f.probeCalls, 1)
	if f.probeFailFor < 0 {
		return errors.New(f.probeErrValue)
	}
	if n <= f.probeFailFor {
		return errors.New(f.probeErrValue)
	}
	return nil
}

// registerFakeKind swaps a fake constructor into adapterFactories for the
// duration of one test. It restores the previous binding on cleanup.
func registerFakeKind(t *testing.T, kind string, ctor func(map[string]string, time.Duration) (ArtifactStore, error)) {
	t.Helper()
	prev, existed := adapterFactories[kind]
	adapterFactories[kind] = ctor
	t.Cleanup(func() {
		if existed {
			adapterFactories[kind] = prev
		} else {
			delete(adapterFactories, kind)
		}
	})
}

func TestLoad_DisabledOnEmptyConfig(t *testing.T) {
	_, err := Load(config.ArtifactsConfig{})
	if !errors.Is(err, ErrRegistryDisabled) {
		t.Errorf("Load(empty) = %v, want ErrRegistryDisabled", err)
	}
}

func TestLoad_UnknownKindIsRejected(t *testing.T) {
	_, err := Load(config.ArtifactsConfig{
		Stores: map[string]config.ArtifactStoreConfig{
			"MAIN": {ID: "MAIN", Kind: "does-not-exist", Options: map[string]string{}},
		},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
	if errors.Is(err, ErrRegistryDisabled) {
		t.Errorf("unknown kind returned ErrRegistryDisabled — want a distinct error")
	}
}

func TestLoad_HappyPath(t *testing.T) {
	registerFakeKind(t, "fake", func(opts map[string]string, ttl time.Duration) (ArtifactStore, error) {
		return &fakeStore{kind: "fake", delivery: DeliveryBytes}, nil
	})
	reg, err := Load(config.ArtifactsConfig{
		Stores: map[string]config.ArtifactStoreConfig{
			"MAIN": {ID: "MAIN", Kind: "fake", Options: map[string]string{}},
		},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})
	if err != nil {
		t.Fatalf("Load = err %v", err)
	}
	if reg.DefaultID() != "MAIN" {
		t.Errorf("DefaultID = %q, want MAIN", reg.DefaultID())
	}
	if reg.Default() == nil {
		t.Error("Default() returned nil after successful Load")
	}
	if reg.Get("MAIN") == nil {
		t.Error(`Get("MAIN") returned nil after successful Load`)
	}
	if reg.Get("nonexistent") != nil {
		t.Error(`Get("nonexistent") should return nil for unknown store`)
	}
}

func TestProbeAll_SucceedsOnFirstTry(t *testing.T) {
	registerFakeKind(t, "fake", func(opts map[string]string, ttl time.Duration) (ArtifactStore, error) {
		return &fakeStore{kind: "fake", delivery: DeliveryBytes}, nil
	})
	reg, err := Load(config.ArtifactsConfig{
		Stores: map[string]config.ArtifactStoreConfig{
			"MAIN": {ID: "MAIN", Kind: "fake"},
		},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})
	if err != nil {
		t.Fatalf("Load = err %v", err)
	}

	ctx := context.Background()
	if err := reg.ProbeAll(ctx); err != nil {
		t.Fatalf("ProbeAll = err %v, want nil", err)
	}

	main := reg.Default().(*fakeStore)
	if got := atomic.LoadInt32(&main.probeCalls); got != 1 {
		t.Errorf("Probe called %d times, want 1", got)
	}
}

func TestProbeAll_RetriesOnTransientError(t *testing.T) {
	fake := &fakeStore{kind: "fake", delivery: DeliveryBytes, probeFailFor: 2, probeErrValue: "transient"}
	registerFakeKind(t, "fake", func(opts map[string]string, ttl time.Duration) (ArtifactStore, error) {
		return fake, nil
	})
	reg, err := Load(config.ArtifactsConfig{
		Stores: map[string]config.ArtifactStoreConfig{
			"MAIN": {ID: "MAIN", Kind: "fake"},
		},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})
	if err != nil {
		t.Fatalf("Load = err %v", err)
	}

	// This test would take ~5s with real backoffs (250ms + 1s = 1.25s
	// of sleeps between 3 attempts). Cap the total context timeout
	// generously so we're not tuned to the exact backoff schedule.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := reg.ProbeAll(ctx); err != nil {
		t.Fatalf("ProbeAll = err %v, want nil after retries", err)
	}
	if got := atomic.LoadInt32(&fake.probeCalls); got != 3 {
		t.Errorf("Probe called %d times, want 3", got)
	}
}

func TestProbeAll_FailsAfterAllRetriesExhausted(t *testing.T) {
	fake := &fakeStore{kind: "fake", delivery: DeliveryBytes, probeFailFor: -1, probeErrValue: "permanent"}
	registerFakeKind(t, "fake", func(opts map[string]string, ttl time.Duration) (ArtifactStore, error) {
		return fake, nil
	})
	reg, err := Load(config.ArtifactsConfig{
		Stores: map[string]config.ArtifactStoreConfig{
			"MAIN": {ID: "MAIN", Kind: "fake"},
		},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})
	if err != nil {
		t.Fatalf("Load = err %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = reg.ProbeAll(ctx)
	if err == nil {
		t.Fatal("expected error after all probes exhausted")
	}
	if !errors.Is(err, ErrProbeFailed) {
		t.Errorf("returned error should wrap ErrProbeFailed, got: %v", err)
	}
	if got := atomic.LoadInt32(&fake.probeCalls); got != 3 {
		t.Errorf("Probe called %d times, want 3 attempts on permanent failure", got)
	}
}

func TestProbeAll_ProbesEveryStore(t *testing.T) {
	fakeMain := &fakeStore{kind: "fake", delivery: DeliveryBytes}
	fakeBackup := &fakeStore{kind: "fake", delivery: DeliveryBytes}
	// The factory must return distinct instances per id — the fake
	// bindings track probe calls per instance.
	seq := int32(0)
	instances := []*fakeStore{fakeMain, fakeBackup}
	registerFakeKind(t, "fake", func(opts map[string]string, ttl time.Duration) (ArtifactStore, error) {
		i := atomic.AddInt32(&seq, 1) - 1
		return instances[i], nil
	})
	reg, err := Load(config.ArtifactsConfig{
		Stores: map[string]config.ArtifactStoreConfig{
			"MAIN":   {ID: "MAIN", Kind: "fake"},
			"BACKUP": {ID: "BACKUP", Kind: "fake"},
		},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})
	if err != nil {
		t.Fatalf("Load = err %v", err)
	}
	if err := reg.ProbeAll(context.Background()); err != nil {
		t.Fatalf("ProbeAll = err %v", err)
	}
	// Each store's Probe should have been called at least once — we
	// don't assert exact counts because Load's map iteration order
	// isn't stable and either fake could be MAIN vs BACKUP depending
	// on map order. The important invariant: both stores got probed.
	total := atomic.LoadInt32(&instances[0].probeCalls) + atomic.LoadInt32(&instances[1].probeCalls)
	if total != 2 {
		t.Errorf("total probes across both stores = %d, want 2", total)
	}
}

func TestDeleteWithGuard_RefusesWhenLocked(t *testing.T) {
	registerFakeKind(t, "fake", func(opts map[string]string, ttl time.Duration) (ArtifactStore, error) {
		return &fakeStore{kind: "fake", delivery: DeliveryBytes}, nil
	})
	reg, _ := Load(config.ArtifactsConfig{
		Stores:       map[string]config.ArtifactStoreConfig{"MAIN": {ID: "MAIN", Kind: "fake"}},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})

	// Stub that reports "yes, this ref is locked".
	check := func(ctx context.Context, storeID, uri string) (bool, error) {
		if storeID != "MAIN" || uri != "sha256/deadbeef" {
			t.Errorf("check called with unexpected args: storeID=%q uri=%q", storeID, uri)
		}
		return true, nil
	}

	err := reg.DeleteWithGuard(context.Background(), check, "MAIN", "sha256/deadbeef")
	if !errors.Is(err, ErrDeleteRefused) {
		t.Errorf("DeleteWithGuard(locked) = %v, want ErrDeleteRefused", err)
	}
}

func TestDeleteWithGuard_AllowsWhenNotLocked(t *testing.T) {
	fake := &fakeStore{kind: "fake", delivery: DeliveryBytes}
	registerFakeKind(t, "fake", func(opts map[string]string, ttl time.Duration) (ArtifactStore, error) {
		return fake, nil
	})
	reg, _ := Load(config.ArtifactsConfig{
		Stores:       map[string]config.ArtifactStoreConfig{"MAIN": {ID: "MAIN", Kind: "fake"}},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})

	// Stub that reports "not locked".
	check := func(ctx context.Context, storeID, uri string) (bool, error) {
		return false, nil
	}

	if err := reg.DeleteWithGuard(context.Background(), check, "MAIN", "sha256/deadbeef"); err != nil {
		t.Fatalf("DeleteWithGuard(unlocked) = err %v, want nil", err)
	}
}

func TestDeleteWithGuard_UnknownStoreRefused(t *testing.T) {
	registerFakeKind(t, "fake", func(opts map[string]string, ttl time.Duration) (ArtifactStore, error) {
		return &fakeStore{kind: "fake"}, nil
	})
	reg, _ := Load(config.ArtifactsConfig{
		Stores:       map[string]config.ArtifactStoreConfig{"MAIN": {ID: "MAIN", Kind: "fake"}},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})

	// The store id isn't in the registry — guard should refuse before
	// even calling check.
	checkCalls := 0
	check := func(ctx context.Context, storeID, uri string) (bool, error) {
		checkCalls++
		return false, nil
	}
	err := reg.DeleteWithGuard(context.Background(), check, "UNKNOWN", "sha256/x")
	if !errors.Is(err, ErrDeleteRefused) {
		t.Errorf("DeleteWithGuard(unknown store) = %v, want ErrDeleteRefused", err)
	}
	if checkCalls != 0 {
		t.Errorf("check called %d times for unknown-store path, want 0 (short-circuit expected)", checkCalls)
	}
}

func TestDeleteWithGuard_PropagatesCheckError(t *testing.T) {
	registerFakeKind(t, "fake", func(opts map[string]string, ttl time.Duration) (ArtifactStore, error) {
		return &fakeStore{kind: "fake"}, nil
	})
	reg, _ := Load(config.ArtifactsConfig{
		Stores:       map[string]config.ArtifactStoreConfig{"MAIN": {ID: "MAIN", Kind: "fake"}},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})

	sentinel := errors.New("simulated SQL failure")
	check := func(ctx context.Context, storeID, uri string) (bool, error) {
		return false, sentinel
	}
	err := reg.DeleteWithGuard(context.Background(), check, "MAIN", "sha256/x")
	if err == nil || !errors.Is(err, sentinel) {
		t.Errorf("DeleteWithGuard(check error) should wrap the SQL error; got %v", err)
	}
}

func TestDeleteWithGuard_NilChecker(t *testing.T) {
	registerFakeKind(t, "fake", func(opts map[string]string, ttl time.Duration) (ArtifactStore, error) {
		return &fakeStore{kind: "fake"}, nil
	})
	reg, _ := Load(config.ArtifactsConfig{
		Stores:       map[string]config.ArtifactStoreConfig{"MAIN": {ID: "MAIN", Kind: "fake"}},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})
	err := reg.DeleteWithGuard(context.Background(), nil, "MAIN", "sha256/x")
	if !errors.Is(err, ErrDeleteRefused) {
		t.Errorf("DeleteWithGuard(nil check) = %v, want ErrDeleteRefused (defensive)", err)
	}
}

func TestNilRegistryReturnsSafeZeroValues(t *testing.T) {
	var reg *Registry
	if reg.Get("anything") != nil {
		t.Error("nil registry Get should return nil, not panic")
	}
	if reg.Default() != nil {
		t.Error("nil registry Default should return nil")
	}
	if reg.DefaultID() != "" {
		t.Error("nil registry DefaultID should return \"\"")
	}
	if reg.IDs() != nil {
		t.Error("nil registry IDs should return nil")
	}
	if !errors.Is(reg.ProbeAll(context.Background()), ErrRegistryDisabled) {
		t.Error("nil registry ProbeAll should return ErrRegistryDisabled")
	}
}
