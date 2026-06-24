package tools

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestParseBackendHeaderRouting_Basic(t *testing.T) {
	payload, _ := json.Marshal(map[string]map[string]any{
		"slack":  {"_all": false, "Authorization": "Bearer xoxb"},
		"github": {"_all": true},
	})
	enc := base64.URLEncoding.EncodeToString(payload)
	routing, err := ParseBackendHeaderRouting(enc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	slack, ok := routing.LookupByBackendName("Slack") // case-insensitive
	if !ok {
		t.Fatal("slack not found")
	}
	if slack.All {
		t.Fatal("slack._all should be false")
	}
	if slack.Headers["Authorization"] != "Bearer xoxb" {
		t.Fatalf("slack auth: %v", slack.Headers)
	}
	gh, ok := routing.LookupByBackendName("github")
	if !ok || !gh.All {
		t.Fatalf("github should be all-forward: %+v", gh)
	}
}

func TestParseBackendHeaderRouting_EmptyHeader(t *testing.T) {
	r, err := ParseBackendHeaderRouting("")
	if err != nil {
		t.Fatalf("empty header should not error, got %v", err)
	}
	if _, ok := r.LookupByBackendName("anything"); ok {
		t.Fatal("empty header should not match any backend")
	}
}

func TestParseBackendHeaderRouting_MalformedBase64(t *testing.T) {
	_, err := ParseBackendHeaderRouting("not!base64!!!")
	if err == nil {
		t.Fatal("expected error on malformed base64")
	}
}

func TestParseBackendHeaderRouting_MalformedJSON(t *testing.T) {
	enc := base64.URLEncoding.EncodeToString([]byte("not json"))
	_, err := ParseBackendHeaderRouting(enc)
	if err == nil {
		t.Fatal("expected error on malformed json")
	}
}

func TestParseBackendHeaderRouting_StdEncodingFallback(t *testing.T) {
	// Some FEs use standard (padded) base64 instead of URL-safe base64.
	// Parser must accept both.
	payload, _ := json.Marshal(map[string]map[string]any{
		"myapi": {"Authorization": "Bearer std"},
	})
	enc := base64.StdEncoding.EncodeToString(payload)
	routing, err := ParseBackendHeaderRouting(enc)
	if err != nil {
		t.Fatalf("std-encoding fallback failed: %v", err)
	}
	block, ok := routing.LookupByBackendName("myapi")
	if !ok {
		t.Fatal("myapi not found")
	}
	if block.Headers["Authorization"] != "Bearer std" {
		t.Fatalf("got: %v", block.Headers)
	}
}

func TestParseBackendHeaderRouting_CaseInsensitiveKeys(t *testing.T) {
	payload, _ := json.Marshal(map[string]map[string]any{
		"MyBackend": {"X-Custom": "value"},
	})
	enc := base64.URLEncoding.EncodeToString(payload)
	routing, err := ParseBackendHeaderRouting(enc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Lookup must work with any casing.
	for _, lookup := range []string{"mybackend", "MYBACKEND", "MyBackend"} {
		if _, ok := routing.LookupByBackendName(lookup); !ok {
			t.Fatalf("case-insensitive lookup failed for %q", lookup)
		}
	}
}
