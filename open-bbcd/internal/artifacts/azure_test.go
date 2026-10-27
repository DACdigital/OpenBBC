package artifacts

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
)

// azuriteKey is Azurite's published well-known dev account key — not a
// secret. Any valid base64 works for offline tests.
const azuriteKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="

func azureOpts(over map[string]string) map[string]string {
	o := map[string]string{
		"ACCOUNT_NAME": "acct",
		"ACCOUNT_KEY":  azuriteKey,
		"CONTAINER":    "artifacts",
	}
	for k, v := range over {
		if v == "<unset>" {
			delete(o, k)
			continue
		}
		o[k] = v
	}
	return o
}

func TestNewAzureBlob_RequiredOptions(t *testing.T) {
	for _, key := range []string{"ACCOUNT_NAME", "ACCOUNT_KEY", "CONTAINER"} {
		t.Run(key, func(t *testing.T) {
			_, err := NewAzureBlob(azureOpts(map[string]string{key: "<unset>"}), time.Minute)
			if err == nil {
				t.Fatalf("want error for missing %s", key)
			}
			if !strings.HasPrefix(err.Error(), "azure_blob:") || !strings.Contains(err.Error(), key) {
				t.Fatalf("error %q should be azure_blob:-prefixed and name %s", err, key)
			}
		})
	}
}

func TestNewAzureBlob_KeyNotBase64(t *testing.T) {
	bad := "not base64 !!secret-material"
	_, err := NewAzureBlob(azureOpts(map[string]string{"ACCOUNT_KEY": bad}), time.Minute)
	if err == nil {
		t.Fatal("want error for non-base64 key")
	}
	if !strings.Contains(err.Error(), "ACCOUNT_KEY") {
		t.Fatalf("error %q should name ACCOUNT_KEY", err)
	}
	if strings.Contains(err.Error(), "secret-material") {
		t.Fatalf("error leaks the key value: %q", err)
	}
}

func TestNewAzureBlob_TTL(t *testing.T) {
	if _, err := NewAzureBlob(azureOpts(nil), 0); err == nil {
		t.Fatal("want error for zero TTL")
	}
}

func TestNewAzureBlob_Endpoint(t *testing.T) {
	cases := []struct {
		name, endpoint, wantURL string
		wantErr                 bool
	}{
		{"default", "", "https://acct.blob.core.windows.net/artifacts", false},
		{"https custom", "https://acct.blob.core.chinacloudapi.cn", "https://acct.blob.core.chinacloudapi.cn/artifacts", false},
		{"azurite loopback", "http://127.0.0.1:10000/devstoreaccount1/", "http://127.0.0.1:10000/devstoreaccount1/artifacts", false},
		{"azurite compose service", "http://azurite:10000/devstoreaccount1", "http://azurite:10000/devstoreaccount1/artifacts", false},
		{"localhost", "http://localhost:10000/devstoreaccount1/", "http://localhost:10000/devstoreaccount1/artifacts", false},
		{"plain http public host", "http://example.com/", "", true},
		{"bad scheme", "ftp://x", "", true},
		{"not absolute", "acct.blob.core.windows.net", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := newAzureBlob(azureOpts(map[string]string{"ENDPOINT": tc.endpoint}), time.Minute, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for ENDPOINT=%q", tc.endpoint)
				}
				if !strings.Contains(err.Error(), "ENDPOINT") {
					t.Fatalf("error %q should name ENDPOINT", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("newAzureBlob: %v", err)
			}
			if got := s.container.URL(); got != tc.wantURL {
				t.Fatalf("container URL = %q, want %q", got, tc.wantURL)
			}
		})
	}
}

