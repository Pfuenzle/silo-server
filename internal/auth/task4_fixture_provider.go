//go:build task4fixture

package auth

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type task4FixturePluginClient struct{ subject string }

func (c task4FixturePluginClient) Authenticate(_ context.Context, _ *pluginv1.AuthenticateRequest) (*pluginv1.AuthenticateResponse, error) {
	return &pluginv1.AuthenticateResponse{ExternalSubject: c.subject}, nil
}

func (task4FixturePluginClient) InitAuthorize(context.Context, *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error) {
	return nil, ErrInvalidCredentials
}

func (task4FixturePluginClient) ExchangeCode(context.Context, *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error) {
	return nil, ErrInvalidCredentials
}

func NewTask4FixturePluginProvider(config PluginProviderConfig, sessions *SessionRepository, users *UserRepository, pool *pgxpool.Pool, subject string) *PluginProvider {
	return NewPluginProviderWithClientFactory(config, sessions, users, pool, func(context.Context) (pluginAuthClient, error) {
		return task4FixturePluginClient{subject: subject}, nil
	})
}
