package auth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/google/uuid"
)

// Sentinel errors for service operations.
var (
	ErrSessionRevoked          = errors.New("session has been revoked")
	ErrSetupAlreadyComplete    = errors.New("initial setup already complete")
	ErrSignupDisabled          = errors.New("public signups are not enabled")
	ErrImpersonationNotAllowed = errors.New("impersonation not allowed")
	ErrAlreadyImpersonating    = errors.New("already impersonating")
	ErrNotImpersonating        = errors.New("not impersonating")
	ErrPasswordLoginDisabled   = errors.New("local password login is disabled")
	ErrCurrentPasswordInvalid  = errors.New("current password is invalid")
	ErrPasswordTooShort        = errors.New("password is too short")
	ErrPasswordTooLong         = errors.New("password is too long")
)

const (
	MinimumPasswordLength = 8
	MaximumPasswordBytes  = 72
)

// TokenPair holds the access and refresh tokens returned after login or refresh.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // seconds until access token expires
}

// ErrOAuthLinkingUnsupported reports that OAuth account linking is not part of
// the current login contract. Callers must not treat a linking request as a
// normal sign-in for a different account.
var ErrOAuthLinkingUnsupported = errors.New("oauth account linking is unsupported")

// SettingsGetter retrieves server settings by key.
// Implemented by catalog.ServerSettingsRepo.
type SettingsGetter interface {
	Get(ctx context.Context, key string) (string, error)
}

type claimsContextKey struct{}

// WithClaims stores auth claims on the context for auth-owned flows.
func WithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

// ClaimsFromContext retrieves auth claims previously stored with WithClaims.
func ClaimsFromContext(ctx context.Context) *Claims {
	claims, _ := ctx.Value(claimsContextKey{}).(*Claims)
	return claims
}

// Service orchestrates authentication operations using an AuthProvider,
// JWTService, and session/user repositories.
type Service struct {
	provider    AuthProvider
	jwt         *JWTService
	sessions    *SessionRepository
	users       *UserRepository
	inviteCodes *InviteCodeRepository
	settings    SettingsGetter
	providers   map[string]AuthProvider
	metadata    map[string]LoginProviderInfo
	defaultID   string
	accounts    *AccountProvisioner
}

type LoginProviderInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Mode        string `json:"mode"`
	Default     bool   `json:"default"`
	// IconURL is rendered next to the "Sign in with X" button. Set for
	// auth_provider.v1 plugins that ship an icon (icon_url manifest field).
	IconURL string `json:"icon_url,omitempty"`
	// InstallationID is non-zero when the provider is backed by a plugin.
	// The login UI uses it to build /api/v1/auth/oauth/{install_id}/init URLs.
	InstallationID int `json:"installation_id,omitempty"`
}

type RegisteredProvider struct {
	Info     LoginProviderInfo
	Provider AuthProvider
}

// NewService creates a new auth Service with the given dependencies.
func NewService(
	provider AuthProvider,
	jwt *JWTService,
	sessions *SessionRepository,
	users *UserRepository,
	inviteCodes *InviteCodeRepository,
	settings SettingsGetter,
	storeProvider userstore.UserStoreProvider,
) *Service {
	service := &Service{
		provider:    provider,
		jwt:         jwt,
		sessions:    sessions,
		users:       users,
		inviteCodes: inviteCodes,
		settings:    settings,
		providers:   map[string]AuthProvider{},
		metadata:    map[string]LoginProviderInfo{},
		accounts:    NewAccountProvisioner(users, storeProvider),
	}
	if provider != nil {
		service.RegisterProvider(LoginProviderInfo{
			ID:          "local",
			DisplayName: "Local",
			Mode:        "credentials",
			Default:     true,
		}, provider)
	}
	return service
}

// Login authenticates the user with the given credentials and creates a new
// session. Returns a TokenPair containing the access and refresh tokens.
func (s *Service) Login(ctx context.Context, username, password, deviceName, ip string) (*TokenPair, *models.User, error) {
	return s.loginWithProvider(ctx, "local", username, password, deviceName, ip)
}

