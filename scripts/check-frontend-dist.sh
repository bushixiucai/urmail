#!/usr/bin/env bash
# Fail the build when the embedded frontend is stale or missing.
#
# The go:embed directive in internal/web embeds whatever happens to sit in
# internal/web/dist. Running "go build" without "make frontend" silently
# ships the previous release's frontend in a fresh binary — that is exactly
# how the first v0.6.7 package lost the #30/#31 fixes. Every build path
# (make build-go, packaging/deb/build.sh) must call this script first and
# fail loudly instead of embedding stale assets.
#
# Three checks, each independently sufficient to reject:
#   1. presence: dist/index.html references a real assets/index-*.js bundle
#   2. freshness: no file under web/src (or web/index.html) is newer than
#      the bundle — a dirty source tree must be rebuilt first
#   3. stamp: the bundle records the git HEAD it was built from; a bundle
#      built before the newest web-touching commit is rejected on clean
#      trees (dirty trees are already caught by check 2)
set -euo pipefail

# URMAIL_DIST_GUARD_ROOT lets tests point the guard at a fixture tree.
ROOT="${URMAIL_DIST_GUARD_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
EMBED_DIR="$ROOT/internal/web/dist"
WEB_DIR="$ROOT/web"

fail() {
	printf 'frontend-dist guard: %s\n' "$1" >&2
	printf 'frontend-dist guard: run "make frontend" to rebuild the web assets, then build again.\n' >&2
	exit 1
}

# 1. Presence: the placeholder .gitkeep alone cannot be embedded.
[ -f "$EMBED_DIR/index.html" ] || fail "internal/web/dist/index.html is missing (no frontend build output found)"
# POSIX portable extraction: grep the module script src, strip the prefix.
BUNDLE="$(grep -o 'src="[^"]*assets/index-[^"]*\.js"' "$EMBED_DIR/index.html" | head -n1 | sed 's/^src="//; s/"$//')"
[ -n "$BUNDLE" ] || fail "internal/web/dist/index.html does not reference a bundled assets/index-*.js"
BUNDLE_PATH="$EMBED_DIR/${BUNDLE#/}"
[ -f "$BUNDLE_PATH" ] || fail "referenced bundle $BUNDLE is missing from internal/web/dist"

# 2. Freshness: any frontend source newer than the bundle means the dist
# output predates the last source edit, so embedding it would ship stale UI.
NEWER="$(find "$WEB_DIR/src" "$WEB_DIR/index.html" -type f -newer "$BUNDLE_PATH" -print -quit 2>/dev/null || true)"
if [ -n "$NEWER" ]; then
	printf 'frontend-dist guard: frontend source %s is newer than the built bundle\n' "$NEWER" >&2
	fail "internal/web/dist is stale"
fi

# 3. Stamp: make frontend writes .build-stamp with the git HEAD it built
# from. When that HEAD predates the newest commit touching web sources on a
# clean tree, the bundle cannot contain the current frontend.
STAMP_FILE="$EMBED_DIR/.build-stamp"
if [ -f "$STAMP_FILE" ] && git -C "$ROOT" rev-parse --verify HEAD >/dev/null 2>&1; then
	STAMP_SHA="$(head -n1 "$STAMP_FILE" | cut -d' ' -f1)"
	HEAD_SHA="$(git -C "$ROOT" rev-parse HEAD)"
	if [ "$STAMP_SHA" != "$HEAD_SHA" ]; then
		LAST_WEB_COMMIT="$(git -C "$ROOT" log -1 --format=%H -- web/src web/index.html 2>/dev/null || true)"
		if [ -n "$LAST_WEB_COMMIT" ] \
			&& git -C "$ROOT" merge-base --is-ancestor "$LAST_WEB_COMMIT" "$HEAD_SHA" 2>/dev/null \
			&& ! git -C "$ROOT" merge-base --is-ancestor "$LAST_WEB_COMMIT" "$STAMP_SHA" 2>/dev/null; then
			fail "internal/web/dist was built from $STAMP_SHA but HEAD ($HEAD_SHA) contains newer web commits"
		fi
	fi
fi

printf 'frontend-dist guard: ok\n'
