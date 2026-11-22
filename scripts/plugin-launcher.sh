#!/bin/sh
# Runs the specs-index binary bundled for this OS and CPU; else specs-index from PATH.
# Exit 127 when neither can run, so callers can fall back or report it.
dir=$(cd "$(dirname "$0")" && pwd)
case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  MINGW*|MSYS*|CYGWIN*) os=windows ;;
  *) os= ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|AMD64|amd64) arch=amd64 ;;
  *) arch= ;;
esac
if [ -n "$os" ] && [ -n "$arch" ]; then
  bin="$dir/specs-index-$os-$arch"
  [ "$os" = windows ] && bin="$bin.exe"
  if [ -f "$bin" ]; then
    # A synced or copied plugin directory can lose the executable bit.
    [ -x "$bin" ] || chmod +x "$bin" 2>/dev/null
    [ -x "$bin" ] && exec "$bin" "$@"
  fi
fi
# This directory is on PATH too (plugin bin/), so skip it or the launcher would run itself.
oldifs=$IFS; IFS=:
for p in $PATH; do
  [ -n "$p" ] || continue
  [ "$(cd "$p" 2>/dev/null && pwd)" = "$dir" ] && continue
  for c in "$p/specs-index" "$p/specs-index.exe"; do
    if [ -f "$c" ] && [ -x "$c" ]; then IFS=$oldifs; exec "$c" "$@"; fi
  done
done
IFS=$oldifs
echo "specs-index: no usable binary for ${os:-unknown}/${arch:-unknown} in $dir and none on PATH" >&2
exit 127
