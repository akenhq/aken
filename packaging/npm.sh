#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 3 ]]; then
  printf '%s\n' 'Usage: npm.sh DIST VERSION OUT' >&2
  exit 2
fi
dist="$1"
version="$2"
out="$3"
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { printf '%s\n' 'npm.sh: invalid version' >&2; exit 1; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
for asset in SHA256SUMS aken-mcp_linux_amd64 aken-mcp_linux_arm64 aken-mcp_darwin_arm64; do
  [[ -f "$dist/$asset" ]] || { printf 'npm.sh: missing %s\n' "$asset" >&2; exit 1; }
done
if [[ -e "$out" && -n "$(ls -A -- "$out")" ]]; then
  printf '%s\n' 'npm.sh: OUT must be empty' >&2
  exit 1
fi
mkdir -p "$out"
out=$(cd -- "$out" && pwd)
if command -v sha256sum >/dev/null 2>&1; then
  checksum=(sha256sum)
else
  checksum=(shasum -a 256)
fi
for key in linux-x64 linux-arm64 darwin-arm64; do
  os=${key%-*}
  cpu=${key#*-}
  arch=$cpu
  if [[ "$cpu" == x64 ]]; then arch=amd64; fi
  asset="aken-mcp_${os}_${arch}"
  package="$out/mcp-$key"
  mkdir -p "$package/bin"
  install -m 0755 "$dist/$asset" "$package/bin/aken-mcp"
  expected=$(awk -v asset="$asset" '$2 == asset { print $1 }' "$dist/SHA256SUMS")
  actual=$("${checksum[@]}" "$package/bin/aken-mcp")
  [[ -n "$expected" && "${actual%% *}" == "$expected" ]] || { printf 'npm.sh: checksum mismatch for %s\n' "$asset" >&2; exit 1; }
  cp "$script_dir/../LICENSE" "$script_dir/../NOTICE" "$package/"
  cat > "$package/package.json" <<EOF
{
  "name": "@akenhq/mcp-$key",
  "version": "${version#v}",
  "description": "aken-mcp binary for $os $cpu. Install aken-mcp instead.",
  "license": "Apache-2.0",
  "repository": {"type": "git", "url": "git+https://github.com/akenhq/aken.git", "directory": "packaging/npm"},
  "homepage": "https://aken.dev",
  "os": ["$os"],
  "cpu": ["$cpu"],
  "files": ["bin/aken-mcp", "NOTICE"]
}
EOF
done
package="$out/aken-mcp"
mkdir -p "$package/bin"
install -m 0755 "$script_dir/npm/aken-mcp.js" "$package/bin/aken-mcp.js"
cp "$script_dir/npm/README.md" "$script_dir/../LICENSE" "$script_dir/../NOTICE" "$package/"
cat > "$package/package.json" <<EOF
{
  "name": "aken-mcp",
  "version": "${version#v}",
  "description": "Local MCP server for Aken: gives your coding agent redacted, reviewed access to server logs.",
  "license": "Apache-2.0",
  "repository": {"type": "git", "url": "git+https://github.com/akenhq/aken.git", "directory": "packaging/npm"},
  "homepage": "https://aken.dev",
  "bin": {"aken-mcp": "bin/aken-mcp.js"},
  "files": ["bin/aken-mcp.js", "NOTICE"],
  "engines": {"node": ">=18"},
  "optionalDependencies": {
    "@akenhq/mcp-linux-x64": "${version#v}",
    "@akenhq/mcp-linux-arm64": "${version#v}",
    "@akenhq/mcp-darwin-arm64": "${version#v}"
  }
}
EOF
for directory in mcp-linux-x64 mcp-linux-arm64 mcp-darwin-arm64 aken-mcp; do
  (cd "$out/$directory" && npm pack --offline --no-audit --no-fund --silent --pack-destination "$out" >/dev/null)
done
