package artifacts

import (
	"bytes"
	"image"
	_ "image/gif"  // register decoder for DecodeConfig
	_ "image/jpeg" // register decoder for DecodeConfig
	_ "image/png"  // register decoder for DecodeConfig
	"net/http"
	"strings"
	"unicode"

	_ "golang.org/x/image/webp" // register decoder for DecodeConfig
)

const mimeOctetStream = "application/octet-stream"

// nativeRenderMIMEs are the MIMEs a shipped llm.MultimodalRenderer inlines
// as bytes. Only these labels are ever trusted by the renderer, so only
// these are verified against the bytes.
var nativeRenderMIMEs = map[string]bool{
	"image/png":       true,
	"image/jpeg":      true,
	"image/gif":       true,
	"image/webp":      true,
	"application/pdf": true,
}

// IsNativeRenderMIME reports whether mime is in the native-render set.
func IsNativeRenderMIME(mime string) bool { return nativeRenderMIMEs[mime] }

// ResolveMIME decides the MIME stored for an artifact from the declared
// label (multipart Content-Type or MCP mimeType) and the bytes. See the
// spec § MIME resolution:
//   - bytes that sniff as a native-render type get that type, whatever was declared;
//   - a native-render label on bytes that don't sniff as it becomes octet-stream;
//   - images in the native-render set must pass image.DecodeConfig, else octet-stream;
//   - everything else keeps the declared label (it only ever reaches the model as a text surrogate).
//
// Never rejects: resolution only decides the label.
func ResolveMIME(declared string, data []byte) string {
	d := normaliseMIME(declared)
	sniffed := normaliseMIME(http.DetectContentType(data))

	var mime string
	switch {
	case nativeRenderMIMEs[sniffed]:
		mime = sniffed
	case nativeRenderMIMEs[d]:
		return mimeOctetStream
	default:
		return d
	}
	if strings.HasPrefix(mime, "image/") {
		// Validates the image header only, not full decodability.
		if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
			return mimeOctetStream
		}
	}
	return mime
}

// CleanText makes a client- or tool-supplied label safe for a Postgres
// TEXT column: invalid UTF-8 and control characters (NUL included) are
// dropped, then surrounding space is trimmed.
func CleanText(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// normaliseMIME cleans (CleanText), strips parameters, trims and
// lower-cases; empty becomes application/octet-stream. Cleaning keeps an MCP
// tool-result mimeType or multipart Content-Type from breaking the insert.
func normaliseMIME(s string) string {
	s = CleanText(s)
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return mimeOctetStream
	}
	return s
}
