-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION external_authorization_matched_group_ids_valid(ids TEXT[])
RETURNS BOOLEAN
LANGUAGE SQL
IMMUTABLE
AS $$
    SELECT cardinality(ids) <= 100
       AND COALESCE(bool_and(octet_length(group_id) BETWEEN 1 AND 256), TRUE)
    FROM unnest(ids) AS group_id;
$$;
-- +goose StatementEnd

CREATE TABLE external_authorization_states (
    user_id                BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE RESTRICT,
    capability_id          TEXT NOT NULL CHECK (octet_length(capability_id) BETWEEN 1 AND 128),
    PRIMARY KEY (user_id, plugin_installation_id, capability_id)
);

CREATE TABLE external_authorization_audit (
    id                     BIGSERIAL PRIMARY KEY,
    user_id                BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE RESTRICT,
    capability_id          TEXT NOT NULL CHECK (octet_length(capability_id) BETWEEN 1 AND 128),
    old_role               TEXT NOT NULL,
    new_role               TEXT NOT NULL,
    old_access_group_id    BIGINT,
    new_access_group_id    BIGINT,
    matched_group_ids      TEXT[] NOT NULL CHECK (external_authorization_matched_group_ids_valid(matched_group_ids)),
    reason                 TEXT NOT NULL CHECK (octet_length(reason) BETWEEN 1 AND 64),
    correlation_id         UUID NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_external_authorization_audit_user_created
    ON external_authorization_audit (user_id, created_at DESC);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION USING MESSAGE = 'external auth migration is irreversible; roll back binaries only';
END $$;
-- +goose StatementEnd
