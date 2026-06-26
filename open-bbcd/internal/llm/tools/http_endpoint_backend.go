package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// HTTPEndpointDef is a single endpoint, derived from bundle.tools[]
// joined with the version's wiring. Lives in this package to keep the
// runtime interface decoupled from types/ versioning churn.
type HTTPEndpointDef struct {
	ID          string
	Name        string // sanitized, what the LLM sees
	Description string
	Method      string
	Path        string
	PathParams  []ParamSpec
	QueryParams []ParamSpec
	BodyShape   any
}

// HTTPBackendCfg mirrors types.HTTPBackendConfig at the runtime boundary.
// Field tags MUST match the persisted JSONB shape (base_url, default_headers,
// forwarded_headers) so json.Unmarshal in Builder.Build populates them from
// the tool_backends.config column.
type HTTPBackendCfg struct {
	BaseURL          string            `json:"base_url"`
	DefaultHeaders   map[string]string `json:"default_headers,omitempty"`
	ForwardedHeaders []string          `json:"forwarded_headers,omitempty"`
}

type HTTPEndpointBackend struct {
	name      string
	id        string
	cfg       HTTPBackendCfg
	endpoints []HTTPEndpointDef
	mapping   map[string]string // endpoint_id → backend_id
	client    *http.Client
}

func NewHTTPEndpointBackend(name, id string, cfg HTTPBackendCfg, endpoints []HTTPEndpointDef, mapping map[string]string) *HTTPEndpointBackend {
	return &HTTPEndpointBackend{
		name: name, id: id, cfg: cfg,
		endpoints: endpoints, mapping: mapping,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (b *HTTPEndpointBackend) Name() string { return b.name }

func (b *HTTPEndpointBackend) Tools(ctx context.Context) ([]llm.ToolDef, error) {
	out := []llm.ToolDef{}
	for _, ep := range b.endpoints {
		if b.mapping[ep.ID] != b.id {
			continue
		}
		schema, err := BuildEndpointSchema(EndpointSchemaInput{
			PathParams: ep.PathParams, QueryParams: ep.QueryParams, BodyShape: ep.BodyShape,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, llm.ToolDef{
			Name: ep.Name, Description: ep.Description, InputSchema: schema,
		})
	}
	return out, nil
}

func (b *HTTPEndpointBackend) Call(ctx context.Context, name string, input json.RawMessage) (Result, error) {
	ep := b.findByName(name)
	if ep == nil {
		return errResult(fmt.Sprintf("unknown tool %s", name)), nil
	}
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return errResult(fmt.Sprintf("invalid input json: %s", err)), nil
		}
	}
	urlStr, body, err := b.buildRequest(*ep, args)
	if err != nil {
		return errResult(err.Error()), nil
	}
	req, err := http.NewRequestWithContext(ctx, ep.Method, urlStr, body)
	if err != nil {
		return errResult(err.Error()), nil
	}
	b.applyHeaders(ctx, req)

	resp, err := b.client.Do(req)
	if err != nil {
		return errResult(err.Error()), nil
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	out, _ := json.Marshal(map[string]any{
		"status": resp.StatusCode,
		"body":   string(rb),
	})
	return Result{Output: out, IsError: resp.StatusCode >= 400}, nil
}

func (b *HTTPEndpointBackend) findByName(name string) *HTTPEndpointDef {
	for i := range b.endpoints {
		if b.endpoints[i].Name == name {
			return &b.endpoints[i]
		}
	}
	return nil
}

func (b *HTTPEndpointBackend) buildRequest(ep HTTPEndpointDef, args map[string]any) (string, io.Reader, error) {
	consumed := map[string]bool{}
	path := ep.Path
	for _, p := range ep.PathParams {
		v, ok := args[p.Name]
		if !ok {
			if p.Required {
				return "", nil, fmt.Errorf("missing required path param %s", p.Name)
			}
			consumed[p.Name] = true
			continue
		}
		path = strings.ReplaceAll(path, "{"+p.Name+"}", fmt.Sprint(v))
		consumed[p.Name] = true
	}
	u, err := url.Parse(b.cfg.BaseURL)
	if err != nil {
		return "", nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	q := u.Query()
	for _, p := range ep.QueryParams {
		if v, ok := args[p.Name]; ok {
			q.Set(p.Name, fmt.Sprint(v))
			consumed[p.Name] = true
		}
	}
	u.RawQuery = q.Encode()

	var body io.Reader
	if ep.Method == http.MethodPost || ep.Method == http.MethodPut || ep.Method == http.MethodPatch {
		bodyMap := map[string]any{}
		for k, v := range args {
			if !consumed[k] {
				bodyMap[k] = v
			}
		}
		if len(bodyMap) > 0 {
			buf, _ := json.Marshal(bodyMap)
			body = bytes.NewReader(buf)
		}
	}
	return u.String(), body, nil
}

// hopByHopHeaders are headers that must not be forwarded by a proxy/relay
// (RFC 7230 section 6.1) plus a few that Go's http.Client manages itself.
// We forward everything else from the live FE request to the target backend.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"host":                true, // set by http.NewRequest from URL
	"content-length":      true, // set by http.Client from body
}

func (b *HTTPEndpointBackend) applyHeaders(ctx context.Context, req *http.Request) {
	// 1. Default headers (lowest).
	for k, v := range b.cfg.DefaultHeaders {
		req.Header.Set(k, v)
	}

	// 2/3. Routing-envelope-controlled FE headers + explicit map.
	if routing, ok := backendHeaderRoutingFromContext(ctx); ok {
		if block, found := routing.LookupByBackendName(b.name); found {
			// 2. _all flag: forward live FE headers minus hop-by-hop minus
			//    the routing envelope header itself.
			if block.All {
				if live := forwardedHeadersFromContext(ctx); live != nil {
					for name, vals := range live {
						lc := strings.ToLower(name)
						if hopByHopHeaders[lc] || lc == strings.ToLower(RoutingEnvelopeHeader) {
							continue
						}
						if len(vals) > 0 {
							req.Header.Set(name, vals[0])
						}
					}
				}
			}
			// 3. Explicit headers (overwrite live-FE on conflict).
			for k, v := range block.Headers {
				req.Header.Set(k, v)
			}
		}
		// backend not in envelope → no FE headers forwarded
	}

	// 4. Session overrides (highest).
	if sess := sessionHeaderOverridesFromContext(ctx); sess != nil {
		if mine, ok := sess[b.id]; ok {
			for k, v := range mine {
				req.Header.Set(k, v)
			}
		}
	}

	if req.Body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
}

func errResult(msg string) Result {
	out, _ := json.Marshal(map[string]string{"error": msg})
	return Result{Output: out, IsError: true}
}
