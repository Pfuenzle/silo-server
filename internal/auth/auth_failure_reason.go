package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// AuthFailureReason classifies the internal cause of an authentication failure
// for structured diagnostic logging. Values are allowlisted safe strings — they
// never contain raw credentials, external subjects, OAuth tokens, or upstream
// error text. Unknown errors map to AuthReasonInternalError.
type AuthFailureReason string

const (
	// AuthReasonInvalidCredentials is the default for bad username/password
	// or unknown external subject. It is intentionally broad so callers cannot
	// distinguish "user does not exist" from "wrong password".
	AuthReasonInvalidCredentials AuthFailureReason = "invalid_credentials"

	// AuthReasonUserDisabled reports that the account is disabled.
	AuthReasonUserDisabled AuthFailureReason = "user_disabled"

	// AuthReasonProviderUnavailable reports that the plugin gRPC client could
	// not be resolved or the plugin was unreachable.
	AuthReasonProviderUnavailable AuthFailureReason = "provider_unavailable"

	// AuthReasonPluginUnavailable is the OAuth-specific alias for an
	// InitAuthorize/ExchangeCode gRPC failure.
	AuthReasonPluginUnavailable AuthFailureReason = "plugin_unavailable"

	// AuthReasonPluginConfigInvalid reports that the plugin binding is
	// missing, disabled, or has an unsupported authorization_mode.
	AuthReasonPluginConfigInvalid AuthFailureReason = "plugin_config_invalid"

	// AuthReasonTrustedLinkConflict reports that another external subject
	// already owns the asserted username for this installation/capability.
	AuthReasonTrustedLinkConflict AuthFailureReason = "trusted_link_conflict"

	// AuthReasonExternalAuthorizationConflict reports that two LDAP group
	// mappings resolve to different role/access-group targets.
	AuthReasonExternalAuthorizationConflict AuthFailureReason = "external_authorization_conflict"

	// AuthReasonCanonicalizationForbidden reports that the actor is not
	// authorized to run canonicalization.
	AuthReasonCanonicalizationForbidden AuthFailureReason = "canonicalization_forbidden"

	// AuthReasonCanonicalizationBlocked reports that the source account has
	// blockers preventing canonicalization.
	AuthReasonCanonicalizationBlocked AuthFailureReason = "canonicalization_blocked"

	// AuthReasonCanonicalizationStale reports that the preview token has
	// expired or the account state changed.
	AuthReasonCanonicalizationStale AuthFailureReason = "canonicalization_stale"

	// AuthReasonExchangeFailed reports an OAuth ExchangeCode gRPC error.
	AuthReasonExchangeFailed AuthFailureReason = "exchange_failed"

	// AuthReasonAuditStorageError reports that writing to an audit or
	// operational log table failed.
	AuthReasonAuditStorageError AuthFailureReason = "audit_storage_error"

	// AuthReasonSessionError reports a session creation or lookup failure.
	AuthReasonSessionError AuthFailureReason = "session_error"

	// AuthReasonExternalGroupsInvalid reports that the LDAP groups claims
	// envelope was malformed.
	AuthReasonExternalGroupsInvalid AuthFailureReason = "external_groups_invalid"

	// AuthReasonStateStoreUnavailable reports that the OAuth session store
	// (state persistence) could not be reached.
	AuthReasonStateStoreUnavailable AuthFailureReason = "state_store_unavailable"

	// AuthReasonCompletionUnavailable reports that the OAuth completion
	// store is nil or unreachable.
	AuthReasonCompletionUnavailable AuthFailureReason = "completion_unavailable"

	// AuthReasonLinkingUnsupported reports that OAuth account linking was
	// attempted but the installation/capability does not support it.
	AuthReasonLinkingUnsupported AuthFailureReason = "linking_unsupported"

	// AuthReasonInternalError is the catch-all for any error that does not
	// match a more specific reason. Public responses remain generic.
	AuthReasonInternalError AuthFailureReason = "auth_internal_error"

	// AuthReasonAccountNotFound reports that the plugin confirmed the
	// account does not exist in its directory. The login flow may fall
	// back to the next provider in the fallback order.
	AuthReasonAccountNotFound AuthFailureReason = "account_not_found"
)

