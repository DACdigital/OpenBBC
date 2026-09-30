package chat

import (
	"context"
	"testing"
)

// Inline media with empty data carries no bytes: it must become the
// "unknown"-size note and never upload a zero-byte artifact.
func TestNormalise_EmptyInlineMediaIsNotUploaded(t *testing.T) {
	cases := map[string]struct{ payload, note string }{
		"image empty data":   {`{"content":[{"type":"image","data":"","mimeType":"image/png"}]}`, "[artifact unavailable: image/png, unknown]"},
		"image missing data": {`{"content":[{"type":"image","mimeType":"image/png"}]}`, "[artifact unavailable: image/png, unknown]"},
		"audio empty data":   {`{"content":[{"type":"audio","data":"","mimeType":"audio/wav"}]}`, "[artifact unavailable: audio/wav, unknown]"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			up := &stubUploader{}
			res := normaliseToolResult(context.Background(), []byte(tc.payload), up, testLogger(), "Skill")
			if up.uploads != 0 || len(res.Refs) != 0 {
				t.Fatalf("empty media must not be uploaded: uploads=%d refs=%d", up.uploads, len(res.Refs))
			}
			c := remainingContent(t, res.Output)
			if len(c) != 1 || c[0]["type"] != "text" || c[0]["text"] != tc.note {
				t.Fatalf("remainder = %v, want note %q", c, tc.note)
			}
		})
	}
}

// A resource whose blob is empty carries no inline bytes either: it is
// not uploaded.
func TestNormalise_EmptyResourceBlobIsNotUploaded(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"resource","resource":{"uri":"file:///x","mimeType":"application/pdf","blob":""}}]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if up.uploads != 0 || len(res.Refs) != 0 {
		t.Fatalf("empty resource blob must not be uploaded: uploads=%d refs=%d", up.uploads, len(res.Refs))
	}
}
