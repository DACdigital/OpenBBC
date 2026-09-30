package artifacts

import (
	"encoding/base64"
	"testing"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"

func TestResolveMIME(t *testing.T) {
	png := mustB64(t, onePixelPNG)
	docx := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	cases := []struct {
		name     string
		declared string
		data     []byte
		want     string
	}{
		{"real PNG declared octet-stream", "application/octet-stream", png, "image/png"},
		{"PNG bytes declared jpeg", "image/jpeg", png, "image/png"},
		{"text bytes declared png", "image/png", []byte("definitely not an image"), "application/octet-stream"},
		{"truncated PNG", "image/png", png[:20], "application/octet-stream"},
		{"pdf declared octet-stream", "application/octet-stream", []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n"), "application/pdf"},
		{"text bytes declared pdf", "application/pdf", []byte("not a pdf"), "application/octet-stream"},
		{"docx keeps declared office mime", docx, []byte("PK\x03\x04\x14\x00\x06\x00"), docx},
		{"declared params stripped and lower-cased", "Text/CSV; charset=utf-8", []byte("a,b\n1,2\n"), "text/csv"},
		{"absent declared on text", "", []byte("hello"), "application/octet-stream"},
		{"NUL and controls stripped from declared", "text/c\x00s\x01v", []byte("a,b\n"), "text/csv"},
		{"invalid UTF-8 stripped from declared", "text/\xffcsv", []byte("a,b\n"), "text/csv"},
		{"declared only NUL", "\x00", []byte("hello"), "application/octet-stream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveMIME(tc.declared, tc.data); got != tc.want {
				t.Fatalf("ResolveMIME(%q) = %q, want %q", tc.declared, got, tc.want)
			}
		})
	}
}

func TestIsNativeRenderMIME(t *testing.T) {
	for _, m := range []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"} {
		if !IsNativeRenderMIME(m) {
			t.Errorf("%s should be native-render", m)
		}
	}
	if IsNativeRenderMIME("application/zip") {
		t.Error("application/zip must not be native-render")
	}
}
