package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// ArtifactHandler wires the two chat-artifacts routes:
//   - POST /agent_versions/{version_id}/chat/{session_id}/artifacts
//   - GET  /agent_versions/{version_id}/chat/{session_id}/artifacts/{path}
//
// Both routes verify (sessionID, versionID) via the session store's
// existing GetSession semantics — an unknown session or a session
// belonging to a different version resolves as 404 (via
// types.ErrNotFound / types.ErrSessionAgentMismatch mapping in
// handler.Error).
type ArtifactHandler struct {
	sessions    ArtifactSessionReader
	refs        ArtifactRefResolver
	registry    *artifacts.Registry
	maxUploadMB int
	logger      *slog.Logger
}

func NewArtifactHandler(
	sessions ArtifactSessionReader,
	refs ArtifactRefResolver,
	registry *artifacts.Registry,
	maxUploadMB int,
	logger *slog.Logger,
) *ArtifactHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ArtifactHandler{
		sessions:    sessions,
		refs:        refs,
		registry:    registry,
		maxUploadMB: maxUploadMB,
		logger:      logger,
	}
}

// ArtifactUploadResponse is the JSON body the client receives on a
// successful upload. Note: the turn endpoint ignores client-supplied
// artifact_ref input blocks, so an uploaded file cannot currently be
// attached to a BO turn (staged uploads replace this flow).
type ArtifactUploadResponse struct {
	StoreID   string `json:"store_id"`
	URI       string `json:"uri"`
	MIME      string `json:"mime"`
	SizeBytes int64  `json:"size_bytes"`
	Sha256    string `json:"sha256"`
	Filename  string `json:"filename,omitempty"`
}

// HandleUpload handles POST /agent_versions/{v}/chat/{s}/artifacts.
//
// Body: multipart/form-data with a single "file" field. The multipart
// filename parameter (RFC 7578) is preserved on the response as a
// display label; storage URI is content-addressable and does not use
// the filename.
//
// Errors follow the daemon convention via handler.Error:
//   - 400 malformed multipart / missing "file" field
//   - 404 session not found or belongs to a different version
//   - 409 session is dataset-locked (types.ErrSessionLocked)
//   - 413 payload exceeds ARTIFACT_MAX_UPLOAD_MB
//   - 502 upstream store error
func (h *ArtifactHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	sessionID := r.PathValue("session_id")

	// Session existence + version match. Reuse the existing
	// GetSession semantics so all BO chat routes stay in lock-step.
	sess, err := h.sessions.GetSession(r.Context(), sessionID, versionID)
	if err != nil {
		Error(w, err)
		return
	}
	if sess.LockedAt != nil {
		Error(w, types.ErrSessionLocked)
		return
	}

	// Cap request body size at the boundary — Go's http.MaxBytesReader
	// wraps the body and returns an error on Read past the limit, which
	// ParseMultipartForm surfaces as a request-too-large.
	maxBytes := int64(h.maxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	// ParseMultipartForm buffers small parts to memory and larger to
	// disk temp files. The size parameter is a soft memory budget, not
	// a hard cap — the hard cap comes from MaxBytesReader above.
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		// MaxBytesError has an unexported type on some Go versions;
		// distinguish by message so 413 wins over 400.
		if isMaxBytesError(err) {
			http.Error(w, "upload exceeds ARTIFACT_MAX_UPLOAD_MB", http.StatusRequestEntityTooLarge)
			return
		}
		h.logger.Info("artifact upload: malformed multipart", slog.Any("err", err))
		http.Error(w, "malformed multipart body", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		h.logger.Info("artifact upload: missing 'file' field", slog.Any("err", err))
		http.Error(w, "missing 'file' field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Content-Type from the multipart header, falling back to a generic
	// value if the client didn't send one. This is only a hint:
	// artifacts.ResolveMIME decides the stored label below (native-render
	// types are verified against the bytes). The framework accepts anything.
	mime := header.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	filename := header.Filename

	// Route the write to the env-nominated default store.
	store := h.registry.Default()
	if store == nil {
		h.logger.Error("artifact upload: registry has no default store (boot invariant violated)")
		http.Error(w, "artifact registry misconfigured", http.StatusInternalServerError)
		return
	}
	storeID := h.registry.DefaultID()

	// Buffer the upload while hashing it. Uploading a 10-30MB media file
	// into memory is acceptable at the ARTIFACT_MAX_UPLOAD_MB scale spec
	// commits to; larger caps warrant temp-file spilling in a follow-on.
	data, sum, err := hashAndBuffer(file, maxBytes)
	if err != nil {
		if errors.Is(err, errMaxSizeExceeded) {
			http.Error(w, "upload exceeds ARTIFACT_MAX_UPLOAD_MB", http.StatusRequestEntityTooLarge)
			return
		}
		Error(w, err)
		return
	}
	size := int64(len(data))
	buf := bytes.NewReader(data)
	mime = artifacts.ResolveMIME(mime, data)
	uri := "sha256/" + hex.EncodeToString(sum)

	// Dedup: if this exact content is already in the store, skip the
	// Put and return the existing ref. On any Stat error other than
	// "not exists" we err on the side of retrying the Put — a Stat
	// failure isn't a reason to refuse the upload.
	if stat, err := store.Stat(r.Context(), uri); err == nil && stat.Exists {
		// Prefer the stored metadata as source-of-truth for size/sha256
		// even though they should equal the incoming stream's values.
		// The framework computes sha256 from the URI (verbatim tail);
		// size falls back to the incoming stream if Stat lacks it.
		respondUploadJSON(w, ArtifactUploadResponse{
			StoreID:   storeID,
			URI:       uri,
			MIME:      mime,
			SizeBytes: firstNonZero(stat.SizeBytes, size),
			Sha256:    hex.EncodeToString(sum),
			Filename:  filename,
		})
		return
	}

	if _, err := store.Put(r.Context(), uri, mime, buf, size); err != nil {
		h.logger.Error("artifact put failed",
			slog.String("store_id", storeID),
			slog.String("uri", uri),
			slog.Any("err", err),
		)
		http.Error(w, "upstream store error", http.StatusBadGateway)
		return
	}

	respondUploadJSON(w, ArtifactUploadResponse{
		StoreID:   storeID,
		URI:       uri,
		MIME:      mime,
		SizeBytes: size,
		Sha256:    hex.EncodeToString(sum),
		Filename:  filename,
	})
}

func respondUploadJSON(w http.ResponseWriter, resp ArtifactUploadResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	JSON(w, http.StatusCreated, resp)
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
	versionID := r.PathValue("version_id")
	sessionID := r.PathValue("session_id")

	// Session existence + version match; 404 on either failure.
	if _, err := h.sessions.GetSession(r.Context(), sessionID, versionID); err != nil {
		Error(w, err)
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
		h.logger.Error("artifact retrieve: session-scope check failed",
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

	store := h.registry.Get(storeID)
	if store == nil {
		// Registry doesn't have this store — treat as 404 (indistinguishable
		// from an unknown ref) rather than a specific "store not configured"
		// error, which would leak registry membership.
		Error(w, types.ErrNotFound)
		return
	}

	switch store.PreferredDelivery() {
	case artifacts.DeliverySignedURL:
		url, err := store.Sign(r.Context(), uri, 0) // 0 = adapter default TTL
		if err != nil {
			h.logger.Error("artifact retrieve: sign failed",
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
			h.logger.Error("artifact retrieve: get failed",
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
		h.logger.Error("artifact retrieve: unknown delivery mode",
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
