package handler

import (
	"encoding/json"
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

// inRootSession writes 404 and returns false unless messageID belongs to
// the path's sessionID and that session is a root (spec § root-only rule:
// feedback is scoped to the path session). Called before Get/Upsert/Delete,
// which key on message_id alone.
func (h *FeedbackHandler) inRootSession(w http.ResponseWriter, r *http.Request, sessionID, messageID string) bool {
	if !validUUID(sessionID) || !validUUID(messageID) {
		Error(w, types.ErrNotFound)
		return false
	}
	ok, err := h.repo.MessageInRootSession(r.Context(), sessionID, messageID)
	if err != nil {
		Error(w, err)
		return false
	}
	if !ok {
		Error(w, types.ErrNotFound)
		return false
	}
	return true
}

// Footer handles GET /agent_versions/{version_id}/chat/{session_id}/messages/{message_id}/feedback
// Returns the feedback_footer HTML fragment (empty or filled) so the
// streaming chat client can attach a footer to a just-finalized bubble
// without a full page reload. Locked=false is safe here — writes on a
// locked session are already refused earlier in the Turn path, so a
// footer we render mid-session is always for an unlockable message.
func (h *FeedbackHandler) Footer(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	sessionID := r.PathValue("session_id")
	messageID := r.PathValue("message_id")
	if !h.inRootSession(w, r, sessionID, messageID) {
		return
	}
	fb, _ := h.repo.Get(r.Context(), messageID) // nil on ErrNotFound is fine
	renderTemplate(w, h.tmpl, "feedback_footer", map[string]any{
		"MessageID": messageID,
		"Feedback":  fb,
		"Locked":    false,
		"VersionID": versionID,
		"SessionID": sessionID,
	})
}

// Upsert handles POST /agent_versions/{version_id}/chat/{session_id}/messages/{message_id}/feedback
func (h *FeedbackHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	sessionID := r.PathValue("session_id")
	messageID := r.PathValue("message_id")
	if !h.inRootSession(w, r, sessionID, messageID) {
		return
	}
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
	var criteria []string
	if raw := r.FormValue("judge_criteria_json"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &criteria); err != nil {
			http.Error(w, "invalid judge_criteria_json", http.StatusBadRequest)
			return
		}
	}
	if err := h.repo.Upsert(r.Context(), messageID, rating, comment, expected, criteria); err != nil {
		Error(w, err)
		return
	}
	fb, _ := h.repo.Get(r.Context(), messageID)
	renderTemplate(w, h.tmpl, "feedback_footer", map[string]any{
		"MessageID": messageID,
		"Feedback":  fb,
		"Locked":    false,
		"VersionID": versionID,
		"SessionID": sessionID,
	})
}

// Delete handles DELETE /agent_versions/{version_id}/chat/{session_id}/messages/{message_id}/feedback
func (h *FeedbackHandler) Delete(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	sessionID := r.PathValue("session_id")
	messageID := r.PathValue("message_id")
	if !h.inRootSession(w, r, sessionID, messageID) {
		return
	}
	if err := h.repo.Delete(r.Context(), messageID); err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, h.tmpl, "feedback_footer", map[string]any{
		"MessageID": messageID,
		"Feedback":  nil,
		"Locked":    false,
		"VersionID": versionID,
		"SessionID": sessionID,
	})
}
