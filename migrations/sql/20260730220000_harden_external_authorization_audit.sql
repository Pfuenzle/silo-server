-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION external_authorization_matched_group_ids_valid(ids TEXT[])
RETURNS BOOLEAN
LANGUAGE SQL
IMMUTABLE
AS $$
    SELECT cardinality(ids) <= 100
       AND COALESCE(bool_and(group_id IS NOT NULL AND octet_length(group_id) BETWEEN 1 AND 256), TRUE)
    FROM unnest(ids) AS group_id;
$$;
-- +goose StatementEnd

ALTER TABLE external_authorization_audit
    DROP CONSTRAINT IF EXISTS external_authorization_audit_old_access_group_id_fkey,
    DROP CONSTRAINT IF EXISTS external_authorization_audit_new_access_group_id_fkey;

ALTER TABLE external_authorization_audit
    ADD CONSTRAINT external_authorization_audit_capability_id_length
        CHECK (octet_length(capability_id) BETWEEN 1 AND 128),
    ADD CONSTRAINT external_authorization_audit_reason_length
        CHECK (octet_length(reason) BETWEEN 1 AND 64),
    ADD CONSTRAINT external_authorization_audit_matched_group_ids_bounds
        CHECK (external_authorization_matched_group_ids_valid(matched_group_ids)),
    ADD CONSTRAINT external_authorization_audit_installation_id_bounds
        CHECK (plugin_installation_id > 0);

ALTER TABLE external_authorization_states
    ADD CONSTRAINT external_authorization_states_capability_id_length
        CHECK (octet_length(capability_id) BETWEEN 1 AND 128),
    ADD CONSTRAINT external_authorization_states_installation_id_bounds
        CHECK (plugin_installation_id > 0);

ALTER TABLE plugin_auth_bindings
    ADD CONSTRAINT plugin_auth_bindings_capability_id_length
        CHECK (octet_length(capability_id) BETWEEN 1 AND 128),
    ADD CONSTRAINT plugin_auth_bindings_installation_id_bounds
        CHECK (plugin_installation_id > 0);

ALTER TABLE plugin_auth_group_mappings
    ADD CONSTRAINT plugin_auth_group_mappings_external_group_id_length
        CHECK (octet_length(external_group_id) BETWEEN 1 AND 256),
    ADD CONSTRAINT plugin_auth_group_mappings_installation_id_bounds
        CHECK (plugin_installation_id > 0);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION USING MESSAGE = 'external auth migration is irreversible; roll back binaries only';
END $$;
-- +goose StatementEnd
