package chat

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// blockTypes returns the "type" of each block in a persisted content array.
func blockTypes(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("content is not a block array: %v (%s)", err, raw)
	}
	out := make([]string, len(blocks))
	for i, b := range blocks {
		out[i] = b.Type
	}
	return out
}

func TestCodec_ArtifactRefRoundTrip(t *testing.T) {
	in := []llm.Block{
		llm.TextBlock{Text: "summarise"},
		llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/abc", MIME: "application/pdf", SizeBytes: 42, Sha256: "abc", Filename: "q3.pdf"},
		llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/def", MIME: "image/png", SizeBytes: 7, Sha256: "def"},
	}
	raw, err := blocksToJSON(in)
	if err != nil {
		t.Fatalf("blocksToJSON: %v", err)
	}
	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(raw, &rawBlocks); err != nil {
		t.Fatal(err)
	}
	got := parseBlocks(rawBlocks)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", got, in)
	}
}

func TestCodec_ArtifactRefJSONShape(t *testing.T) {
	raw, err := blocksToJSON([]llm.Block{
		llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/def", MIME: "image/png", SizeBytes: 7, Sha256: "def"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	b := blocks[0]
	for _, k := range []string{"type", "store_id", "uri", "mime", "size_bytes", "sha256"} {
		if _, ok := b[k]; !ok {
			t.Errorf("missing key %q in %s", k, raw)
		}
	}
	if b["type"] != "artifact_ref" {
		t.Errorf("type = %v, want artifact_ref", b["type"])
	}
	if _, ok := b["filename"]; ok {
		t.Errorf("empty filename must be omitted: %s", raw)
	}
}

func TestCodec_InlineMediaIsNeverPersisted(t *testing.T) {
	_, err := blocksToJSON([]llm.Block{llm.InlineMediaBlock{MIME: "image/png", Data: []byte{1}}})
	if !errors.Is(err, ErrInlineMediaNotPersistable) {
		t.Fatalf("err = %v, want ErrInlineMediaNotPersistable", err)
	}
}
