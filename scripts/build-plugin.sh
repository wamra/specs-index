#!/usr/bin/env bash
# Stage the installable plugin tree, with the six binaries inside, into OUT.
# CI force-pushes OUT as the orphan "plugin" branch. Usage: scripts/build-plugin.sh OUT [VERSION]
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:?usage: build-plugin.sh OUT [VERSION]}"
VERSION="${2:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"
# OUT is deleted first: refuse anything that is not a dedicated output directory.
OUT_ABS="$(mkdir -p "$OUT" && cd "$OUT" && pwd -P)"
ROOT_ABS="$(cd "$ROOT" && pwd -P)"
HOME_ABS="$(cd "${HOME:-/}" 2>/dev/null && pwd -P || echo /)"
case "$OUT_ABS" in
  / | "$HOME_ABS" | "$ROOT_ABS") echo "build-plugin.sh: refusing to delete $OUT_ABS" >&2; exit 2 ;;
esac
case "$ROOT_ABS/" in
  "$OUT_ABS"/*) echo "build-plugin.sh: refusing to delete $OUT_ABS, which contains the repository" >&2; exit 2 ;;
esac
rm -rf "$OUT"; mkdir -p "$OUT/.claude-plugin" "$OUT/bin" "$OUT/commands" "$OUT/skills/specs-search"
for t in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os="${t%/*}"; arch="${t#*/}"; ext=""; [ "$os" = windows ] && ext=".exe"
  ( cd "$ROOT" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/bin/specs-index-$os-$arch$ext" ./cmd/specs-index )
done
cp "$ROOT/scripts/plugin-launcher.sh" "$OUT/bin/specs-index"
# No pinned version in plugin.json: the commit SHA is then the version, so a rebuilt branch reaches users.
cp "$ROOT/.claude-plugin/plugin.json" "$OUT/.claude-plugin/"
cp "$ROOT/.mcp.json" "$OUT/"
cp "$ROOT"/commands/*.md "$OUT/commands/"
cp "$ROOT/skills/specs-search/SKILL.md" "$OUT/skills/specs-search/"
cp "$ROOT/LICENSE" "$ROOT/README.md" "$OUT/"
chmod +x "$OUT"/bin/*
