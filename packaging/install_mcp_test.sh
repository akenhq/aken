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
  local -a leftovers=("$TMPDIR"/*)
  [[ "${#leftovers[@]}" -eq 0 ]] || fail 'temporary files leaked'
}
assert_line() { grep -Fxq -- "$1" "$2" || fail "missing line: $1"; }
shopt -s nullglob

tag=v0.0.0
other_tag=v0.0.1
export AKEN_TEST_RELEASES="$test_dir/releases"
for version in "$tag" "$other_tag"; do
  dist="$AKEN_TEST_RELEASES/$version"
  mkdir -p "$dist"
  for target in linux_amd64 linux_arm64 darwin_arm64; do
    # These deliberately cannot execute: installation must only copy the asset.
    printf 'MCP fixture %s %s\n' "$version" "$target" > "$dist/aken-mcp_$target"
  done
  (cd "$dist" && sha256sum aken-mcp_* > SHA256SUMS)
  bash "$script_dir/render.sh" "$dist" "$version" mcp > "$dist/install-mcp.sh"
  bash -n "$dist/install-mcp.sh"
  if grep -Eq '@(VERSION|SHA256_[A-Z0-9_]+)@' "$dist/install-mcp.sh"; then
    fail 'unreplaced placeholder'
  fi
done
dist="$AKEN_TEST_RELEASES/$tag"

# Keep downloads offline, but exercise URL selection and failure handling.
mkdir -p "$test_dir/commands" "$test_dir/tmp"
cat > "$test_dir/commands/curl" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
url='' dest=''
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --proto|--proto-redir) [[ "$2" == '=https' ]]; shift 2 ;;
    -o) dest="$2"; shift 2 ;;
    https://github.com/akenhq/aken/releases/download/*) url="$1"; shift ;;
    -fsSL|--tlsv1.2) shift ;;
    *) exit 2 ;;
  esac
done
cp "$AKEN_TEST_RELEASES/${url#https://github.com/akenhq/aken/releases/download/}" "$dest"
MOCK
cat > "$test_dir/commands/uname" <<'MOCK'
#!/usr/bin/env bash
case "$1" in
  -s) printf '%s\n' "$AKEN_TEST_OS" ;;
  -m) printf '%s\n' "$AKEN_TEST_MACHINE" ;;
  *) exit 2 ;;
esac
MOCK
chmod +x "$test_dir/commands/"*
export PATH="$test_dir/commands:$PATH"
export TMPDIR="$test_dir/tmp"
unset AKEN_RELEASE_URL
export AKEN_TEST_OS=Linux AKEN_TEST_MACHINE=x86_64
bin_dir="$test_dir/bin with spaces"

run_case 1 bash "$script_dir/install-mcp.sh" --bin-dir "$bin_dir"
run_case 0 bash "$dist/install-mcp.sh" --help
run_case 2 bash "$dist/install-mcp.sh" --unknown
run_case 2 bash "$dist/install-mcp.sh" --version
run_case 2 bash "$dist/install-mcp.sh" --bin-dir
run_case 2 bash "$dist/install-mcp.sh" --bin-dir --help
run_case 1 bash "$dist/install-mcp.sh" --version invalid
run_case 1 bash "$dist/install-mcp.sh" --bin-dir relative
printf '%s\n' 'ok: unrendered copy, help, and argument validation'

for target in linux_amd64 linux_arm64 darwin_arm64; do
  case "$target" in
    linux_amd64) export AKEN_TEST_OS=Linux AKEN_TEST_MACHINE=x86_64 ;;
    linux_arm64) export AKEN_TEST_OS=Linux AKEN_TEST_MACHINE=aarch64 ;;
    darwin_arm64) export AKEN_TEST_OS=Darwin AKEN_TEST_MACHINE=arm64 ;;
  esac
  run_case 0 bash "$dist/install-mcp.sh" --bin-dir "$bin_dir"
  cmp "$dist/aken-mcp_$target" "$bin_dir/aken-mcp" || fail 'wrong installed asset'
  [[ -x "$bin_dir/aken-mcp" ]] || fail 'binary is not executable'
  assert_line 'Add this line to your shell profile, then run it in this terminal:' "$test_dir/stdout"
done
printf '%s\n' 'ok: supported platforms, executable installation, and PATH hint'

run_case 0 bash -s -- --bin-dir "$bin_dir" < "$dist/install-mcp.sh"
run_case 0 env PATH="$bin_dir:$PATH" bash "$dist/install-mcp.sh" --bin-dir "$bin_dir"
if grep -Fq 'export PATH=' "$test_dir/stdout"; then fail 'unnecessary PATH hint'; fi
mkdir -p "$test_dir/directory-destination/aken-mcp"
run_case 1 bash "$dist/install-mcp.sh" --bin-dir "$test_dir/directory-destination"
[[ ! -e "$test_dir/directory-destination/aken-mcp/aken-mcp_darwin_arm64" ]] || fail 'installed inside destination directory'
printf '%s\n' 'ok: piped script, existing PATH, and directory destination rejection'

export AKEN_TEST_OS=Darwin AKEN_TEST_MACHINE=x86_64
run_case 1 bash "$dist/install-mcp.sh" --bin-dir "$bin_dir"
assert_line 'Unsupported platform: Darwin/x86_64 (supported: Linux amd64/arm64 and macOS arm64).' "$test_dir/stderr"
export AKEN_TEST_OS=Linux AKEN_TEST_MACHINE=x86_64
run_case 0 bash "$dist/install-mcp.sh" --version "$other_tag" --bin-dir "$bin_dir"
cmp "$AKEN_TEST_RELEASES/$other_tag/aken-mcp_linux_amd64" "$bin_dir/aken-mcp"
run_case 1 bash "$dist/install-mcp.sh" --version v0.0.2 --bin-dir "$bin_dir"
assert_line 'Could not download install-mcp.sh.' "$test_dir/stderr"
printf '%s\n' 'ok: unsupported platform, version forwarding, and failed download cleanup'

# Exercise the shasum fallback with a PATH that has no sha256sum.
mkdir -p "$test_dir/fallback"
for command in bash shasum cp mktemp install mkdir rm; do
  ln -s "$(command -v "$command")" "$test_dir/fallback/$command"
done
run_case 0 env PATH="$test_dir/commands:$test_dir/fallback" bash "$dist/install-mcp.sh" --bin-dir "$bin_dir"
cmp "$dist/aken-mcp_linux_amd64" "$bin_dir/aken-mcp"
printf '%s\n' 'ok: shasum fallback'

# Isolate PATH so agent detection never depends on the test runner's agents.
agent_commands="$test_dir/agents"
mkdir -p "$agent_commands"
export AKEN_TEST_AGENT_LOG="$test_dir/agent-executed"
agent_path="$agent_commands:$test_dir/commands:$test_dir/fallback"
run_case 0 env PATH="$agent_path" bash "$dist/install-mcp.sh" --bin-dir "$bin_dir"
assert_line 'Agent setup: https://github.com/akenhq/aken/blob/main/docs/mcp.md' "$test_dir/stdout"
if grep -Eq 'Register with|Connect Cursor' "$test_dir/stdout"; then fail 'instructions for an absent agent'; fi

for agent in claude codex cursor cursor-agent; do
  cat > "$agent_commands/$agent" <<'MOCK'
#!/usr/bin/env bash
printf 'agent was executed\n' >> "$AKEN_TEST_AGENT_LOG"
exit 99
MOCK
  # An entry that cannot execute must not count as an installed agent.
  run_case 0 env PATH="$agent_path" bash "$dist/install-mcp.sh" --bin-dir "$bin_dir"
  if grep -Eq 'Register with|Connect Cursor' "$test_dir/stdout"; then fail 'non-executable agent detected'; fi
  chmod +x "$agent_commands/$agent"
  run_case 0 env PATH="$agent_path" bash "$dist/install-mcp.sh" --bin-dir "$bin_dir"
  case "$agent" in
    claude)
      assert_line 'Register with Claude Code:' "$test_dir/stdout"
      if grep -Eq 'Codex CLI|Connect Cursor' "$test_dir/stdout"; then fail 'unrelated agent instructions'; fi
      ;;
    codex)
      assert_line 'Register with Codex CLI:' "$test_dir/stdout"
      if grep -Eq 'Claude Code|Connect Cursor' "$test_dir/stdout"; then fail 'unrelated agent instructions'; fi
      ;;
    cursor|cursor-agent)
      assert_line 'Connect Cursor: https://github.com/akenhq/aken/blob/main/docs/mcp.md#cursor' "$test_dir/stdout"
      if grep -Fq 'Register with' "$test_dir/stdout"; then fail 'unrelated agent instructions'; fi
      ;;
  esac
  mv "$agent_commands/$agent" "$test_dir/saved-$agent"
done
for agent in claude codex cursor cursor-agent; do
  cp "$test_dir/saved-$agent" "$agent_commands/$agent"
done
run_case 0 env PATH="$agent_path" bash "$dist/install-mcp.sh" --bin-dir "$bin_dir"
assert_line 'Register with Claude Code:' "$test_dir/stdout"
assert_line 'Register with Codex CLI:' "$test_dir/stdout"
[[ "$(grep -c '^Connect Cursor:' "$test_dir/stdout")" -eq 1 ]] || fail 'duplicate Cursor instructions'
[[ ! -e "$AKEN_TEST_AGENT_LOG" ]] || fail 'agent detection executed an agent'
printf '%s\n' 'ok: absent, non-executable, individual, and multiple agents; no agent execution'

cp "$bin_dir/aken-mcp" "$test_dir/previous"
printf 'tampered\n' >> "$dist/aken-mcp_linux_amd64"
run_case 1 bash "$dist/install-mcp.sh" --bin-dir "$bin_dir"
assert_line 'Release checksum verification failed.' "$test_dir/stderr"
cmp "$test_dir/previous" "$bin_dir/aken-mcp" || fail 'altered download replaced existing install'
rm "$dist/aken-mcp_linux_amd64"
run_case 1 bash "$dist/install-mcp.sh" --bin-dir "$bin_dir"
assert_line 'Could not download aken-mcp_linux_amd64.' "$test_dir/stderr"
cmp "$test_dir/previous" "$bin_dir/aken-mcp"
printf '%s\n' 'ok: checksum and download failures preserve the existing installation'

sed -i '/aken-mcp_darwin_arm64/d' "$dist/SHA256SUMS"
run_case 1 bash "$script_dir/render.sh" "$dist" "$tag" mcp
assert_line 'render.sh: missing or invalid hash for aken-mcp_darwin_arm64' "$test_dir/stderr"
printf '%s\n' 'ok: rendering refuses a missing MCP hash' 'install_mcp_test.sh: all tests passed'
