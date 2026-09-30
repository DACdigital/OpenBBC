package handler

import (
	"bytes"
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

// ArtifactRefResolver looks up whether a given (store_id, uri) is
// referenced by any message in a session. Used by the retrieval handler
// to enforce session-scope access — the ref alone is not a bearer
// capability. Implementation lives in the repository layer where the
// JSONB SQL is; the handler holds only the shape.
type ArtifactRefResolver interface {
	// SessionReferences returns (true, mime, nil) if any chat_messages
	// row for sessionID contains an artifact_ref content-block with
	// matching (storeID, uri). Returns (false, "", nil) if not
	// referenced. mime is required because the retrieval route uses it
	// as the Content-Type when serving bytes.
	SessionReferences(ctx context.Context, sessionID, storeID, uri string) (found bool, mime string, err error)
}

// rowRefResolver adapts the session-artifact table lookup to the legacy
// ArtifactRefResolver shape. Removed in Task 9.
type rowRefResolver struct {
	rows interface {
		LookupSessionArtifact(ctx context.Context, sessionID, storeID, uri string) (*types.SessionArtifact, error)
	}
}

func (r rowRefResolver) SessionReferences(ctx context.Context, sessionID, storeID, uri string) (bool, string, error) {
	a, err := r.rows.LookupSessionArtifact(ctx, sessionID, storeID, uri)
	if errors.Is(err, types.ErrNotFound) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, a.MIME, nil
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
	refs     ArtifactRefResolver // removed in Task 9
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
		refs:     rowRefResolver{rows: rows},
		svc: &artifactService{rows: rows, registry: registry, maxUploadMB: maxUploadMB,
			maxPending: maxPending, logger: logger},
	}
}

// boSession runs the BO preamble: GetSession(session_id, version_id)
// (unknown → 404, other version → handler.Error's mapping); with
// requireUnlocked, a dataset-locked session → 409. Returns the session id.
func (h *ArtifactHandler) boSession(w http.ResponseWriter, r *http.Request, requireUnlocked bool) (string, bool) {
	sessionID := r.PathValue("session_id")
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

// hashAndBuffer reads r fully into memory while computing sha256. Aborts
// with errMaxSizeExceeded if the stream would grow past maxBytes.
func hashAndBuffer(r io.Reader, maxBytes int64) (data []byte, sum []byte, err error) {
	h := sha256.New()
	var b bytes.Buffer
	buf := make([]byte, 32*1024)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			if int64(b.Len())+int64(n) > maxBytes {
				return nil, nil, errMaxSizeExceeded
			}
			_, _ = h.Write(buf[:n])
			_, _ = b.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, nil, readErr
		}
	}
	return b.Bytes(), h.Sum(nil), nil
}

// isMaxBytesError checks the error returned by ParseMultipartForm for
// the http.MaxBytesReader signal. Go's stdlib returns *http.MaxBytesError
// on Go 1.19+; before that, the error had unexported type. This form
// covers both cases without a hard version dependency.
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

