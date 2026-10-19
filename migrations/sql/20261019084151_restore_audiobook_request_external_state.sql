-- +goose Up
-- Movie and series fulfillment state belongs to media_request_targets. The
-- audiobook import lifecycle also stores its aggregate provider phase on the
-- request while a scan is pending, independently of an individual target.
ALTER TABLE public.media_requests
    ADD COLUMN IF NOT EXISTS external_status text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE public.media_requests
    DROP COLUMN IF EXISTS external_status;
