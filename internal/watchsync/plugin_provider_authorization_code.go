package watchsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func (p *PluginProvider) StartAuthorizationCodeAuth(ctx context.Context, redirectURI, state string) (AuthorizationCodeSession, error) {
	if p.authMethod != AuthMethodAuthorizationCode {
		return AuthorizationCodeSession{}, ProviderCapabilityError{Key: p.Key(), What: "does not support authorization-code auth"}
	}
	if !validAuthorizationRedirectURI(redirectURI) || strings.TrimSpace(state) == "" {
		return AuthorizationCodeSession{}, InvalidConnectionInputError{Message: "watch sync authorization redirect URI and state are required"}
	}
	config, err := p.providerConfig(ctx)
	if err != nil {
		return AuthorizationCodeSession{}, err
	}
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return AuthorizationCodeSession{}, watchSyncUnavailableError()
	}
	response, err := client.InitAuthorize(ctx, &pluginv1.WatchSyncInitAuthorizeRequest{
		CapabilityId:   p.capabilityID,
		ProviderConfig: config,
		RedirectUri:    redirectURI,
		State:          state,
	})
	if err != nil {
		return AuthorizationCodeSession{}, watchSyncRPCError()
	}
	providerState := base64.RawURLEncoding.EncodeToString(response.GetProviderState())
	if fault := response.GetFault(); fault.GetCode() != pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_UNSPECIFIED {
		secrets := append(providerConfigSecretStrings(config), state, providerState)
		secrets = append(secrets, authorizationProviderStateSecrets(response.GetProviderState())...)
		return AuthorizationCodeSession{}, connectFaultError(watchSyncFaultError(p.Key(), fault, secrets...))
	}
	authorizeURL, err := validDeviceVerificationURL(response.GetAuthorizationUrl())
	if err != nil {
		return AuthorizationCodeSession{}, errors.New("watch sync plugin returned an invalid authorization URL")
	}
	return AuthorizationCodeSession{
		RedirectURI:   redirectURI,
		State:         state,
		ProviderState: providerState,
		AuthorizeURL:  authorizeURL,
	}, nil
}

func (p *PluginProvider) CompleteAuthorizationCodeAuth(ctx context.Context, code string, session AuthorizationCodeSession) (TokenSet, ProviderAccount, error) {
	if p.authMethod != AuthMethodAuthorizationCode {
		return TokenSet{}, ProviderAccount{}, ProviderCapabilityError{Key: p.Key(), What: "does not support authorization-code auth"}
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return TokenSet{}, ProviderAccount{}, InvalidConnectionInputError{Message: "authorization code is required"}
	}
	if !validAuthorizationRedirectURI(session.RedirectURI) {
		return TokenSet{}, ProviderAccount{}, errors.New("watch sync authorization session is invalid")
	}
	// Empty provider state is valid: AniList keeps no flow data. Other plugins
	// may place their PKCE verifier here; preserve its bytes without inspection.
	providerState, err := base64.RawURLEncoding.DecodeString(session.ProviderState)
	if err != nil {
		return TokenSet{}, ProviderAccount{}, errors.New("watch sync plugin authorization state is invalid")
	}
	config, err := p.providerConfig(ctx)
	if err != nil {
		return TokenSet{}, ProviderAccount{}, err
	}
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return TokenSet{}, ProviderAccount{}, watchSyncUnavailableError()
	}
	response, err := client.ExchangeCode(ctx, &pluginv1.WatchSyncExchangeCodeRequest{
		CapabilityId:      p.capabilityID,
		ProviderConfig:    config,
		RedirectUri:       session.RedirectURI,
		AuthorizationCode: code,
		ProviderState:     providerState,
	})
	if err != nil {
		return TokenSet{}, ProviderAccount{}, watchSyncRPCError()
	}
	if fault := response.GetFault(); fault.GetCode() != pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_UNSPECIFIED {
		secrets := authenticatedContextSecrets(&pluginv1.WatchSyncAuthenticatedContext{
			Credentials:    response.GetCredentials(),
			ProviderConfig: config,
		})
		secrets = append(secrets, code, session.State, session.ProviderState)
		secrets = append(secrets, authorizationProviderStateSecrets(providerState)...)
		return TokenSet{}, ProviderAccount{}, connectFaultError(watchSyncFaultError(p.Key(), fault, secrets...))
	}
	tokens, err := tokenSetFromProto(response.GetCredentials())
	if err != nil {
		return TokenSet{}, ProviderAccount{}, err
	}
	account, err := accountFromProto(response.GetAccount())
	if err != nil {
		return TokenSet{}, ProviderAccount{}, err
	}
	return tokens, account, nil
}

func validAuthorizationRedirectURI(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed != nil && parsed.IsAbs() && parsed.Host != "" && parsed.User == nil &&
		parsed.Fragment == "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

// Opaque JSON flow data may contain a PKCE verifier that a provider quotes on
// its own in a fault. Scrub both the complete state and any JSON scalar values.
func authorizationProviderStateSecrets(state []byte) []string {
	secrets := []string{string(state)}
	var decoded any
	if json.Unmarshal(state, &decoded) == nil {
		secrets = append(secrets, connectionConfigSecretStrings(decoded)...)
	}
	return secrets
}
