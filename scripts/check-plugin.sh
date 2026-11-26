#!/usr/bin/env bash
# Gate for a staged plugin tree: CI runs it before publishing, so a bad build never replaces the
# good plugin branch. Usage: scripts/check-plugin.sh OUT [EXPECTED_VERSION_TEXT]
set -euo pipefail
OUT="${1:?usage: check-plugin.sh OUT [EXPECTED_VERSION_TEXT]}"; WANT="${2:-}"
fail() { echo "check-plugin: $*" >&2; exit 1; }
for t in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64.exe windows-arm64.exe; do
  f="$OUT/bin/specs-index-$t"; [ -f "$f" ] || fail "missing $f"
  [ "$(wc -c < "$f")" -gt 5000000 ] || fail "$f is smaller than 5 MB"
done
[ -f "$OUT/bin/specs-index" ] || fail "missing the launcher"
[ -z "$(find "$OUT/bin" -type f ! -perm -u+x)" ] || fail "a file in bin/ is not executable"
for c in add index search trace coverage status; do
  [ -f "$OUT/commands/$c.md" ] || fail "missing commands/$c.md"
done
[ -f "$OUT/skills/specs-search/SKILL.md" ] || fail "missing skills/specs-search/SKILL.md"
for f in LICENSE THIRD_PARTY_NOTICES.md; do
  [ -s "$OUT/$f" ] || fail "missing or empty $f"
done
grep -q "MIT License" "$OUT/LICENSE" || fail "LICENSE is not the MIT license text"
grep -q "Apache-2.0" "$OUT/THIRD_PARTY_NOTICES.md" || fail "THIRD_PARTY_NOTICES.md lists no Apache-2.0 modules"
! grep -q "UNKNOWN: no license file" "$OUT/THIRD_PARTY_NOTICES.md" || fail "THIRD_PARTY_NOTICES.md has a module without a license"
[ "$(du -sk "$OUT" | cut -f1)" -lt 120000 ] || fail "tree is larger than 120 MB"
node - "$OUT" <<'JS' || fail "manifest check failed"
const fs = require('fs'), out = process.argv[2]
const plugin = JSON.parse(fs.readFileSync(`${out}/.claude-plugin/plugin.json`, 'utf8'))
if ('version' in plugin) throw new Error('plugin.json pins a version')
if (plugin.name !== 'specs-index') throw new Error(`plugin name is ${plugin.name}, not specs-index`)
const mcp = JSON.parse(fs.readFileSync(`${out}/.mcp.json`, 'utf8'))
const s = mcp.mcpServers && mcp.mcpServers['specs-index']
if (!s || s.command !== 'sh') throw new Error('.mcp.json has no specs-index server started through sh')
if (JSON.stringify(s.args) !== JSON.stringify(['${CLAUDE_PLUGIN_ROOT}/bin/specs-index', 'mcp'])) throw new Error('.mcp.json does not point at the launcher')
JS
# With no bundled binary and nothing on PATH the launcher must exit 127, not loop or hang.
iso="$(mktemp -d)"; cp "$OUT/bin/specs-index" "$iso/"; code=0
( PATH="$iso:/usr/bin:/bin" "$iso/specs-index" version >/dev/null 2>&1 ) || code=$?
rm -rf "$iso"
[ "$code" = 127 ] || fail "launcher without a binary exited $code, not 127"
got="$("$OUT/bin/specs-index" version)" || fail "launcher could not run the bundled binary"
[ -z "$WANT" ] || case "$got" in *"$WANT"*) ;; *) fail "version '$got' does not contain '$WANT'" ;; esac
echo "check-plugin: ok ($got)"
