-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.media_requests
    ADD COLUMN IF NOT EXISTS fulfillment_key text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS submission_state text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS submission_started_at timestamp with time zone;

UPDATE public.media_requests
SET fulfillment_key = id
WHERE fulfillment_key = '';

ALTER TABLE public.media_requests
    ADD CONSTRAINT media_requests_submission_state_check
    CHECK (submission_state IN ('', 'in_flight', 'fulfilled', 'failed'));

CREATE UNIQUE INDEX IF NOT EXISTS idx_media_requests_fulfillment_key
    ON public.media_requests (fulfillment_key)
    WHERE fulfillment_key <> '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_media_requests_fulfillment_key;
ALTER TABLE public.media_requests
    DROP CONSTRAINT IF EXISTS media_requests_submission_state_check,
    DROP COLUMN IF EXISTS submission_started_at,
    DROP COLUMN IF EXISTS submission_state,
    DROP COLUMN IF EXISTS fulfillment_key;
-- +goose StatementEnd
