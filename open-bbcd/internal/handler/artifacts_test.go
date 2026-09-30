package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// --- test fakes ---------------------------------------------------------

type fakeSessionStore struct {
	sessions map[string]*types.ChatSession // keyed by sessionID
	err      error                         // if non-nil, GetSession returns it
}

func (f *fakeSessionStore) GetSession(ctx context.Context, sessionID, versionID string) (*types.ChatSession, error) {
	if f.err != nil {
		return nil, f.err
	}
	s, ok := f.sessions[sessionID]
	if !ok {
		return nil, types.ErrNotFound
	}
	if s.AgentVersionID != versionID {
		return nil, types.ErrSessionAgentMismatch
	}
	return s, nil
}

type fakeRefResolver struct {
	found bool
	mime  string
	err   error
}

func (f *fakeRefResolver) SessionReferences(ctx context.Context, sessionID, storeID, uri string) (bool, string, error) {
	return f.found, f.mime, f.err
}

// fakeArtifactStore — minimal ArtifactStore stub for the handler tests.
// Records calls; can be programmed with canned Get/Sign/Stat/Put outcomes.
type fakeArtifactStore struct {
	kind      string
	delivery  artifacts.DeliveryMode
	statHit   bool // if true, Stat reports existing blob (dedup path)
	statSize  int64
	putCalls  int
	getCalls  int
	signCalls int
	getData   []byte
	signedURL string
	putErr    error
	getErr    error
	signErr   error
	statErr   error
}

func (s *fakeArtifactStore) Kind() string                             { return s.kind }
func (s *fakeArtifactStore) PreferredDelivery() artifacts.DeliveryMode { return s.delivery }
func (s *fakeArtifactStore) Put(ctx context.Context, uri, mime string, r io.Reader, size int64) (artifacts.PutResult, error) {
	s.putCalls++
	if s.putErr != nil {
		return artifacts.PutResult{}, s.putErr
	}
	// Drain the reader so the caller doesn't leak bytes.
	drained, _ := io.ReadAll(r)
	return artifacts.PutResult{URI: uri, SizeBytes: int64(len(drained))}, nil
}
func (s *fakeArtifactStore) Get(ctx context.Context, uri string) (io.ReadCloser, error) {
	s.getCalls++
	if s.getErr != nil {
		return nil, s.getErr
	}
	return io.NopCloser(bytes.NewReader(s.getData)), nil
}
func (s *fakeArtifactStore) Sign(ctx context.Context, uri string, ttl time.Duration) (string, error) {
	s.signCalls++
	if s.signErr != nil {
		return "", s.signErr
	}
	return s.signedURL, nil
}
func (s *fakeArtifactStore) Stat(ctx context.Context, uri string) (artifacts.StatResult, error) {
	if s.statErr != nil {
		return artifacts.StatResult{}, s.statErr
	}
	return artifacts.StatResult{Exists: s.statHit, SizeBytes: s.statSize}, nil
}
func (s *fakeArtifactStore) Delete(ctx context.Context, uri string) error { return nil }
func (s *fakeArtifactStore) Probe(ctx context.Context) error              { return nil }

// buildRegistry constructs a real *artifacts.Registry populated with the
// provided fake store keyed by "MAIN". Registers a fake adapter kind so
// artifacts.Load() succeeds via its normal path. Test cleanup restores
// the adapter-factory registration.
func buildRegistry(t *testing.T, store *fakeArtifactStore) *artifacts.Registry {
	t.Helper()
	// Register a fake adapter kind and clean up after the test.
	// Test package cannot modify adapterFactories directly (unexported);
	// use the internal exported registerFakeKind pattern by leveraging
	// the artifacts package's testonly interface. Simpler: instead of
	// mocking the registry, use a test constructor that returns a
	// pre-populated Registry. artifacts.NewRegistryForTest is defined
	// in a test helper below.
	//
	// Since we don't have such a helper, wire through Load with a fake
	// adapter kind. That path is exercised in artifacts/registry_test.go.
	// Here, we construct via a direct config + a one-shot fake kind.
	reg, err := loadFakeRegistry(store)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	return reg
}

// loadFakeRegistry is a test helper that builds a registry with one
// store keyed "MAIN". It relies on a test-only side door: registering
// a "test-fake" kind in the artifacts package's factory map. See
// artifacts/registry_test.go for the corresponding helper pattern.
func loadFakeRegistry(store *fakeArtifactStore) (*artifacts.Registry, error) {
	artifacts.RegisterKindForTest("test-fake", func(opts map[string]string, ttl time.Duration) (artifacts.ArtifactStore, error) {
		return store, nil
	})
	return artifacts.Load(config.ArtifactsConfig{
		Stores: map[string]config.ArtifactStoreConfig{
			"MAIN": {ID: "MAIN", Kind: "test-fake"},
		},
		DefaultID:    "MAIN",
		MaxUploadMB:  10,
		SignedURLTTL: 300 * time.Second,
	})
}