// AuthFailureDiagnostic carries only safe fields for structured diagnostic
// logging at the HTTP handler boundary. It never includes raw credentials,
// OAuth tokens, external subjects, asserted usernames, emails, DN/filters,
// upstream response bodies, SQL, or stack traces.
type AuthFailureDiagnostic struct {
	// Reason is the allowlisted reason code.
	Reason AuthFailureReason

	// RequestID is the chi-generated request identifier (UUID).
	RequestID string

	// InstallationID is the plugin installation ID, if the failure path
	// involves a plugin. Zero when not applicable.
	InstallationID int

	// CapabilityID is the plugin capability slug (e.g. "ldap", "oidc"),
	// only when the binding policy permits disclosing it.
	CapabilityID string

	// StatusClass is the HTTP status class ("4xx", "5xx") for log filtering.
	StatusClass string

	// DurationMs is the wall-clock time of the auth attempt.
	DurationMs int64

	// Component identifies the subsystem ("auth", "oauth").
	Component string
}

// ClassifyAuthFailure maps an error to an allowlisted AuthFailureReason.
// The classification is conservative: anything unrecognized becomes
// AuthReasonInternalError. This prevents internal error text from leaking
// into logs via reason codes. Wrapped errors are matched via errors.Is
// without logging or exposing the wrapped text.
func ClassifyAuthFailure(err error) AuthFailureReason {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		return AuthReasonInvalidCredentials
	case errors.Is(err, ErrAccountNotFound):
		return AuthReasonAccountNotFound
	case errors.Is(err, ErrUserDisabled):
		return AuthReasonUserDisabled
	case errors.Is(err, ErrProviderUnavailable):
		return AuthReasonProviderUnavailable
	case errors.Is(err, ErrPluginConfigInvalid):
		return AuthReasonPluginConfigInvalid
	case errors.Is(err, ErrAuditStorageError):
		return AuthReasonAuditStorageError
	case errors.Is(err, ErrSessionError):
		return AuthReasonSessionError
	case errors.Is(err, ErrExternalAuthorizationConflict):
		return AuthReasonExternalAuthorizationConflict
	case errors.Is(err, ErrTrustedLinkOwnershipConflict):
		return AuthReasonTrustedLinkConflict
	case errors.Is(err, ErrCanonicalizationForbidden):
		return AuthReasonCanonicalizationForbidden
	case errors.Is(err, ErrCanonicalizationBlocked):
		return AuthReasonCanonicalizationBlocked
	case errors.Is(err, ErrCanonicalizationStale):
		return AuthReasonCanonicalizationStale
	case errors.Is(err, ErrExternalGroupsInvalid):
		return AuthReasonExternalGroupsInvalid
	case errors.Is(err, ErrOAuthLinkingUnsupported):
		return AuthReasonLinkingUnsupported
	default:
		return AuthReasonInternalError
	}
}

// ClassifyOAuthFailure maps an OAuth handler error (which may be a gRPC
// transport error rather than a typed sentinel) to a reason code. It is
// intentionally separate from ClassifyAuthFailure because the OAuth path
// uses different diagnostics (plugin_unavailable vs provider_unavailable).
func ClassifyOAuthFailure(err error, phase string) AuthFailureReason {
	if err == nil {
		return ""
	}
	classified := ClassifyAuthFailure(err)
	if classified != AuthReasonInternalError {
		return classified
	}
	// OAuth-specific fallbacks based on the call phase.
	switch phase {
	case "init_authorize", "resolve_client":
		return AuthReasonPluginUnavailable
	case "exchange_code":
		return AuthReasonExchangeFailed
	case "login_completion":
		return AuthReasonInternalError
	case "state_store":
		return AuthReasonStateStoreUnavailable
	case "completion_store":
		return AuthReasonCompletionUnavailable
	default:
		return AuthReasonInternalError
	}
}

