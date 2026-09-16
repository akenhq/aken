#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
run_case() {
  local expected="$1" status=0
  shift
  "$@" > "$test_dir/stdout" 2> "$test_dir/stderr" || status="$?"
  if [[ "$status" -ne "$expected" ]]; then
    cat "$test_dir/stdout" "$test_dir/stderr" >&2
    fail "expected exit $expected, got $status: $*"
  fi
}
export npm_config_cache="$test_dir/npm-cache"
dist="$test_dir/dist"
out="$test_dir/out"
mkdir -p "$dist"
for target in linux_amd64 linux_arm64 darwin_arm64; do
  cat > "$dist/aken-mcp_$target" <<'MOCK'
#!/bin/sh
printf '%s\n' "$@"
exit "${AKEN_TEST_EXIT:-0}"
MOCK
done
(cd "$dist" && sha256sum aken-mcp_* > SHA256SUMS)

run_case 0 bash "$script_dir/npm.sh" "$dist" v1.2.3 "$out"
tarballs=("$out/"*.tgz)
[[ "${#tarballs[@]}" -eq 4 ]] || fail 'expected four tarballs'
for name in aken-mcp akenhq-mcp-linux-x64 akenhq-mcp-linux-arm64 akenhq-mcp-darwin-arm64; do
  [[ -f "$out/$name-1.2.3.tgz" ]] || fail "missing tarball: $name"
done
node - "$out" <<'NODE'
const assert = require('node:assert/strict');
const out = process.argv[2];
const launcher = require(`${out}/aken-mcp/package.json`);
assert.equal(launcher.version, '1.2.3');
assert.equal('scripts' in launcher, false);
assert.deepEqual(Object.keys(launcher.optionalDependencies).sort(),
  ['@akenhq/mcp-darwin-arm64', '@akenhq/mcp-linux-arm64', '@akenhq/mcp-linux-x64']);
for (const key of ['linux-x64', 'linux-arm64', 'darwin-arm64']) {
  const pkg = require(`${out}/mcp-${key}/package.json`);
  const [os, cpu] = key.split('-');
  assert.equal(pkg.version, '1.2.3');
  assert.equal(launcher.optionalDependencies[`@akenhq/mcp-${key}`], '1.2.3');
  assert.deepEqual(pkg.os, [os]);
  assert.deepEqual(pkg.cpu, [cpu]);
  assert.equal('scripts' in pkg, false);
}
NODE
printf '%s\n' 'ok: 1. four tarballs and package metadata'

for tarball in "${tarballs[@]}"; do
  tar -tzf "$tarball" > "$test_dir/contents"
  for file in LICENSE NOTICE; do
    grep -Fxq "package/$file" "$test_dir/contents" || fail "missing $file: $tarball"
  done
  if [[ "$tarball" == "$out/aken-mcp-1.2.3.tgz" ]]; then
    grep -Fxq 'package/README.md' "$test_dir/contents" || fail 'missing launcher README.md'
  fi
done
for key in linux-x64 linux-arm64 darwin-arm64; do
  mkdir -p "$test_dir/unpack-$key"
  tar -xzf "$out/akenhq-mcp-$key-1.2.3.tgz" -C "$test_dir/unpack-$key"
  binary="$test_dir/unpack-$key/package/bin/aken-mcp"
  node -e 'require("node:assert/strict").equal(require("node:fs").statSync(process.argv[1]).mode & 0o777, 0o755)' "$binary"
  target=${key/-/_}
  target=${target/x64/amd64}
  expected=$(awk -v asset="aken-mcp_$target" '$2 == asset { print $1 }' "$dist/SHA256SUMS")
  actual=$(sha256sum "$binary")
  [[ "${actual%% *}" == "$expected" ]] || fail "wrong tarball hash: $key"
done
printf '%s\n' 'ok: 2. tarball binary modes, checksums, LICENSE, NOTICE, and launcher README'

run_case 0 bash "$script_dir/npm.sh" "$dist" v1.2.3-rc.1 "$test_dir/prerelease"
node - "$test_dir/prerelease" <<'NODE'
for (const name of ['aken-mcp', 'mcp-linux-x64', 'mcp-linux-arm64', 'mcp-darwin-arm64']) {
  require('node:assert/strict').equal(require(`${process.argv[2]}/${name}/package.json`).version, '1.2.3-rc.1');
}
NODE
printf '%s\n' 'ok: 3. prerelease versions'
for version in 1.2.3 v1.2 v1.2.3+meta; do
  run_case 1 bash "$script_dir/npm.sh" "$dist" "$version" "$test_dir/invalid"
  grep -Fxq 'npm.sh: invalid version' "$test_dir/stderr" || fail 'missing invalid version message'