// --- helpers -----------------------------------------------------------

func newMultipartBody(t *testing.T, filename string, contentType string, body []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreatePart(multipartHeader(filename, contentType))
	if err != nil {
		t.Fatalf("multipart create part: %v", err)
	}
	_, _ = fw.Write(body)
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

func multipartHeader(filename, contentType string) map[string][]string {
	return map[string][]string{
		"Content-Disposition": {`form-data; name="file"; filename="` + filename + `"`},
		"Content-Type":        {contentType},
	}
}

// --- Upload tests ------------------------------------------------------

func TestUpload_HappyPath_NoDedup(t *testing.T) {
	store := &fakeArtifactStore{
		kind:     "test-fake",
		delivery: artifacts.DeliverySignedURL,
	}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{
			"s1": {ID: "s1", AgentVersionID: "v1"},
		},
	}
	h := NewArtifactHandler(sessions, &fakeRefResolver{}, reg, 10, nil)

	body, ct := newMultipartBody(t, "hello.txt", "text/plain", []byte("hello world"))
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/v1/chat/s1/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	rec := httptest.NewRecorder()

	h.HandleUpload(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var got ArtifactUploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("json unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if got.StoreID != "MAIN" {
		t.Errorf("StoreID = %q, want MAIN", got.StoreID)
	}
	if !strings.HasPrefix(got.URI, "sha256/") {
		t.Errorf("URI = %q, want sha256/ prefix", got.URI)
	}
	if got.MIME != "text/plain" {
		t.Errorf("MIME = %q, want text/plain", got.MIME)
	}
	if got.SizeBytes != int64(len("hello world")) {
		t.Errorf("SizeBytes = %d, want %d", got.SizeBytes, len("hello world"))
	}
	if got.Filename != "hello.txt" {
		t.Errorf("Filename = %q, want hello.txt", got.Filename)
	}
	if store.putCalls != 1 {
		t.Errorf("Put calls = %d, want 1", store.putCalls)
	}
}

func TestUpload_DedupSkipsPut(t *testing.T) {
	store := &fakeArtifactStore{
		kind:     "test-fake",
		delivery: artifacts.DeliverySignedURL,
		statHit:  true,
		statSize: 11,
	}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{
			"s1": {ID: "s1", AgentVersionID: "v1"},
		},
	}
	h := NewArtifactHandler(sessions, &fakeRefResolver{}, reg, 10, nil)

	body, ct := newMultipartBody(t, "hello.txt", "text/plain", []byte("hello world"))
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/v1/chat/s1/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	rec := httptest.NewRecorder()

	h.HandleUpload(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	if store.putCalls != 0 {
		t.Errorf("Put calls = %d, want 0 (dedup should skip Put)", store.putCalls)
	}
}

func TestUpload_MissingSession404(t *testing.T) {
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{sessions: map[string]*types.ChatSession{}}
	h := NewArtifactHandler(sessions, &fakeRefResolver{}, reg, 10, nil)

	body, ct := newMultipartBody(t, "f.txt", "text/plain", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/v1/chat/missing/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "missing")
	rec := httptest.NewRecorder()
	h.HandleUpload(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestUpload_LockedSession409(t *testing.T) {
	now := time.Now()
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{
			"s1": {ID: "s1", AgentVersionID: "v1", LockedAt: &now},
		},
	}
	h := NewArtifactHandler(sessions, &fakeRefResolver{}, reg, 10, nil)

	body, ct := newMultipartBody(t, "f.txt", "text/plain", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/v1/chat/s1/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	rec := httptest.NewRecorder()
	h.HandleUpload(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestUpload_TooLarge413(t *testing.T) {
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{"s1": {ID: "s1", AgentVersionID: "v1"}},
	}
	// Cap at 1 MB.
	h := NewArtifactHandler(sessions, &fakeRefResolver{}, reg, 1, nil)

	body, ct := newMultipartBody(t, "big.bin", "application/octet-stream", make([]byte, 2*1024*1024))
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/v1/chat/s1/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	rec := httptest.NewRecorder()
	h.HandleUpload(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestUpload_MissingFileField400(t *testing.T) {
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{"s1": {ID: "s1", AgentVersionID: "v1"}},
	}
	h := NewArtifactHandler(sessions, &fakeRefResolver{}, reg, 10, nil)

	// Multipart with a NON-"file" field.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="wrong_field"; filename="f.txt"`},
	})
	_, _ = fw.Write([]byte("x"))
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/agent_versions/v1/chat/s1/artifacts", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	rec := httptest.NewRecorder()
	h.HandleUpload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// --- Retrieval tests ---------------------------------------------------

func TestRetrieve_SignedURL302(t *testing.T) {
	store := &fakeArtifactStore{
		kind:      "test-fake",
		delivery:  artifacts.DeliverySignedURL,
		signedURL: "https://example.com/signed/foo",
	}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{"s1": {ID: "s1", AgentVersionID: "v1"}},
	}
	refs := &fakeRefResolver{found: true, mime: "image/png"}
	h := NewArtifactHandler(sessions, refs, reg, 10, nil)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/v1/chat/s1/artifacts/MAIN/sha256/abc", nil)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	req.SetPathValue("path", "MAIN/sha256/abc")
	rec := httptest.NewRecorder()
	h.HandleRetrieve(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "https://example.com/signed/foo" {
		t.Errorf("Location = %q, want the signed URL", got)
	}
}

func TestRetrieve_BytesMode200(t *testing.T) {
	payload := []byte{0x89, 0x50, 0x4e, 0x47}
	store := &fakeArtifactStore{
		kind:     "test-fake",
		delivery: artifacts.DeliveryBytes,
		getData:  payload,
	}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{"s1": {ID: "s1", AgentVersionID: "v1"}},
	}
	refs := &fakeRefResolver{found: true, mime: "image/png"}
	h := NewArtifactHandler(sessions, refs, reg, 10, nil)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/v1/chat/s1/artifacts/MAIN/sha256/abc", nil)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	req.SetPathValue("path", "MAIN/sha256/abc")
	rec := httptest.NewRecorder()
	h.HandleRetrieve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Errorf("body bytes mismatch")
	}
}

func TestRetrieve_UnknownRef404(t *testing.T) {
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{"s1": {ID: "s1", AgentVersionID: "v1"}},
	}
	refs := &fakeRefResolver{found: false} // ref NOT referenced by this session
	h := NewArtifactHandler(sessions, refs, reg, 10, nil)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/v1/chat/s1/artifacts/MAIN/sha256/abc", nil)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	req.SetPathValue("path", "MAIN/sha256/abc")
	rec := httptest.NewRecorder()
	h.HandleRetrieve(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (not referenced by session)", rec.Code)
	}
}

func TestRetrieve_UnknownStore404(t *testing.T) {
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{"s1": {ID: "s1", AgentVersionID: "v1"}},
	}
	// ref resolver reports found — but the store_id doesn't match registry.
	refs := &fakeRefResolver{found: true, mime: "image/png"}
	h := NewArtifactHandler(sessions, refs, reg, 10, nil)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/v1/chat/s1/artifacts/UNKNOWN/sha256/abc", nil)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	req.SetPathValue("path", "UNKNOWN/sha256/abc")
	rec := httptest.NewRecorder()
	h.HandleRetrieve(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (unknown store)", rec.Code)
	}
}

