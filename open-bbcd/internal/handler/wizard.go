package handler

import (
	"context"
	"io"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type WizardAgentRepository interface {
	CreateFromWizard(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, error)
}

type WizardHandler struct {
	agentRepo WizardAgentRepository
	schema    *types.WizardSchema
}

func NewWizardHandler(agentRepo WizardAgentRepository, schema *types.WizardSchema) *WizardHandler {
	return &WizardHandler{agentRepo: agentRepo, schema: schema}
}

func (h *WizardHandler) Submit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}

	fields := h.schema.OrderedFields()
	wizardInput := make(map[string]string, len(fields))

	for _, of := range fields {
		if of.Field.Type == "file" {
			file, _, err := r.FormFile(of.Key)
			if err != nil {
				if of.Field.Required {
					http.Error(w, of.Key+" is required", http.StatusBadRequest)
					return
				}
				continue
			}
			content, readErr := io.ReadAll(file)
			file.Close()
			if readErr != nil {
				http.Error(w, "failed to read uploaded file", http.StatusInternalServerError)
				return
			}
			wizardInput[of.Key] = string(content)
		} else {
			val := r.FormValue(of.Key)
			if of.Field.Required && val == "" {
				http.Error(w, of.Key+" is required", http.StatusBadRequest)
				return
			}
			wizardInput[of.Key] = val
		}
	}

	_, err := h.agentRepo.CreateFromWizard(r.Context(), types.CreateAgentFromWizardOpts{
		Name:          wizardInput["name"],
		WizardInput:   wizardInput,
		SchemaVersion: h.schema.Version,
	})
	if err != nil {
		http.Error(w, "failed to create agent", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/agents/ui", http.StatusSeeOther)
}
