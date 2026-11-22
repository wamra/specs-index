#!/usr/bin/env bash
# Install specs-index (macOS or Linux), from a release archive or a checkout.
# Puts the binary in ~/.local/bin, writes a starter config when none exists, then prints the next steps.
#
# Release archive: run ./install.sh from the extracted directory; the prebuilt
# binary next to it is installed and Go is not needed.
# Source checkout: scripts/install.sh builds the binary with Go.
#
# Usage: install.sh [--binary FILE] [--bin-dir DIR]
set -euo pipefail

BIN_DIR="${HOME}/.local/bin"
BINARY=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --binary) BINARY="${2:?--binary needs a file}"; shift 2 ;;
    --bin-dir) BIN_DIR="${2:?--bin-dir needs a directory}"; shift 2 ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -z "$BINARY" ] && [ -f "$HERE/specs-index" ]; then BINARY="$HERE/specs-index"; fi
ROOT="$(cd "$HERE/.." && pwd)"

mkdir -p "$BIN_DIR"
if [ -n "$BINARY" ]; then
  [ -f "$BINARY" ] || { echo "binary not found: $BINARY" >&2; exit 1; }
  echo "== installing the prebuilt binary"
  cp "$BINARY" "$BIN_DIR/specs-index.new"
  chmod 0755 "$BIN_DIR/specs-index.new"
else
  command -v go >/dev/null 2>&1 || { echo "Go is required to build from source (or use a release archive): https://go.dev/dl/" >&2; exit 1; }
  echo "== building specs-index"
  ( cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)" -o "$BIN_DIR/specs-index.new" ./cmd/specs-index )
fi
mv -f "$BIN_DIR/specs-index.new" "$BIN_DIR/specs-index"
echo "installed $BIN_DIR/specs-index ($("$BIN_DIR/specs-index" version))"

case ":$PATH:" in *":$BIN_DIR:"*) ;; *) echo "NOTE: $BIN_DIR is not on PATH; add it to your shell profile." ;; esac

CONFIG="${HOME}/.config/specs-index/config.json"
if [ ! -f "$CONFIG" ]; then
  echo "== writing a starter config"
  "$BIN_DIR/specs-index" config init
fi
echo
echo "Next: cd <project> && specs-index add docs features && specs-index index   (specs-index doctor checks the index and the embedder)"
