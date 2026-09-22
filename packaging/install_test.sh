#!/usr/bin/env bash
set -euo pipefail

# Root installation and privilege dropping are covered by the CI container job.
if [[ "$(id -u)" -eq 0 ]]; then
  printf '%s\n' 'install_test.sh: run as a non-root user; root cases run in CI containers' >&2
  exit 1
fi
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
test_dir=$(mktemp -d)
server_pid=''
cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
  fi
  rm -rf -- "$test_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
shopt -s nullglob
before=(/tmp/aken-once.*)
assert_clean() {
  local -a after=(/tmp/aken-once.*)
  [[ "${before[*]}" == "${after[*]}" ]] || fail 'temporary directory leaked'
}
run_case() {
  local expected="$1" status=0
  shift
  "$@" > "$test_dir/stdout" 2> "$test_dir/stderr" || status="$?"
  if [[ "$status" -ne "$expected" ]]; then
    printf 'Expected exit %s, got %s\n' "$expected" "$status" >&2
    sed -n '1,80p' "$test_dir/stdout" "$test_dir/stderr" >&2
    fail "$*"
  fi
  assert_clean
}
assert_line() {
  grep -Fxq -- "$1" "$2" || fail "missing line: $1"
}

tag=v0.0.0
other_tag=v0.0.1
dist="$test_dir/releases/$tag"
mkdir -p "$dist"
cat > "$dist/aken_linux_amd64" <<'DUMMY'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == version ]]; then printf '%s\n' 'aken v0.0.0'; exit 0; fi
printf 'arg=<%s>\n' "$@"
state=''
status=0
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --state-dir) state="$2"; shift 2 ;;
    --exit) status="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[[ -n "$state" ]]
printf '%s\n' 'local data' > "$state/dummy.txt"
exit "$status"
DUMMY
cp "$dist/aken_linux_amd64" "$dist/aken_linux_arm64"
(cd "$dist" && sha256sum aken_linux_amd64 aken_linux_arm64 > SHA256SUMS)
bash "$script_dir/render.sh" "$dist" "$tag" install > "$dist/install.sh"
bash "$script_dir/render.sh" "$dist" "$tag" once > "$dist/run.sh"
bash -n "$dist/install.sh" "$dist/run.sh"
if grep -Eq '@(VERSION|MODE|LAUNCHER|SHA256_LINUX_AMD64|SHA256_LINUX_ARM64)@' "$dist/install.sh" "$dist/run.sh"; then
  fail 'unreplaced placeholder'
fi
# The rendered scripts and the by-hand install steps carry packaging/launcher.sh verbatim.
embedded_launcher() {
  awk -v start="<<'AKEN_LAUNCHER'" 'index($0, start) { inside = 1; next } $0 == "AKEN_LAUNCHER" { inside = 0 } inside' "$1"
}
sh -n "$script_dir/launcher.sh"
for source in "$dist/install.sh" "$dist/run.sh" "$script_dir/../docs/verify.md"; do
  embedded_launcher "$source" > "$test_dir/launcher.embedded"
  diff -u "$script_dir/launcher.sh" "$test_dir/launcher.embedded" || fail "launcher differs in $source"
done
printf '%s\n' 'ok: launcher embedded verbatim'
mkdir -p "$test_dir/releases/$other_tag"
cp "$dist"/aken_linux_* "$test_dir/releases/$other_tag/"
for asset in "$test_dir/releases/$other_tag"/aken_linux_*; do
  printf '\n# another release\n' >> "$asset"
done
(cd "$test_dir/releases/$other_tag" && sha256sum aken_linux_* > SHA256SUMS)
bash "$script_dir/render.sh" "$test_dir/releases/$other_tag" "$other_tag" install > "$test_dir/releases/$other_tag/install.sh"

: > "$test_dir/server.log"
PYTHONUNBUFFERED=1 python3 -m http.server 0 --bind 127.0.0.1 --directory "$test_dir/releases" > "$test_dir/server.log" 2>&1 &
server_pid="$!"
port=''
for ((attempt=0; attempt<100; attempt++)); do
  port=$(sed -n 's/.* port \([0-9]*\) .*/\1/p' "$test_dir/server.log" 2>/dev/null || true)
  if [[ -n "$port" ]]; then break; fi
  kill -0 "$server_pid" 2>/dev/null || { sed -n '1,80p' "$test_dir/server.log" >&2; fail 'HTTP server exited'; }
  sleep 0.05
done
[[ -n "$port" ]] || fail 'HTTP server did not start'
export AKEN_RELEASE_URL="http://127.0.0.1:$port"
export XDG_STATE_HOME="$test_dir/state with spaces"
state="$XDG_STATE_HOME/aken"
# Version forwarding must also remove its downloaded script before exec.
export TMPDIR="$test_dir/tmp"
mkdir -p "$TMPDIR"