func (s *Service) LoginWithProvider(
	ctx context.Context,
	providerID string,
	username string,
	password string,
	deviceName string,
	ip string,
) (*TokenPair, *models.User, error) {
	if providerID != "" {
		return s.loginWithProvider(ctx, providerID, username, password, deviceName, ip)
	}
	policy, err := s.loadCredentialProviderPolicy(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("loading credential provider policy: %w", err)
	}
	if policy.IsEmpty() {
		return s.loginWithProvider(ctx, s.defaultID, username, password, deviceName, ip)
	}
	return s.loginWithFallback(ctx, policy, username, password, deviceName, ip)
}

func (s *Service) RegisterProvider(info LoginProviderInfo, provider AuthProvider) {
	if provider == nil || info.ID == "" {
		return
	}
	if info.DisplayName == "" {
		info.DisplayName = info.ID
	}
	if info.Mode == "" {
		info.Mode = "credentials"
	}
	if pluginProvider, ok := provider.(*PluginProvider); ok {
		pluginProvider.setAuthMode(info.Mode)
	}

	s.providers[info.ID] = provider
	s.metadata[info.ID] = info
	if s.defaultID == "" || info.Default {
		s.defaultID = info.ID
	}
}

// FindOAuthInstallation returns the PluginProvider registered for the given
// plugin installation, if it is an OAuth-capable provider. nil if no match.
func (s *Service) FindOAuthInstallation(installationID int) *PluginProvider {
	if installationID <= 0 {
		return nil
	}
	for providerID, p := range s.providers {
		pp, ok := p.(*PluginProvider)
		if !ok || pp == nil {
			continue
		}
		info := s.metadata[providerID]
		if info.Mode != "oauth" {
			continue
		}
		if pp.InstallationID() != installationID {
			continue
		}
		// Only OAuth-capable installs participate in /oauth/... routes. The
		// caller (OAuthHandler.ResolveClient) checks Mode metadata; here we
		// simply scope to PluginProvider instances bound to this install.
		return pp
	}
	return nil
}

// CompleteOAuthLogin runs the post-ExchangeCode half of login: the handler
// has already called the plugin's ExchangeCode RPC and is passing the
// AuthenticateResponse back. Service finds the matching PluginProvider,
// looks up or auto-provisions the user, creates a session, and mints
// access/refresh tokens.
func (s *Service) CompleteOAuthLogin(ctx context.Context, in OAuthLoginInput) (*TokenPair, *models.User, error) {
	if in.LinkingUserID != 0 {
		return nil, nil, ErrOAuthLinkingUnsupported
	}
	provider := s.FindOAuthInstallation(in.InstallationID)
	if provider == nil {
		return nil, nil, ErrInvalidCredentials
	}
	if provider.CapabilityID() != in.CapabilityID {
		return nil, nil, ErrInvalidCredentials
	}
	providerKey, err := models.NewPluginSessionProviderKey(in.InstallationID, in.CapabilityID)
	if err != nil {
		return nil, nil, ErrInvalidCredentials
	}
	sessionID := uuid.New().String()
	session := models.AuthSession{
		ID:          sessionID,
		UserID:      0,
		DeviceName:  in.DeviceName,
		IPAddress:   in.IP,
		ExpiresAt:   time.Now().Add(s.jwt.RefreshExpiry()),
		ProviderKey: &providerKey,
	}
	user, err := provider.CompleteOAuthAndComplete(ctx, in.Response, session)
	if err != nil {
		return nil, nil, err
	}
	pair, err := s.generateTokenPair(Claims{
		UserID:    user.ID,
		Role:      user.Role,
		SessionID: sessionID,
	})
	if err != nil {
		return nil, nil, err
	}
	return pair, user, nil
}

func (s *Service) ListProviders() []LoginProviderInfo {
	providers := make([]LoginProviderInfo, 0, len(s.metadata))
	for _, info := range s.metadata {
		info.Default = info.ID == s.defaultID
		providers = append(providers, info)
	}
	sort.Slice(providers, func(i, j int) bool {
		if providers[i].Default != providers[j].Default {
			return providers[i].Default
		}
		return providers[i].DisplayName < providers[j].DisplayName
	})
	return providers
}

