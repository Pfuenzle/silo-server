package auth

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

type canonicalizationOwnershipDependency struct {
	table  string
	column string
	source string
}

func TestExternalAccountCanonicalization_dependencyInventoryRequiresExplicitClassification(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	dependencies := inventoryCanonicalizationOwnershipDependencies(t, ctx, pool)
	registry := make(map[string]canonicalizationDependency, len(canonicalizationDependencyRegistry))
	for _, dependency := range canonicalizationDependencyRegistry {
		registry[dependency.name] = dependency
	}
	ownershipColumns := make(map[string]struct{}, len(canonicalizationOwnershipColumns))
	for _, column := range canonicalizationOwnershipColumns {
		ownershipColumns[column] = struct{}{}
	}

	// When
	problems := make([]string, 0)
	for _, dependency := range dependencies {
		classification, reason := classifyCanonicalizationOwnershipDependency(dependency, registry)
		if classification == "" {
			problems = append(problems, dependency.table+"."+dependency.column+" ("+dependency.source+"): unclassified")
		} else if classification == "ignored" && reason == "" {
			problems = append(problems, dependency.table+"."+dependency.column+" ("+dependency.source+"): ignored without reason")
		}
		if classification != "ignored" {
			if _, scanned := ownershipColumns[dependency.column]; !scanned {
				problems = append(problems, dependency.table+"."+dependency.column+" ("+dependency.source+"): absent from canonicalizationOwnershipColumns")
			}
		}
	}
	sort.Strings(problems)

	// Then
	if len(problems) != 0 {
		t.Fatalf("canonicalization ownership inventory requires exactly one supported, blocking, or ignored-with-reason classification:\n%s", strings.Join(problems, "\n"))
	}
}

