package plugins

import (
	"context"
	"testing"
)

func TestAuthBinding_RoundTripsExistingFields_whenPersisted(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	actorID := seedAuthBindingRevocationUser(t, pool)
	store := NewRuntimeConfigStore(pool)
	want := AuthBinding{
		InstallationID:    installationID,
		CapabilityID:      "ldap",
		Enabled:           true,
		DisplayOrder:      7,
		AutoProvision:     true,
		DefaultLogin:      false,
		AuthorizationMode: AuthBindingAuthorizationModeExternalGroupsV1,
	}

	// When
	err := store.UpsertAuthBinding(context.Background(), actorID, want)

	// Then
	if err != nil {
		t.Fatalf("UpsertAuthBinding() error = %v", err)
	}
	got, err := store.GetAuthBinding(context.Background(), installationID, want.CapabilityID)
	if err != nil {
		t.Fatalf("GetAuthBinding() error = %v", err)
	}
	if got.InstallationID != want.InstallationID ||
		got.CapabilityID != want.CapabilityID ||
		got.Enabled != want.Enabled ||
		got.DisplayOrder != want.DisplayOrder ||
		got.AutoProvision != want.AutoProvision ||
		got.DefaultLogin != want.DefaultLogin ||
		got.AuthorizationMode != want.AuthorizationMode {
		t.Fatalf("round-tripped binding = %#v, want %#v", got, want)
	}
}

func TestPluginAuthIdentity_RejectsDuplicateImmutableSubject_whenPersisted(t *testing.T) {
	// Given
	pool := authGroupMappingTestPool(t)
	installationID := seedAuthGroupMappingInstallation(t, pool)
	userID := seedAuthBindingRevocationUser(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, 'baseline-immutable-subject', $2)`, installationID, userID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	// When
	_, err := pool.Exec(ctx, `
		INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, 'baseline-immutable-subject', $2)`, installationID, userID)

	// Then
	if err == nil {
		t.Fatal("duplicate immutable identity was accepted")
	}
}
