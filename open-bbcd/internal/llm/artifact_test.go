package llm

import (
	"strings"
	"testing"
)

func TestTextSurrogate_WithFilename(t *testing.T) {
	ref := ArtifactRefBlock{
		StoreID:   "MAIN",
		URI:       "sha256/9c1f2b7d",
		MIME:      "application/pdf",
		SizeBytes: 245678,
		Sha256:    "9c1f2b7d",
		Filename:  "Q3-report.pdf",
	}
	got := TextSurrogate(ref)
	if !strings.Contains(got.Text, "Q3-report.pdf") {
		t.Errorf("surrogate should include filename, got: %q", got.Text)
	}
	if !strings.Contains(got.Text, "application/pdf") {
		t.Errorf("surrogate should include mime, got: %q", got.Text)
	}
	if !strings.Contains(got.Text, "KB") {
		t.Errorf("surrogate should include human-readable size, got: %q", got.Text)
	}
	if !strings.HasPrefix(got.Text, "[Attachment: ") {
		t.Errorf("surrogate should start with [Attachment: prefix, got: %q", got.Text)
	}
}

func TestTextSurrogate_WithoutFilename(t *testing.T) {
	ref := ArtifactRefBlock{
		StoreID:   "MAIN",
		URI:       "sha256/9c1f2b7d",
		MIME:      "image/png",
		SizeBytes: 4096,
		Sha256:    "9c1f2b7d",
		// Filename left empty.
	}
	got := TextSurrogate(ref)
	// Fallback should use the URI tail (the sha256 hex).
	if !strings.Contains(got.Text, "9c1f2b7d") {
		t.Errorf("surrogate should fall back to URI tail when filename empty, got: %q", got.Text)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1 MB"},
		{5*1024*1024 + 500*1024, "5.4 MB"},
		{1024 * 1024 * 1024, "1 GB"},
	}
	for _, tc := range cases {
		if got := HumanBytes(tc.in); got != tc.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestArtifactRefBlockImplementsBlock(t *testing.T) {
	// Compile-time check that ArtifactRefBlock satisfies Block.
	var _ Block = ArtifactRefBlock{}
}