func TestAzureBlob_KindAndDelivery(t *testing.T) {
	s, err := NewAzureBlob(azureOpts(nil), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind() != "azure_blob" {
		t.Fatalf("Kind = %q", s.Kind())
	}
	if s.PreferredDelivery() != DeliverySignedURL {
		t.Fatalf("PreferredDelivery = %v, want DeliverySignedURL", s.PreferredDelivery())
	}
}

func TestAzureBlob_Sign(t *testing.T) {
	s, err := newAzureBlob(azureOpts(nil), 5*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	raw, err := s.Sign(t.Context(), "sha256/abc", 2*time.Minute, SignOptions{
		ContentType:        "image/png",
		ContentDisposition: `inline; filename="x.png"`,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	if u.Scheme != "https" || u.Host != "acct.blob.core.windows.net" || u.Path != "/artifacts/sha256/abc" {
		t.Fatalf("signed URL = %q", raw)
	}
	q := u.Query()
	if q.Get("sp") != "r" {
		t.Errorf("sp = %q, want r", q.Get("sp"))
	}
	if q.Get("spr") != "https" {
		t.Errorf("spr = %q, want https", q.Get("spr"))
	}
	if q.Get("sig") == "" {
		t.Error("missing sig")
	}
	if q.Get("rsct") != "image/png" || q.Get("rscd") != `inline; filename="x.png"` {
		t.Errorf("rsct=%q rscd=%q", q.Get("rsct"), q.Get("rscd"))
	}
	st, _ := time.Parse(time.RFC3339, q.Get("st"))
	se, _ := time.Parse(time.RFC3339, q.Get("se"))
	if d := before.Sub(st); d < 4*time.Minute || d > 6*time.Minute {
		t.Errorf("st = %v, want ~5m before %v", st, before)
	}
	if d := se.Sub(before); d < 110*time.Second || d > 130*time.Second {
		t.Errorf("se = %v, want ~2m after %v", se, before)
	}
}

func TestAzureBlob_SignDefaultsAndNoOverrides(t *testing.T) {
	s, err := newAzureBlob(azureOpts(map[string]string{"ENDPOINT": "http://127.0.0.1:10000/devstoreaccount1"}), 3*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	raw, err := s.Sign(t.Context(), "sha256/abc", 0, SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Has("rsct") || q.Has("rscd") {
		t.Errorf("empty SignOptions must not add overrides: %q", raw)
	}
	if q.Get("spr") != "https,http" {
		t.Errorf("spr = %q, want https,http for an http endpoint", q.Get("spr"))
	}
	se, _ := time.Parse(time.RFC3339, q.Get("se"))
	if d := se.Sub(before); d < 170*time.Second || d > 190*time.Second {
		t.Errorf("ttl<=0 should use constructor TTL (3m); se=%v", se)
	}
}

// fakeAzure answers every request with the status/code/headers the test
// configured for that HTTP method. Retries are disabled so a 5xx does not
// stall the test in backoff.
type fakeAzure struct {
	byMethod map[string]fakeResp
	calls    []string
}

type fakeResp struct {
	status  int
	code    string
	headers map[string]string
}

func (f *fakeAzure) Do(req *http.Request) (*http.Response, error) {
	f.calls = append(f.calls, req.Method+" "+req.URL.Path)
	r, ok := f.byMethod[req.Method]
	if !ok {
		r = fakeResp{status: http.StatusCreated}
	}
	h := http.Header{}
	if r.code != "" {
		h.Set("x-ms-error-code", r.code)
	}
	for k, v := range r.headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: r.status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func fakeAzureStore(t *testing.T, f *fakeAzure) *azureStore {
	t.Helper()
	s, err := newAzureBlob(azureOpts(nil), time.Minute, &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: f,
			Retry:     policy.RetryOptions{MaxRetries: -1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAzureBlob_StatNotFound(t *testing.T) {
	for _, code := range []string{"BlobNotFound", "ContainerNotFound"} {
		t.Run(code, func(t *testing.T) {
			s := fakeAzureStore(t, &fakeAzure{byMethod: map[string]fakeResp{
				http.MethodHead: {status: 404, code: code},
			}})
			st, err := s.Stat(t.Context(), "sha256/abc")
			if err != nil || st.Exists {
				t.Fatalf("Stat = %+v, %v; want Exists=false, nil", st, err)
			}
		})
	}
}

func TestAzureBlob_StatExists(t *testing.T) {
	s := fakeAzureStore(t, &fakeAzure{byMethod: map[string]fakeResp{
		http.MethodHead: {status: 200, headers: map[string]string{"Content-Type": "image/png", "Content-Length": "42"}},
	}})
	st, err := s.Stat(t.Context(), "sha256/abc")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.MIME != "image/png" || st.SizeBytes != 42 || st.Sha256 != "abc" {
		t.Fatalf("Stat = %+v", st)
	}
}

func TestAzureBlob_StatServerErrorIsNotNotFound(t *testing.T) {
	s := fakeAzureStore(t, &fakeAzure{byMethod: map[string]fakeResp{
		http.MethodHead: {status: 500, code: "InternalError"},
	}})
	if _, err := s.Stat(t.Context(), "sha256/abc"); err == nil {
		t.Fatal("want error on 500")
	}
}

func TestAzureBlob_GetMissingIsErrBlobMissing(t *testing.T) {
	s := fakeAzureStore(t, &fakeAzure{byMethod: map[string]fakeResp{
		http.MethodGet: {status: 404, code: "BlobNotFound"},
	}})
	_, err := s.Get(t.Context(), "sha256/abc")
	if !errors.Is(err, ErrBlobMissing) {
		t.Fatalf("Get err = %v, want ErrBlobMissing", err)
	}
}

func TestAzureBlob_DeleteMissingIsNil(t *testing.T) {
	s := fakeAzureStore(t, &fakeAzure{byMethod: map[string]fakeResp{
		http.MethodDelete: {status: 404, code: "BlobNotFound"},
	}})
	if err := s.Delete(t.Context(), "sha256/abc"); err != nil {
		t.Fatalf("Delete of missing blob = %v, want nil", err)
	}
}

func TestAzureBlob_DeleteOtherErrorSurfaces(t *testing.T) {
	s := fakeAzureStore(t, &fakeAzure{byMethod: map[string]fakeResp{
		http.MethodDelete: {status: 403, code: "AuthenticationFailed"},
	}})
	if err := s.Delete(t.Context(), "sha256/abc"); err == nil {
		t.Fatal("want error on 403")
	}
}

func TestAzureBlob_ProbeHints(t *testing.T) {
	cases := []struct {
		status   int
		code     string
		wantHint string
	}{
		{403, "AuthenticationFailed", "wrong or rotated ACCOUNT_KEY"},
		{403, "AuthorizationFailure", "firewall"},
		{403, "KeyBasedAuthenticationNotPermitted", "allowSharedKeyAccess=false"},
		{404, "ContainerNotFound", "CONTAINER must exist"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			s := fakeAzureStore(t, &fakeAzure{byMethod: map[string]fakeResp{
				http.MethodPut: {status: tc.status, code: tc.code},
			}})
			err := s.Probe(t.Context())
			if !errors.Is(err, ErrProbeFailed) {
				t.Fatalf("Probe err = %v, want ErrProbeFailed", err)
			}
			for _, want := range []string{tc.code, tc.wantHint, `"acct"`, `"artifacts"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Probe err %q missing %q", err, want)
				}
			}
		})
	}
}

func TestAzureBlob_ProbeHappyPath(t *testing.T) {
	// PUT → 201; first HEAD (probe blob) → 200; DELETE → 202; second
	// HEAD (missing key) → 404 BlobNotFound.
	heads := 0
	s, err := newAzureBlob(azureOpts(nil), time.Minute, &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				status, h := http.StatusCreated, http.Header{}
				switch req.Method {
				case http.MethodHead:
					heads++
					status = http.StatusOK
					if heads > 1 {
						status = http.StatusNotFound
						h.Set("x-ms-error-code", "BlobNotFound")
					}
				case http.MethodDelete:
					status = http.StatusAccepted
				}
				return &http.Response{StatusCode: status, Header: h, Body: http.NoBody, Request: req}, nil
			}),
			Retry: policy.RetryOptions{MaxRetries: -1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Probe(t.Context()); err != nil {
		t.Fatalf("Probe = %v", err)
	}
	if heads != 2 {
		t.Fatalf("HEAD calls = %d, want 2", heads)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (fn transportFunc) Do(req *http.Request) (*http.Response, error) { return fn(req) }