func TestExternalAccountCanonicalization_dependencyInventoryCountsPartitionedRowsOnceAndBlocksUnknownOwnership(t *testing.T) {
	// Given
	ctx, pool := newPluginProviderDBTest(t)
	sourceID := insertPluginProviderTestUser(t, ctx, pool, "dependency-inventory-source")
	profileID := fmt.Sprintf("dependency-inventory-profile-%d", time.Now().UnixNano())
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin dependency inventory transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, 'Dependency inventory', true)`, profileID, sourceID); err != nil {
		t.Fatalf("seed source profile: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_favorites (user_id, profile_id, media_item_id) VALUES ($1, $2, 'dependency-inventory-supported')`, sourceID, profileID); err != nil {
		t.Fatalf("seed supported dependency: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE canonicalization_dependency_probe (id integer NOT NULL, user_id integer NOT NULL) PARTITION BY RANGE (id)`,
		`CREATE TABLE canonicalization_dependency_probe_p0 PARTITION OF canonicalization_dependency_probe FOR VALUES FROM (0) TO (10)`,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatalf("create unknown partitioned dependency: %v", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO canonicalization_dependency_probe (id, user_id) VALUES (1, $1)`, sourceID); err != nil {
		t.Fatalf("seed unknown partitioned dependency: %v", err)
	}

	// When
	dependencies, err := canonicalizationDependencies(ctx, tx, sourceID, profileID)

	// Then
	if err != nil {
		t.Fatalf("inventory canonicalization dependencies: %v", err)
	}
	if dependencies["user_favorites"] != 1 {
		t.Fatalf("supported dependency count = %d, want 1", dependencies["user_favorites"])
	}
	if dependencies["canonicalization_dependency_probe"] != 1 {
		t.Fatalf("partitioned unknown dependency count = %d, want 1 without parent/child double count", dependencies["canonicalization_dependency_probe"])
	}
	if canonicalizationSupportedDependency("canonicalization_dependency_probe") {
		t.Fatal("unknown ownership dependency is supported; want fail-closed blocker")
	}
}

func inventoryCanonicalizationOwnershipDependencies(t *testing.T, ctx context.Context, q canonicalizationQuerier) []canonicalizationOwnershipDependency {
	t.Helper()
	queries := []struct {
		source string
		query  string
	}{
		{"user FK", `SELECT DISTINCT relation.relname, attribute.attname FROM pg_constraint fk JOIN pg_class child ON child.oid = fk.conrelid LEFT JOIN pg_constraint parent_constraint ON parent_constraint.oid = fk.conparentid JOIN pg_class relation ON relation.oid = COALESCE(parent_constraint.conrelid, child.oid) JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace JOIN unnest(fk.conkey) AS key(attnum) ON true JOIN pg_attribute attribute ON attribute.attrelid = relation.oid AND attribute.attnum = key.attnum WHERE fk.contype = 'f' AND fk.confrelid = 'public.users'::regclass AND namespace.nspname = 'public' ORDER BY relation.relname, attribute.attname`},
		{"profile-like", `SELECT relation.relname, attribute.attname FROM pg_class relation JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace JOIN pg_attribute attribute ON attribute.attrelid = relation.oid WHERE namespace.nspname = 'public' AND relation.relkind IN ('r', 'p') AND NOT relation.relispartition AND attribute.attname = ANY(ARRAY['profile_id', 'silo_profile_id', 'default_profile_id', 'host_profile_id', 'suggester_profile_id', 'voter_profile_id']) AND attribute.attnum > 0 AND NOT attribute.attisdropped ORDER BY relation.relname, attribute.attname`},
		{"non-FK owner candidate", `SELECT relation.relname, attribute.attname FROM pg_class relation JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace JOIN pg_attribute attribute ON attribute.attrelid = relation.oid WHERE namespace.nspname = 'public' AND relation.relkind IN ('r', 'p') AND NOT relation.relispartition AND attribute.attnum > 0 AND NOT attribute.attisdropped AND (attribute.attname ~ '(^|_)(user|owner|creator|actor|requester|approved_by|impersonator|linking)(_id)?$' OR attribute.attname IN ('user_id', 'owner_id', 'created_by_user_id', 'updated_by_user_id', 'requested_by_user_id', 'actor_user_id', 'approved_by_user_id', 'impersonator_user_id', 'linking_user_id')) AND NOT EXISTS (SELECT 1 FROM pg_constraint fk JOIN unnest(fk.conkey) AS key(attnum) ON true WHERE fk.contype = 'f' AND fk.confrelid = 'public.users'::regclass AND fk.conrelid = relation.oid AND key.attnum = attribute.attnum) ORDER BY relation.relname, attribute.attname`},
	}
	seen := make(map[string]struct{})
	dependencies := make([]canonicalizationOwnershipDependency, 0)
	for _, query := range queries {
		rows, err := q.Query(ctx, query.query)
		if err != nil {
			t.Fatalf("inventory %s dependencies: %v", query.source, err)
		}
		for rows.Next() {
			var dependency canonicalizationOwnershipDependency
			if err := rows.Scan(&dependency.table, &dependency.column); err != nil {
				rows.Close()
				t.Fatalf("scan %s dependency: %v", query.source, err)
			}
			dependency.source = query.source
			key := dependency.table + "." + dependency.column
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				dependencies = append(dependencies, dependency)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate %s dependencies: %v", query.source, err)
		}
		rows.Close()
	}
	return dependencies
}

func classifyCanonicalizationOwnershipDependency(dependency canonicalizationOwnershipDependency, registry map[string]canonicalizationDependency) (string, string) {
	if dependency.source == "non-FK owner candidate" {
		if reason := ignoredCanonicalizationOwnershipCandidate(dependency); reason != "" {
			return "ignored", reason
		}
	}
	registered, found := registry[dependency.table]
	if !found {
		return "", ""
	}
	supported := canonicalizationSupportedDependency(dependency.table)
	if !registered.blocking && supported {
		return "supported", ""
	}
	if registered.blocking && !supported {
		return "blocking", ""
	}
	return "", "registry and runtime classification disagree"
}

func ignoredCanonicalizationOwnershipCandidate(dependency canonicalizationOwnershipDependency) string {
	switch dependency.column {
	case "discord_user_id", "external_user_id", "pseudo_user_id", "streamapp_user_id":
		return "external or compatibility identifier, not a local account owner"
	case "lease_owner":
		return "worker lease identifier, not a local account owner"
	case "connect_user_id":
		return "upstream Connect identity, not a local account owner"
	case "impersonator_user_id", "created_by_user_id", "updated_by_user_id", "downloaded_by", "original_user_id", "silo_user_id", "invited_by", "created_by", "linking_user_id":
		return "immutable provenance or external-link attribution, not active account ownership"
	default:
		return ""
	}
}
