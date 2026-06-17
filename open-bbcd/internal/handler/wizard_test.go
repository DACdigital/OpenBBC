package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/storage"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"gopkg.in/yaml.v3"
)

const wizardTestSchema = `
version: v1
wizard:
  name:
    label: "Agent name"
    type: text
    required: true
    order: 1
  scope:
    label: "Scope"
    type: textarea
    required: true
    order: 2
  discovery_file:
    label: "Upload discovery zip"
    type: file
    required: true
    order: 3
`

type mockWizardRepo struct {
	createFromWizardFn func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error)
}

func (m *mockWizardRepo) CreateFromWizard(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error) {
	return m.createFromWizardFn(ctx, opts)
}

type mockStorage struct {
	putFn func(ctx context.Context, key string, r io.Reader) error
	calls int
}

func (m *mockStorage) Put(ctx context.Context, key string, r io.Reader) error {
	m.calls++
	if m.putFn != nil {
		return m.putFn(ctx, key, r)
	}
	_, _ = io.Copy(io.Discard, r)
	return nil
}

// Open is unused in wizard tests; included so mockStorage satisfies the
// storage.Storage interface.
func (m *mockStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return nil, errors.New("mockStorage.Open: not implemented")
}

var _ storage.Storage = (*mockStorage)(nil)

const testMaxUploadBytes = 50 << 20 // 50 MB

func newTestWizardHandler(t *testing.T, repo WizardAgentRepository, store storage.Storage) *WizardHandler {
	t.Helper()
	var schema types.WizardSchema
	if err := yaml.Unmarshal([]byte(wizardTestSchema), &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return NewWizardHandler(repo, &schema, store, testMaxUploadBytes, testLogger())
}

// buildWizardForm returns a multipart body with the given text fields and an
// optional file part for `discovery_file`. If filePart is nil, no file is sent.
func buildWizardForm(t *testing.T, fields map[string]string, fileName string, fileContents []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	if fileName != "" {
		fw, err := w.CreateFormFile("discovery_file", fileName)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := fw.Write(fileContents); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return body, w.FormDataContentType()
}

func TestWizardHandler_Submit_HappyPath(t *testing.T) {
	var capturedOpts types.CreateAgentFromWizardOpts
	repo := &mockWizardRepo{
		createFromWizardFn: func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error) {
			capturedOpts = opts
			return &types.Agent{ID: opts.ID, Name: opts.Name},
				&types.AgentVersion{ID: "v-" + opts.ID, AgentID: opts.ID, Status: "INITIALIZING"},
				nil
		},
	}
	var capturedKey string
	store := &mockStorage{
		putFn: func(ctx context.Context, key string, r io.Reader) error {
			capturedKey = key
			_, _ = io.Copy(io.Discard, r)
			return nil
		},
	}

	h := newTestWizardHandler(t, repo, store)
	body, ct := buildWizardForm(t,
		map[string]string{"name": "My Agent", "scope": "Handle support queries"},
		"flow-map.zip", []byte("zip body"),
	)
	req := httptest.NewRequest(http.MethodPost, "/agents/wizard", body)
	req.Header.Set("Content-Type", ct)
	req.ContentLength = int64(body.Len())
	w := httptest.NewRecorder()
	h.Submit(w, req)

	// 303 redirect goes to the configurator now.
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/agent_versions/") || !strings.HasSuffix(loc, "/configure") {
		t.Errorf("Location = %q, want /agent_versions/<id>/configure", loc)
	}
	if store.calls != 1 {
		t.Errorf("store.Put called %d times, want 1", store.calls)
	}
	if !strings.HasSuffix(capturedKey, ".zip") {
		t.Errorf("Put key = %q, want <uuid>.zip", capturedKey)
	}
	if capturedOpts.DiscoveryFilePath != capturedKey {
		t.Errorf("DiscoveryFilePath = %q, want %q", capturedOpts.DiscoveryFilePath, capturedKey)
	}
	// The zip body in the test is "zip body" — not a real zip — so parse fails.
	// We expect the row to be created with a non-empty FlowMapParseError and a nil FlowMapConfig.
	if capturedOpts.FlowMapParseError == "" {
		t.Error("FlowMapParseError should be set when zip body is invalid")
	}
	if capturedOpts.FlowMapConfig != nil {
		t.Errorf("FlowMapConfig should be nil on parse failure, got %s", string(capturedOpts.FlowMapConfig))
	}
}

