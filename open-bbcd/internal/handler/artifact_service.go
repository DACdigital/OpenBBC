package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// SessionArtifactStore is a session-owning context's artifact table (spec
// § Data — new tables). Implemented by *repository.ChatRepository
// (chat_session_artifacts) and *repository.DeployedRepository
// (deployed_session_artifacts).
type SessionArtifactStore interface {
	PrecheckUpload(ctx context.Context, sessionID, storeID, uri string, maxPending int) (*types.SessionArtifact, error)
	CommitUpload(ctx context.Context, a types.SessionArtifact, maxPending int) (*types.SessionArtifact, error)
	ListPendingArtifacts(ctx context.Context, sessionID string) ([]*types.SessionArtifact, error)
	DeletePendingArtifact(ctx context.Context, sessionID, id string) error
	LookupSessionArtifact(ctx context.Context, sessionID, storeID, uri string) (*types.SessionArtifact, error)
}

// PendingArtifact is the pending-artifact object returned by upload and by
// the pending list. Additive over PR #53's upload response: every earlier
// field keeps its meaning; id and status are new. store_id + uri let the
// client build a retrieval URL; the turn never reads refs from the client.
type PendingArtifact struct {
	ID        string `json:"id"`
	StoreID   string `json:"store_id"`
	URI       string `json:"uri"`
	Filename  string `json:"filename,omitempty"`
	MIME      string `json:"mime"`
	SizeBytes int64  `json:"size_bytes"`
	Sha256    string `json:"sha256"`
	Status    string `json:"status"`
}

func pendingArtifactFrom(a *types.SessionArtifact) PendingArtifact {
	return PendingArtifact{
		ID: a.ID, StoreID: a.StoreID, URI: a.URI, Filename: a.Filename,
		MIME: a.MIME, SizeBytes: a.SizeBytes, Sha256: a.Sha256, Status: "pending",
	}
}

// artifactService is the surface-independent half of the artifact routes.
// BO and deployed handlers run their own preamble (session scope) and then
// call into it with the resolved session id.
type artifactService struct {
	rows        SessionArtifactStore
	registry    *artifacts.Registry
	maxUploadMB int
	maxPending  int
	logger      *slog.Logger
}

