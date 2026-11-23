package bifrost

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/bifrost/bifrosttest"
)

var update = flag.Bool("update", false, "rewrite golden files")

func wireRequest() llm.Request {
	return llm.Request{
		System:      "sys",
		MaxTokens:   256,
		Temperature: 0.5,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.TextBlock{Text: "hi"}}}},
		Tools:       []llm.ToolDef{{Name: "lookup", Description: "Look up", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}},
	}
}

// stableHeaders drops headers that vary by run (the fake's random port).
func stableHeaders(h http.Header) map[string][]string {
	out := map[string][]string{}
	for k, v := range h {
		if k != "Host" {
			out[k] = v
		}
	}
	return out
}

func compareGolden(t *testing.T, path string, got any) {
	t.Helper()
	js, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	js = append(js, '\n')
	if *update {
		if err := os.WriteFile(path, js, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if string(want) != string(js) {
		t.Fatalf("request differs from %s:\n got: %s\nwant: %s", path, js, want)
	}
}

// testdata/unchanged_request.json was recorded before llm-request-extras
// existed: with no matching rule the adapter must still send exactly this.
func TestWire_NoRuleRequestUnchanged(t *testing.T) {
	fake := bifrosttest.New(t)
	fake.Route("sys", bifrosttest.Text("hello"))
	if _, err := collect(t, newTestLLM(t, fake.URL(), "sk-test"), wireRequest()); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests("sys")
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	compareGolden(t, "testdata/unchanged_request.json", map[string]any{"headers": stableHeaders(reqs[0].Header), "body": reqs[0].Body})
}
