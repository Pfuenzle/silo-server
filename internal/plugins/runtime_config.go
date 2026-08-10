package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/secret"
)

var (
	ErrAuthBindingNotFound                 = errors.New("plugin auth binding not found")
	ErrAuthBindingTrustedLinkModeInvalid   = errors.New("plugin auth binding trusted link mode is invalid")
	ErrAuthBindingDefaultLoginNotEnabled   = errors.New("plugin auth binding default login must be enabled")
	ErrAuthBindingDefaultLoginConflict     = errors.New("plugin auth binding default login already assigned")
	ErrAuthBindingDefaultLoginRevokePolicy = errors.New("plugin auth binding default login policy requires session revocation")
	ErrAuthBindingNoActor                  = errors.New("policy audit requires an authenticated actor")
	ErrTaskBindingNotFound                 = errors.New("plugin task binding not found")
)

type RuntimeConfig struct {
	InstallationID int
	Key            string
	Value          map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type AuthBinding struct {
	InstallationID    int
	CapabilityID      string
	Enabled           bool
	DisplayOrder      int
	AutoProvision     bool
	DefaultLogin      bool
	AuthorizationMode AuthBindingAuthorizationMode
	TrustedLinkMode   AuthBindingTrustedLinkMode
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type AuthBindingTrustedLinkMode string

const (
	AuthBindingTrustedLinkModeDisabled        AuthBindingTrustedLinkMode = "disabled"
	AuthBindingTrustedLinkModeTrustedExisting AuthBindingTrustedLinkMode = "trusted_existing"
)

func (binding AuthBinding) EffectiveTrustedLinkMode() AuthBindingTrustedLinkMode {
	if binding.TrustedLinkMode == "" {
		return AuthBindingTrustedLinkModeDisabled
	}
	return binding.TrustedLinkMode
}

func (binding AuthBinding) HasSupportedTrustedLinkMode() bool {
	switch binding.EffectiveTrustedLinkMode() {
	case AuthBindingTrustedLinkModeDisabled, AuthBindingTrustedLinkModeTrustedExisting:
		return true
	default:
		return false
	}
}

type AuthBindingAuthorizationMode string

const (
	AuthBindingAuthorizationModeNone             AuthBindingAuthorizationMode = "none"
	AuthBindingAuthorizationModeExternalGroupsV1 AuthBindingAuthorizationMode = "external_groups_v1"
)

func (binding AuthBinding) EffectiveAuthorizationMode() AuthBindingAuthorizationMode {
	if binding.AuthorizationMode == "" {
		return AuthBindingAuthorizationModeNone
	}
	return binding.AuthorizationMode
}

func (binding AuthBinding) HasSupportedAuthorizationMode() bool {
	switch binding.EffectiveAuthorizationMode() {
	case AuthBindingAuthorizationModeNone, AuthBindingAuthorizationModeExternalGroupsV1:
		return true
	default:
		return false
	}
}

type TaskBinding struct {
	InstallationID int
	CapabilityID   string
	Enabled        bool
	Trigger        map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type RuntimeConfigStore struct {
	pool   *pgxpool.Pool
	cipher *secret.Cipher
}

// plugin_runtime_configs.config_value is intentionally an opaque, whole-row
// encrypted envelope. Plugins may write undeclared keys and manifest secret
// annotations can drift or be unavailable during startup backfill, so field
// classification is an Admin redaction concern, not the at-rest boundary.
// Runtime code must use RuntimeConfigStore rather than querying JSON members.
const encryptedRuntimeConfigField = "__silo_encrypted_runtime_config_v1"

// NewRuntimeConfigStore creates the plugin config store. Production callers
// pass the server data cipher; the variadic form keeps DB-only tests concise.
func NewRuntimeConfigStore(pool *pgxpool.Pool, ciphers ...*secret.Cipher) *RuntimeConfigStore {
	var cipher *secret.Cipher
	if len(ciphers) > 0 {
		cipher = ciphers[0]
	}
	return &RuntimeConfigStore{pool: pool, cipher: cipher}
}

func (s *RuntimeConfigStore) AuthGroupMappings() *AuthGroupMappingStore {
	return NewAuthGroupMappingStore(s.pool)
}

func (s *RuntimeConfigStore) PutGlobalConfig(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
) error {
	if value == nil {
		value = map[string]any{}
	}
	valueJSON, err := encodeRuntimeConfigValue(s.cipher, installationID, key, value)
	if err != nil {
		return fmt.Errorf("marshaling plugin runtime config: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO plugin_runtime_configs (plugin_installation_id, config_key, config_value)
		VALUES ($1, $2, $3)
		ON CONFLICT (plugin_installation_id, config_key) DO UPDATE SET
			config_value = EXCLUDED.config_value,
			updated_at = NOW()
	`, installationID, key, valueJSON)
	if err != nil {
		return fmt.Errorf("upserting plugin runtime config: %w", err)
	}
	return nil
}

// CompareAndSwapGlobalConfig persists value only when the row still matches
// the version the caller merged. A nil expectedUpdatedAt creates the row only
// when it does not already exist.
func (s *RuntimeConfigStore) CompareAndSwapGlobalConfig(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
	expectedUpdatedAt *time.Time,
) (bool, error) {
	if value == nil {
		value = map[string]any{}
	}
	valueJSON, err := encodeRuntimeConfigValue(s.cipher, installationID, key, value)
	if err != nil {
		return false, fmt.Errorf("marshaling plugin runtime config: %w", err)
	}

	var tag pgconn.CommandTag
	if expectedUpdatedAt == nil {
		tag, err = s.pool.Exec(ctx, `
			INSERT INTO plugin_runtime_configs (plugin_installation_id, config_key, config_value)
			VALUES ($1, $2, $3)
			ON CONFLICT (plugin_installation_id, config_key) DO NOTHING
		`, installationID, key, valueJSON)
	} else {
		tag, err = s.pool.Exec(ctx, `
			UPDATE plugin_runtime_configs
			SET config_value = $3, updated_at = NOW()
			WHERE plugin_installation_id = $1
				AND config_key = $2
				AND updated_at = $4
		`, installationID, key, valueJSON, *expectedUpdatedAt)
	}
	if err != nil {
		return false, fmt.Errorf("compare-and-swap plugin runtime config: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *RuntimeConfigStore) ListGlobalConfigs(ctx context.Context, installationID int) ([]*RuntimeConfig, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT plugin_installation_id, config_key, config_value, created_at, updated_at
		FROM plugin_runtime_configs
		WHERE plugin_installation_id = $1
		ORDER BY config_key ASC
	`, installationID)
	if err != nil {
		return nil, fmt.Errorf("listing plugin runtime configs: %w", err)
	}
	defer rows.Close()

	var configs []*RuntimeConfig
	for rows.Next() {
		var config RuntimeConfig
		var valueJSON []byte
		if err := rows.Scan(
			&config.InstallationID,
			&config.Key,
			&valueJSON,
			&config.CreatedAt,
			&config.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning plugin runtime config: %w", err)
		}
		config.Value, err = decodeRuntimeConfigValue(s.cipher, config.InstallationID, config.Key, valueJSON)
		if err != nil {
			return nil, fmt.Errorf("decoding plugin runtime config %d/%s: %w", config.InstallationID, config.Key, err)
		}
		configs = append(configs, &config)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin runtime configs: %w", err)
	}
	return configs, nil
}

func runtimeConfigAAD(installationID int, key string) string {
	return secret.RowAAD(
		"plugin_runtime_configs",
		"config_value",
		strconv.Itoa(installationID)+":"+key,
	)
}

func encodeRuntimeConfigValue(
	cipher *secret.Cipher,
	installationID int,
	key string,
	value map[string]any,
) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	plaintext, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshaling plugin runtime config: %w", err)
	}
	return encodeRuntimeConfigJSON(cipher, installationID, key, plaintext)
}

func encodeRuntimeConfigJSON(
	cipher *secret.Cipher,
	installationID int,
	key string,
	plaintext []byte,
) ([]byte, error) {
	if !json.Valid(plaintext) {
		return nil, errors.New("plugin runtime config is not valid JSON")
	}
	if cipher == nil {
		return append([]byte(nil), plaintext...), nil
	}
	ciphertext, err := cipher.Encrypt(string(plaintext), runtimeConfigAAD(installationID, key))
	if err != nil {
		return nil, fmt.Errorf("encrypting plugin runtime config: %w", err)
	}
	wrapped, err := json.Marshal(map[string]string{encryptedRuntimeConfigField: ciphertext})
	if err != nil {
		return nil, fmt.Errorf("marshaling encrypted plugin runtime config: %w", err)
	}
	return wrapped, nil
}

func decodeRuntimeConfigValue(
	cipher *secret.Cipher,
	installationID int,
	key string,
	valueJSON []byte,
) (map[string]any, error) {
	if len(valueJSON) == 0 {
		return map[string]any{}, nil
	}
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(valueJSON, &wrapped); err != nil {
		return nil, fmt.Errorf("unmarshaling plugin runtime config: %w", err)
	}
	if rawCiphertext, ok := wrapped[encryptedRuntimeConfigField]; ok && len(wrapped) == 1 {
		if cipher == nil {
			return nil, errors.New("encrypted plugin runtime config requires the server data cipher")
		}
		var ciphertext string
		if err := json.Unmarshal(rawCiphertext, &ciphertext); err != nil || !secret.IsEncrypted(ciphertext) {
			return nil, errors.New("invalid encrypted plugin runtime config envelope")
		}
		plaintext, err := cipher.Decrypt(ciphertext, runtimeConfigAAD(installationID, key))
		if err != nil {
			return nil, fmt.Errorf("decrypting plugin runtime config: %w", err)
		}
		valueJSON = []byte(plaintext)
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(valueJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("unmarshaling plugin runtime config value: %w", err)
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, nil
}

// BackfillEncryptedConfigs wraps legacy plaintext JSON objects with the same
// row-bound encryption used by PutGlobalConfig. It is idempotent and may be
// rerun after a partial failure.
func (s *RuntimeConfigStore) BackfillEncryptedConfigs(ctx context.Context) (int, error) {
	if s == nil || s.pool == nil || s.cipher == nil {
		return 0, nil
	}
	return backfillEncryptedConfigs(ctx, s.pool, s.cipher)
}

func backfillEncryptedConfigs(
	ctx context.Context,
	db secret.Executor,
	cipher *secret.Cipher,
) (int, error) {
	rows, err := db.Query(ctx, `
		SELECT id, plugin_installation_id, config_key, config_value
		FROM plugin_runtime_configs
		ORDER BY id ASC
	`)
	if err != nil {
		return 0, fmt.Errorf("listing plugin runtime configs for encryption backfill: %w", err)
	}
	type rowValue struct {
		id             int64
		installationID int
		key            string
		valueJSON      []byte
	}
	var pending []rowValue
	for rows.Next() {
		var row rowValue
		if err := rows.Scan(&row.id, &row.installationID, &row.key, &row.valueJSON); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scanning plugin runtime config for encryption backfill: %w", err)
		}
		var wrapped map[string]json.RawMessage
		if err := json.Unmarshal(row.valueJSON, &wrapped); err != nil {
			rows.Close()
			return 0, fmt.Errorf("decode plugin runtime config %d for encryption backfill: %w", row.id, err)
		}
		if raw, ok := wrapped[encryptedRuntimeConfigField]; ok && len(wrapped) == 1 {
			var ciphertext string
			if json.Unmarshal(raw, &ciphertext) == nil && secret.IsEncrypted(ciphertext) {
				continue
			}
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterating plugin runtime configs for encryption backfill: %w", err)
	}
	rows.Close()

	updated := 0
	for _, row := range pending {
		encoded, err := encodeRuntimeConfigJSON(
			cipher,
			row.installationID,
			row.key,
			row.valueJSON,
		)
		if err != nil {
			return updated, fmt.Errorf("encrypt plugin runtime config %d: %w", row.id, err)
		}
		tag, err := db.Exec(ctx,
			`UPDATE plugin_runtime_configs
			 SET config_value = $2, updated_at = updated_at
			 WHERE id = $1 AND config_value = $3::jsonb`,
			row.id,
			encoded,
			row.valueJSON,
		)
		if err != nil {
			return updated, fmt.Errorf("update plugin runtime config %d encryption backfill: %w", row.id, err)
		}
		if tag.RowsAffected() > 0 {
			updated++
		}
	}
	return updated, nil
}

// UpsertAuthBinding persists the given auth binding. actorUserID is the
// authenticated admin user performing the change; it is recorded in the
// policy audit trail. Pass a positive user ID — zero or negative returns
// ErrAuthBindingNoActor before any database mutation.
func (s *RuntimeConfigStore) UpsertAuthBinding(ctx context.Context, actorUserID int, binding AuthBinding) error {
	if actorUserID <= 0 {
		return ErrAuthBindingNoActor
	}
	if !binding.HasSupportedTrustedLinkMode() {
		return ErrAuthBindingTrustedLinkModeInvalid
	}
	if binding.DefaultLogin && !binding.Enabled {
		return ErrAuthBindingDefaultLoginNotEnabled
	}
	if binding.DefaultLogin {
		conflict, err := s.defaultLoginConflict(ctx, binding.InstallationID, binding.CapabilityID)
		if err != nil {
			return fmt.Errorf("checking default login conflict: %w", err)
		}
		if conflict {
			return ErrAuthBindingDefaultLoginConflict
		}
	}
	providerKey, err := models.NewPluginSessionProviderKey(binding.InstallationID, binding.CapabilityID)
	if err != nil {
		return fmt.Errorf("auth binding provider key: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning auth binding update: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuthGroupMappingInstallation(ctx, tx, binding.InstallationID); err != nil {
		return err
	}
	var previousEnabled bool
	var previousMode AuthBindingAuthorizationMode
	var previousDefaultLogin bool
	var previousTrustedLinkMode AuthBindingTrustedLinkMode
	var previousExists bool
	err = tx.QueryRow(ctx, `SELECT enabled, authorization_mode, default_login, trusted_link_mode FROM plugin_auth_bindings WHERE plugin_installation_id = $1 AND capability_id = $2 FOR UPDATE`, binding.InstallationID, binding.CapabilityID).Scan(&previousEnabled, &previousMode, &previousDefaultLogin, &previousTrustedLinkMode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("locking plugin auth binding: %w", err)
	}
	previousExists = err == nil
	_, err = tx.Exec(ctx, `
		INSERT INTO plugin_auth_bindings (
			plugin_installation_id, capability_id, enabled, display_order, auto_provision, default_login, authorization_mode, trusted_link_mode
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (plugin_installation_id, capability_id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			display_order = EXCLUDED.display_order,
			auto_provision = EXCLUDED.auto_provision,
			default_login = EXCLUDED.default_login,
			authorization_mode = EXCLUDED.authorization_mode,
			trusted_link_mode = EXCLUDED.trusted_link_mode,
			updated_at = NOW()
	`,
		binding.InstallationID,
		binding.CapabilityID,
		binding.Enabled,
		binding.DisplayOrder,
		binding.AutoProvision,
		binding.DefaultLogin,
		binding.EffectiveAuthorizationMode(),
		binding.EffectiveTrustedLinkMode(),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && binding.DefaultLogin {
			return ErrAuthBindingDefaultLoginConflict
		}
		return fmt.Errorf("upserting plugin auth binding: %w", err)
	}
	removingExternalAuthorization := previousMode == AuthBindingAuthorizationModeExternalGroupsV1 && binding.EffectiveAuthorizationMode() != AuthBindingAuthorizationModeExternalGroupsV1
	policyChanged := previousExists && (removingExternalAuthorization ||
		(previousEnabled && !binding.Enabled) ||
		(previousDefaultLogin && !binding.DefaultLogin) ||
		(previousTrustedLinkMode != binding.EffectiveTrustedLinkMode()))
	if previousEnabled && (!binding.Enabled || removingExternalAuthorization) {
		if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = NOW() WHERE provider_key = $1 AND revoked_at IS NULL`, providerKey.String()); err != nil {
			return fmt.Errorf("revoking auth provider sessions: %w", err)
		}
		reason := "provider_disabled"
		if binding.Enabled {
			reason = "authorization_mode_removed"
		}
		if err := demoteExternalAuthorization(ctx, tx, binding.InstallationID, &binding.CapabilityID, reason); err != nil {
			return err
		}
	} else if policyChanged {
		if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = NOW() WHERE provider_key = $1 AND revoked_at IS NULL`, providerKey.String()); err != nil {
			return fmt.Errorf("revoking auth provider sessions on policy change: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO auth_provider_policy_audit (actor_user_id, plugin_installation_id, capability_id, old_trusted_link_mode, new_trusted_link_mode, old_default_login, new_default_login, reason_code, correlation_id) VALUES ($1, $2, $3, $4, $5, $6, $7, 'policy_change', gen_random_uuid())`, actorUserID, binding.InstallationID, binding.CapabilityID, string(previousTrustedLinkMode), string(binding.EffectiveTrustedLinkMode()), previousDefaultLogin, binding.DefaultLogin); err != nil {
			return fmt.Errorf("recording policy audit: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing auth binding update: %w", err)
	}
	return nil
}

// defaultLoginConflict returns true when another binding already claims default_login.
func (s *RuntimeConfigStore) defaultLoginConflict(ctx context.Context, excludeInstallationID int, excludeCapabilityID string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM plugin_auth_bindings
		WHERE default_login = true
		  AND (plugin_installation_id, capability_id) != ($1, $2)
	`, excludeInstallationID, excludeCapabilityID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("count default login bindings: %w", err)
	}
	return count > 0, nil
}

func (s *RuntimeConfigStore) GetAuthBinding(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*AuthBinding, error) {
	var binding AuthBinding
	err := s.pool.QueryRow(ctx, `
		SELECT plugin_installation_id, capability_id, enabled, display_order, auto_provision, default_login, authorization_mode, trusted_link_mode, created_at, updated_at
		FROM plugin_auth_bindings
		WHERE plugin_installation_id = $1 AND capability_id = $2
	`, installationID, capabilityID).Scan(
		&binding.InstallationID,
		&binding.CapabilityID,
		&binding.Enabled,
		&binding.DisplayOrder,
		&binding.AutoProvision,
		&binding.DefaultLogin,
		&binding.AuthorizationMode,
		&binding.TrustedLinkMode,
		&binding.CreatedAt,
		&binding.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAuthBindingNotFound
		}
		return nil, fmt.Errorf("getting plugin auth binding: %w", err)
	}
	return &binding, nil
}

func (s *RuntimeConfigStore) ListAuthBindings(ctx context.Context) ([]*AuthBinding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT plugin_installation_id, capability_id, enabled, display_order, auto_provision, default_login, authorization_mode, trusted_link_mode, created_at, updated_at
		FROM plugin_auth_bindings
		ORDER BY display_order ASC, plugin_installation_id ASC, capability_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing plugin auth bindings: %w", err)
	}
	defer rows.Close()

	var bindings []*AuthBinding
	for rows.Next() {
		var binding AuthBinding
		if err := rows.Scan(
			&binding.InstallationID,
			&binding.CapabilityID,
			&binding.Enabled,
			&binding.DisplayOrder,
			&binding.AutoProvision,
			&binding.DefaultLogin,
			&binding.AuthorizationMode,
			&binding.TrustedLinkMode,
			&binding.CreatedAt,
			&binding.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning plugin auth binding: %w", err)
		}
		bindings = append(bindings, &binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin auth bindings: %w", err)
	}
	return bindings, nil
}

func (s *RuntimeConfigStore) UpsertTaskBinding(ctx context.Context, binding TaskBinding) error {
	trigger := binding.Trigger
	if trigger == nil {
		trigger = map[string]any{}
	}
	triggerJSON, err := json.Marshal(trigger)
	if err != nil {
		return fmt.Errorf("marshaling plugin task binding trigger: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO plugin_task_bindings (plugin_installation_id, capability_id, enabled, trigger)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (plugin_installation_id, capability_id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			trigger = EXCLUDED.trigger,
			updated_at = NOW()
	`,
		binding.InstallationID,
		binding.CapabilityID,
		binding.Enabled,
		triggerJSON,
	)
	if err != nil {
		return fmt.Errorf("upserting plugin task binding: %w", err)
	}
	return nil
}

func (s *RuntimeConfigStore) GetTaskBinding(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*TaskBinding, error) {
	var binding TaskBinding
	var triggerJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT plugin_installation_id, capability_id, enabled, trigger, created_at, updated_at
		FROM plugin_task_bindings
		WHERE plugin_installation_id = $1 AND capability_id = $2
	`, installationID, capabilityID).Scan(
		&binding.InstallationID,
		&binding.CapabilityID,
		&binding.Enabled,
		&triggerJSON,
		&binding.CreatedAt,
		&binding.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTaskBindingNotFound
		}
		return nil, fmt.Errorf("getting plugin task binding: %w", err)
	}
	binding.Trigger = map[string]any{}
	if len(triggerJSON) > 0 {
		if err := json.Unmarshal(triggerJSON, &binding.Trigger); err != nil {
			return nil, fmt.Errorf("unmarshaling plugin task binding trigger: %w", err)
		}
	}
	return &binding, nil
}

func (s *RuntimeConfigStore) ListTaskBindings(ctx context.Context) ([]*TaskBinding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT plugin_installation_id, capability_id, enabled, trigger, created_at, updated_at
		FROM plugin_task_bindings
		ORDER BY plugin_installation_id ASC, capability_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing plugin task bindings: %w", err)
	}
	defer rows.Close()

	var bindings []*TaskBinding
	for rows.Next() {
		var binding TaskBinding
		var triggerJSON []byte
		if err := rows.Scan(
			&binding.InstallationID,
			&binding.CapabilityID,
			&binding.Enabled,
			&triggerJSON,
			&binding.CreatedAt,
			&binding.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning plugin task binding: %w", err)
		}
		binding.Trigger = map[string]any{}
		if len(triggerJSON) > 0 {
			if err := json.Unmarshal(triggerJSON, &binding.Trigger); err != nil {
				return nil, fmt.Errorf("unmarshaling plugin task binding trigger: %w", err)
			}
		}
		bindings = append(bindings, &binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin task bindings: %w", err)
	}
	return bindings, nil
}
