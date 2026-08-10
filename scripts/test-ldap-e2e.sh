#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cache="$(mktemp -d "${TMPDIR:-/tmp}/silo-ldap-build.XXXXXX")"
receipt="$cache/production-build-receipt"
process_registry="$cache/process-groups"
run_id="${SILO_TEST_RUN_ID:-silo-$(od -An -N16 -tx1 /dev/urandom | tr -d '[:space:]')}"
host_network="silo-ldap-host-${run_id#silo-}"
host_postgres="${host_network}-postgres"
host_label="silo.ldap.host.test=$run_id"
test_pattern="${SILO_LDAP_TEST_PATTERN:-^TestPackagedLDAP_}"
host_pattern='^(TestExternalGroups_Parse_rejects_untrusted_shapes|TestExternalGroups_Parse_normalizesIDsAndRejectsBounds|TestExternalGroups_Parse_rejectsOversizedEnvelope|TestExternalAuthorization_RejectsMalformedEnvelope|TestExternalAuthorization_RejectsConflictingMappings|TestPluginProvider_ConcurrentFirstLogin)$'
source_hash=""
backend_tests=0
expected_backend_tests=0
evidence_log="${SILO_LDAP_EVIDENCE_LOG:-}"
evidence_dir="${SILO_LDAP_EVIDENCE_DIR:-}"
source "$root/scripts/test-process-groups.sh"
[[ "$run_id" =~ ^silo-[a-f0-9]{32}$ ]] || { printf 'invalid SILO_TEST_RUN_ID\n' >&2; exit 2; }
mkdir -m 700 "$process_registry"
export SILO_TEST_PROCESS_REGISTRY="$process_registry" SILO_TEST_RUN_ID="$run_id"

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  silo_cleanup_status "$status" "$process_registry" || status=$?
  docker ps -aq --filter "label=silo.oidc.test=$run_id" | xargs -r docker rm -f >/dev/null
  docker network ls -q --filter "label=silo.oidc.test=$run_id" | xargs -r docker network rm >/dev/null
  docker ps -aq --filter "label=silo.ldap.test=$run_id" | xargs -r docker rm -f >/dev/null
  docker ps -aq --filter "label=$host_label" | xargs -r docker rm -f >/dev/null
  docker network rm "$host_network" >/dev/null 2>&1 || true
  for _ in {1..20}; do
    if [[ -z "$(docker ps -aq --filter "label=silo.oidc.test=$run_id")" && -z "$(docker network ls -q --filter "label=silo.oidc.test=$run_id")" && -z "$(docker ps -aq --filter "label=silo.ldap.test=$run_id")" && -z "$(docker ps -aq --filter "label=$host_label")" && -z "$(docker network ls -q --filter "label=$host_label")" && -z "$(docker network inspect -f '{{.Name}}' "$host_network" 2>/dev/null)" ]]; then
      break
    fi
    sleep 1
  done
  if [[ -n "$(docker ps -aq --filter "label=silo.oidc.test=$run_id")" || -n "$(docker network ls -q --filter "label=silo.oidc.test=$run_id")" || -n "$(docker ps -aq --filter "label=silo.ldap.test=$run_id")" || -n "$(docker ps -aq --filter "label=$host_label")" || -n "$(docker network ls -q --filter "label=$host_label")" || -n "$(docker network inspect -f '{{.Name}}' "$host_network" 2>/dev/null)" ]]; then
    status=1
  fi
  if [[ "$status" -eq 0 ]]; then
    combined_evidence="$cache/ldap-combined-evidence.log"
    cat "$backend_log" "$receipt" > "$combined_evidence"
    if [[ -n "$evidence_log" && -f "$evidence_log" ]]; then
      cat "$evidence_log" >> "$combined_evidence"
    fi
    for secret in service-password test-password '(&(objectClass=inetOrgPerson)(uid={username}))' '(&(objectClass=posixGroup)(memberUid={username}))' '-----BEGIN PRIVATE KEY-----'; do
      if grep -Fq -- "$secret" "$combined_evidence"; then
        printf 'LDAP backend/receipt/evidence leaked fixture secret fingerprint=%s\n' "$(printf %s "$secret" | sha256sum | cut -d' ' -f1)" >&2
        status=1
      fi
    done
    if grep -Fq -- 'cn=admin,dc=example,dc=org' "$combined_evidence"; then
      printf 'LDAP backend/receipt/evidence leaked fixture-only bind DN; LDAP fixture logs must redact it\n' >&2
      status=1
    fi
  fi
  if [[ "$status" -eq 0 ]]; then
    if [[ -n "$evidence_dir" ]]; then
      mkdir -p -m 700 "$evidence_dir" 2>/dev/null || true
      printf 'source_hash=%s production_builds=1 backend_tests=%s expected_backend_tests=%s browser_tests=0 expected_browser_tests=0 cleanup=clean evidence_log_present=%s exit=0\n' "$source_hash" "$backend_tests" "$expected_backend_tests" "$([[ -n "$evidence_log" ]] && printf true || printf false)" > "$evidence_dir/receipt" 2>/dev/null || true
      printf 'source_hash=%s backend_tests=%s expected_backend_tests=%s exit=0\n' "$source_hash" "$backend_tests" "$expected_backend_tests" > "$evidence_dir/backend-summary.log" 2>/dev/null || true
    fi
    printf 'ldap_e2e_receipt source_hash=%s production_builds=1 backend_tests=%s expected_backend_tests=%s browser_tests=0 cleanup=clean exit=0\n' "$source_hash" "$backend_tests" "$expected_backend_tests"
  fi
  chmod -R u+w "$cache/source" 2>/dev/null || true
  rm -rf "$cache"
  exit "$status"
}
trap cleanup EXIT INT TERM

