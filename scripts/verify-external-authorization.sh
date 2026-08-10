#!/usr/bin/env bash
set -euo pipefail

: "${SILO_TEST_DATABASE_URL:?set SILO_TEST_DATABASE_URL to an isolated migrated PostgreSQL URL}"

regex='^TestExternalAuthorization_(PromoteDemoteAndAudit|RejectsMalformedEnvelope|RejectsConflictingMappings|AuditFailureRollsBack|AuditFailureRollsBackFirstProvision)$'
tests=(PromoteDemoteAndAudit RejectsMalformedEnvelope RejectsConflictingMappings AuditFailureRollsBack AuditFailureRollsBackFirstProvision)
output="$(mktemp)"
trap 'rm -f "$output"' EXIT

go test -race -shuffle=on -count=20 -v ./internal/auth -run "$regex" 2>&1 | tee "$output"
for test_name in "${tests[@]}"; do
	if [[ "$(grep -c "^=== RUN   TestExternalAuthorization_${test_name}$" "$output")" -ne 20 ]]; then
		echo "required test did not execute 20 times: TestExternalAuthorization_${test_name}" >&2
		exit 1
	fi
	done
if grep -q '^--- SKIP:' "$output"; then
	echo 'focused authorization gate skipped a test' >&2
	exit 1
fi