func (s *Service) RegisteredProviderMetadata() map[string]LoginProviderInfo {
	result := make(map[string]LoginProviderInfo, len(s.metadata))
	for id, info := range s.metadata {
		result[id] = info
	}
	return result
}

func (s *Service) loginWithProvider(
	ctx context.Context,
	providerID string,
	username string,
	password string,
	deviceName string,
	ip string,
) (*TokenPair, *models.User, error) {
	provider := s.providers[providerID]
	if provider == nil {
		return nil, nil, ErrInvalidCredentials
	}
	if s.metadata[providerID].Mode == "oauth" {
		return nil, nil, ErrInvalidCredentials
	}
	providerKey, err := s.providerSessionKey(providerID)
	if err != nil {
		return nil, nil, ErrInvalidCredentials
	}

	// Create a new session with a pre-generated ID to avoid the race condition
	// of looking up the session after creation.
	sessionID := uuid.New().String()
	session := models.AuthSession{
		ID:          sessionID,
		UserID:      0,
		DeviceName:  deviceName,
		IPAddress:   ip,
		ExpiresAt:   time.Now().Add(s.jwt.RefreshExpiry()),
		ProviderKey: &providerKey,
	}

	credentials := Credentials{Username: username, Password: password}
	if pluginProvider, ok := provider.(*PluginProvider); ok {
		user, completeErr := pluginProvider.AuthenticateAndComplete(ctx, credentials, session)
		if completeErr != nil {
			return nil, nil, completeErr
		}
		session.UserID = user.ID
		return s.tokenPairForSession(user, sessionID)
	}
	user, err := provider.Authenticate(ctx, credentials)
	if err != nil {
		return nil, nil, err
	}
	session.UserID = user.ID
	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, nil, fmt.Errorf("creating session: %w", err)
	}

	pair, err := s.generateTokenPair(Claims{
		UserID:    user.ID,
		Role:      user.Role,
		SessionID: sessionID,
	})
	if err != nil {
		return nil, nil, err
	}

	return pair, user, nil
}

func (s *Service) tokenPairForSession(user *models.User, sessionID string) (*TokenPair, *models.User, error) {
	pair, err := s.generateTokenPair(Claims{UserID: user.ID, Role: user.Role, SessionID: sessionID})
	if err != nil {
		return nil, nil, err
	}
	return pair, user, nil
}

func (s *Service) providerSessionKey(providerID string) (models.SessionProviderKey, error) {
	if providerID == "local" {
		return models.LocalSessionProviderKey(), nil
	}
	provider, ok := s.providers[providerID].(*PluginProvider)
	if !ok || provider == nil {
		return models.SessionProviderKey{}, ErrInvalidCredentials
	}
	return models.NewPluginSessionProviderKey(provider.InstallationID(), provider.CapabilityID())
}

// NeedsSetup reports whether the system still needs its initial user account.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	count, err := s.users.Count(ctx)
	if err != nil {
		return false, fmt.Errorf("counting users: %w", err)
	}
	return count == 0, nil
}

