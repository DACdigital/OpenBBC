package handler

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// ArtifactSessionReader is the narrow slice of the chat repository the
// artifact handlers need. Kept as an interface (not the concrete
// *repository.ChatRepository) so tests can substitute a stub.
type ArtifactSessionReader interface {
	GetSession(ctx context.Context, sessionID, versionID string) (*types.ChatSession, error)
}

// ArtifactHandler wires the BO chat-artifacts routes:
//   - POST   /agent_versions/{version_id}/chat/{session_id}/artifacts
//   - GET    /agent_versions/{version_id}/chat/{session_id}/artifacts/{path}
//   - GET    /agent_versions/{version_id}/chat/{session_id}/pending-artifacts
//   - DELETE /agent_versions/{version_id}/chat/{session_id}/pending-artifacts/{id}
//
// Each route runs the BO preamble (boSession) and then delegates to the
// surface-independent artifactService.
type ArtifactHandler struct {
	sessions ArtifactSessionReader
	svc      *artifactService
}

func NewArtifactHandler(
	sessions ArtifactSessionReader,
	rows SessionArtifactStore,
	registry *artifacts.Registry,
	maxUploadMB, maxPending int,
	logger *slog.Logger,
) *ArtifactHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ArtifactHandler{
		sessions: sessions,
		svc: &artifactService{rows: rows, registry: registry, maxUploadMB: maxUploadMB,
			maxPending: maxPending, logger: logger},
	}
}

// boSession runs the BO preamble: a non-canonical session_id → 404 (as on
// deployed), then GetSession(session_id, version_id) (unknown → 404, other
// version → handler.Error's mapping); with
// requireUnlocked, a dataset-locked session → 409. Returns the session id.
func (h *ArtifactHandler) boSession(w http.ResponseWriter, r *http.Request, requireUnlocked bool) (string, bool) {
	sessionID := r.PathValue("session_id")
	if !validUUID(sessionID) {
		Error(w, types.ErrNotFound)
		return "", false
	}
	sess, err := h.sessions.GetSession(r.Context(), sessionID, r.PathValue("version_id"))
	if err != nil {
		Error(w, err)
		return "", false
	}
	if requireUnlocked && sess.LockedAt != nil {
		Error(w, types.ErrSessionLocked)
		return "", false
	}
	return sessionID, true
}

// HandleUpload handles POST /agent_versions/{v}/chat/{s}/artifacts: writes a
// pending artifact (spec § REST — BO). 400 malformed, 404 session,
// 409 locked or at ARTIFACT_MAX_PENDING, 413 too large, 502 store.
func (h *ArtifactHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	if sid, ok := h.boSession(w, r, true); ok {
		h.svc.upload(w, r, sid)
	}
}

// HandleListPending handles GET /agent_versions/{v}/chat/{s}/pending-artifacts.
func (h *ArtifactHandler) HandleListPending(w http.ResponseWriter, r *http.Request) {
	if sid, ok := h.boSession(w, r, false); ok {
		h.svc.listPending(w, r, sid)
	}
}

// HandleDeletePending handles DELETE /agent_versions/{v}/chat/{s}/pending-artifacts/{id}.
// A locked session → 409, matching upload.
func (h *ArtifactHandler) HandleDeletePending(w http.ResponseWriter, r *http.Request) {
	if sid, ok := h.boSession(w, r, true); ok {
		h.svc.deletePending(w, r, sid)
	}
}

var errMaxSizeExceeded = errors.New("upload exceeds ARTIFACT_MAX_UPLOAD_MB")

// defaultUploadBufferBytes is hashAndBuffer's starting capacity when the
// caller has no usable size hint.
const defaultUploadBufferBytes = 32 << 10

// hashAndBuffer reads r fully into memory while computing sha256. Aborts
// with errMaxSizeExceeded if the stream would grow past maxBytes. sizeHint,
// when 0 < sizeHint <= maxBytes, is the starting capacity: a hint at or above
// the stream length means the buffer is allocated once.
func hashAndBuffer(r io.Reader, maxBytes, sizeHint int64) (data []byte, sum []byte, err error) {
	if sizeHint <= 0 || sizeHint > maxBytes {
		sizeHint = min(defaultUploadBufferBytes, maxBytes)
	}
	// One byte past the hint lets a stream of exactly sizeHint bytes reach
	// EOF without growing.
	data = make([]byte, 0, sizeHint+1)
	for {
		if len(data) == cap(data) {
			if int64(len(data)) > maxBytes {
				return nil, nil, errMaxSizeExceeded
			}
			grown := make([]byte, len(data), min(2*int64(cap(data)), maxBytes+1))
			copy(grown, data)
			data = grown
		}
		n, readErr := r.Read(data[len(data):cap(data)])
		data = data[:len(data)+n]
		if int64(len(data)) > maxBytes {
			return nil, nil, errMaxSizeExceeded
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, nil, readErr
		}
	}
	s := sha256.Sum256(data)
	return data, s[:], nil
}

// isMaxBytesError reports whether err (from reading or parsing a request
// body) is the http.MaxBytesReader signal. Go's stdlib returns
// *http.MaxBytesError on Go 1.19+; before that, the error had unexported
// type. This form covers both cases without a hard version dependency.
func isMaxBytesError(err error) bool {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return true
	}
	return strings.Contains(err.Error(), "request body too large")
}

func firstNonZero(vals ...int64) int64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// HandleRetrieve handles GET /agent_versions/{v}/chat/{s}/artifacts/{path...}.
func (h *ArtifactHandler) HandleRetrieve(w http.ResponseWriter, r *http.Request) {
	if sid, ok := h.boSession(w, r, false); ok {
		h.svc.retrieve(w, r, sid)
	}
}

// artifactContentDisposition returns the Content-Disposition value for a
// proxied artifact: "inline" for the native-render MIME set, "attachment"
// otherwise, with an RFC 5987 filename* parameter when filename is known.
func artifactContentDisposition(mime, filename string) string {
	d := "attachment"
	if artifacts.IsNativeRenderMIME(mime) {
		d = "inline"
	}
	if filename != "" {
		d += "; filename*=UTF-8''" + rfc5987Escape(filename)
	}
	return d
}

// rfc5987Escape percent-encodes every byte of s outside RFC 5987
// attr-char (ALPHA / DIGIT / "!#$&+-.^_`|~").
func rfc5987Escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') ||
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}
