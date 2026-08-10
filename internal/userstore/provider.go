package userstore

import "context"

// UserStoreProvider returns a UserStore scoped to a specific user.
// For SQLite, this returns a store wrapping the per-user SQLite DB from the pool.
// For Postgres, this returns a store scoped to the user_id in shared tables.
type UserStoreProvider interface {
	ForUser(ctx context.Context, userID int) (UserStore, error)
	Close() error
}

// ProvisioningCleanupProvider removes all provider-owned state for an account
// that was created during a failed provisioning attempt.
type ProvisioningCleanupProvider interface {
	DeleteUser(ctx context.Context, userID int) error
}

// CanonicalizationStoreState is the deletion-safety proof for storage owned
// outside the shared account transaction. Empty is meaningful only when Proven
// is true; callers must fail closed for providers that do not expose this proof.
type CanonicalizationStoreState struct {
	Proven            bool
	Empty             bool
	SharedTransaction bool
	Kind              CanonicalizationStoreKind
}

// CanonicalizationStoreKind identifies the persistence boundary that owns a
// user's store state. Canonicalization can only delete an account when every
// owned row participates in its PostgreSQL transaction.
type CanonicalizationStoreKind uint8

const (
	CanonicalizationStoreUnknown CanonicalizationStoreKind = iota
	CanonicalizationStorePostgres
	CanonicalizationStoreSQLite
)

// CanonicalizationStoreStateProvider exposes whether a user has separately
// owned store state that a canonicalization would have to delete.
type CanonicalizationStoreStateProvider interface {
	CanonicalizationStoreState(ctx context.Context, userID int) (CanonicalizationStoreState, error)
}

// TransactionalProvisioningProvider declares whether provider-owned account
// state can be created in the PostgreSQL transaction used for external login.
// External authentication must opt in explicitly: unknown providers are not
// safe to use for first-login provisioning.
type TransactionalProvisioningProvider interface {
	SupportsTransactionalProvisioning() bool
}