// upload runs spec § REST — deployed, upload steps 1–6. No DB transaction
// or lock is held while the body is read or while the store is called.
func (s *artifactService) upload(w http.ResponseWriter, r *http.Request, sessionID string) {
	ctx := r.Context()
	maxBytes := int64(s.maxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		if isMaxBytesError(err) {
			http.Error(w, "upload exceeds ARTIFACT_MAX_UPLOAD_MB", http.StatusRequestEntityTooLarge)
			return
		}
		// The parser error can echo the part's Content-Disposition line,
		// which carries the filename: log a fixed message only.
		s.logger.Info("artifact upload: malformed multipart", slog.Bool("max_bytes", false))
		http.Error(w, "malformed multipart body", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing 'file' field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	store := s.registry.Default()
	if store == nil {
		s.logger.Error("artifact upload: registry has no default store (boot invariant violated)")
		http.Error(w, "artifact registry misconfigured", http.StatusInternalServerError)
		return
	}
	storeID := s.registry.DefaultID()

	// 1. Read: hash + buffer, resolve MIME. No DB access.
	data, sum, err := hashAndBuffer(file, maxBytes)
	if err != nil {
		if errors.Is(err, errMaxSizeExceeded) {
			http.Error(w, "upload exceeds ARTIFACT_MAX_UPLOAD_MB", http.StatusRequestEntityTooLarge)
			return
		}
		Error(w, err)
		return
	}
	declared := header.Header.Get("Content-Type")
	if declared == "" {
		declared = "application/octet-stream"
	}
	mime := artifacts.ResolveMIME(declared, data)
	sha := hex.EncodeToString(sum)
	uri := "sha256/" + sha

	// 2. Pre-check (non-locking, fast fail): dedup hit or cap.
	existing, err := s.rows.PrecheckUpload(ctx, sessionID, storeID, uri, s.maxPending)
	if err != nil {
		s.fail(w, "precheck upload", err, slog.String("session_id", sessionID),
			slog.String("store_id", storeID), slog.String("uri", uri))
		return
	}
	if existing != nil {
		JSON(w, http.StatusCreated, pendingArtifactFrom(existing))
		return
	}

	// 3. Store, outside any transaction. A Stat error is not a reason to
	// refuse the upload: fall through to Put.
	st, statErr := store.Stat(ctx, uri)
	if statErr != nil {
		s.logger.Warn("artifact upload: stat failed; attempting put",
			slog.String("store_id", storeID), slog.String("uri", uri), slog.String("mime", mime), slog.Any("err", statErr))
	}
	if statErr != nil || !st.Exists {
		if _, err := store.Put(ctx, uri, mime, bytes.NewReader(data), int64(len(data))); err != nil {
			s.logger.Error("artifact put failed",
				slog.String("store_id", storeID), slog.String("uri", uri), slog.String("mime", mime), slog.Any("err", err))
			http.Error(w, "upstream store error", http.StatusBadGateway)
			return
		}
	}

	// 4–6. Short commit transaction: advisory lock, session re-read,
	// authoritative dedup + cap, insert.
	row, err := s.rows.CommitUpload(ctx, types.SessionArtifact{
		SessionID: sessionID,
		Origin:    types.ArtifactOriginUpload,
		StoreID:   storeID,
		URI:       uri,
		MIME:      mime,
		SizeBytes: int64(len(data)),
		Sha256:    sha,
		Filename:  header.Filename,
	}, s.maxPending)
	if err != nil {
		s.fail(w, "commit upload", err, slog.String("session_id", sessionID),
			slog.String("store_id", storeID), slog.String("uri", uri))
		return
	}
	JSON(w, http.StatusCreated, pendingArtifactFrom(row))
}

// listPending writes {"pending_artifacts":[…]} in claim order.
func (s *artifactService) listPending(w http.ResponseWriter, r *http.Request, sessionID string) {
	rows, err := s.rows.ListPendingArtifacts(r.Context(), sessionID)
	if err != nil {
		s.fail(w, "list pending artifacts", err, slog.String("session_id", sessionID))
		return
	}
	out := make([]PendingArtifact, 0, len(rows))
	for _, a := range rows {
		out = append(out, pendingArtifactFrom(a))
	}
	JSON(w, http.StatusOK, struct {
		PendingArtifacts []PendingArtifact `json:"pending_artifacts"`
	}{out})
}

// deletePending removes one pending row (not the blob): 204, 404, or 409 if consumed.
func (s *artifactService) deletePending(w http.ResponseWriter, r *http.Request, sessionID string) {
	if err := s.rows.DeletePendingArtifact(r.Context(), sessionID, r.PathValue("id")); err != nil {
		s.fail(w, "delete pending artifact", err, slog.String("session_id", sessionID),
			slog.String("artifact_id", r.PathValue("id")))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fail writes err via handler.Error, logging it first unless it is one of
// the sentinels the artifact routes map to a client status. attrs must
// never carry the filename.
func (s *artifactService) fail(w http.ResponseWriter, op string, err error, attrs ...slog.Attr) {
	switch {
	case errors.Is(err, types.ErrNotFound),
		errors.Is(err, types.ErrSessionLocked),
		errors.Is(err, types.ErrPendingArtifactCap),
		errors.Is(err, types.ErrArtifactConsumed):
	default:
		args := make([]any, 0, len(attrs)+1)
		for _, a := range attrs {
			args = append(args, a)
		}
		args = append(args, slog.Any("err", err))
		s.logger.Error("artifact "+op+" failed", args...)
	}
	Error(w, err)
}

// splitArtifactPath splits a {path...} wildcard into <store_id>/<uri>; the
// uri keeps its own slashes (sha256/<hex>).
func splitArtifactPath(raw string) (storeID, uri string, ok bool) {
	slash := strings.IndexByte(raw, '/')
	if slash < 1 || slash == len(raw)-1 {
		return "", "", false
	}
	return raw[:slash], raw[slash+1:], true
}

// retrieve serves one artifact of sessionID: authorised by a row in the
// session's artifact table (any origin, pending or consumed), then Stat
// (missing -> 410), then the adapter's delivery mode with the same
// Content-Type / Content-Disposition on both. Unknown ref, other session's
// ref and unregistered store are all 404 so nothing leaks.
func (s *artifactService) retrieve(w http.ResponseWriter, r *http.Request, sessionID string) {
	ctx := r.Context()
	storeID, uri, ok := splitArtifactPath(r.PathValue("path"))
	if !ok {
		http.Error(w, "path must be <store_id>/<uri>", http.StatusBadRequest)
		return
	}
	row, err := s.rows.LookupSessionArtifact(ctx, sessionID, storeID, uri)
	if err != nil {
		s.fail(w, "retrieve lookup", err, slog.String("session_id", sessionID), slog.String("store_id", storeID), slog.String("uri", uri))
		return
	}
	store := s.registry.Get(storeID)
	if store == nil {
		Error(w, types.ErrNotFound)
		return
	}
	// Sign presigns offline and cannot see a missing blob; Stat can.
	st, err := store.Stat(ctx, uri)
	if err != nil {
		s.logger.Error("artifact retrieve: stat failed",
			slog.String("session_id", sessionID), slog.String("store_id", storeID), slog.String("uri", uri), slog.String("mime", row.MIME), slog.Any("err", err))
		http.Error(w, "upstream store error", http.StatusBadGateway)
		return
	}
	if !st.Exists {
		http.Error(w, "blob was removed", http.StatusGone)
		return
	}

	// Non-native types are served as octet-stream: a tool can label bytes
	// text/javascript or text/css, which a browser would run via
	// <script src>/<link> despite Content-Disposition: attachment.
	ct := "application/octet-stream"
	if artifacts.IsNativeRenderMIME(row.MIME) {
		ct = row.MIME
	}
	disp := artifactContentDisposition(row.MIME, row.Filename)

	storeErr := func(op string, err error) {
		s.logger.Error("artifact retrieve: "+op+" failed",
			slog.String("session_id", sessionID), slog.String("store_id", storeID), slog.String("uri", uri), slog.String("mime", row.MIME), slog.Any("err", err))
		if errors.Is(err, artifacts.ErrBlobMissing) {
			http.Error(w, "blob was removed", http.StatusGone)
			return
		}
		http.Error(w, "upstream store error", http.StatusBadGateway)
	}

	switch store.PreferredDelivery() {
	case artifacts.DeliverySignedURL:
		url, err := store.Sign(ctx, uri, 0, artifacts.SignOptions{ContentType: ct, ContentDisposition: disp})
		if err != nil {
			storeErr("sign", err)
			return
		}
		w.Header().Set("Location", url)
		w.WriteHeader(http.StatusFound)
	case artifacts.DeliveryBytes:
		rc, err := store.Get(ctx, uri)
		if err != nil {
			storeErr("get", err)
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Length", strconv.FormatInt(row.SizeBytes, 10))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", disp)
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, rc)
	default:
		s.logger.Error("artifact retrieve: unknown delivery mode",
			slog.String("session_id", sessionID), slog.String("store_id", storeID),
			slog.Int("delivery", int(store.PreferredDelivery())))
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