func TestRetrieve_BlobMissing410(t *testing.T) {
	store := &fakeArtifactStore{
		kind:     "test-fake",
		delivery: artifacts.DeliverySignedURL,
		signErr:  artifacts.ErrBlobMissing,
	}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{"s1": {ID: "s1", AgentVersionID: "v1"}},
	}
	refs := &fakeRefResolver{found: true, mime: "image/png"}
	h := NewArtifactHandler(sessions, refs, reg, 10, nil)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/v1/chat/s1/artifacts/MAIN/sha256/abc", nil)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	req.SetPathValue("path", "MAIN/sha256/abc")
	rec := httptest.NewRecorder()
	h.HandleRetrieve(rec, req)

	if rec.Code != http.StatusGone {
		t.Errorf("status = %d, want 410 (blob externally removed)", rec.Code)
	}
}

func TestRetrieve_MalformedPath400(t *testing.T) {
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{"s1": {ID: "s1", AgentVersionID: "v1"}},
	}
	h := NewArtifactHandler(sessions, &fakeRefResolver{}, reg, 10, nil)

	// Path has no slash — cannot be split into store_id + uri.
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/v1/chat/s1/artifacts/nosolash", nil)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	req.SetPathValue("path", "nosolash")
	rec := httptest.NewRecorder()
	h.HandleRetrieve(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestRetrieve_ResolverError500(t *testing.T) {
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{"s1": {ID: "s1", AgentVersionID: "v1"}},
	}
	refs := &fakeRefResolver{err: errors.New("db down")}
	h := NewArtifactHandler(sessions, refs, reg, 10, nil)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/v1/chat/s1/artifacts/MAIN/sha256/abc", nil)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	req.SetPathValue("path", "MAIN/sha256/abc")
	rec := httptest.NewRecorder()
	h.HandleRetrieve(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestUpload_ResolvesMIMEFromBytes(t *testing.T) {
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{sessions: map[string]*types.ChatSession{"s1": {ID: "s1", AgentVersionID: "v1"}}}
	h := NewArtifactHandler(sessions, &fakeRefResolver{}, reg, 10, nil)

	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC")
	body, ct := newMultipartBody(t, "shot.bin", "application/octet-stream", png)
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/v1/chat/s1/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	rec := httptest.NewRecorder()

	h.HandleUpload(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got ArtifactUploadResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.MIME != "image/png" {
		t.Fatalf("MIME = %q, want image/png (resolved from bytes)", got.MIME)
	}
}
