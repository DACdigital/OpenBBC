package eval

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func mcpStore(mock bool) *fakeStore {
	return &fakeStore{
		eval: &types.Eval{
			ID: "e-mcp", AgentVersionID: "av-1", DatasetVersionID: "dv-1", MockMCPTools: mock,
			MCPHeaderOverrides: map[string]map[string]string{"kb": {"X-Identity": "override", "X-Extra": "e"}},
		},
		bundle: []byte(`{"main_prompt":"hi","tools":[]}`),
		mcpBackends: []MCPBackendRef{{
			ID: "b-kb", Name: "kb",
			Config: tools.MCPBackendCfg{
				URL: "http://kb/mcp", Transport: "streamable_http",
				DefaultHeaders: map[string]string{"X-Identity": "default", "X-Default": "d"},
			},
		}},
	}
}

type recordingDiscoverer struct {
	gotOverrides map[string]string
	gotDeadline  time.Duration
	tools        []InputMCPTool
	err          error
}

func (r *recordingDiscoverer) discover(ctx context.Context, b MCPBackendRef, ovr map[string]string) ([]InputMCPTool, error) {
	r.gotOverrides = ovr
	if dl, ok := ctx.Deadline(); ok {
		r.gotDeadline = time.Until(dl)
	}
	return r.tools, r.err
}

var kbTools = []InputMCPTool{{
	Name: "kb__search", Description: "Search",
	InputSchema:  map[string]any{"type": "object"},
	OutputSchema: map[string]any{"type": "object"},
}}

func TestExportBuild_MCPBackends_RealMode_MergesHeaders(t *testing.T) {
	d := &recordingDiscoverer{tools: kbTools}
	got, err := BuildWith(context.Background(), mcpStore(false), "e-mcp", Options{Discover: d.discover})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	kb, ok := got.MCPBackends["kb"]
	if !ok {
		t.Fatalf("mcp_backends = %+v", got.MCPBackends)
	}
	if kb.URL != "http://kb/mcp" || kb.Transport != "streamable_http" {
		t.Errorf("url/transport = %q/%q", kb.URL, kb.Transport)
	}
	want := map[string]string{"X-Identity": "override", "X-Default": "d", "X-Extra": "e"}
	if len(kb.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", kb.Headers, want)
	}
	for k, v := range want {
		if kb.Headers[k] != v {
			t.Errorf("headers[%s] = %q, want %q", k, kb.Headers[k], v)
		}
	}
	if d.gotOverrides["X-Identity"] != "override" {
		t.Errorf("discoverer overrides = %v", d.gotOverrides)
	}
	if len(kb.Tools) != 1 || kb.Tools[0].Name != "kb__search" {
		t.Errorf("tools = %+v", kb.Tools)
	}
}

