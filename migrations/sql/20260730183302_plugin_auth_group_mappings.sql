-- +goose Up
-- +goose StatementBegin
ALTER TABLE plugin_auth_bindings
    ADD COLUMN authorization_mode TEXT NOT NULL DEFAULT 'none'
    CHECK (authorization_mode IN ('none', 'external_groups_v1'));

CREATE TABLE plugin_auth_group_mappings (
    id                     BIGSERIAL PRIMARY KEY,
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE CASCADE,
    external_group_id      TEXT NOT NULL CHECK (octet_length(external_group_id) BETWEEN 1 AND 256 AND BTRIM(external_group_id) <> '' AND external_group_id NOT LIKE '%*%'),
    target_role            TEXT CHECK (target_role IS NULL OR target_role IN ('user', 'admin')),
    access_group_id        BIGINT REFERENCES public.access_groups(id) ON DELETE RESTRICT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (plugin_installation_id, external_group_id),
    CHECK (target_role IS NOT NULL OR access_group_id IS NOT NULL)
);

CREATE INDEX idx_plugin_auth_group_mappings_installation
    ON plugin_auth_group_mappings (plugin_installation_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION USING MESSAGE = 'external auth migration is irreversible; roll back binaries only';
END $$;
-- +goose StatementEnd
