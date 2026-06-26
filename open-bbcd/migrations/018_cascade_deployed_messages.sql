-- +goose Up
-- deployed_messages.agent_version_id was NO ACTION, which blocks agent
-- delete: cascading from agents → agent_versions errors on any deployed
-- message rows that still pin a version. The session cascade chain
-- (deployed_sessions → deployed_messages via session_id) handles cleanup
-- functionally; switch the version FK to CASCADE so the constraint
-- evaluation no longer races against statement order.
ALTER TABLE deployed_messages
    DROP CONSTRAINT deployed_messages_agent_version_id_fkey;
ALTER TABLE deployed_messages
    ADD CONSTRAINT deployed_messages_agent_version_id_fkey
    FOREIGN KEY (agent_version_id) REFERENCES agent_versions(id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE deployed_messages
    DROP CONSTRAINT deployed_messages_agent_version_id_fkey;
ALTER TABLE deployed_messages
    ADD CONSTRAINT deployed_messages_agent_version_id_fkey
    FOREIGN KEY (agent_version_id) REFERENCES agent_versions(id);
