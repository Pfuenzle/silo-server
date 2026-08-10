package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestClassifyAuthFailure_InvalidCredentials(t *testing.T) {
	got := ClassifyAuthFailure(ErrInvalidCredentials)
	if got != AuthReasonInvalidCredentials {
		t.Fatalf("ClassifyAuthFailure(ErrInvalidCredentials) = %q, want %q", got, AuthReasonInvalidCredentials)
	}
}

func TestClassifyAuthFailure_UserDisabled(t *testing.T) {
	got := ClassifyAuthFailure(ErrUserDisabled)
	if got != AuthReasonUserDisabled {
		t.Fatalf("ClassifyAuthFailure(ErrUserDisabled) = %q, want %q", got, AuthReasonUserDisabled)
	}
}

func TestClassifyAuthFailure_ExternalAuthorizationConflict(t *testing.T) {
	got := ClassifyAuthFailure(ErrExternalAuthorizationConflict)
	if got != AuthReasonExternalAuthorizationConflict {
		t.Fatalf("got %q, want %q", got, AuthReasonExternalAuthorizationConflict)
	}
}

func TestClassifyAuthFailure_TrustedLinkOwnershipConflict(t *testing.T) {
	err := &TrustedLinkOwnershipConflictError{InstallationID: 42, CapabilityID: "ldap", ExistingUserID: 7, ExternalSubject: "should-not-leak"}
	got := ClassifyAuthFailure(err)
	if got != AuthReasonTrustedLinkConflict {
		t.Fatalf("got %q, want %q", got, AuthReasonTrustedLinkConflict)
	}
}