// StatusForReason returns the HTTP status code that corresponds to a given
// AuthFailureReason. This is used for diagnostic logging where the logged
// status class must reflect the failure severity, not the actual HTTP response
// code (e.g. OAuth callbacks always return 302 but the diagnostic should
// record the failure class).
func StatusForReason(reason AuthFailureReason) int {
	switch reason {
	case AuthReasonInvalidCredentials, AuthReasonAccountNotFound:
		return http.StatusUnauthorized
	case AuthReasonUserDisabled, AuthReasonCanonicalizationForbidden, AuthReasonLinkingUnsupported:
		return http.StatusForbidden
	case AuthReasonExternalGroupsInvalid:
		return http.StatusBadRequest
	case AuthReasonCanonicalizationBlocked, AuthReasonCanonicalizationStale:
		return http.StatusConflict
	case AuthReasonProviderUnavailable, AuthReasonPluginUnavailable, AuthReasonExchangeFailed:
		return http.StatusBadGateway
	case AuthReasonCompletionUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// StatusClassFor returns the HTTP status class string for log attributes.
func StatusClassFor(status int) string {
	switch {
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500:
		return "5xx"
	default:
		return "2xx"
	}
}

// LogAuthFailure emits a single structured slog entry at the handler boundary.
// It is called once per failed auth request, never duplicated inside
// sub-components. Fields are restricted to the safe allowlist.
func LogAuthFailure(diag AuthFailureDiagnostic) {
	attrs := []any{
		"component", diag.Component,
		"reason", string(diag.Reason),
		"status_class", diag.StatusClass,
		"duration_ms", diag.DurationMs,
	}
	if diag.RequestID != "" {
		attrs = append(attrs, "request_id", diag.RequestID)
	}
	if diag.InstallationID > 0 {
		attrs = append(attrs, "installation_id", diag.InstallationID)
	}
	if diag.CapabilityID != "" {
		attrs = append(attrs, "capability_id", diag.CapabilityID)
	}
	// Invalid credentials are expected user-facing failures, not server errors.
	if diag.Reason == AuthReasonInvalidCredentials {
		slog.Warn("auth failure", attrs...)
		return
	}
	slog.Error("auth failure", attrs...)
}

// RequestIDFromContext extracts the chi request ID, returning empty string
// when no request ID middleware ran.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	return chimw.GetReqID(ctx)
}

// FormatAuthDiagnosticSafe formats a diagnostic for human-readable log
// output. It contains only safe fields and is suitable for log aggregation.
func FormatAuthDiagnosticSafe(diag AuthFailureDiagnostic) string {
	return fmt.Sprintf("reason=%s status=%s dur=%dms req=%s install=%d cap=%s",
		diag.Reason, diag.StatusClass, diag.DurationMs, diag.RequestID, diag.InstallationID, diag.CapabilityID)
}

// NewAuthFailureDiagnostic constructs a diagnostic from an HTTP request context.
func NewAuthFailureDiagnostic(ctx context.Context, reason AuthFailureReason, status int, start time.Time, component string) AuthFailureDiagnostic {
	return AuthFailureDiagnostic{
		Reason:      reason,
		RequestID:   RequestIDFromContext(ctx),
		StatusClass: StatusClassFor(status),
		DurationMs:  time.Since(start).Milliseconds(),
		Component:   component,
	}
}

// NewOAuthFailureDiagnostic constructs a diagnostic for the OAuth path.
func NewOAuthFailureDiagnostic(ctx context.Context, reason AuthFailureReason, status int, start time.Time, installationID int, capabilityID string) AuthFailureDiagnostic {
	return AuthFailureDiagnostic{
		Reason:         reason,
		RequestID:      RequestIDFromContext(ctx),
		InstallationID: installationID,
		CapabilityID:   capabilityID,
		StatusClass:    StatusClassFor(status),
		DurationMs:     time.Since(start).Milliseconds(),
		Component:      "oauth",
	}
}

// allowedLogKeys is the strict allowlist for auth diagnostic log attributes.
// Any key not in this set is dropped. installation_id and capability_id are
// conditionally added by LogAuthFailure when applicable.
var allowedLogKeys = map[string]bool{
	"reason":          true,
	"request_id":      true,
	"installation_id": true,
	"capability_id":   true,
	"status_class":    true,
	"duration_ms":     true,
	"component":       true,
}

// SanitizeLogAttrs enforces a strict allowlist on log attributes. Only keys
// in allowedLogKeys are kept; all others (username, arbitrary provider/user
// payloads, any unlisted key) are dropped. This is the handler-boundary guard;
// the logredact.Handler provides sink-level defense in depth.
func SanitizeLogAttrs(attrs []slog.Attr) []slog.Attr {
	safe := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if allowedLogKeys[a.Key] {
			safe = append(safe, a)
		}
	}
	return safe
}
