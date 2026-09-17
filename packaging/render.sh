#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 3 ]]; then
  printf '%s\n' 'Usage: render.sh DIST VERSION MODE' >&2
  exit 2
fi
dist="$1"
version="$2"
mode="$3"
[[ "$version" =~ ^v[0-9][A-Za-z0-9._-]*$ ]] || { printf '%s\n' 'render.sh: invalid version' >&2; exit 1; }
[[ "$mode" == install || "$mode" == once || "$mode" == mcp ]] || { printf '%s\n' 'render.sh: invalid mode' >&2; exit 1; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
binary=aken
template=install.sh
targets=(linux_amd64 linux_arm64)
if [[ "$mode" == mcp ]]; then
  binary=aken-mcp
  template=install-mcp.sh
  targets+=(darwin_arm64)
fi
replacements=(-e "s/@VERSION@/$version/g" -e "s/@MODE@/$mode/g")
for target in "${targets[@]}"; do
  asset="${binary}_${target}"
  hash=$(awk -v asset="$asset" '$2 == asset { print $1 }' "$dist/SHA256SUMS")
  [[ "$hash" =~ ^[[:xdigit:]]{64}$ ]] || { printf 'render.sh: missing or invalid hash for %s\n' "$asset" >&2; exit 1; }
  placeholder=$(printf '%s' "$target" | tr '[:lower:]' '[:upper:]')
  replacements+=(-e "s/@SHA256_${placeholder}@/$hash/g")
done
launcher="$script_dir/launcher.sh"
[[ -f "$launcher" ]] || { printf '%s\n' 'render.sh: missing launcher.sh' >&2; exit 1; }
# The collector template carries the launcher inside a quoted heredoc at the @LAUNCHER@ line.
awk -v launcher="$launcher" '
  $0 == "@LAUNCHER@" {
    n = 0
    while ((getline line < launcher) > 0) { print line; n++ }
    close(launcher)
    if (n == 0) { print "render.sh: could not read " launcher > "/dev/stderr"; exit 1 }
    next
  }
  { print }
' "$script_dir/$template" | sed "${replacements[@]}"
