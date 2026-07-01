package types

import "time"

// FeedbackRating is the two-state rating attached to an assistant message.
type FeedbackRating string

const (
	FeedbackRatingUp   FeedbackRating = "up"
	FeedbackRatingDown FeedbackRating = "down"
)

// ChatMessageFeedback is a per-assistant-message row.
// The rating='down' branch requires a non-empty comment (enforced by DB CHECK).
// expected_output is optional in either branch.
type ChatMessageFeedback struct {
	MessageID      string         `json:"message_id"`
	Rating         FeedbackRating `json:"rating"`
	Comment        string         `json:"comment"`
	ExpectedOutput string         `json:"expected_output"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}
