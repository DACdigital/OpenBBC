package handler

import (
	"html/template"
	"io/fs"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// FeedbackHandler handles per-message feedback upserts and deletes,
// scoped under the existing chat routes. Returns the message's new
// feedback-footer HTML fragment so htmx can swap it in place.
type FeedbackHandler struct {
	repo *repository.FeedbackRepository
	tmpl *template.Template
}

func NewFeedbackHandler(repo *repository.FeedbackRepository, webFS fs.FS) (*FeedbackHandler, error) {
	tmpl, err := template.New("").ParseFS(webFS,
		"templates/chat/feedback_footer.html",
	)
	if err != nil {
		return nil, err
	}
	return &FeedbackHandler{repo: repo, tmpl: tmpl}, nil
}

// Upsert handles POST /agent_versions/{version_id}/chat/{session_id}/messages/{message_id}/feedback
func (h *FeedbackHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	messageID := r.PathValue("message_id")
	if err := r.ParseForm(); err != nil {
		Error(w, err)
		return
	}
	rating := types.FeedbackRating(r.FormValue("rating"))
	if rating != types.FeedbackRatingUp && rating != types.FeedbackRatingDown {
		http.Error(w, "invalid rating", http.StatusBadRequest)
		return
	}
	comment := r.FormValue("comment")
	expected := r.FormValue("expected_output")
	if err := h.repo.Upsert(r.Context(), messageID, rating, comment, expected); err != nil {
		Error(w, err)
		return
	}
	fb, _ := h.repo.Get(r.Context(), messageID)
	renderTemplate(w, h.tmpl, "feedback_footer", map[string]any{
		"MessageID": messageID,
		"Feedback":  fb,
		"Locked":    false,
	})
}

// Delete handles DELETE /agent_versions/{version_id}/chat/{session_id}/messages/{message_id}/feedback
func (h *FeedbackHandler) Delete(w http.ResponseWriter, r *http.Request) {
	messageID := r.PathValue("message_id")
	if err := h.repo.Delete(r.Context(), messageID); err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, h.tmpl, "feedback_footer", map[string]any{
		"MessageID": messageID,
		"Feedback":  nil,
		"Locked":    false,
	})
}