// SetupInitialUser creates the first admin account and signs it in.
func (s *Service) SetupInitialUser(
	ctx context.Context,
	username, email, password string,
	createDefaultProfile bool,
	defaultProfileName string,
	deviceName, ip string,
) (*TokenPair, *models.User, error) {
	needsSetup, err := s.NeedsSetup(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !needsSetup {
		return nil, nil, ErrSetupAlreadyComplete
	}

	if _, err := s.accounts.CreateAccount(ctx, CreateAccountInput{
		User: models.CreateUserInput{
			Username: username,
			Email:    email,
			Password: password,
			Role:     "admin",
		},
		DefaultProfile: DefaultProfileOptions{
			Enabled: createDefaultProfile,
			Name:    defaultProfileName,
		},
	}); err != nil {
		return nil, nil, fmt.Errorf("creating initial user: %w", err)
	}

	// Reuse the standard login flow so setup creates a normal session pair.
	return s.Login(ctx, username, password, deviceName, ip)
}

// Signup creates a new user account using an invite code. Requires that
// public signups are enabled via the "signup.enabled" server setting.
func (s *Service) Signup(
	ctx context.Context,
	username, email, password, code string,
	createDefaultProfile bool,
	defaultProfileName string,
	deviceName, ip string,
) (*TokenPair, *models.User, error) {
	// Check global signup toggle.
	if s.settings != nil {
		enabled, err := s.settings.Get(ctx, "signup.enabled")
		if err != nil {
			return nil, nil, fmt.Errorf("checking signup setting: %w", err)
		}
		if enabled != "true" {
			return nil, nil, ErrSignupDisabled
		}
	} else {
		return nil, nil, ErrSignupDisabled
	}

	// Redeem the invite code (atomic increment).
	if err := s.inviteCodes.RedeemCode(ctx, code); err != nil {
		return nil, nil, err
	}

	// Create the user with standard role and access to all libraries.
	if _, err := s.accounts.CreateAccount(ctx, CreateAccountInput{
		User: models.CreateUserInput{
			Username: username,
			Email:    email,
			Password: password,
			Role:     "user",
		},
		DefaultProfile: DefaultProfileOptions{
			Enabled: createDefaultProfile,
			Name:    defaultProfileName,
		},
	}); err != nil {
		return nil, nil, fmt.Errorf("creating user: %w", err)
	}

	// Log them in to create a session and return tokens.
	return s.Login(ctx, username, password, deviceName, ip)
}

// IsSignupEnabled reports whether public signups are enabled.
func (s *Service) IsSignupEnabled(ctx context.Context) (bool, error) {
	if s.settings == nil {
		return false, nil
	}
	enabled, err := s.settings.Get(ctx, "signup.enabled")
	if err != nil {
		return false, fmt.Errorf("checking signup setting: %w", err)
	}
	return enabled == "true", nil
}

// Logout revokes the session identified by sessionID.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return s.sessions.Revoke(ctx, sessionID)
}

