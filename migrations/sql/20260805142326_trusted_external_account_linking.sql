-- +goose Up
-- +goose StatementBegin
ALTER TABLE plugin_auth_bindings
    ADD COLUMN trusted_link_mode TEXT NOT NULL DEFAULT 'disabled';

UPDATE plugin_auth_bindings SET default_login = false WHERE NOT enabled AND default_login;

UPDATE plugin_auth_bindings SET default_login = false
WHERE (SELECT COUNT(*) FROM plugin_auth_bindings WHERE default_login) > 1;

ALTER TABLE plugin_auth_bindings
    ADD CONSTRAINT plugin_auth_bindings_trusted_link_mode_check
        CHECK (trusted_link_mode IN ('disabled', 'trusted_existing')),
    ADD CONSTRAINT plugin_auth_bindings_default_login_enabled_check
        CHECK (NOT default_login OR enabled);

CREATE UNIQUE INDEX idx_plugin_auth_bindings_one_default_login
    ON plugin_auth_bindings (default_login)
    WHERE default_login;

ALTER TABLE external_authorization_audit
    ADD COLUMN original_user_id BIGINT,
    ADD COLUMN original_profile_id TEXT NOT NULL DEFAULT '' CHECK (octet_length(original_profile_id) <= 256),
    ADD COLUMN canonicalization_audit_id BIGINT;

UPDATE external_authorization_audit
SET original_user_id = user_id
WHERE original_user_id IS NULL;

ALTER TABLE external_authorization_audit
    ALTER COLUMN original_user_id SET NOT NULL,
    ADD CONSTRAINT external_authorization_audit_original_user_id_bounds CHECK (original_user_id > 0);

CREATE FUNCTION preserve_external_authorization_audit_source()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.canonicalization_audit_id IS NOT NULL THEN
        RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = 'external authorization audit canonicalization must be authorized by re-home';
    END IF;
    NEW.original_user_id := NEW.user_id;
    RETURN NEW;
END;
$$;

CREATE TRIGGER external_authorization_audit_preserve_source
BEFORE INSERT ON external_authorization_audit
FOR EACH ROW EXECUTE FUNCTION preserve_external_authorization_audit_source();

CREATE TABLE external_identity_link_audit (
    id                            BIGSERIAL PRIMARY KEY,
    user_id                       BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    original_user_id              BIGINT NOT NULL CHECK (original_user_id > 0),
    original_profile_id           TEXT NOT NULL DEFAULT '' CHECK (octet_length(original_profile_id) <= 256),
    plugin_installation_id        BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE RESTRICT,
    capability_id                 TEXT NOT NULL CHECK (octet_length(capability_id) BETWEEN 1 AND 128),
    event_type                    TEXT NOT NULL CHECK (event_type IN ('trusted_link', 'canonicalization')),
    trusted_link_mode             TEXT NOT NULL CHECK (trusted_link_mode IN ('disabled', 'trusted_existing')),
    external_subject_fingerprint  TEXT NOT NULL CHECK (external_subject_fingerprint ~ '^[0-9a-f]{64}$'),
    asserted_username_fingerprint TEXT CHECK (asserted_username_fingerprint IS NULL OR asserted_username_fingerprint ~ '^[0-9a-f]{64}$'),
    reason_code                   TEXT NOT NULL CHECK (octet_length(reason_code) BETWEEN 1 AND 64),
    correlation_id                UUID NOT NULL,
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT external_identity_link_audit_provenance CHECK (
        (event_type = 'trusted_link' AND original_user_id = user_id AND original_profile_id = '')
        OR (event_type = 'canonicalization' AND original_user_id <> user_id AND octet_length(original_profile_id) BETWEEN 1 AND 256)
    )
);

CREATE INDEX idx_external_identity_link_audit_user_created
    ON external_identity_link_audit (user_id, created_at DESC);

CREATE FUNCTION reject_external_identity_link_audit_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = 'external identity link audit is immutable';
END;
$$;

CREATE TRIGGER external_identity_link_audit_immutable
BEFORE UPDATE OR DELETE ON external_identity_link_audit
FOR EACH ROW EXECUTE FUNCTION reject_external_identity_link_audit_mutation();

