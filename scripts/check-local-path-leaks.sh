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

workspace_root=$(cd "$repo_root/.." && pwd)
if [[ -f "$workspace_root/HANDOFF.md" ]]; then
	if grep -n -I -E "$old_silo_checkout" "$workspace_root/HANDOFF.md" >&2; then
		printf '%s\n' "local path leak check failed: stale pre-consolidation Silo checkout path in ../HANDOFF.md" >&2
		failed=1
	fi
fi

if [[ "$failed" -ne 0 ]]; then
	printf '%s\n' "Remove local machine paths from committed content. Use repository-relative paths instead." >&2
	exit 1
fi
