#!/usr/bin/env bash
set -euo pipefail

usage() {
	printf 'usage: %s [--cached]\n' "${0##*/}" >&2
}

cached=0
case "${1:-}" in
	"")
		;;
	--cached)
		cached=1
		;;
	-h|--help)
		usage
		exit 0
		;;
	*)
		usage
		exit 2
		;;
esac

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

failed=0
t3_worktree_dir='\.t3'/'worktrees'
t3_worktree_id_prefix='t3''code-'
old_admin_silo_prefix='/blyatflix/admin/'
old_silo_checkout="(${old_admin_silo_prefix}silo-(server|plugin-sdk|plugin-auth-ldap|plugin-auth-oidc|plugin-requests-listenarr|plugin-watchprovider-anilist|plugins)|${old_admin_silo_prefix}silo-anislist)"

check_pattern() {
	local label=$1
	local pattern=$2
	shift 2

	local matches
	if [[ "$cached" -eq 1 ]]; then
		matches=$(git grep --cached -n -I -E "$pattern" -- "$@" 2>/dev/null) || return 0
	else
		matches=$(git grep -n -I -E "$pattern" -- "$@" 2>/dev/null) || return 0
	fi

	if [[ -n "$matches" ]]; then
		printf '%s\n' "local path leak check failed: $label" >&2
		printf '%s\n\n' "$matches" >&2
		failed=1
	fi
}

check_pattern \
	"T3 worktree path or generated worktree id" \
	"(${t3_worktree_dir}|/Users/[^[:space:]]*/${t3_worktree_dir}/|${t3_worktree_id_prefix}[0-9a-f]{8})" \
	docs/superpowers/specs docs/superpowers/plans

check_pattern \
	"absolute local filesystem path in generated superpowers docs" \
	'(/Users/[^[:space:]]+|/home/[^[:space:]]+|/Volumes/[^[:space:]]+|/var/folders/[^[:space:]]+|/private/tmp/[^[:space:]]+|[A-Za-z]:\\Users\\[^[:space:]]+)' \
	docs/superpowers/specs docs/superpowers/plans

check_pattern \
	"stale pre-consolidation Silo checkout path" \
	"${old_silo_checkout}" \
	AGENTS.md CLAUDE.md CONTRIBUTING.md DEVELOPMENT.md README.md docs scripts

check_pattern \
	"credential-bearing PostgreSQL URL in documentation" \
	'postgresql?://[a-z0-9._-]+:[^@[:space:]]+@' \
	DEVELOPMENT.md

shell_fixture_exception() {
	local path=$1
	local content=$2

	if [[ "$path" == scripts/dev-remote-setup.sh && "$content" == 'DATABASE_URL=postgres://silo:silo@localhost:5432/silo?sslmode=disable' ]]; then
		return 0
	fi
	if [[ "$path" == scripts/test-ldap-e2e.sh && "$content" == 'host_database_url="postgres://silo:silo@127.0.0.1:${host_port}/silo?sslmode=disable"' ]]; then
		return 0
	fi
	if [[ "$path" == scripts/test-ldap-e2e.sh && "$content" == 'docker run -d --rm --name "$host_postgres" --network "$host_network" --label "$host_label" -e POSTGRES_USER=silo -e POSTGRES_PASSWORD=silo -e POSTGRES_DB=silo -p 127.0.0.1::5432 pgvector/pgvector:pg17 >/dev/null' ]]; then
		return 0
	fi
	if [[ "$path" == scripts/test-ldap-e2e.sh && "$content" == 'DATABASE_URL="$host_database_url" SECRET_KEY=integration-secret-key-0123456789abcdef "$cache/source/silo" --migrate-only' ]]; then
		return 0
	fi

	return 1
}

check_shell_script_credentials() {
	local matches
	if [[ "$cached" -eq 1 ]]; then
		matches=$(git grep --cached -n -I -E 'postgresql?://[a-z0-9._-]+:[^@[:space:]]+@|POSTGRES_PASSWORD=[^[:space:]]+|SECRET_KEY=[^[:space:]"'"'"']+' -- 'scripts/*.sh' ':(exclude)scripts/check-local-path-leaks.sh' 2>/dev/null) || return 0
	else
		matches=$(git grep -n -I -E 'postgresql?://[a-z0-9._-]+:[^@[:space:]]+@|POSTGRES_PASSWORD=[^[:space:]]+|SECRET_KEY=[^[:space:]"'"'"']+' -- 'scripts/*.sh' ':(exclude)scripts/check-local-path-leaks.sh' 2>/dev/null) || return 0
	fi

	local unexpected=''
	local record path rest line content
	while IFS= read -r record; do
		path=${record%%:*}
		rest=${record#*:}
		line=${rest%%:*}
		content=${rest#*:}
		content="${content#"${content%%[![:space:]]*}"}"
		if ! shell_fixture_exception "$path" "$content"; then
			unexpected+="$path:$line:$content\n"
		fi
	done <<< "$matches"

	if [[ -n "$unexpected" ]]; then
		printf '%s\n' "local path leak check failed: credential-bearing default in tracked shell script" >&2
		printf '%b\n' "$unexpected" >&2
		failed=1
	fi
}

check_shell_script_credentials

workspace_root=$(cd "$repo_root/.." && pwd)
if [[ -f "$workspace_root/HANDOFF.md" ]]; then
	if grep -n -I -E "$old_silo_checkout" "$workspace_root/HANDOFF.md" >&2; then
		printf '%s\n' "local path leak check failed: stale pre-consolidation Silo checkout path in ../HANDOFF.md" >&2
		failed=1
	fi
	if grep -n -I -E 'postgresql?://[a-z0-9._-]+:[^@[:space:]]+@' "$workspace_root/AGENTS.md" "$workspace_root/HANDOFF.md" >&2; then
		printf '%s\n' "local path leak check failed: credential-bearing PostgreSQL URL in ../AGENTS.md or ../HANDOFF.md" >&2
		failed=1
	fi
fi

if [[ "$failed" -ne 0 ]]; then
	printf '%s\n' "Remove local machine paths from committed content. Use repository-relative paths instead." >&2
	exit 1
fi
