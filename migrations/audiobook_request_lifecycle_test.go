package migrations

import (
	"strings"
	"testing"
)

func TestAudiobookRequestLifecycleMigrationPersistsRecoveryState(t *testing.T) {
	// Given the Todo 4 migration source.
	migrationBytes, err := FS.ReadFile("sql/20260909130000_audiobook_request_lifecycle.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := strings.Join(strings.Fields(string(migrationBytes)), " ")

	// When the migration is inspected for its durable lifecycle contract.
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS external_library_id text NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS external_download_id text NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS external_detail text NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS imported_path text NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS scan_run_id text NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS silo_audiobook_id text NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS silo_audiobook_link text NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS retryable boolean NOT NULL DEFAULT false",
		"DROP COLUMN IF EXISTS external_library_id",
	} {
		// Then every field is present in both the upgrade contract and recovery path.
		if !strings.Contains(migration, fragment) {
			t.Fatalf("lifecycle migration missing %q", fragment)
		}
	}
}

func TestAudiobookSubmissionMigrationPersistsIdempotencyLease(t *testing.T) {
	// Given the durable submission-state migration.
	migrationBytes, err := FS.ReadFile("sql/20260909140000_audiobook_request_submission_idempotency.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := strings.Join(strings.Fields(string(migrationBytes)), " ")

	// When the migration is inspected for replay-safe submission state.
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS fulfillment_key text NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS submission_state text NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS submission_started_at timestamp with time zone",
		"UPDATE public.media_requests SET fulfillment_key = id WHERE fulfillment_key = ''",
		"CHECK (submission_state IN ('', 'in_flight', 'fulfilled', 'failed'))",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_media_requests_fulfillment_key",
		"DROP COLUMN IF EXISTS fulfillment_key",
	} {
		// Then key, state, lease, uniqueness, and rollback are all durable.
		if !strings.Contains(migration, fragment) {
			t.Fatalf("submission migration missing %q", fragment)
		}
	}
}