func TestWizardHandler_Submit_MissingFile(t *testing.T) {
	repo := &mockWizardRepo{
		createFromWizardFn: func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error) {
			t.Fatal("repo should not be called")
			return nil, nil, nil
		},
	}
	store := &mockStorage{}

	h := newTestWizardHandler(t, repo, store)
	body, ct := buildWizardForm(t,
		map[string]string{"name": "X", "scope": "Y"},
		"", nil,
	)
	req := httptest.NewRequest(http.MethodPost, "/agents/wizard", body)
	req.Header.Set("Content-Type", ct)
	req.ContentLength = int64(body.Len())
	w := httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if store.calls != 0 {
		t.Errorf("store.Put called %d times, want 0", store.calls)
	}
}

func TestWizardHandler_Submit_BadExtension(t *testing.T) {
	repo := &mockWizardRepo{
		createFromWizardFn: func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error) {
			t.Fatal("repo should not be called")
			return nil, nil, nil
		},
	}
	store := &mockStorage{}

	h := newTestWizardHandler(t, repo, store)
	body, ct := buildWizardForm(t,
		map[string]string{"name": "X", "scope": "Y"},
		"flow-map.tar", []byte("not a zip"),
	)
	req := httptest.NewRequest(http.MethodPost, "/agents/wizard", body)
	req.Header.Set("Content-Type", ct)
	req.ContentLength = int64(body.Len())
	w := httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if store.calls != 0 {
		t.Errorf("store.Put called %d times, want 0", store.calls)
	}
}

func TestWizardHandler_Submit_TooLarge(t *testing.T) {
	repo := &mockWizardRepo{
		createFromWizardFn: func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error) {
			t.Fatal("repo should not be called")
			return nil, nil, nil
		},
	}
	store := &mockStorage{}

	var schema types.WizardSchema
	if err := yaml.Unmarshal([]byte(wizardTestSchema), &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	// Tiny cap so a small body trips the pre-check.
	h := NewWizardHandler(repo, &schema, store, 16, testLogger())

	body, ct := buildWizardForm(t,
		map[string]string{"name": "X", "scope": "Y"},
		"flow-map.zip", []byte("more than sixteen bytes of content"),
	)
	req := httptest.NewRequest(http.MethodPost, "/agents/wizard", body)
	req.Header.Set("Content-Type", ct)
	req.ContentLength = int64(body.Len())
	w := httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if store.calls != 0 {
		t.Errorf("store.Put called %d times, want 0", store.calls)
	}
}

