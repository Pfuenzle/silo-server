-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.media_requests
    ADD COLUMN IF NOT EXISTS external_library_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS external_download_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS external_detail text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS imported_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS scan_run_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS silo_audiobook_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS silo_audiobook_link text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS retryable boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.media_requests
    DROP COLUMN IF EXISTS retryable,
    DROP COLUMN IF EXISTS silo_audiobook_link,
    DROP COLUMN IF EXISTS silo_audiobook_id,
    DROP COLUMN IF EXISTS scan_run_id,
    DROP COLUMN IF EXISTS imported_path,
    DROP COLUMN IF EXISTS external_detail,
    DROP COLUMN IF EXISTS external_download_id,
    DROP COLUMN IF EXISTS external_library_id;
-- +goose StatementEnd
