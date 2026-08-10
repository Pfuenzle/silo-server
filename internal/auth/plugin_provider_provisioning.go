package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

func (p *PluginProvider) provisionIdentity(
	ctx context.Context,
	creds Credentials,
	response *pluginv1.AuthenticateResponse,
) (*models.User, error) {
	key := PluginIdentityKey{
		InstallationID:  p.config.InstallationID,
		ExternalSubject: response.GetExternalSubject(),
	}
	var user, createdUser *models.User
	err := p.identities.withProvisioningTransaction(ctx, key, func(ctx context.Context, tx pgx.Tx) error {
		userID, err := lookupPluginIdentity(ctx, tx, key)
		if err == nil {
			existingUser, lookupErr := p.users.GetByID(ctx, userID)
			if lookupErr != nil {
				return lookupErr
			}
			user = existingUser
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}

		provisionedUser, err := p.autoProvisionUser(ctx, creds, response)
		if err != nil {
			return err
		}
		createdUser = provisionedUser
		if err := p.identities.claimInTransaction(ctx, tx, key, provisionedUser.ID); err != nil {
			return fmt.Errorf("associate provisioned plugin user: %w", err)
		}
		user = provisionedUser
		return nil
	})
	if err != nil {
		if createdUser != nil {
			if cleanupErr := p.accounts.DeleteAccount(ctx, createdUser.ID); cleanupErr != nil {
				return nil, errors.Join(err, fmt.Errorf("cleanup provisioned plugin user: %w", cleanupErr))
			}
		}
		return nil, err
	}
	if !user.Enabled {
		return nil, ErrUserDisabled
	}
	return user, nil
}

func (p *PluginProvider) autoProvisionUser(
	ctx context.Context,
	creds Credentials,
	response *pluginv1.AuthenticateResponse,
) (*models.User, error) {
	usernameBase := strings.TrimSpace(response.GetDisplayName())
	if usernameBase == "" {
		usernameBase = strings.TrimSpace(creds.Username)
	}
	if usernameBase == "" {
		usernameBase = response.GetExternalSubject()
	}
	usernameBase = sanitizeUsername(usernameBase)
	if usernameBase == "" {
		usernameBase = fmt.Sprintf("plugin_%d", p.config.InstallationID)
	}

	email := strings.TrimSpace(response.GetEmail())
	if email == "" {
		email = fmt.Sprintf("%s@plugin-%d.local", usernameBase, p.config.InstallationID)
	}

	localPasswordLoginEnabled := false
	password, err := randomPluginOnlyPassword()
	if err != nil {
		return nil, fmt.Errorf("generate plugin-only password: %w", err)
	}

	username := usernameBase
	usingSyntheticEmail := false
	for i := 0; i < 10; i++ {
		user, err := p.accounts.CreateAccount(ctx, CreateAccountInput{
			User: models.CreateUserInput{
				Email:                     email,
				Username:                  username,
				Password:                  password,
				LocalPasswordLoginEnabled: &localPasswordLoginEnabled,
				Role:                      "user",
			},
			DefaultProfile: DefaultProfileOptions{Enabled: true, Name: usernameBase},
		})
		if err == nil {
			return user, nil
		}
		if !IsDuplicate(err) {
			return nil, fmt.Errorf("auto-provision plugin user: %w", err)
		}
		if !usingSyntheticEmail && IsDuplicateEmail(err) {
			email = syntheticPluginEmail(p.config.InstallationID, response.GetExternalSubject())
			usingSyntheticEmail = true
			username = usernameBase
			continue
		}
		username = fmt.Sprintf("%s_%d", usernameBase, i+2)
	}
	return nil, fmt.Errorf("auto-provision plugin user: exhausted username attempts")
}

func detachedCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func syntheticPluginEmail(installationID int, externalSubject string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", installationID, externalSubject)))
	return fmt.Sprintf("plugin-%d-%x@plugin.local", installationID, sum[:12])
}

func randomPluginOnlyPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "plugin-only-" + hex.EncodeToString(buf), nil
}

func sanitizeUsername(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "_")
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "_.-")
}
