package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalizeDeviceLoginPurpose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		purpose   string
		temporary bool
		want      string
		wantTemp  bool
		wantErr   error
	}{
		{name: "legacy default", want: DeviceLoginPurposeLogin},
		{name: "explicit login", purpose: DeviceLoginPurposeLogin, want: DeviceLoginPurposeLogin},
		{name: "remote playback", purpose: DeviceLoginPurposeRemote, temporary: true, want: DeviceLoginPurposeRemote, wantTemp: true},
		{name: "temporary generic rejected", temporary: true, wantErr: ErrDeviceLoginBadPurpose},
		{name: "non-temporary remote rejected", purpose: DeviceLoginPurposeRemote, wantErr: ErrDeviceLoginBadPurpose},
		{name: "unknown rejected", purpose: "other", wantErr: ErrDeviceLoginBadPurpose},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, gotTemp, err := normalizeDeviceLoginPurpose(tt.purpose, tt.temporary)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want || gotTemp != tt.wantTemp {
				t.Fatalf("purpose = (%q, %v), want (%q, %v)", got, gotTemp, tt.want, tt.wantTemp)
			}
		})
	}
}

func TestSameApprovedIdentityIncludesProfile(t *testing.T) {
	t.Parallel()

	userID := 7
	profileID := "profile-a"
	record := &deviceLoginRecord{
		ApprovedByUserID:  &userID,
		ApprovedProfileID: &profileID,
	}
	if !sameApprovedIdentity(record, userID, profileID) {
		t.Fatal("matching user/profile was not idempotent")
	}
	if sameApprovedIdentity(record, userID, "profile-b") {
		t.Fatal("different profile was treated as the same identity")
	}
	if sameApprovedIdentity(record, 8, profileID) {
		t.Fatal("different user was treated as the same identity")
	}
}

func TestValidateDeviceLoginDecisionTerminalStates(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(time.Minute)
	tests := []struct {
		status string
		want   error
	}{
		{status: DeviceLoginStatusPending},
		{status: DeviceLoginStatusApproved},
		{status: DeviceLoginStatusDenied, want: ErrDeviceLoginDenied},
		{status: DeviceLoginStatusConsumed, want: ErrDeviceLoginConsumed},
		{status: "unexpected", want: ErrDeviceLoginConflict},
	}
	for _, tt := range tests {
		record := &deviceLoginRecord{Status: tt.status, ExpiresAt: future}
		if err := validateDeviceLoginDecision(record); !errors.Is(err, tt.want) {
			t.Fatalf("status %q error = %v, want %v", tt.status, err, tt.want)
		}
	}
}

func TestDeviceLoginPollRecordsLocalSessionProvenance(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	userID := insertPluginProviderTestUser(t, ctx, pool, "device-session-provider")
	sessions := NewSessionRepository(pool)
	service := NewDeviceLoginService(
		pool,
		NewUserRepository(pool),
		NewJWTService("device-session-provider-test", time.Minute, time.Hour),
		sessions,
		nil,
		nil,
	)
	deviceCode := uuid.NewString()
	browserCode := uuid.NewString()
	record := deviceLoginRecord{
		ID:              uuid.NewString(),
		DeviceCodeHash:  hashDeviceLoginSecret(deviceCode),
		BrowserCodeHash: hashDeviceLoginSecret(browserCode),
		UserCodeHash:    hashDeviceLoginSecret(uuid.NewString()),
		MatchCode:       "match-code",
		DeviceName:      "Device session provider test",
		IPAddress:       "127.0.0.1",
		Status:          DeviceLoginStatusPending,
		ClientPurpose:   DeviceLoginPurposeLogin,
		ExpiresAt:       time.Now().Add(time.Minute),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := service.create(ctx, record); err != nil {
		t.Fatalf("create device login request: %v", err)
	}
	if err := service.Approve(ctx, DeviceLoginLookupInput{BrowserCode: browserCode}, userID); err != nil {
		t.Fatalf("approve device login: %v", err)
	}

	// When
	result, err := service.Poll(ctx, deviceCode)

	// Then
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if result.TokenPair == nil {
		t.Fatal("Poll() returned no token pair")
	}
	var providerKey *string
	if err := pool.QueryRow(ctx, `SELECT provider_key FROM auth_sessions WHERE id = (SELECT auth_session_id FROM device_login_requests WHERE id = $1)`, record.ID).Scan(&providerKey); err != nil {
		t.Fatalf("load created auth session provenance: %v", err)
	}
	if providerKey == nil || *providerKey != "local" {
		t.Fatalf("device login session provider key = %v, want local", providerKey)
	}
}