func TestClassifyAuthFailure_CanonicalizationErrors(t *testing.T) {
	cases := []struct {
		err  error
		want AuthFailureReason
	}{
		{ErrCanonicalizationForbidden, AuthReasonCanonicalizationForbidden},
		{ErrCanonicalizationBlocked, AuthReasonCanonicalizationBlocked},
		{ErrCanonicalizationStale, AuthReasonCanonicalizationStale},
	}
	for _, tc := range cases {
		t.Run(string(tc.want), func(t *testing.T) {
			if got := ClassifyAuthFailure(tc.err); got != tc.want {
				t.Fatalf("ClassifyAuthFailure(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestClassifyAuthFailure_ExternalGroupsInvalid(t *testing.T) {
	got := ClassifyAuthFailure(ErrExternalGroupsInvalid)
	if got != AuthReasonExternalGroupsInvalid {
		t.Fatalf("got %q, want %q", got, AuthReasonExternalGroupsInvalid)
	}
}

func TestClassifyAuthFailure_UnknownMapsToInternal(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"random_db_error", errors.New("some random db error")},
		{"wrapped_postgres", fmt.Errorf("wrapped: %w", errors.New("postgres connection refused"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyAuthFailure(tc.err)
			if got != AuthReasonInternalError {
				t.Fatalf("ClassifyAuthFailure(%v) = %q, want %q", tc.err, got, AuthReasonInternalError)
			}
		})
	}
}

func TestClassifyAuthFailure_OAuthLinkingUnsupported(t *testing.T) {
	got := ClassifyAuthFailure(ErrOAuthLinkingUnsupported)
	if got != AuthReasonLinkingUnsupported {
		t.Fatalf("ClassifyAuthFailure(ErrOAuthLinkingUnsupported) = %q, want %q", got, AuthReasonLinkingUnsupported)
	}
}

func TestClassifyAuthFailure_NilReturnsEmpty(t *testing.T) {
	if got := ClassifyAuthFailure(nil); got != "" {
		t.Fatalf("ClassifyAuthFailure(nil) = %q, want empty", got)
	}
}

func TestClassifyAuthFailure_ProviderUnavailable(t *testing.T) {
	got := ClassifyAuthFailure(ErrProviderUnavailable)
	if got != AuthReasonProviderUnavailable {
		t.Fatalf("got %q, want %q", got, AuthReasonProviderUnavailable)
	}
}

func TestClassifyAuthFailure_PluginConfigInvalid(t *testing.T) {
	got := ClassifyAuthFailure(ErrPluginConfigInvalid)
	if got != AuthReasonPluginConfigInvalid {
		t.Fatalf("got %q, want %q", got, AuthReasonPluginConfigInvalid)
	}
}

func TestClassifyAuthFailure_AuditStorageError(t *testing.T) {
	got := ClassifyAuthFailure(ErrAuditStorageError)
	if got != AuthReasonAuditStorageError {
		t.Fatalf("got %q, want %q", got, AuthReasonAuditStorageError)
	}
}

func TestClassifyAuthFailure_SessionError(t *testing.T) {
	got := ClassifyAuthFailure(ErrSessionError)
	if got != AuthReasonSessionError {
		t.Fatalf("got %q, want %q", got, AuthReasonSessionError)
	}
}

func TestClassifyAuthFailure_AllReasonsCoveredMatrix(t *testing.T) {
	matrix := []struct {
		name string
		err  error
		want AuthFailureReason
	}{
		{"invalid_credentials", ErrInvalidCredentials, AuthReasonInvalidCredentials},
		{"user_disabled", ErrUserDisabled, AuthReasonUserDisabled},
		{"provider_unavailable", ErrProviderUnavailable, AuthReasonProviderUnavailable},
		{"plugin_config_invalid", ErrPluginConfigInvalid, AuthReasonPluginConfigInvalid},
		{"audit_storage_error", ErrAuditStorageError, AuthReasonAuditStorageError},
		{"session_error", ErrSessionError, AuthReasonSessionError},
		{"external_authorization_conflict", ErrExternalAuthorizationConflict, AuthReasonExternalAuthorizationConflict},
		{"trusted_link_conflict", ErrTrustedLinkOwnershipConflict, AuthReasonTrustedLinkConflict},
		{"canonicalization_forbidden", ErrCanonicalizationForbidden, AuthReasonCanonicalizationForbidden},
		{"canonicalization_blocked", ErrCanonicalizationBlocked, AuthReasonCanonicalizationBlocked},
		{"canonicalization_stale", ErrCanonicalizationStale, AuthReasonCanonicalizationStale},
		{"external_groups_invalid", ErrExternalGroupsInvalid, AuthReasonExternalGroupsInvalid},
		{"unknown_maps_to_internal", errors.New("random failure"), AuthReasonInternalError},
		{"oauth_linking_unsupported", ErrOAuthLinkingUnsupported, AuthReasonLinkingUnsupported},
	}
	for _, tc := range matrix {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAuthFailure(tc.err); got != tc.want {
				t.Fatalf("ClassifyAuthFailure(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestClassifyAuthFailure_WrappedSentinelsDetected(t *testing.T) {
	matrix := []struct {
		name string
		err  error
		want AuthFailureReason
	}{
		{"wrapped_invalid_credentials", fmt.Errorf("acquiring lock: %w", ErrInvalidCredentials), AuthReasonInvalidCredentials},
		{"wrapped_provider_unavailable", fmt.Errorf("grpc dial: %w", ErrProviderUnavailable), AuthReasonProviderUnavailable},
		{"wrapped_plugin_config", fmt.Errorf("init plugin: %w", ErrPluginConfigInvalid), AuthReasonPluginConfigInvalid},
		{"wrapped_audit_storage", fmt.Errorf("write audit: %w", ErrAuditStorageError), AuthReasonAuditStorageError},
		{"wrapped_session_error", fmt.Errorf("create session: %w", ErrSessionError), AuthReasonSessionError},
		{"wrapped_user_disabled", fmt.Errorf("check status: %w", ErrUserDisabled), AuthReasonUserDisabled},
		{"wrapped_trusted_link", fmt.Errorf("link check: %w", ErrTrustedLinkOwnershipConflict), AuthReasonTrustedLinkConflict},
		{"wrapped_ext_auth_conflict", fmt.Errorf("mapping: %w", ErrExternalAuthorizationConflict), AuthReasonExternalAuthorizationConflict},
		{"wrapped_canon_forbidden", fmt.Errorf("auth check: %w", ErrCanonicalizationForbidden), AuthReasonCanonicalizationForbidden},
		{"wrapped_canon_blocked", fmt.Errorf("preview: %w", ErrCanonicalizationBlocked), AuthReasonCanonicalizationBlocked},
		{"wrapped_canon_stale", fmt.Errorf("token: %w", ErrCanonicalizationStale), AuthReasonCanonicalizationStale},
		{"wrapped_ext_groups", fmt.Errorf("parse groups: %w", ErrExternalGroupsInvalid), AuthReasonExternalGroupsInvalid},
		{"wrapped_linking_unsupported", fmt.Errorf("link attempt: %w", ErrOAuthLinkingUnsupported), AuthReasonLinkingUnsupported},
	}
	for _, tc := range matrix {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAuthFailure(tc.err); got != tc.want {
				t.Fatalf("ClassifyAuthFailure(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestClassifyOAuthFailure_PhaseBasedFallback(t *testing.T) {
	cases := []struct {
		phase string
		want  AuthFailureReason
	}{
		{"init_authorize", AuthReasonPluginUnavailable},
		{"resolve_client", AuthReasonPluginUnavailable},
		{"exchange_code", AuthReasonExchangeFailed},
		{"state_store", AuthReasonStateStoreUnavailable},
		{"completion_store", AuthReasonCompletionUnavailable},
		{"login_completion", AuthReasonInternalError},
		{"unknown_phase", AuthReasonInternalError},
	}
	for _, tc := range cases {
		t.Run(tc.phase, func(t *testing.T) {
			got := ClassifyOAuthFailure(errors.New("gRPC transport error"), tc.phase)
			if got != tc.want {
				t.Fatalf("ClassifyOAuthFailure(err, %q) = %q, want %q", tc.phase, got, tc.want)
			}
		})
	}
}

func TestClassifyOAuthFailure_TypedErrorOverridesPhase(t *testing.T) {
	got := ClassifyOAuthFailure(ErrInvalidCredentials, "exchange_code")
	if got != AuthReasonInvalidCredentials {
		t.Fatalf("typed error should override phase: got %q, want %q", got, AuthReasonInvalidCredentials)
	}
}

func TestClassifyOAuthFailure_NilReturnsEmpty(t *testing.T) {
	if got := ClassifyOAuthFailure(nil, "any"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestStatusClassFor(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{200, "2xx"},
		{201, "2xx"},
		{302, "2xx"},
		{400, "4xx"},
		{401, "4xx"},
		{403, "4xx"},
		{500, "5xx"},
		{502, "5xx"},
		{503, "5xx"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("status_%d", tc.status), func(t *testing.T) {
			if got := StatusClassFor(tc.status); got != tc.want {
				t.Fatalf("StatusClassFor(%d) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

func TestLogAuthFailure_EmitsOnlySafeFields(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	LogAuthFailure(AuthFailureDiagnostic{
		Reason:         AuthReasonTrustedLinkConflict,
		RequestID:      "req-abc-123",
		InstallationID: 42,
		CapabilityID:   "ldap",
		StatusClass:    "5xx",
		DurationMs:     150,
		Component:      "oauth",
	})

	logOutput := buf.String()
	for _, want := range []string{
		"reason=trusted_link_conflict",
		"request_id=req-abc-123",
		"installation_id=42",
		"capability_id=ldap",
		"status_class=5xx",
		"duration_ms=150",
		"component=oauth",
	} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log missing %q: %s", want, logOutput)
		}
	}
}

func TestLogAuthFailure_DoesNotEmitSensitiveFields(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	LogAuthFailure(AuthFailureDiagnostic{
		Reason:    AuthReasonInternalError,
		RequestID: "req-xyz",
	})

	logOutput := buf.String()
	forbidden := []string{
		"password", "secret", "token", "oauth_secret",
		"external_subject", "asserted_username", "email",
		"ldap_dn", "upstream_body", "sql", "stack",
		"code", "state", "access_token", "refresh_token",
	}
	for _, term := range forbidden {
		if strings.Contains(strings.ToLower(logOutput), term) {
			t.Errorf("log leaked forbidden term %q: %s", term, logOutput)
		}
	}
}

func TestLogAuthFailure_InvalidCredentialsEmitsWarn(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	LogAuthFailure(AuthFailureDiagnostic{
		Reason:    AuthReasonInvalidCredentials,
		RequestID: "req-1",
	})

	logOutput := buf.String()
	if !strings.Contains(logOutput, "level=WARN") {
		t.Errorf("invalid_credentials should emit WARN: %s", logOutput)
	}
	if strings.Contains(logOutput, "level=ERROR") {
		t.Errorf("invalid_credentials must not emit ERROR: %s", logOutput)
	}
}

func TestLogAuthFailure_InternalErrorEmitsError(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	LogAuthFailure(AuthFailureDiagnostic{
		Reason:    AuthReasonInternalError,
		RequestID: "req-2",
	})

	logOutput := buf.String()
	if !strings.Contains(logOutput, "level=ERROR") {
		t.Errorf("auth_internal_error should emit ERROR: %s", logOutput)
	}
}

func TestFormatAuthDiagnosticSafe_ContainsOnlySafeFields(t *testing.T) {
	diag := AuthFailureDiagnostic{
		Reason:         AuthReasonPluginUnavailable,
		RequestID:      "req-abc",
		InstallationID: 7,
		CapabilityID:   "oidc",
		StatusClass:    "5xx",
		DurationMs:     42,
	}
	safe := FormatAuthDiagnosticSafe(diag)
	if !strings.Contains(safe, "reason=plugin_unavailable") {
		t.Errorf("missing reason: %s", safe)
	}
	if !strings.Contains(safe, "install=7") {
		t.Errorf("missing install: %s", safe)
	}
	for _, forbidden := range []string{"password", "token", "secret", "code", "subject"} {
		if strings.Contains(strings.ToLower(safe), forbidden) {
			t.Errorf("safe format leaked %q: %s", forbidden, safe)
		}
	}
}

func TestNewAuthFailureDiagnostic_ConstructsWithRequestID(t *testing.T) {
	start := time.Now().Add(-100 * time.Millisecond)
	ctx := context.Background()
	diag := NewAuthFailureDiagnostic(ctx, AuthReasonInvalidCredentials, 401, start, "auth")
	if diag.Reason != AuthReasonInvalidCredentials {
		t.Errorf("reason = %q", diag.Reason)
	}
	if diag.StatusClass != "4xx" {
		t.Errorf("status class = %q", diag.StatusClass)
	}
	if diag.Component != "auth" {
		t.Errorf("component = %q", diag.Component)
	}
	if diag.DurationMs < 90 {
		t.Errorf("duration_ms = %d, want >= 90", diag.DurationMs)
	}
}

func TestNewOAuthFailureDiagnostic_ContainsInstallationID(t *testing.T) {
	start := time.Now()
	ctx := context.Background()
	diag := NewOAuthFailureDiagnostic(ctx, AuthReasonExchangeFailed, 502, start, 42, "ldap")
	if diag.InstallationID != 42 {
		t.Errorf("installation_id = %d", diag.InstallationID)
	}
	if diag.CapabilityID != "ldap" {
		t.Errorf("capability_id = %q", diag.CapabilityID)
	}
	if diag.Component != "oauth" {
		t.Errorf("component = %q", diag.Component)
	}
}

func TestSanitizeLogAttrs_StrictAllowlist(t *testing.T) {
	attrs := []slog.Attr{
		slog.String("reason", "invalid_credentials"),
		slog.String("request_id", "req-1"),
		slog.String("installation_id", "42"),
		slog.String("capability_id", "ldap"),
		slog.String("status_class", "4xx"),
		slog.String("duration_ms", "150"),
		slog.String("component", "auth"),
		slog.String("username", "alice"),
		slog.String("password", "hunter2"),
		slog.String("access_token", "secret-jwt"),
		slog.String("api_key", "sk-123"),
		slog.String("external_subject", "uid=alice"),
		slog.String("arbitrary_field", "should-be-dropped"),
	}
	safe := SanitizeLogAttrs(attrs)
	safeKeys := make(map[string]bool)
	for _, a := range safe {
		safeKeys[a.Key] = true
	}
	for _, want := range []string{"reason", "request_id", "installation_id", "capability_id", "status_class", "duration_ms", "component"} {
		if !safeKeys[want] {
			t.Errorf("expected allowed key %q was dropped", want)
		}
	}
	for _, forbidden := range []string{"username", "password", "access_token", "api_key", "external_subject", "arbitrary_field"} {
		if safeKeys[forbidden] {
			t.Errorf("forbidden key %q was not dropped", forbidden)
		}
	}
	if len(safe) != 7 {
		t.Errorf("allowed count = %d, want 7", len(safe))
	}
}

func TestSanitizeLogAttrs_EmptyInputReturnsEmpty(t *testing.T) {
	safe := SanitizeLogAttrs(nil)
	if len(safe) != 0 {
		t.Errorf("nil input returned %d attrs", len(safe))
	}
	safe = SanitizeLogAttrs([]slog.Attr{})
	if len(safe) != 0 {
		t.Errorf("empty input returned %d attrs", len(safe))
	}
}

func TestSanitizeLogAttrs_AdversarialForbiddenCorpus(t *testing.T) {
	adversarial := []slog.Attr{
		slog.String("username", "alice"),
		slog.String("email", "alice@example.com"),
		slog.String("display_name", "Alice"),
		slog.String("ldap_dn", "CN=svc_ldap,DC=corp"),
		slog.String("upstream_body", "500 Internal Server Error"),
		slog.String("sql_error", "duplicate key"),
		slog.String("stack_trace", "goroutine 1..."),
		slog.String("external_subject", "uid=alice,ou=people"),
		slog.String("asserted_username", "alice"),
		slog.String("access_token", "eyJhbGciOi..."),
		slog.String("refresh_token", "eyJhbGciOi..."),
		slog.String("oauth_code", "auth-code-123"),
		slog.String("state", "signed-state-value"),
		slog.String("provider_response", "{\"sub\": \"alice\"}"),
		slog.String("claims", "{\"email\": \"a@b.com\"}"),
		slog.String("user_agent", "Mozilla/5.0..."),
		slog.String("ip_address", "10.0.0.5"),
		slog.Int("user_id", 42),
	}
	safe := SanitizeLogAttrs(adversarial)
	if len(safe) != 0 {
		keys := make([]string, len(safe))
		for i, a := range safe {
			keys[i] = a.Key
		}
		t.Errorf("adversarial corpus: %d attrs passed allowlist: %v", len(safe), keys)
	}
}

func TestStatusForReason_MapsAllReasonsToExpectedHTTPStatus(t *testing.T) {
	matrix := []struct {
		reason AuthFailureReason
		status int
	}{
		{AuthReasonInvalidCredentials, 401},
		{AuthReasonUserDisabled, 403},
		{AuthReasonLinkingUnsupported, 403},
		{AuthReasonCanonicalizationForbidden, 403},
		{AuthReasonExternalGroupsInvalid, 400},
		{AuthReasonCanonicalizationBlocked, 409},
		{AuthReasonCanonicalizationStale, 409},
		{AuthReasonProviderUnavailable, 502},
		{AuthReasonPluginUnavailable, 502},
		{AuthReasonExchangeFailed, 502},
		{AuthReasonCompletionUnavailable, 503},
		{AuthReasonInternalError, 500},
		{AuthReasonPluginConfigInvalid, 500},
		{AuthReasonTrustedLinkConflict, 500},
		{AuthReasonExternalAuthorizationConflict, 500},
		{AuthReasonAuditStorageError, 500},
		{AuthReasonSessionError, 500},
		{AuthReasonStateStoreUnavailable, 500},
	}
	for _, tc := range matrix {
		t.Run(string(tc.reason), func(t *testing.T) {
			if got := StatusForReason(tc.reason); got != tc.status {
				t.Fatalf("StatusForReason(%q) = %d, want %d", tc.reason, got, tc.status)
			}
		})
	}
}

func TestStatusForReason_UnknownReasonReturns500(t *testing.T) {
	if got := StatusForReason(AuthFailureReason("bogus")); got != 500 {
		t.Fatalf("StatusForReason(bogus) = %d, want 500", got)
	}
}

func TestStatusClassFor_StatusForReason_Consistency(t *testing.T) {
	reasons := []AuthFailureReason{
		AuthReasonInvalidCredentials,
		AuthReasonUserDisabled,
		AuthReasonProviderUnavailable,
		AuthReasonPluginUnavailable,
		AuthReasonExchangeFailed,
		AuthReasonInternalError,
		AuthReasonLinkingUnsupported,
		AuthReasonCompletionUnavailable,
	}
	for _, reason := range reasons {
		status := StatusForReason(reason)
		class := StatusClassFor(status)
		if status >= 400 && class == "2xx" {
			t.Errorf("reason=%q → status=%d → class=%q (should not be 2xx)", reason, status, class)
		}
	}
}
