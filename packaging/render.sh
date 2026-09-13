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
[[ "$mode" == install || "$mode" == once ]] || { printf '%s\n' 'render.sh: invalid mode' >&2; exit 1; }
for arch in amd64 arm64; do
  hash=$(awk -v asset="aken_linux_$arch" '$2 == asset { print $1 }' "$dist/SHA256SUMS")
  [[ "$hash" =~ ^[[:xdigit:]]{64}$ ]] || { printf 'render.sh: missing or invalid hash for aken_linux_%s\n' "$arch" >&2; exit 1; }
  if [[ "$arch" == amd64 ]]; then amd64="$hash"; else arm64="$hash"; fi
done
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
sed -e "s/@VERSION@/$version/g" -e "s/@MODE@/$mode/g" \
  -e "s/@SHA256_LINUX_AMD64@/$amd64/g" -e "s/@SHA256_LINUX_ARM64@/$arm64/g" \
  "$script_dir/install.sh"
