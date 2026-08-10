-- +goose Up
-- +goose StatementBegin
ALTER TABLE auth_sessions
    ADD COLUMN provider_key TEXT;

CREATE INDEX idx_auth_sessions_active_provider_key
    ON auth_sessions (provider_key)
    WHERE revoked_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION USING MESSAGE = 'external auth migration is irreversible; roll back binaries only';
END $$;
-- +goose StatementEnd