// StartImpersonation creates a new target-user session with admin provenance.
func (s *Service) StartImpersonation(ctx context.Context, adminUserID, targetUserID int, deviceName, ip string) (*TokenPair, *models.User, *models.User, error) {
	if claims := ClaimsFromContext(ctx); claims != nil {
		if claims.TokenType == TokenTypeAPIKey || claims.SessionID == "" {
			return nil, nil, nil, ErrImpersonationNotAllowed
		}
		currentSession, err := s.sessions.GetByID(ctx, claims.SessionID)
		if err != nil {
			if !IsSessionNotFound(err) {
				return nil, nil, nil, fmt.Errorf("getting current session: %w", err)
			}
		} else if currentSession.ImpersonatorUserID != nil {
			return nil, nil, nil, ErrAlreadyImpersonating
		}
	}

	admin, err := s.users.GetByID(ctx, adminUserID)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil, nil, ErrImpersonationNotAllowed
		}
		return nil, nil, nil, fmt.Errorf("getting admin user: %w", err)
	}
	if admin.Role != "admin" || !admin.Enabled {
		return nil, nil, nil, ErrImpersonationNotAllowed
	}
	if adminUserID == targetUserID {
		return nil, nil, nil, ErrImpersonationNotAllowed
	}

	target, err := s.users.GetByID(ctx, targetUserID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("getting target user: %w", err)
	}
	if !target.Enabled || target.Role == "admin" {
		return nil, nil, nil, ErrImpersonationNotAllowed
	}

	sessionID := uuid.New().String()
	impersonatorUserID := admin.ID
	startedAt := time.Now()
	session := models.AuthSession{
		ID:                     sessionID,
		UserID:                 target.ID,
		DeviceName:             deviceName,
		IPAddress:              ip,
		ExpiresAt:              startedAt.Add(s.jwt.RefreshExpiry()),
		ProviderKey:            sessionProviderKeyPointer(models.LocalSessionProviderKey()),
		ImpersonatorUserID:     &impersonatorUserID,
		ImpersonationStartedAt: &startedAt,
	}

	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, nil, nil, fmt.Errorf("creating session: %w", err)
	}

	pair, err := s.generateTokenPair(Claims{
		UserID:             target.ID,
		Role:               target.Role,
		SessionID:          sessionID,
		ImpersonatorUserID: &impersonatorUserID,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	return pair, admin, target, nil
}

func sessionProviderKeyPointer(key models.SessionProviderKey) *models.SessionProviderKey {
	return &key
}

// EndImpersonation revokes an impersonated session without affecting the original admin session.
func (s *Service) EndImpersonation(ctx context.Context, sessionID string, impersonatorUserID int) error {
	session, err := s.sessions.GetByID(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.ImpersonatorUserID == nil {
		return ErrNotImpersonating
	}
	if *session.ImpersonatorUserID != impersonatorUserID {
		return ErrImpersonationNotAllowed
	}

	return s.sessions.Revoke(ctx, sessionID)
}

// Refresh validates the refresh token, checks that the associated session is
// still valid, and issues a new token pair.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	claims, err := s.jwt.ValidateToken(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("invalid refresh token: %w", err)
	}
	if claims.TokenType != TokenTypeRefresh {
		return nil, fmt.Errorf("invalid refresh token: %w", ErrInvalidToken)
	}

	session, err := s.sessions.GetByID(ctx, claims.SessionID)
	if err != nil {
		if IsSessionNotFound(err) {
			return nil, ErrSessionRevoked
		}
		return nil, fmt.Errorf("getting session: %w", err)
	}
	if session.RevokedAt != nil || !session.ExpiresAt.After(time.Now()) {
		return nil, ErrSessionRevoked
	}

	user, err := s.users.GetByID(ctx, session.UserID)
	if err != nil {
		if IsNotFound(err) {
			return nil, ErrSessionRevoked
		}
		return nil, fmt.Errorf("getting user: %w", err)
	}
	if !user.Enabled {
		return nil, ErrSessionRevoked
	}
	if err := s.validateImpersonator(ctx, session.ImpersonatorUserID); err != nil {
		return nil, err
	}

	// Slide the session window forward so an active client never hits the
	// hard expires_at set at login. A failure here is non-fatal: the refresh
	// still returns fresh tokens; the session just keeps its prior expiry.
	newExpiry := time.Now().Add(s.jwt.RefreshExpiry())
	if err := s.sessions.ExtendExpiresAt(ctx, session.ID, newExpiry); err != nil && !IsSessionNotFound(err) {
		return nil, fmt.Errorf("extending session: %w", err)
	}

	return s.generateTokenPair(Claims{
		UserID:             user.ID,
		Role:               user.Role,
		SessionID:          session.ID,
		ImpersonatorUserID: session.ImpersonatorUserID,
	})
}

func (s *Service) validateImpersonator(ctx context.Context, impersonatorUserID *int) error {
	if impersonatorUserID == nil {
		return nil
	}

	impersonator, err := s.users.GetByID(ctx, *impersonatorUserID)
	if err != nil {
		if IsNotFound(err) {
			return ErrSessionRevoked
		}
		return fmt.Errorf("getting impersonator user: %w", err)
	}
	if !impersonator.Enabled || impersonator.Role != "admin" {
		return ErrSessionRevoked
	}
	return nil
}

// GetCurrentUser retrieves the user associated with the given JWT claims.
func (s *Service) GetCurrentUser(ctx context.Context, claims *Claims) (*models.User, error) {
	user, err := s.users.GetByID(ctx, claims.UserID)
	if err != nil {
		return nil, fmt.Errorf("getting user: %w", err)
	}
	return user, nil
}

// PasswordChangeAvailable reports whether the account has a local password
// that can be verified and replaced through the self-service password flow.
// OAuth-only accounts keep their provider-managed credential boundary.
func (s *Service) PasswordChangeAvailable(ctx context.Context, userID int) (bool, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("getting user: %w", err)
	}
	return user.LocalPasswordLoginEnabled && user.PasswordHash != "", nil
}

// ChangePassword verifies the existing local credential before replacing it.
// Profile authorization and impersonation checks belong to the HTTP boundary;
// this method owns only the account credential transition.
func (s *Service) ChangePassword(ctx context.Context, userID int, currentPassword, newPassword string) error {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("getting user: %w", err)
	}
	if err := validatePasswordChange(user, currentPassword, newPassword); err != nil {
		return err
	}

	if err := s.users.CompareAndSwapPassword(ctx, userID, user.PasswordHash, newPassword); err != nil {
		return fmt.Errorf("updating password: %w", err)
	}
	return nil
}