run_case 0 bash "$script_dir/install.sh" --help
assert_line '       install.sh --uninstall [--purge]' "$test_dir/stdout"
printf '%s\n' 'ok: template help works'
run_case 1 bash "$script_dir/install.sh"
assert_line 'install.sh: this copy is not rendered; download it from a release: https://github.com/akenhq/aken/releases' "$test_dir/stderr"
printf '%s\n' 'ok: unrendered copy refuses'
run_case 1 bash "$dist/install.sh"
assert_line 'install.sh must run as root (sudo). The collector itself never runs as root.' "$test_dir/stderr"
printf '%s\n' 'ok: non-root install refuses'
run_case 1 bash "$dist/install.sh" --uninstall
assert_line 'install.sh --uninstall must run as root (sudo).' "$test_dir/stderr"
run_case 2 bash "$dist/install.sh" --purge
run_case 2 bash "$dist/install.sh" --once --uninstall
printf '%s\n' 'ok: uninstall requires root and purge requires uninstall'

# Redirect installation paths and stub account operations so these cases need no root.
(
  test_root="$test_dir/root"
  export test_root
  mkdir -p "$test_root/usr/local/bin"
  sed -e "s#/usr/local/#$test_root/usr/local/#g" \
    -e "s#/var/lib/aken#$test_root/var/lib/aken#g" \
    -e "s#/etc/aken#$test_root/etc/aken#g" \
    -e 's#/bin/aken version#/libexec/aken version#' \
    "$dist/install.sh" > "$test_dir/install-local.sh"
  # The real privilege-switching launcher is exercised in CI containers.
  id() { if [[ "$1" == -u ]]; then echo 0; else echo 'aken adm systemd-journal'; fi; }
  getent() { [[ "$1" == group || -f "$test_root/user" ]]; }
  useradd() { touch "$test_root/user"; mkdir -p "$test_root/var/lib/aken"; }
  userdel() { [[ "$*" == aken ]]; rm "$test_root/user"; }
  usermod() { :; }
  chown() { :; }
  install() {
    local -a args=()
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        -o|-g) shift 2 ;;
        *) args+=("$1"); shift ;;
      esac
    done
    command install "${args[@]}"
  }
  export -f id getent useradd userdel usermod chown install
  run_case 0 bash "$test_dir/install-local.sh"
  assert_line "Downloading aken $tag for linux/$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')..." "$test_dir/stdout"
  assert_line 'Verifying the checksum...' "$test_dir/stdout"
  assert_line "Installing $test_root/usr/local/libexec/aken and the launcher $test_root/usr/local/bin/aken..." "$test_dir/stdout"
  assert_line "Creating the aken user and $test_root/var/lib/aken..." "$test_dir/stdout"
  cat > "$test_dir/closing" <<CLOSING
Installed aken $tag.
Created: the aken user (groups: aken adm systemd-journal), $test_root/var/lib/aken
Next:
  sudo aken serve                             open a live session
  sudo aken collect --dry-run --unit <unit>   try a collection without uploading
Docs: https://github.com/akenhq/aken/tree/main/docs
Uninstall: curl -fsSL https://aken.dev/install.sh | sudo bash -s -- --uninstall
CLOSING
  tail -n 7 "$test_dir/stdout" > "$test_dir/actual-closing"
  diff -u "$test_dir/closing" "$test_dir/actual-closing"
  [[ -x "$test_root/usr/local/bin/aken" && -x "$test_root/usr/local/libexec/aken" ]] || fail 'binaries missing'
  rm "$test_root/user"
  run_case 0 bash "$test_dir/install-local.sh"
  assert_line 'Created: the aken user (groups: aken adm systemd-journal)' "$test_dir/stdout"
  mkdir -p "$test_root/etc/aken"
  echo audit > "$test_root/var/lib/aken/audit"
  echo config > "$test_root/etc/aken/rules.json"
  run_case 0 bash "$test_dir/install-local.sh"
  assert_line 'The aken user already exists.' "$test_dir/stdout"
  if grep -q '^Created:' "$test_dir/stdout"; then fail 'upgrade claims existing resources'; fi
  assert_line audit "$test_root/var/lib/aken/audit"
  assert_line config "$test_root/etc/aken/rules.json"
  printf '%s\n' 'ok: install progress, closing block, and upgrade preserve state'

  curl() { echo 'uninstall attempted a download' >&2; return 1; }
  export -f curl
  run_case 0 bash "$test_dir/install-local.sh" --uninstall --version "$other_tag"
  for path in usr/local/bin/aken usr/local/libexec/aken; do
    [[ ! -e "$test_root/$path" ]] || fail "kept $path"
    assert_line "Removed $test_root/$path" "$test_dir/stdout"
  done
  [[ ! -e "$test_root/user" ]] || fail 'kept user'
  assert_line 'Removed the aken user.' "$test_dir/stdout"
  assert_line "Kept $test_root/var/lib/aken (local audit copies and placeholder mappings). Remove it with: sudo rm -rf $test_root/var/lib/aken" "$test_dir/stdout"
  assert_line "Kept $test_root/etc/aken (configuration). Remove it with: sudo rm -rf $test_root/etc/aken" "$test_dir/stdout"
  assert_line audit "$test_root/var/lib/aken/audit"
  assert_line config "$test_root/etc/aken/rules.json"
  run_case 0 bash "$test_dir/install-local.sh" --uninstall
  printf '%s\n' 'ok: uninstall downloads nothing, removes binaries and user, and keeps state'
  run_case 0 bash "$test_dir/install-local.sh" --uninstall --purge
  for path in var/lib/aken etc/aken; do
    [[ ! -e "$test_root/$path" ]] || fail "kept $path"
    assert_line "Removed $test_root/$path" "$test_dir/stdout"
  done
  run_case 0 bash "$test_dir/install-local.sh" --uninstall --purge
  printf '%s\n' 'ok: purge removes saved state and configuration; repeated uninstall succeeds'
)

