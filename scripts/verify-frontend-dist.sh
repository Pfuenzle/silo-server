#!/bin/sh
set -eu

dist_dir=${1:-web/dist}
index_file="$dist_dir/index.html"

fail() {
	printf 'frontend dist: %s\n' "$1" >&2
	exit 1
}

[ -f "$index_file" ] || fail "missing $index_file"
grep -q '<div id="root"></div>' "$index_file" || fail "index shell has no React root"
grep -Eq '<script[^>]+type="module"[^>]+src="/assets/[^" ]+\.js"' "$index_file" ||
	fail "index shell has no production module entry"
grep -q '/src/main.tsx' "$index_file" && fail "index shell still points at Vite source entry"

asset_refs=$(grep -oE '(src|href)="/assets/[^" ]+"' "$index_file" | sed 's/^[^=]*="//; s/"$//' || true)
[ -n "$asset_refs" ] || fail "index shell references no assets"

for asset_ref in $asset_refs; do
	asset_path="$dist_dir${asset_ref}"
	[ -f "$asset_path" ] || fail "index shell references missing asset $asset_ref"
done

printf 'frontend dist verified: %s (%s asset references)\n' "$index_file" "$(printf '%s\n' "$asset_refs" | wc -l | tr -d ' ')"
