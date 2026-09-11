package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type PluginAuthClient interface {
	Authenticate(ctx context.Context, req *pluginv1.AuthenticateRequest) (*pluginv1.AuthenticateResponse, error)
	InitAuthorize(ctx context.Context, req *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error)
	ExchangeCode(ctx context.Context, req *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error)
}

type pluginAuthClient = PluginAuthClient

type pluginAuthClientFactory func(ctx context.Context) (pluginAuthClient, error)

type PluginProviderConfig struct {
	InstallationID int
	CapabilityID   string
	DisplayName    string
	AuthMode       string
	AutoProvision  bool
	StoreProvider  userstore.UserStoreProvider
}

type PluginProvider struct {
	config     PluginProviderConfig
	client     pluginAuthClientFactory
	sessions   *SessionRepository
	users      *UserRepository
	identities *PluginIdentityRepository
	accounts   *AccountProvisioner
	pool       *pgxpool.Pool
}

func NewPluginProviderWithClientFactory(
	config PluginProviderConfig,
	sessions *SessionRepository,
	users *UserRepository,
	pool *pgxpool.Pool,
	clientFactory pluginAuthClientFactory,
) *PluginProvider {
	return &PluginProvider{
		config:     config,
		client:     clientFactory,
		sessions:   sessions,
		users:      users,
		identities: NewPluginIdentityRepository(pool),
		accounts:   NewAccountProvisioner(users, config.StoreProvider),
		pool:       pool,
	}
}

func NewPluginProvider(
	config PluginProviderConfig,
	sessions *SessionRepository,
	users *UserRepository,
	pool *pgxpool.Pool,
	resolver interface {
		AuthProviderClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.AuthProviderClient, error)
	},
) *PluginProvider {
	return NewPluginProviderWithClientFactory(config, sessions, users, pool, func(ctx context.Context) (pluginAuthClient, error) {
		return resolver.AuthProviderClient(ctx, config.InstallationID, config.CapabilityID)
	})
}

func (p *PluginProvider) Authenticate(ctx context.Context, creds Credentials) (*models.User, error) {
	return p.authenticate(ctx, creds, nil)
}

// AuthenticateAndComplete authenticates credentials and durably completes the
// resulting login. Plugin binding state, external authorization, revocation,
// and session creation share one transaction when authorization is enabled.
func (p *PluginProvider) AuthenticateAndComplete(ctx context.Context, creds Credentials, session models.AuthSession) (*models.User, error) {
	if session.ID == "" || session.ExpiresAt.IsZero() {
		return nil, ErrInvalidCredentials
	}
	return p.authenticate(ctx, creds, &session)
}

func (p *PluginProvider) authenticate(ctx context.Context, creds Credentials, session *models.AuthSession) (*models.User, error) {
	if !p.bindingEnabled(ctx) {
		return nil, ErrInvalidCredentials
	}
	client, err := p.client(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "plugin auth client unavailable", "component", "auth", "installation_id", p.config.InstallationID, "capability_id", p.config.CapabilityID, "error", err)
		if status.Code(err) == codes.Unauthenticated {
			return nil, ErrInvalidCredentials
		}
		if errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrUserDisabled) {
			return nil, err
		}
		if errors.Is(err, plugins.ErrInstallationDisabled) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("load plugin auth client: %w", err)
	}

	response, err := client.Authenticate(ctx, &pluginv1.AuthenticateRequest{
		Username: creds.Username,
		Password: creds.Password,
	})
	if err != nil {
		slog.ErrorContext(ctx, "plugin auth RPC failed", "component", "auth", "installation_id", p.config.InstallationID, "capability_id", p.config.CapabilityID, "error", err)
		if status.Code(err) == codes.NotFound {
			return nil, ErrAccountNotFound
		}
		if status.Code(err) == codes.Unauthenticated {
			return nil, ErrInvalidCredentials
		}
		if errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrUserDisabled) {
			return nil, err
		}
		return nil, fmt.Errorf("plugin auth authenticate: %w", err)
	}
	if response.GetStatus() == pluginv1.AuthStatus_AUTH_STATUS_ACCOUNT_NOT_FOUND {
		return nil, ErrAccountNotFound
	}
	if strings.TrimSpace(response.GetExternalSubject()) == "" {
		return nil, ErrInvalidCredentials
	}

	return p.completeExternalLogin(ctx, creds, response, session)
}

