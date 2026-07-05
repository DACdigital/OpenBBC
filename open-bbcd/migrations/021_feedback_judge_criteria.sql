-- migrations/021_feedback_judge_criteria.sql
-- Adds a list of acceptance-criteria bullets to each feedback row. Blank
-- criteria are allowed at insert time (casual capture); dataset close-draft
-- refuses if any member session still has a feedback row with an empty list.
-- See docs/superpowers/specs/2026-07-02-agent-eval-on-dataset-version-design.md.

-- +goose Up
ALTER TABLE chat_message_feedback
    ADD COLUMN judge_criteria JSONB NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(judge_criteria) = 'array');

-- +goose Down
ALTER TABLE chat_message_feedback DROP COLUMN judge_criteria;
