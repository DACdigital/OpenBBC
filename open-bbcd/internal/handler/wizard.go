package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/flowmap"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/storage"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

type WizardAgentRepository interface {
	CreateFromWizard(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error)
}

type WizardHandler struct {
	agentRepo      WizardAgentRepository
	schema         *types.WizardSchema
	store          storage.Storage
	maxUploadBytes int64
	logger         *slog.Logger
}

func NewWizardHandler(agentRepo WizardAgentRepository, schema *types.WizardSchema, store storage.Storage, maxUploadBytes int64, logger *slog.Logger) *WizardHandler {
	return &WizardHandler{
		agentRepo:      agentRepo,
		schema:         schema,
		store:          store,
		maxUploadBytes: maxUploadBytes,
		logger:         logger,
	}
}

func (h *WizardHandler) Submit(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > h.maxUploadBytes {
		Error(w, types.ErrDiscoveryFileTooLarge)
		return
	}
	if err := r.ParseMultipartForm(h.maxUploadBytes); err != nil {
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}

	fields := h.schema.OrderedFields()
	wizardInput := make(map[string]string, len(fields))
	agentID := uuid.NewString()
	var (
		discoveryKey string
		zipBytes     []byte
	)

	for _, of := range fields {
		if of.Field.Type == "file" {
			file, header, err := r.FormFile(of.Key)
			if err != nil {
				if of.Field.Required {
					Error(w, types.ErrDiscoveryFileRequired)
					return
				}
				continue
			}
			ext := strings.ToLower(filepath.Ext(header.Filename))
			if ext != ".zip" {
				file.Close()
				Error(w, types.ErrDiscoveryFileBadExtension)
				return
			}

			// Buffer the zip so we can both store it and parse it.
			b, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				h.logger.Error("wizard: read upload", slog.Any("error", err))
				http.Error(w, "failed to read upload", http.StatusInternalServerError)
				return
			}
			zipBytes = b
			continue
		}

		val := r.FormValue(of.Key)
		if of.Field.Required && val == "" {
			http.Error(w, of.Key+" is required", http.StatusBadRequest)
			return
		}
		wizardInput[of.Key] = val
	}

	// Parse the zip BEFORE persisting anything. A parse failure means the
	// upload is structurally wrong — surface it as 400 so the wizard stays
	// open with the user's inputs intact rather than creating an unrunnable
	// agent row with an error stamp.
	cfg, parseErr := flowmap.Parse(bytes.NewReader(zipBytes))
	if parseErr != nil {
		http.Error(w, "Discovery archive could not be parsed: "+parseErr.Error(), http.StatusBadRequest)
		return
	}
	cfg.Name = wizardInput["name"]
	cfg.Scope = wizardInput["scope"]
	cfg.ShouldDo = wizardInput["should_do"]
	cfg.ShouldNotDo = wizardInput["should_not_do"]
	cfg.BusinessDomain = wizardInput["business_domain"]

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		h.logger.Error("wizard: marshal flow_map_config", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Parse succeeded — store the original zip blob now so it's available
	// for later re-rendering or debugging.
	discoveryKey = agentID + ".zip"
	if err := h.store.Put(r.Context(), discoveryKey, bytes.NewReader(zipBytes)); err != nil {
		h.logger.Error("wizard: storage.Put", slog.String("key", discoveryKey), slog.Any("error", err))
		http.Error(w, "failed to save discovery file", http.StatusInternalServerError)
		return
	}

	_, version, err := h.agentRepo.CreateFromWizard(r.Context(), types.CreateAgentFromWizardOpts{
		ID:                agentID,
		Name:              wizardInput["name"],
		FlowMapConfig:     cfgJSON,
		DiscoveryFilePath: discoveryKey,
	})
	if err != nil {
		h.logger.Error("wizard: orphan discovery file after insert failure", slog.String("key", discoveryKey), slog.Any("error", err))
		Error(w, err)
		return
	}

	http.Redirect(w, r, "/agent_versions/"+version.ID+"/configure", http.StatusSeeOther)
}