func TestWizardHandler_Submit_StorageFails(t *testing.T) {
	repo := &mockWizardRepo{
		createFromWizardFn: func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error) {
			t.Fatal("repo should not be called when storage fails")
			return nil, nil, nil
		},
	}
	store := &mockStorage{
		putFn: func(ctx context.Context, key string, r io.Reader) error {
			_, _ = io.Copy(io.Discard, r)
			return errors.New("disk full")
		},
	}

	h := newTestWizardHandler(t, repo, store)
	body, ct := buildWizardForm(t,
		map[string]string{"name": "X", "scope": "Y"},
		"flow-map.zip", []byte("zip body"),
	)
	req := httptest.NewRequest(http.MethodPost, "/agents/wizard", body)
	req.Header.Set("Content-Type", ct)
	req.ContentLength = int64(body.Len())
	w := httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestWizardHandler_Submit_RepoFailLogsOrphan(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	repo := &mockWizardRepo{
		createFromWizardFn: func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error) {
			return nil, nil, errors.New("db down")
		},
	}
	var savedKey string
	store := &mockStorage{
		putFn: func(ctx context.Context, key string, r io.Reader) error {
			savedKey = key
			_, _ = io.Copy(io.Discard, r)
			return nil
		},
	}

	var schema types.WizardSchema
	if err := yaml.Unmarshal([]byte(wizardTestSchema), &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	h := NewWizardHandler(repo, &schema, store, testMaxUploadBytes, logger)
	body, ct := buildWizardForm(t,
		map[string]string{"name": "X", "scope": "Y"},
		"flow-map.zip", []byte("zip body"),
	)
	req := httptest.NewRequest(http.MethodPost, "/agents/wizard", body)
	req.Header.Set("Content-Type", ct)
	req.ContentLength = int64(body.Len())
	w := httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "orphan") || !strings.Contains(logged, savedKey) {
		t.Errorf("expected orphan log mentioning %q, got:\n%s", savedKey, logged)
	}
}

func TestWizardHandler_Submit_MissingName(t *testing.T) {
	repo := &mockWizardRepo{
		createFromWizardFn: func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error) {
			t.Fatal("repo should not be called when name is empty")
			return nil, nil, nil
		},
	}
	store := &mockStorage{}

	h := newTestWizardHandler(t, repo, store)
	body, ct := buildWizardForm(t,
		map[string]string{"name": "", "scope": "Y"},
		"flow-map.zip", []byte("zip body"),
	)
	req := httptest.NewRequest(http.MethodPost, "/agents/wizard", body)
	req.Header.Set("Content-Type", ct)
	req.ContentLength = int64(body.Len())
	w := httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if store.calls != 0 {
		t.Errorf("store.Put called %d times, want 0", store.calls)
	}
}

func TestWizardHandler_Submit_RealZip_HappyPath(t *testing.T) {
	// Build a real zip from the flowmap testdata.
	zipBytes := buildSampleFlowMapZip(t)

	var capturedOpts types.CreateAgentFromWizardOpts
	repo := &mockWizardRepo{
		createFromWizardFn: func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error) {
			capturedOpts = opts
			return &types.Agent{ID: opts.ID, Name: opts.Name},
				&types.AgentVersion{ID: "v-" + opts.ID, AgentID: opts.ID, Status: "INITIALIZING"},
				nil
		},
	}
	store := &mockStorage{}
	h := newTestWizardHandler(t, repo, store)

	body, ct := buildWizardForm(t,
		map[string]string{"name": "agent", "scope": "support"},
		"flow-map.zip", zipBytes,
	)
	req := httptest.NewRequest(http.MethodPost, "/agents/wizard", body)
	req.Header.Set("Content-Type", ct)
	req.ContentLength = int64(body.Len())
	w := httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if capturedOpts.FlowMapParseError != "" {
		t.Errorf("FlowMapParseError = %q, want empty", capturedOpts.FlowMapParseError)
	}
	if len(capturedOpts.FlowMapConfig) == 0 {
		t.Fatal("FlowMapConfig should be populated")
	}
	// Sanity: decode it.
	var cfg types.FlowMapConfig
	if err := json.Unmarshal(capturedOpts.FlowMapConfig, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Name != "agent" || cfg.Scope != "support" {
		t.Errorf("phase-1 fields not folded into FlowMapConfig: %+v", cfg)
	}
	if len(cfg.Flows) != 1 {
		t.Errorf("Flows = %d, want 1", len(cfg.Flows))
	}
}

// buildSampleFlowMapZip returns a zip of internal/flowmap/testdata/sample-flowmap.
func buildSampleFlowMapZip(t *testing.T) []byte {
	t.Helper()
	root := "../flowmap/testdata/sample-flowmap"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		fw, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}
