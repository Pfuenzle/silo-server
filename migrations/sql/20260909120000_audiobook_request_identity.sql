-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.media_requests
    ALTER COLUMN tmdb_id DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS provider_item_id text NOT NULL DEFAULT '';

ALTER TABLE public.media_requests
    DROP CONSTRAINT IF EXISTS media_requests_provider_check,
    DROP CONSTRAINT IF EXISTS media_requests_media_type_check,
    DROP CONSTRAINT IF EXISTS media_requests_tmdb_positive;

ALTER TABLE public.media_requests
    ADD CONSTRAINT media_requests_provider_check
        CHECK (provider IN ('tmdb', 'audiobook-metadata')),
    ADD CONSTRAINT media_requests_media_type_check
        CHECK (media_type IN ('movie', 'series', 'audiobook')),
    ADD CONSTRAINT media_requests_identity_check
        CHECK (
            (provider = 'tmdb' AND tmdb_id IS NOT NULL AND tmdb_id > 0)
            OR (provider = 'audiobook-metadata' AND provider_item_id <> '')
        );

CREATE UNIQUE INDEX IF NOT EXISTS idx_media_requests_active_provider_item
    ON public.media_requests (provider, provider_item_id)
    WHERE outcome = 'active' AND status <> 'completed' AND provider_item_id <> '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_media_requests_active_provider_item;
ALTER TABLE public.media_requests
    DROP CONSTRAINT IF EXISTS media_requests_identity_check,
    DROP CONSTRAINT IF EXISTS media_requests_provider_check,
    DROP CONSTRAINT IF EXISTS media_requests_media_type_check;
ALTER TABLE public.media_requests
    DROP COLUMN IF EXISTS provider_item_id,
    ALTER COLUMN tmdb_id SET NOT NULL;
ALTER TABLE public.media_requests
    ADD CONSTRAINT media_requests_provider_check CHECK (provider IN ('tmdb')),
    ADD CONSTRAINT media_requests_media_type_check CHECK (media_type IN ('movie', 'series')),
    ADD CONSTRAINT media_requests_tmdb_positive CHECK (tmdb_id > 0);
-- +goose StatementEnd
