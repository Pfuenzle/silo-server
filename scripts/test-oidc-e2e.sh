#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
plugin_bin="${OIDC_PLUGIN_BIN:-$root/../silo-plugin-auth-oidc/dist/linux-amd64/silo-plugin-auth-oidc}"
chrome="${PLAYWRIGHT_CHROME_EXECUTABLE_PATH:-/blyatflix/.nix-profile/bin/chromium}"
cache="$(mktemp -d "${TMPDIR:-/tmp}/silo-oidc-build.XXXXXX")"
receipt="$cache/production-build-receipt"
process_registry="$cache/process-groups"
run_id="${SILO_TEST_RUN_ID:-silo-$(od -An -N16 -tx1 /dev/urandom | tr -d '[:space:]')}"
evidence_dir="${SILO_OIDC_EVIDENCE_DIR:-}"
test_pattern="${SILO_OIDC_TEST_PATTERN:-^(TestPackagedOIDC_EndToEnd|TestPackagedOIDC_HostStartsProvider|TestPackagedOIDC_HostRemainsAliveAfterStartup|TestPackagedOIDC_FirstLoginSessionFailureLeavesNoResidue|TestPackagedOIDC_LifecyclePreservesLocalAdminAndExternalIdentity|TestPackagedOIDC_RejectsInvalidUpstreamResponsesWithoutMutation|TestPackagedOIDC_PackageReplacementPreservesInstallationState)$}"
backend_tests=0
browser_tests=0
source_hash=""
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
  if [[ -n "$(docker ps -aq --filter "label=silo.oidc.test=$run_id")" ]] || [[ -n "$(docker network ls -q --filter "label=silo.oidc.test=$run_id")" ]]; then
    status=1
  fi
  if [[ "$status" -eq 0 ]]; then
    if [[ -n "$evidence_dir" ]]; then
      mkdir -p -m 700 "$evidence_dir" 2>/dev/null || true
      {
        printf 'source_hash=%s production_builds=1 backend_tests=%s expected_backend_tests=7 browser_tests=%s expected_browser_tests=6 cleanup=clean exit=0\n' "$source_hash" "$backend_tests" "$browser_tests"
      } > "$evidence_dir/receipt" 2>/dev/null || true
      printf 'source_hash=%s backend_tests=%s expected_backend_tests=7 exit=0\n' "$source_hash" "$backend_tests" > "$evidence_dir/backend-summary.log" 2>/dev/null || true
      printf 'source_hash=%s browser_tests=%s expected_browser_tests=6 exit=0\n' "$source_hash" "$browser_tests" > "$evidence_dir/browser-summary.log" 2>/dev/null || true
    fi
    printf 'oidc_e2e_receipt source_hash=%s production_builds=1 backend_tests=%s browser_tests=%s cleanup=clean exit=0\n' "$source_hash" "$backend_tests" "$browser_tests"
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
printf 'production_builds=1 source_hash=%s\n' "$source_hash"

cd "$root"
backend_log="$cache/oidc-backend.log"
SILO_OIDC_BUILD_CACHE="$cache/source" SILO_OIDC_BUILD_SOURCE_HASH="$source_hash" SILO_OIDC_BUILD_RECEIPT="$receipt" OIDC_PLUGIN_BIN="$plugin_bin" go test -tags=integration -race -count=1 -timeout=40m -v ./internal/auth/integration -run "$test_pattern" | tee "$backend_log"
backend_tests="$(grep -Ec '^--- PASS: TestPackagedOIDC_' "$backend_log")"
[[ "$backend_tests" -eq 7 ]] || { printf 'OIDC backend count=%s expected=7\n' "$backend_tests" >&2; exit 1; }
cd web
if command -v corepack >/dev/null 2>&1; then
  runner=(corepack pnpm)
elif command -v pnpm >/dev/null 2>&1; then
  runner=(pnpm)
else
  runner=(npx --yes pnpm@9.15.0)
fi
browser_log="$cache/oidc-browser.log"
SILO_OIDC_E2E=1 SILO_OIDC_BUILD_CACHE="$cache/source" SILO_OIDC_BUILD_SOURCE_HASH="$source_hash" SILO_OIDC_BUILD_RECEIPT="$receipt" PLAYWRIGHT_CHROME_EXECUTABLE_PATH="$chrome" "${runner[@]}" exec playwright test e2e/oidc-login.spec.ts --project=chromium | tee "$browser_log"
browser_tests="$(grep -Eo '[0-9]+ passed' "$browser_log" | tail -n 1 | cut -d' ' -f1)"
[[ "$browser_tests" -eq 6 ]] || { printf 'OIDC browser count=%s expected=6\n' "$browser_tests" >&2; exit 1; }
cd "$root"
if [[ -n "$(docker ps -aq --filter "label=silo.oidc.test=$run_id")" ]] || [[ -n "$(docker network ls -q --filter "label=silo.oidc.test=$run_id")" ]]; then
  docker ps -a --filter "label=silo.oidc.test=$run_id"
  docker network ls --filter "label=silo.oidc.test=$run_id"
  exit 1
fi