// Mock mode still discovers (names + schemas) but carries no credentials or
// URL — aikdm never connects in mock mode.
func TestExportBuild_MCPBackends_MockMode_OmitsURLAndHeaders(t *testing.T) {
	d := &recordingDiscoverer{tools: kbTools}
	got, err := BuildWith(context.Background(), mcpStore(true), "e-mcp", Options{Discover: d.discover})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	kb := got.MCPBackends["kb"]
	if kb.URL != "" || kb.Headers != nil {
		t.Errorf("mock mode leaked url/headers: %+v", kb)
	}
	if len(kb.Tools) != 1 {
		t.Errorf("tools = %+v", kb.Tools)
	}
	if d.gotOverrides["X-Identity"] != "override" {
		t.Errorf("discovery in mock mode must still send overrides, got %v", d.gotOverrides)
	}
	var y bytes.Buffer
	if err := WriteYAML(&y, got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(y.String(), "override") || strings.Contains(y.String(), "url:") {
		t.Errorf("mock-mode YAML carries credentials or url:\n%s", y.String())
	}
	if !strings.Contains(y.String(), "mcp_backends:") || !strings.Contains(y.String(), "output_schema:") {
		t.Errorf("YAML missing mcp_backends/output_schema:\n%s", y.String())
	}
}

func TestExportBuild_MCPBackends_NoneAttached_NoKey(t *testing.T) {
	called := false
	s := mcpStore(false)
	s.mcpBackends = nil
	got, err := BuildWith(context.Background(), s, "e-mcp", Options{
		Discover: func(context.Context, MCPBackendRef, map[string]string) ([]InputMCPTool, error) {
			called = true
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var y bytes.Buffer
	_ = WriteYAML(&y, got)
	if called || got.MCPBackends != nil || strings.Contains(y.String(), "mcp_backends") {
		t.Errorf("called=%v mcp_backends=%v yaml:\n%s", called, got.MCPBackends, y.String())
	}
}

func TestExportBuild_MCPBackends_DiscoveryFailure(t *testing.T) {
	d := &recordingDiscoverer{err: errors.New("connection refused")}
	_, err := BuildWith(context.Background(), mcpStore(false), "e-mcp", Options{Discover: d.discover})
	var de *MCPDiscoveryError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *MCPDiscoveryError", err)
	}
	if de.Backend != "kb" || de.Details != "connection refused" {
		t.Errorf("err = %+v", de)
	}
	if de.Error() != "mcp_discovery_failed: kb: connection refused" {
		t.Errorf("Error() = %q", de.Error())
	}
}

func TestExportBuild_MCPBackends_TimeoutBounded(t *testing.T) {
	d := &recordingDiscoverer{tools: kbTools}
	if _, err := BuildWith(context.Background(), mcpStore(false), "e-mcp", Options{Discover: d.discover, Timeout: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if d.gotDeadline <= 0 || d.gotDeadline > 2*time.Second {
		t.Errorf("deadline = %v, want (0, 2s]", d.gotDeadline)
	}
	d.gotDeadline = 0
	if _, err := BuildWith(context.Background(), mcpStore(false), "e-mcp", Options{Discover: d.discover}); err != nil {
		t.Fatal(err)
	}
	if d.gotDeadline <= 14*time.Second || d.gotDeadline > DefaultMCPDiscoveryTimeout {
		t.Errorf("default deadline = %v, want ~15s", d.gotDeadline)
	}
}

// A default and an override that differ only in header-name case are one
// header (HTTP names are case-insensitive; the runtime transport uses
// Header.Set). The export must carry exactly one entry with the override
// value, and discovery must send only the override value.
func TestExportBuild_MCPBackends_HeaderMergeCaseInsensitive(t *testing.T) {
	upstream := mcpParityServer(t)
	target, _ := url.Parse(upstream.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var mu sync.Mutex
	var seen [][]string
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Values("X-User"))
		mu.Unlock()
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)

	s := &fakeStore{
		eval: &types.Eval{
			ID: "e-mcp", AgentVersionID: "av-1", DatasetVersionID: "dv-1",
			MCPHeaderOverrides: map[string]map[string]string{"kb": {"x-user": "test"}},
		},
		bundle: []byte(`{"main_prompt":"hi","tools":[]}`),
		mcpBackends: []MCPBackendRef{{
			ID: "b-kb", Name: "kb",
			Config: tools.MCPBackendCfg{
				URL: front.URL, Transport: "streamable_http",
				DefaultHeaders: map[string]string{"X-User": "svc"},
			},
		}},
	}
	got, err := BuildWith(context.Background(), s, "e-mcp", Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	hdrs := got.MCPBackends["kb"].Headers
	if len(hdrs) != 1 {
		t.Fatalf("headers = %v, want exactly one X-User entry", hdrs)
	}
	for _, v := range hdrs {
		if v != "test" {
			t.Errorf("headers = %v, want the override value", hdrs)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("discovery sent no requests")
	}
	for i, vals := range seen {
		if len(vals) != 1 || vals[0] != "test" {
			t.Errorf("request %d X-User = %v, want [test]", i, vals)
		}
	}
}
