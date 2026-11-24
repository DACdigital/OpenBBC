package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/seed"
)

// SeedExporter is the narrow dependency of SeedExportHandler.
type SeedExporter interface {
	Export(ctx context.Context, versionID string, req repository.ExportRequest) (*seed.Set, []string, error)
}

type SeedExportHandler struct {
	versions idResolver
	exporter SeedExporter
}

func NewSeedExportHandler(versions idResolver, exporter SeedExporter) *SeedExportHandler {
	return &SeedExportHandler{versions: versions, exporter: exporter}
}

// Export handles POST /agent_versions/{version_id}/export (declarative-seed
// spec §5). The path accepts a UUID or a version static ID. The optional JSON
// body is {"static_ids": {"<uuid>": "<static id>"}, "deploy": "if_none"}.
// 200 returns the openbbc-seed-v1 YAML as an attachment with the placeholder
// names in X-Seed-Env-Vars; 400 for a bad body or static ID, 404 for an
// unknown version, 409 for a non-exportable status or a static-ID conflict.
func (h *SeedExportHandler) Export(w http.ResponseWriter, r *http.Request) {
	versionID, ok := resolvePathID(w, r, h.versions, "version_id")
	if !ok {
		return
	}

	var req repository.ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid JSON body: " + err.Error()})
		return
	}
	switch req.Deploy {
	case "", seed.DeployNone, seed.DeployIfNone, seed.DeployForce:
	default:
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: `deploy must be one of none, if_none, force`})
		return
	}

	set, envVars, err := h.exporter.Export(r.Context(), versionID, req)
	if err != nil {
		Error(w, err)
		return
	}
	body, err := seed.Marshal(set)
	if err != nil {
		Error(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", "attachment; filename="+set.Versions[0].StaticID+".seed.yaml")
	w.Header().Set("X-Seed-Env-Vars", strings.Join(envVars, ","))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
