package artifacts

import (
	"net/url"
	"strings"
	"testing"
	"time"
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
