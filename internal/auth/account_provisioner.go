package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type AccountUserRepository interface {
	Create(ctx context.Context, input models.CreateUserInput) (*models.User, error)
	Delete(ctx context.Context, id int) error
}

type DefaultProfileOptions struct {
	Enabled bool
	Name    string
}

type CreateAccountInput struct {
	User           models.CreateUserInput
	DefaultProfile DefaultProfileOptions
}

type AccountProvisioner struct {
	users         AccountUserRepository
	storeProvider userstore.UserStoreProvider
}

func NewAccountProvisioner(
	users AccountUserRepository,
	storeProvider userstore.UserStoreProvider,
) *AccountProvisioner {
	return &AccountProvisioner{
		users:         users,
		storeProvider: storeProvider,
	}
}

func (p *AccountProvisioner) CreateAccount(
	ctx context.Context,
	input CreateAccountInput,
) (*models.User, error) {
	user, err := p.users.Create(ctx, input.User)
	if err != nil {
		return nil, err
	}

	if !input.DefaultProfile.Enabled {
		return user, nil
	}

	if err := p.createDefaultProfile(ctx, user.ID, input); err != nil {
		deleteErr := p.DeleteAccount(ctx, user.ID)
		if deleteErr != nil {
			return nil, errors.Join(fmt.Errorf("create default profile: %w", err), fmt.Errorf("cleanup provisioned user: %w", deleteErr))
		}
		return nil, fmt.Errorf("create default profile: %w", err)
	}

	return user, nil
}

func (p *AccountProvisioner) DeleteAccount(ctx context.Context, userID int) error {
	cleanupCtx, cancelCleanup := detachedCleanupContext(ctx)
	defer cancelCleanup()
	return p.deleteAccount(cleanupCtx, userID)
}

func (p *AccountProvisioner) deleteAccount(ctx context.Context, userID int) error {
	if p.storeProvider != nil {
		store, err := p.storeProvider.ForUser(ctx, userID)
		if err != nil {
			return fmt.Errorf("open user store for cleanup: %w", err)
		}
		profiles, err := store.ListProfiles(ctx)
		if err != nil {
			return fmt.Errorf("list profiles for cleanup: %w", err)
		}
		for _, profile := range profiles {
			if err := store.DeleteProfile(ctx, profile.ID); err != nil {
				return fmt.Errorf("delete profile %s for cleanup: %w", profile.ID, err)
			}
		}
		if provider, ok := p.storeProvider.(userstore.ProvisioningCleanupProvider); ok {
			if err := provider.DeleteUser(ctx, userID); err != nil {
				return fmt.Errorf("delete provider state for cleanup: %w", err)
			}
		}
	}
	if err := p.users.Delete(ctx, userID); err != nil {
		return fmt.Errorf("delete user for cleanup: %w", err)
	}
	return nil
}

func (p *AccountProvisioner) createDefaultProfile(
	ctx context.Context,
	userID int,
	input CreateAccountInput,
) error {
	if p.storeProvider == nil {
		return fmt.Errorf("user store provider unavailable")
	}

	store, err := p.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("open user store: %w", err)
	}

	name := strings.TrimSpace(input.DefaultProfile.Name)
	if name == "" {
		name = strings.TrimSpace(input.User.Username)
	}
	if name == "" {
		return fmt.Errorf("default profile name is required")
	}

	if err := store.CreateProfile(ctx, userstore.Profile{
		Name:                name,
		ShowForcedSubtitles: true,
	}); err != nil {
		return fmt.Errorf("store profile: %w", err)
	}

	return nil
}