// HandleRetrieve handles GET /agent_versions/{v}/chat/{s}/artifacts/{path}.
//
// The {path} wildcard captures store_id and the store-local URI joined
// by a `/`; split on first `/` at the handler layer so `uri` values
// carrying additional `/` characters (e.g. `sha256/<hex>`) pass through
// intact.
//
// Delivery mode is the adapter's choice (bytes-proxy or 302 redirect to
// a signed URL). Session-scope authorisation goes through
// ArtifactRefResolver.SessionReferences: mismatch returns 404 (never 403)
// so ref existence does not leak across sessions.
func (h *ArtifactHandler) HandleRetrieve(w http.ResponseWriter, r *http.Request) {
	// Session existence + version match; 404 on either failure.
	sessionID, ok := h.boSession(w, r, false)
	if !ok {
		return
	}

	// Path-suffix parse. The mux registers the route with a wildcard
	// `.../artifacts/{path...}` (Go 1.22 net/http wildcard syntax);
	// the raw suffix ends up in r.PathValue("path"). Split on first
	// `/` — prefix is store_id, remainder is store-local uri.
	raw := r.PathValue("path")
	if raw == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	slash := strings.IndexByte(raw, '/')
	if slash < 1 || slash == len(raw)-1 {
		http.Error(w, "path must be <store_id>/<uri>", http.StatusBadRequest)
		return
	}
	storeID := raw[:slash]
	uri := raw[slash+1:]

	// Session-scope authorisation: the (storeID, uri) ref must be
	// referenced by at least one message in this session. Mismatch is
	// indistinguishable from "unknown store" or "unknown uri" — 404.
	found, mime, err := h.refs.SessionReferences(r.Context(), sessionID, storeID, uri)
	if err != nil {
		h.svc.logger.Error("artifact retrieve: session-scope check failed",
			slog.String("session_id", sessionID),
			slog.String("store_id", storeID),
			slog.String("uri", uri),
			slog.Any("err", err),
		)
		http.Error(w, "session-scope check failed", http.StatusInternalServerError)
		return
	}
	if !found {
		Error(w, types.ErrNotFound)
		return
	}

	store := h.svc.registry.Get(storeID)
	if store == nil {
		// Registry doesn't have this store — treat as 404 (indistinguishable
		// from an unknown ref) rather than a specific "store not configured"
		// error, which would leak registry membership.
		Error(w, types.ErrNotFound)
		return
	}

	switch store.PreferredDelivery() {
	case artifacts.DeliverySignedURL:
		url, err := store.Sign(r.Context(), uri, 0, artifacts.SignOptions{}) // 0 = adapter default TTL
		if err != nil {
			h.svc.logger.Error("artifact retrieve: sign failed",
				slog.String("store_id", storeID),
				slog.String("uri", uri),
				slog.Any("err", err),
			)
			if errors.Is(err, artifacts.ErrBlobMissing) {
				http.Error(w, "blob was removed", http.StatusGone)
				return
			}
			http.Error(w, "upstream store error", http.StatusBadGateway)
			return
		}
		// Content-Type / Content-Disposition for signed-URL delivery are
		// set by the store via presign response overrides, which arrive
		// with Sign's SignOptions parameter.
		w.Header().Set("Location", url)
		w.WriteHeader(http.StatusFound) // 302
		return

	case artifacts.DeliveryBytes:
		rc, err := store.Get(r.Context(), uri)
		if err != nil {
			h.svc.logger.Error("artifact retrieve: get failed",
				slog.String("store_id", storeID),
				slog.String("uri", uri),
				slog.Any("err", err),
			)
			if errors.Is(err, artifacts.ErrBlobMissing) {
				http.Error(w, "blob was removed", http.StatusGone)
				return
			}
			http.Error(w, "upstream store error", http.StatusBadGateway)
			return
		}
		defer rc.Close()
		// Artifact bytes are user- or tool-supplied: never let a client
		// sniff them into something renderable, and only render inline
		// the native-render set. Content-Length is omitted: neither the
		// session-scope check nor Get reports a size here. Filename is
		// likewise unknown at this point, so Content-Disposition carries
		// no filename parameter.
		//
		// Non-native types are served as octet-stream: a tool can label
		// bytes text/javascript or text/css, which a browser would run
		// via <script src>/<link> despite Content-Disposition: attachment.
		ct := "application/octet-stream"
		if artifacts.IsNativeRenderMIME(mime) {
			ct = mime
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", artifactContentDisposition(mime, ""))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, rc)
		return

	default:
		h.svc.logger.Error("artifact retrieve: unknown delivery mode",
			slog.String("store_id", storeID),
			slog.Int("delivery", int(store.PreferredDelivery())),
		)
		http.Error(w, "internal error", http.StatusInternalServerError)
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
