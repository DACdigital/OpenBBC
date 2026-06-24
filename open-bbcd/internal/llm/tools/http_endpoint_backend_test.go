package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

func TestHTTPBackend_Tools_SkipsUnmappedEndpoints(t *testing.T) {
	be := newHTTPTest(t, "https://api.example.com",
		[]HTTPEndpointDef{
			{ID: "orders.create", Name: "orders_create", Method: "POST", Path: "/api/orders"},
		},
		map[string]string{"orders.create": "backend-id-1"},
		"backend-id-1",
	)
	defs, err := be.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "orders_create" {
		t.Fatalf("want 1 tool orders_create, got %+v", defs)
	}
}

func TestHTTPBackend_Call_SubstitutesPathParams_AndForwardsHeaders(t *testing.T) {
	var gotURL string
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotHeader = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	ep := HTTPEndpointDef{
		ID: "orders.show", Name: "orders_show", Method: "GET", Path: "/api/orders/{id}",
		PathParams: []ParamSpec{{Name: "id", Type: "string", Required: true}},
	}
	be := newHTTPTest(t, srv.URL, []HTTPEndpointDef{ep},
		map[string]string{"orders.show": "b1"}, "b1")
	be.cfg.DefaultHeaders = map[string]string{"Authorization": "Bearer default"}

	res, err := be.Call(context.Background(), "orders_show", json.RawMessage(`{"id":"42"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError {
		t.Fatalf("got error: %s", string(res.Output))
	}
	if gotURL != "/api/orders/42" {
		t.Fatalf("URL %s", gotURL)
	}
	if gotHeader != "Bearer default" {
		t.Fatalf("Auth %s", gotHeader)
	}
}

func TestHTTPBackend_Call_LiveFEHeadersOverrideDefaults(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	ep := HTTPEndpointDef{ID: "ping", Name: "ping", Method: "GET", Path: "/ping"}
	be := newHTTPTest(t, srv.URL, []HTTPEndpointDef{ep},
		map[string]string{"ping": "b1"}, "b1")
	be.cfg.DefaultHeaders = map[string]string{"Authorization": "Bearer default"}
	be.cfg.ForwardedHeaders = []string{"Authorization"}

	ctx := WithForwardedHeaders(context.Background(), http.Header{"Authorization": {"Bearer LIVE"}})
	_, _ = be.Call(ctx, "ping", json.RawMessage(`{}`))
	if gotAuth != "Bearer LIVE" {
		t.Fatalf("want LIVE, got %s", gotAuth)
	}
}

func TestHTTPBackend_Call_Returns4xxAsIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"detail":"not found"}`)
	}))
	defer srv.Close()
	ep := HTTPEndpointDef{ID: "x", Name: "x", Method: "GET", Path: "/x"}
	be := newHTTPTest(t, srv.URL, []HTTPEndpointDef{ep},
		map[string]string{"x": "b1"}, "b1")
	res, _ := be.Call(context.Background(), "x", json.RawMessage(`{}`))
	if !res.IsError {
		t.Fatal("expected IsError")
	}
}

// newHTTPTest is a test helper that constructs an HTTPEndpointBackend
// with a literal endpoint list + endpoint→backend mapping.
func newHTTPTest(t *testing.T, baseURL string, endpoints []HTTPEndpointDef, mapping map[string]string, selfID string) *HTTPEndpointBackend {
	return &HTTPEndpointBackend{
		name:      "test",
		id:        selfID,
		cfg:       HTTPBackendCfg{BaseURL: baseURL},
		endpoints: endpoints,
		mapping:   mapping,
		client:    &http.Client{},
	}
}

// Compile-time check: the package satisfies the runtime interface.
var _ Backend = (*HTTPEndpointBackend)(nil)
var _ llm.ToolDef = llm.ToolDef{}
