package auth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
)

func lockCanonicalizationDependencies(ctx context.Context, tx pgx.Tx, token canonicalizationToken) error {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM plugin_installations WHERE id = $1 FOR UPDATE`, token.InstallationID).Scan(&enabled); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock canonicalization installation: %w", err)
	}
	if !enabled {
		return ErrCanonicalizationStale
	}
	if err := tx.QueryRow(ctx, `SELECT enabled FROM plugin_auth_bindings WHERE plugin_installation_id = $1 AND capability_id = $2 FOR UPDATE`, token.InstallationID, token.CapabilityID).Scan(&enabled); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock canonicalization binding: %w", err)
	}
	if !enabled {
		return ErrCanonicalizationStale
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, canonicalizationIdentityLockKey(token)); err != nil {
		return fmt.Errorf("lock canonicalization identity key: %w", err)
	}
	ids := []int{token.Source, token.Target}
	sort.Ints(ids)
	for _, id := range ids {
		var found int
		if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&found); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock canonicalization user: %w", err)
		}
	}
	var subject string
	if err := tx.QueryRow(ctx, `SELECT external_subject FROM plugin_auth_identities WHERE plugin_installation_id = $1 AND user_id = $2 FOR UPDATE`, token.InstallationID, token.Source).Scan(&subject); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock canonicalization identity: %w", err)
	}
	if fingerprint(subject) != token.IdentityFingerprint {
		return ErrCanonicalizationStale
	}
	return nil
}

func canonicalizationIdentityLockKey(token canonicalizationToken) string {
	return strconv.Itoa(token.InstallationID) + ":" + token.CapabilityID + ":" + token.IdentityFingerprint
}
