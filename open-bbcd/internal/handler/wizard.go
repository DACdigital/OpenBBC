package handler

import (
	"context"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/storage"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

type WizardAgentRepository interface {
	CreateFromWizard(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, error)
}

type WizardHandler struct {
	agentRepo      WizardAgentRepository
	schema         *types.WizardSchema
	store          storage.Storage
	maxUploadBytes int64
}

func NewWizardHandler(agentRepo WizardAgentRepository, schema *types.WizardSchema, store storage.Storage, maxUploadBytes int64) *WizardHandler {
	return &WizardHandler{
		agentRepo:      agentRepo,
		schema:         schema,
		store:          store,
		maxUploadBytes: maxUploadBytes,
	}
}

func (h *WizardHandler) Submit(w http.ResponseWriter, r *http.Request) {
	// Pre-check Content-Length. Trusts the client's reported length, which is
	// fine for backoffice usage; tighten with http.MaxBytesReader if needed.
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
	var discoveryKey string

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

			discoveryKey = agentID + ".zip"
			if err := h.store.Put(r.Context(), discoveryKey, file); err != nil {
				file.Close()
				log.Printf("wizard: storage.Put %s: %v", discoveryKey, err)
				http.Error(w, "failed to save discovery file", http.StatusInternalServerError)
				return
			}
			file.Close()
			continue
		}

		val := r.FormValue(of.Key)
		if of.Field.Required && val == "" {
			http.Error(w, of.Key+" is required", http.StatusBadRequest)
			return
		}
		wizardInput[of.Key] = val
	}

	_, err := h.agentRepo.CreateFromWizard(r.Context(), types.CreateAgentFromWizardOpts{
		ID:                agentID,
		Name:              wizardInput["name"],
		WizardInput:       wizardInput,
		SchemaVersion:     h.schema.Version,
		DiscoveryFilePath: discoveryKey,
	})
	if err != nil {
		if discoveryKey != "" {
			log.Printf("wizard: orphan discovery file %s after insert failure: %v", discoveryKey, err)
		} else {
			log.Printf("wizard: CreateFromWizard: %v", err)
		}
		Error(w, err)
		return
	}

	http.Redirect(w, r, "/agents/ui", http.StatusSeeOther)
}
