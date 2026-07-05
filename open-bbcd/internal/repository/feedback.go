package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// FeedbackRepository owns the chat_message_feedback table.
// Feedback attaches only to role='assistant' messages — enforced here,
// not via a partial FK (Postgres doesn't support that shape).
type FeedbackRepository struct{ db *sql.DB }

func NewFeedbackRepository(db *sql.DB) *FeedbackRepository {
	return &FeedbackRepository{db: db}
}

// Upsert writes (or replaces) the feedback row for messageID.
// judgeCriteria must be non-empty (at least one criterion).
// Refuses when:
//   - the message is not an assistant message (ErrFeedbackNotAssistant)
//   - rating='down' and comment is empty (ErrFeedbackCommentRequired)
//   - judgeCriteria is empty (ErrFeedbackCriteriaRequired)
//   - the owning session is locked (ErrSessionLocked)
func (r *FeedbackRepository) Upsert(ctx context.Context, messageID string, rating types.FeedbackRating, comment, expectedOutput string, judgeCriteria []string) error {
	if rating == types.FeedbackRatingDown && comment == "" {
		return types.ErrFeedbackCommentRequired
	}
	if len(judgeCriteria) == 0 {
		return types.ErrFeedbackCriteriaRequired
	}
	var role string
	var locked sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT m.role, s.locked_at
		FROM chat_messages m
		JOIN chat_sessions s ON s.id = m.session_id
		WHERE m.id = $1::uuid
	`, messageID).Scan(&role, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if role != string(types.ChatRoleAssistant) {
		return types.ErrFeedbackNotAssistant
	}
	if locked.Valid {
		return types.ErrSessionLocked
	}
	critJSON, err := json.Marshal(judgeCriteria)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO chat_message_feedback (message_id, rating, comment, expected_output, judge_criteria)
		VALUES ($1::uuid, $2, $3, $4, $5::jsonb)
		ON CONFLICT (message_id) DO UPDATE
		SET rating = EXCLUDED.rating,
		    comment = EXCLUDED.comment,
		    expected_output = EXCLUDED.expected_output,
		    judge_criteria = EXCLUDED.judge_criteria,
		    updated_at = now()
	`, messageID, string(rating), comment, expectedOutput, critJSON)
	return err
}

// Get returns the feedback row for messageID, or ErrNotFound if none.
func (r *FeedbackRepository) Get(ctx context.Context, messageID string) (*types.ChatMessageFeedback, error) {
	fb := &types.ChatMessageFeedback{MessageID: messageID}
	var rating string
	var critJSON []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT rating, comment, expected_output, judge_criteria, created_at, updated_at
		FROM chat_message_feedback
		WHERE message_id = $1::uuid
	`, messageID).Scan(&rating, &fb.Comment, &fb.ExpectedOutput, &critJSON, &fb.CreatedAt, &fb.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	fb.Rating = types.FeedbackRating(rating)
	if len(critJSON) > 0 {
		if err := json.Unmarshal(critJSON, &fb.JudgeCriteria); err != nil {
			return nil, err
		}
	}
	if fb.JudgeCriteria == nil {
		fb.JudgeCriteria = []string{}
	}
	return fb, nil
}

// Delete removes a feedback row. Refuses if the session is locked.
// Idempotent — deleting a missing row returns nil.
func (r *FeedbackRepository) Delete(ctx context.Context, messageID string) error {
	var locked sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT s.locked_at
		FROM chat_messages m JOIN chat_sessions s ON s.id = m.session_id
		WHERE m.id = $1::uuid
	`, messageID).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if locked.Valid {
		return types.ErrSessionLocked
	}
	_, err = r.db.ExecContext(ctx, `DELETE FROM chat_message_feedback WHERE message_id = $1::uuid`, messageID)
	return err
}

// GetForSession returns messageID → feedback for every feedbacked message
// in the session, so the chat view can render footer state in one query.
func (r *FeedbackRepository) GetForSession(ctx context.Context, sessionID string) (map[string]*types.ChatMessageFeedback, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT f.message_id::text, f.rating, f.comment, f.expected_output, f.judge_criteria, f.created_at, f.updated_at
		FROM chat_message_feedback f
		JOIN chat_messages m ON m.id = f.message_id
		WHERE m.session_id = $1::uuid
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*types.ChatMessageFeedback{}
	for rows.Next() {
		fb := &types.ChatMessageFeedback{}
		var rating string
		var critJSON []byte
		if err := rows.Scan(&fb.MessageID, &rating, &fb.Comment, &fb.ExpectedOutput, &critJSON, &fb.CreatedAt, &fb.UpdatedAt); err != nil {
			return nil, err
		}
		fb.Rating = types.FeedbackRating(rating)
		if len(critJSON) > 0 {
			_ = json.Unmarshal(critJSON, &fb.JudgeCriteria)
		}
		if fb.JudgeCriteria == nil {
			fb.JudgeCriteria = []string{}
		}
		out[fb.MessageID] = fb
	}
	return out, rows.Err()
}