source_hash="$(cd "$root" && tar --sort=name --mtime='UTC 1970-01-01' --exclude=.git --exclude=.omo --exclude=web/node_modules --exclude=web/dist --exclude=test-results --exclude=web/test-results --exclude=playwright-report --exclude=web/playwright-report -cf - . | sha256sum | cut -d' ' -f1)"
cp -a "$root/." "$cache/source"
rm -rf "$cache/source/.git" "$cache/source/.omo" "$cache/source/web/node_modules" "$cache/source/web/dist"
if command -v pnpm >/dev/null 2>&1; then
  (cd "$cache/source" && make build)
else
  mkdir -p "$cache/bin"
  printf '%s\n' '#!/usr/bin/env sh' 'exec npx --yes pnpm@9.15.0 "$@"' > "$cache/bin/pnpm"
  chmod 700 "$cache/bin/pnpm"
  (cd "$cache/source" && PATH="$cache/bin:$PATH" make build)
fi
printf 'source_hash=%s production_builds=1\n' "$source_hash" > "$receipt"
chmod -R a-w "$cache/source"
cd "$root"
backend_log="$cache/ldap-backend.log"
mapfile -t packaged_markers < <(go test -tags=integration ./internal/auth/integration -list "$test_pattern" | grep '^TestPackagedLDAP_')
mapfile -t host_markers < <(go test ./internal/auth -list "$host_pattern" | grep '^Test')
expected_backend_tests=$(( ${#packaged_markers[@]} + ${#host_markers[@]} ))
[[ "$expected_backend_tests" -gt 0 ]] || { printf 'no LDAP test markers selected\n' >&2; exit 1; }
SILO_LDAP_BUILD_CACHE="$cache/source" SILO_LDAP_BUILD_SOURCE_HASH="$source_hash" SILO_LDAP_BUILD_RECEIPT="$receipt" go test -tags=integration -race -count=1 -timeout=40m -v ./internal/auth/integration -run "$test_pattern" | tee "$backend_log"
docker network create --label "$host_label" "$host_network" >/dev/null
docker run -d --rm --name "$host_postgres" --network "$host_network" --label "$host_label" -e POSTGRES_USER=silo -e POSTGRES_PASSWORD=silo -e POSTGRES_DB=silo -p 127.0.0.1::5432 pgvector/pgvector:pg17 >/dev/null
for _ in {1..45}; do
  if docker exec "$host_postgres" pg_isready -U silo -d silo >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$host_postgres" pg_isready -U silo -d silo >/dev/null
host_port="$(docker port "$host_postgres" 5432/tcp)"
host_port="${host_port##*:}"
host_database_url="postgres://silo:silo@127.0.0.1:${host_port}/silo?sslmode=disable"
DATABASE_URL="$host_database_url" SECRET_KEY=integration-secret-key-0123456789abcdef "$cache/source/silo" --migrate-only
SILO_TEST_DATABASE_URL="$host_database_url" go test -race -count=1 -timeout=10m -v ./internal/auth -run "$host_pattern" | tee -a "$backend_log"
backend_tests=0
for marker in "${packaged_markers[@]}" "${host_markers[@]}"; do
  grep -Fq -- "--- PASS: $marker" "$backend_log" || { printf 'missing LDAP marker %s\n' "$marker" >&2; exit 1; }
  ((backend_tests += 1))
done
[[ "$backend_tests" -eq "$expected_backend_tests" ]] || { printf 'LDAP backend count=%s expected=%s\n' "$backend_tests" "$expected_backend_tests" >&2; exit 1; }