// CompleteOAuth runs the post-RPC half of plugin authentication for an
// OAuth flow: validate the AuthenticateResponse, look up an existing
// plugin_auth_identities row, auto-provision a new user if needed, and
// claim immutable identity ownership. The handler calls plugin ExchangeCode itself and
// passes the response in here.
func (p *PluginProvider) CompleteOAuth(ctx context.Context, response *pluginv1.AuthenticateResponse) (*models.User, error) {
	return p.completeOAuth(ctx, response, nil)
}

// CompleteOAuthAndComplete finishes an OAuth login inside the same durable
// transaction that reconciles external authorization and creates its session.
func (p *PluginProvider) CompleteOAuthAndComplete(ctx context.Context, response *pluginv1.AuthenticateResponse, session models.AuthSession) (*models.User, error) {
	if session.ID == "" || session.ExpiresAt.IsZero() {
		return nil, ErrInvalidCredentials
	}
	return p.completeOAuth(ctx, response, &session)
}

func (p *PluginProvider) completeOAuth(ctx context.Context, response *pluginv1.AuthenticateResponse, session *models.AuthSession) (*models.User, error) {
	if !p.bindingEnabled(ctx) {
		return nil, ErrInvalidCredentials
	}
	if strings.TrimSpace(response.GetExternalSubject()) == "" {
		return nil, ErrInvalidCredentials
	}

	return p.completeExternalLogin(ctx, Credentials{}, response, session)
}

func (p *PluginProvider) setAuthMode(mode string) {
	p.config.AuthMode = mode
}

func (p *PluginProvider) bindingEnabled(ctx context.Context) bool {
	if p == nil || p.pool == nil {
		return false
	}
	if strings.TrimSpace(p.config.CapabilityID) == "" {
		return true
	}
	var enabled bool
	err := p.pool.QueryRow(ctx, `SELECT enabled FROM plugin_auth_bindings WHERE plugin_installation_id = $1 AND capability_id = $2`, p.config.InstallationID, p.config.CapabilityID).Scan(&enabled)
	return err == nil && enabled
}

// InstallationID exposes the plugin install this provider is bound to —
// used by the OAuth handler to match incoming /oauth/{install_id}/... requests.
func (p *PluginProvider) InstallationID() int { return p.config.InstallationID }

// CapabilityID exposes the bound capability slug (e.g. "whmcs").
func (p *PluginProvider) CapabilityID() string { return p.config.CapabilityID }

func (p *PluginProvider) SessionProviderKey() (models.SessionProviderKey, error) {
	return models.NewPluginSessionProviderKey(p.InstallationID(), p.CapabilityID())
}

// OAuthClient returns a host-side gRPC client wrapping the plugin's
// AuthProvider service. Used by the OAuth handler to call InitAuthorize
// and ExchangeCode without re-resolving the installation.
func (p *PluginProvider) OAuthClient(ctx context.Context) (OAuthClient, error) {
	c, err := p.client(ctx)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (p *PluginProvider) ValidateSession(ctx context.Context, sessionID string) (bool, error) {
	if p.sessions == nil {
		return false, nil
	}
	if _, err := p.client(ctx); err != nil {
		if errors.Is(err, plugins.ErrInstallationDisabled) {
			return false, nil
		}
		return false, fmt.Errorf("load plugin auth client: %w", err)
	}
	return p.sessions.IsValid(ctx, sessionID)
}

func (p *PluginProvider) lookupIdentity(ctx context.Context, externalSubject string) (*models.User, error) {
	userID, err := p.identities.Lookup(ctx, PluginIdentityKey{
		InstallationID:  p.config.InstallationID,
		ExternalSubject: externalSubject,
	})
	if err != nil {
		return nil, err
	}
	user, err := p.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (p *PluginProvider) claimIdentity(ctx context.Context, externalSubject string, userID int) error {
	return p.identities.Claim(ctx, PluginIdentityKey{
		InstallationID:  p.config.InstallationID,
		ExternalSubject: externalSubject,
	}, userID)
}