done
printf '%s\n' 'ok: 4. invalid versions'
printf 'tampered\n' >> "$dist/aken-mcp_linux_amd64"
run_case 1 bash "$script_dir/npm.sh" "$dist" v1.2.3 "$test_dir/tampered"
grep -Fxq 'npm.sh: checksum mismatch for aken-mcp_linux_amd64' "$test_dir/stderr" || fail 'missing mismatch message'
printf '%s\n' 'ok: 5. tampered asset rejected'
run_case 1 bash "$script_dir/npm.sh" "$dist" v1.2.3 "$out"
printf '%s\n' 'ok: 6. non-empty OUT rejected'

key=$(node -p 'process.platform + "-" + process.arch')
case "$key" in
  linux-x64|linux-arm64|darwin-arm64) ;;
  *) printf 'skip: install cases 7-9 on %s\n' "$key"; exit 0 ;;
esac
mkdir -p "$test_dir/project"
cd "$test_dir/project"
run_case 0 npm install --offline --no-audit --no-fund "$out/aken-mcp-1.2.3.tgz" "$out/akenhq-mcp-$key-1.2.3.tgz"
run_case 0 node_modules/.bin/aken-mcp version 'a b' --x
printf '%s\n' version 'a b' --x > "$test_dir/expected"
cmp "$test_dir/expected" "$test_dir/stdout" || fail 'arguments changed'
run_case 7 env AKEN_TEST_EXIT=7 node_modules/.bin/aken-mcp version
cat > "node_modules/@akenhq/mcp-$key/bin/aken-mcp" <<'MOCK'
#!/bin/sh
trap 'printf "TERM\n" > "$1"; exit 0' TERM
touch "$2"
while :; do sleep 0.1; done
MOCK
node - "$test_dir" <<'NODE'
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { spawn } = require('node:child_process');
const dir = process.argv[2];
const child = spawn('node_modules/.bin/aken-mcp', [`${dir}/term`, `${dir}/ready`]);
const timeout = setTimeout(() => { child.kill('SIGKILL'); assert.fail('SIGTERM forwarding timed out'); }, 5000);
const ready = setInterval(() => {
  if (fs.existsSync(`${dir}/ready`)) {
    clearInterval(ready);
    child.kill('SIGTERM');
  }
}, 20);
child.on('exit', (code) => {
  clearTimeout(timeout);
  clearInterval(ready);
  assert.equal(code, 0);
  assert.equal(fs.readFileSync(`${dir}/term`, 'utf8'), 'TERM\n');
});
NODE
printf '%s\n' 'ok: 7. local install, arguments, exit code, and SIGTERM forwarding'

run_case 0 npm install -g --offline --no-audit --no-fund --prefix "$test_dir/global" "$out/aken-mcp-1.2.3.tgz" "$out/akenhq-mcp-$key-1.2.3.tgz"
global_root=$(npm root -g --offline --no-audit --no-fund --prefix "$test_dir/global")
binary=$(cd "$global_root/aken-mcp" && node -p "require.resolve('@akenhq/mcp-$key/bin/aken-mcp')")
[[ -f "$binary" ]] || fail 'resolved global binary does not exist'
target=${key/-/_}
target=${target/x64/amd64}
expected=$(awk -v asset="aken-mcp_$target" '$2 == asset { print $1 }' "$dist/SHA256SUMS")
actual=$(sha256sum "$binary")
[[ "${actual%% *}" == "$expected" ]] || fail 'wrong global binary hash'
run_case 0 "$test_dir/global/bin/aken-mcp" version
grep -Fxq version "$test_dir/stdout" || fail 'global launcher did not run'
printf '%s\n' 'ok: 8. global install binary lookup, checksum, and launcher'

mkdir -p "$test_dir/omit"
cd "$test_dir/omit"
run_case 0 npm install --offline --no-audit --no-fund --omit=optional "$out/aken-mcp-1.2.3.tgz"
run_case 1 node_modules/.bin/aken-mcp version
grep -Fq "@akenhq/mcp-$key is not installed" "$test_dir/stderr" || fail 'missing optional dependency message'
printf '%s\n' 'ok: 9. omitted optional dependency' 'npm_test.sh: all tests passed'
