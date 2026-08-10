-- +goose Up
ALTER TABLE oauth_session
  ADD COLUMN provider_state_ciphertext TEXT;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION USING MESSAGE = 'external auth migration is irreversible; roll back binaries only';
END $$;
-- +goose StatementEnd