ALTER TABLE external_authorization_audit
    ADD CONSTRAINT external_authorization_audit_canonicalization_audit_id_fkey
        FOREIGN KEY (canonicalization_audit_id) REFERENCES external_identity_link_audit(id) ON DELETE RESTRICT;

CREATE INDEX idx_external_authorization_audit_canonicalization_audit
    ON external_authorization_audit (canonicalization_audit_id)
    WHERE canonicalization_audit_id IS NOT NULL;

CREATE UNIQUE INDEX idx_external_identity_link_audit_one_canonicalization_per_source
    ON external_identity_link_audit (original_user_id)
    WHERE event_type = 'canonicalization';

CREATE FUNCTION rehome_external_authorization_audit()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.user_id = OLD.original_user_id
       AND NEW.user_id <> OLD.user_id
       AND NEW.original_user_id = OLD.original_user_id
       AND OLD.original_profile_id = ''
       AND octet_length(NEW.original_profile_id) BETWEEN 1 AND 256
       AND OLD.canonicalization_audit_id IS NULL
       AND NEW.canonicalization_audit_id IS NOT NULL
       AND NEW.plugin_installation_id = OLD.plugin_installation_id
       AND NEW.capability_id = OLD.capability_id
       AND NEW.old_role = OLD.old_role
       AND NEW.new_role = OLD.new_role
       AND NEW.old_access_group_id IS NOT DISTINCT FROM OLD.old_access_group_id
       AND NEW.new_access_group_id IS NOT DISTINCT FROM OLD.new_access_group_id
       AND NEW.matched_group_ids IS NOT DISTINCT FROM OLD.matched_group_ids
       AND NEW.reason = OLD.reason
       AND NEW.correlation_id = OLD.correlation_id
       AND NEW.created_at = OLD.created_at
       AND EXISTS (
           SELECT 1 FROM external_identity_link_audit canonicalization
           WHERE canonicalization.id = NEW.canonicalization_audit_id
             AND canonicalization.event_type = 'canonicalization'
             AND canonicalization.original_user_id = OLD.original_user_id
             AND canonicalization.user_id = NEW.user_id
             AND canonicalization.original_profile_id = NEW.original_profile_id
             AND canonicalization.plugin_installation_id = NEW.plugin_installation_id
             AND canonicalization.capability_id = NEW.capability_id
       ) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = 'external authorization audit update is not an authorized canonicalization re-home';
END;
$$;

CREATE TRIGGER external_authorization_audit_rehome
BEFORE UPDATE ON external_authorization_audit
FOR EACH ROW EXECUTE FUNCTION rehome_external_authorization_audit();

CREATE TABLE auth_provider_policy_audit (
	id                    BIGSERIAL PRIMARY KEY,
	actor_user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE RESTRICT,
    capability_id         TEXT NOT NULL CHECK (octet_length(capability_id) BETWEEN 1 AND 128),
    old_trusted_link_mode TEXT NOT NULL CHECK (old_trusted_link_mode IN ('disabled', 'trusted_existing')),
    new_trusted_link_mode TEXT NOT NULL CHECK (new_trusted_link_mode IN ('disabled', 'trusted_existing')),
    old_default_login     BOOLEAN NOT NULL,
    new_default_login     BOOLEAN NOT NULL,
    reason_code           TEXT NOT NULL CHECK (octet_length(reason_code) BETWEEN 1 AND 64),
    correlation_id        UUID NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT auth_provider_policy_audit_truthful_change CHECK (
        old_trusted_link_mode <> new_trusted_link_mode
        OR old_default_login <> new_default_login
    )
);

CREATE INDEX idx_auth_provider_policy_audit_installation_created
    ON auth_provider_policy_audit (plugin_installation_id, created_at DESC);

CREATE INDEX idx_auth_provider_policy_audit_actor_created
    ON auth_provider_policy_audit (actor_user_id, created_at DESC);

CREATE FUNCTION reject_auth_provider_policy_audit_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = 'auth provider policy audit is immutable';
END;
$$;

CREATE TRIGGER auth_provider_policy_audit_immutable
BEFORE UPDATE OR DELETE ON auth_provider_policy_audit
FOR EACH ROW EXECUTE FUNCTION reject_auth_provider_policy_audit_mutation();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION USING MESSAGE = 'trusted external account linking migration is irreversible; roll back binaries only';
END $$;
-- +goose StatementEnd