run_case 0 bash "$dist/run.sh" collect --dry-run --file 'a log with spaces'
assert_line 'arg=<collect>' "$test_dir/stdout"
assert_line 'arg=<--dry-run>' "$test_dir/stdout"
assert_line 'arg=<a log with spaces>' "$test_dir/stdout"
assert_line 'arg=<--state-dir>' "$test_dir/stdout"
assert_line "arg=<$state>" "$test_dir/stdout"
printf 'arg=<%s>\n' collect --dry-run --file 'a log with spaces' --state-dir "$state" > "$test_dir/expected"
grep '^arg=' "$test_dir/stdout" > "$test_dir/arguments"
diff -u "$test_dir/expected" "$test_dir/arguments"
assert_line "Local copy: $state/runs/" "$test_dir/stdout"
assert_line 'local data' "$state/dummy.txt"
printf '%s\n' 'ok: once mode forwards arguments, keeps local data, and cleans up'
run_case 0 bash "$dist/install.sh" --once -- collect --dry-run
assert_line "Local copy: $state/runs/" "$test_dir/stdout"
run_case 3 bash "$dist/run.sh" serve --exit 3
assert_line "Local copy: $state/sessions/" "$test_dir/stdout"
printf '%s\n' 'ok: --once, serve, and collector exit status'

# Started by a non-root user, the launcher runs the collector as that user, arguments unchanged.
# The root path, which switches to the aken user, runs in the CI container job.
install -m 0755 "$dist/aken_linux_amd64" "$test_dir/collector"
sed "s#^collector=.*#collector=$test_dir/collector#" "$script_dir/launcher.sh" > "$test_dir/launcher"
grep -Fq "collector=$test_dir/collector" "$test_dir/launcher" || fail 'launcher collector path not replaced'
chmod 0755 "$test_dir/launcher"
run_case 0 "$test_dir/launcher" collect --dry-run --file 'a log with spaces' --state-dir "$state"
printf 'arg=<%s>\n' collect --dry-run --file 'a log with spaces' --state-dir "$state" > "$test_dir/expected"
grep '^arg=' "$test_dir/stdout" > "$test_dir/arguments"
diff -u "$test_dir/expected" "$test_dir/arguments"
run_case 3 "$test_dir/launcher" serve --state-dir "$state" --exit 3
printf '%s\n' 'ok: launcher passes a non-root caller through'

run_case 0 bash "$dist/install.sh" --help
assert_line 'Usage: install.sh [--version vX.Y.Z] [--once] [-- ] [collector arguments]' "$test_dir/stdout"
run_case 2 bash "$dist/install.sh" collect
run_case 2 bash "$dist/run.sh"
run_case 2 bash "$dist/run.sh" version
run_case 2 bash "$dist/install.sh" --version
run_case 2 bash "$dist/install.sh" --unknown
run_case 1 bash "$dist/install.sh" --version invalid
printf '%s\n' 'ok: help and argument validation'

run_case 0 bash "$dist/run.sh" --version "$other_tag" collect --file 'forwarded log'
assert_line 'arg=<forwarded log>' "$test_dir/stdout"
assert_line "Local copy: $state/runs/" "$test_dir/stdout"
grep -Fq "GET /$other_tag/aken_linux_" "$test_dir/server.log" || fail 'version did not change'
forwarded=("$TMPDIR"/*)
[[ "${#forwarded[@]}" -eq 0 ]] || fail 'forwarded script leaked'
run_case 0 bash "$dist/install.sh" --once --version "$other_tag" -- collect
assert_line "Local copy: $state/runs/" "$test_dir/stdout"
printf '%s\n' 'ok: version forwarding preserves once mode and cleans up'

printf '\n# changed\n' >> "$dist/aken_linux_amd64"
printf '\n# changed\n' >> "$dist/aken_linux_arm64"
rm -f "$state/dummy.txt"
run_case 1 bash "$dist/run.sh" collect
assert_line 'Release checksum verification failed.' "$test_dir/stderr"
[[ ! -e "$state/dummy.txt" ]] || fail 'unverified binary ran'
printf '%s\n' 'ok: wrong hash refuses and cleans up'

sed -i '/aken_linux_arm64/d' "$dist/SHA256SUMS"
run_case 1 bash "$script_dir/render.sh" "$dist" "$tag" once
assert_line 'render.sh: missing or invalid hash for aken_linux_arm64' "$test_dir/stderr"
printf '%s\n' 'ok: rendering refuses a missing hash' 'install_test.sh: all tests passed'