func validatePasswordChange(user *models.User, currentPassword, newPassword string) error {
	if !user.LocalPasswordLoginEnabled || user.PasswordHash == "" {
		return ErrPasswordLoginDisabled
	}
	if !CheckPassword(user, currentPassword) {
		return ErrCurrentPasswordInvalid
	}
	if utf8.RuneCountInString(newPassword) < MinimumPasswordLength {
		return ErrPasswordTooShort
	}
	if len(newPassword) > MaximumPasswordBytes {
		return ErrPasswordTooLong
	}
	return nil
}

// GetSessions returns all sessions for the given user ID.
func (s *Service) GetSessions(ctx context.Context, userID int) ([]*models.AuthSession, error) {
	return s.sessions.ListByUser(ctx, userID)
}

// RevokeSession revokes a specific session. It verifies the session belongs
// to the given user before revoking.
func (s *Service) RevokeSession(ctx context.Context, sessionID string, userID int) error {
	session, err := s.sessions.GetByID(ctx, sessionID)
	if err != nil {
		return err
	}

	if session.UserID != userID {
		return ErrSessionNotFound
	}

	return s.sessions.Revoke(ctx, sessionID)
}

func (s *Service) loadCredentialProviderPolicy(ctx context.Context) (CredentialProviderPolicy, error) {
	if s.settings == nil {
		return CredentialProviderPolicy{}, nil
	}
	raw, err := s.settings.Get(ctx, SettingKeyCredentialProviderFallback)
	if err != nil {
		return CredentialProviderPolicy{}, fmt.Errorf("get credential provider policy: %w", err)
	}
	return ParseCredentialProviderPolicy(raw, s.metadata)
}

func (s *Service) loginWithFallback(
	ctx context.Context,
	policy CredentialProviderPolicy,
	username string,
	password string,
	deviceName string,
	ip string,
) (*TokenPair, *models.User, error) {
	var lastErr error
	for _, providerID := range policy.ProviderIDs() {
		if s.providers[providerID] == nil {
			continue
		}
		if s.metadata[providerID].Mode == "oauth" {
			continue
		}
		pair, user, err := s.loginWithProvider(ctx, providerID, username, password, deviceName, ip)
		if err == nil {
			return pair, user, nil
		}
		if !errors.Is(err, ErrAccountNotFound) {
			return nil, nil, err
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, nil, ErrInvalidCredentials
	}
	return nil, nil, ErrInvalidCredentials
}

func (s *Service) CredentialProviderFallbackSetting(ctx context.Context) (string, error) {
	if s.settings == nil {
		return "", nil
	}
	raw, err := s.settings.Get(ctx, SettingKeyCredentialProviderFallback)
	if err != nil {
		return "", fmt.Errorf("get credential provider policy: %w", err)
	}
	return raw, nil
}

func (s *Service) SetCredentialProviderFallbackSetting(ctx context.Context, raw string) error {
	if s.settings == nil {
		return fmt.Errorf("settings store not available")
	}
	if raw != "" {
		if _, err := ParseCredentialProviderPolicy(raw, s.metadata); err != nil {
			return err
		}
	}
	setter, ok := s.settings.(interface {
		Set(ctx context.Context, key, value string) error
	})
	if !ok {
		return fmt.Errorf("settings store does not support writes")
	}
	return setter.Set(ctx, SettingKeyCredentialProviderFallback, raw)
}

// generateTokenPair creates a new access/refresh token pair for the given
// claims.
func (s *Service) generateTokenPair(claims Claims) (*TokenPair, error) {
	accessToken, err := s.jwt.generateAccessToken(claims)
	if err != nil {
		return nil, fmt.Errorf("generating access token: %w", err)
	}

	refreshToken, err := s.jwt.generateRefreshToken(claims)
	if err != nil {
		return nil, fmt.Errorf("generating refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int(s.jwt.AccessExpiry().Seconds()),
	}, nil
}
