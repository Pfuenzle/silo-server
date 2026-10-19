package watchsync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const authorizationCodeSessionTTL = 10 * time.Minute

// Authorization-code sessions share the device-auth table, but never its poll
// protocol. The marker and zero polling interval distinguish the persisted flow;
// all provider state and the trusted redirect URI live in encrypted device_code.
type authorizationCodeState struct {
	RedirectURI   string `json:"redirect_uri"`
	ProviderState string `json:"provider_state"`
}

func (s *Service) StartAuthorizationCodeAuth(ctx context.Context, userID int, profileID, providerKey, redirectURI string) (AuthorizationCodeSession, error) {
	if userID <= 0 {
		return AuthorizationCodeSession{}, InvalidConnectionInputError{Message: "user id is required"}
	}
	if strings.TrimSpace(profileID) == "" {
		return AuthorizationCodeSession{}, InvalidConnectionInputError{Message: "profile id is required"}
	}
	provider, ok := s.registry.Get(providerKey)
	if !ok {
		return AuthorizationCodeSession{}, UnknownProviderError{Key: providerKey}
	}
	authProvider, ok := provider.(AuthorizationCodeAuthProvider)
	if !ok || !supportsAuthorizationCodeAuth(provider) {
		return AuthorizationCodeSession{}, ProviderCapabilityError{Key: providerKey, What: "does not support authorization-code auth"}
	}
	if !validAuthorizationRedirectURI(redirectURI) {
		return AuthorizationCodeSession{}, InvalidConnectionInputError{Message: "authorization redirect URI must be an absolute http or https URL"}
	}
	state, err := uuid.NewRandom()
	if err != nil {
		return AuthorizationCodeSession{}, errors.New("generate watch provider authorization state")
	}
	expiresAt := s.now().Add(authorizationCodeSessionTTL)
	session, err := authProvider.StartAuthorizationCodeAuth(ctx, redirectURI, state.String())
	if err != nil {
		return AuthorizationCodeSession{}, err
	}
	if _, err := validDeviceVerificationURL(session.AuthorizeURL); err != nil {
		return AuthorizationCodeSession{}, errors.New("watch provider returned an invalid authorization URL")
	}
	if !session.ExpiresAt.IsZero() && session.ExpiresAt.Before(expiresAt) {
		expiresAt = session.ExpiresAt
	}
	if !expiresAt.After(s.now()) {
		return AuthorizationCodeSession{}, ErrAuthSessionExpired
	}
	// Providers cannot override the host's state, ownership, redirect, or TTL.
	session.ID = state.String()
	session.State = session.ID
	session.Provider = providerKey
	session.UserID = userID
	session.ProfileID = profileID
	session.RedirectURI = redirectURI
	session.ExpiresAt = expiresAt
	session.CompletedAt = nil
	row, err := deviceAuthSessionFromAuthorizationCode(session)
	if err != nil {
		return AuthorizationCodeSession{}, err
	}
	if _, err := s.repo.UpsertAuthSession(ctx, row); err != nil {
		return AuthorizationCodeSession{}, err
	}
	return session, nil
}

func (s *Service) CompleteAuthorizationCodeAuth(ctx context.Context, providerKey, state, code string) (Connection, error) {
	// Do not send arbitrary callback input to PostgreSQL's UUID parser or include
	// it in an error message. Only the exact state emitted by the host is valid.
	parsed, err := uuid.Parse(state)
	if err != nil || parsed.String() != state {
		return Connection{}, ErrAuthSessionNotFound
	}
	provider, ok := s.registry.Get(providerKey)
	if !ok {
		return Connection{}, UnknownProviderError{Key: providerKey}
	}
	authProvider, ok := provider.(AuthorizationCodeAuthProvider)
	if !ok || !supportsAuthorizationCodeAuth(provider) {
		return Connection{}, ProviderCapabilityError{Key: providerKey, What: "does not support authorization-code auth"}
	}
	// Consumption commits before the RPC. Concurrent callbacks on any node,
	// callback replay, and failed exchanges all require a fresh authorization.
	row, err := s.repo.ConsumeAuthorizationCodeAuthSession(ctx, state, providerKey, s.now())
	if err != nil {
		return Connection{}, err
	}
	session, err := authorizationCodeSessionFromDeviceAuthSession(row)
	if err != nil {
		return Connection{}, err
	}
	code = strings.TrimSpace(code)
	if code == "" {
		// Also consumes a provider-denied callback without sending a code RPC.
		return Connection{}, InvalidConnectionInputError{Message: "authorization code is required"}
	}
	tokens, account, err := authProvider.CompleteAuthorizationCodeAuth(ctx, code, session)
	if err != nil {
		return Connection{}, err
	}
	return s.persistConnection(ctx, providerKey, session.UserID, session.ProfileID, tokens, account)
}

func supportsAuthorizationCodeAuth(provider Provider) bool {
	if declared, ok := provider.(interface{ AuthMethod() string }); ok {
		return declared.AuthMethod() == AuthMethodAuthorizationCode
	}
	return true
}

func deviceAuthSessionFromAuthorizationCode(session AuthorizationCodeSession) (DeviceAuthSession, error) {
	encoded, err := json.Marshal(authorizationCodeState{
		RedirectURI: session.RedirectURI, ProviderState: session.ProviderState,
	})
	if err != nil {
		return DeviceAuthSession{}, errors.New("encode watch provider authorization state")
	}
	return DeviceAuthSession{
		ID: session.ID, Provider: session.Provider, UserID: session.UserID, ProfileID: session.ProfileID,
		DeviceCode: string(encoded), UserCode: AuthMethodAuthorizationCode,
		ExpiresAt: session.ExpiresAt, CompletedAt: session.CompletedAt,
	}, nil
}

func isAuthorizationCodeSession(session DeviceAuthSession) bool {
	return session.IntervalSeconds == 0 && session.UserCode == AuthMethodAuthorizationCode
}

func authorizationCodeSessionFromDeviceAuthSession(row DeviceAuthSession) (AuthorizationCodeSession, error) {
	if !isAuthorizationCodeSession(row) {
		return AuthorizationCodeSession{}, ErrAuthSessionMismatch
	}
	var state authorizationCodeState
	if json.Unmarshal([]byte(row.DeviceCode), &state) != nil || !validAuthorizationRedirectURI(state.RedirectURI) ||
		row.UserID <= 0 || row.ProfileID == "" {
		return AuthorizationCodeSession{}, errors.New("watch provider authorization session is invalid")
	}
	return AuthorizationCodeSession{
		ID: row.ID, State: row.ID, Provider: row.Provider, UserID: row.UserID, ProfileID: row.ProfileID,
		RedirectURI: state.RedirectURI, ProviderState: state.ProviderState,
		ExpiresAt: row.ExpiresAt, CompletedAt: row.CompletedAt,
	}, nil
}
