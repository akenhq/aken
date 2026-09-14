#!/usr/bin/env bash
# Install the local MCP for the current user. AKEN_RELEASE_URL is a test hook.
# --version fetches that release's script without verification; it carries its own hashes.
main() {
  set -euo pipefail
  umask 022
  AKEN_VERSION='@VERSION@'
  AKEN_SHA256_LINUX_AMD64='@SHA256_LINUX_AMD64@'
  AKEN_SHA256_LINUX_ARM64='@SHA256_LINUX_ARM64@'
  AKEN_SHA256_DARWIN_ARM64='@SHA256_DARWIN_ARM64@'

  usage() {
    cat <<'USAGE'
Usage: install-mcp.sh [--version vX.Y.Z] [--bin-dir /absolute/path]

  --version TAG   Fetch that release's own install-mcp.sh and run it.
  --bin-dir DIR   Install into DIR (default: $HOME/.local/bin).
  --help
USAGE
  }
  die() { printf '%s\n' "$*" >&2; exit 1; }

  [[ "$AKEN_VERSION" != @* ]] ||
    die 'install-mcp.sh: this copy is not rendered; download it from a release: https://github.com/akenhq/aken/releases'

  local version="$AKEN_VERSION" bin_dir="$HOME/.local/bin"
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --help) usage; exit 0 ;;
      --version|--bin-dir)
        if [[ "$#" -lt 2 || -z "$2" || "$2" == --* ]]; then usage >&2; exit 2; fi
        if [[ "$1" == --version ]]; then version="$2"; else bin_dir="$2"; fi
        shift 2
        ;;
      *) usage >&2; exit 2 ;;
    esac
  done
  [[ "$version" =~ ^v[0-9][A-Za-z0-9._-]*$ ]] || die "Invalid release tag: $version"
  [[ "$bin_dir" == /* ]] || die '--bin-dir must be an absolute path.'

  local command
  for command in curl mktemp install; do
    command -v "$command" >/dev/null 2>&1 || die "Required command not found: $command"
  done
  local -a checksum=()
  if command -v sha256sum >/dev/null 2>&1; then
    checksum=(sha256sum)
  elif command -v shasum >/dev/null 2>&1; then
    checksum=(shasum -a 256)
  else
    die 'Required command not found: sha256sum or shasum'
  fi
  local release_url="${AKEN_RELEASE_URL:-https://github.com/akenhq/aken/releases/download}"
  local proto='=https'
  if [[ "$release_url" == http://127.0.0.1:* || "$release_url" == http://127.0.0.1/* ]]; then
    proto='=http,https'
  fi
  local tmp_dir=''
  # Called through the EXIT trap.
  # shellcheck disable=SC2317
  cleanup() {
    local status="$?"
    trap - EXIT
    if [[ -n "$tmp_dir" ]]; then rm -rf -- "$tmp_dir" || status=1; fi
    exit "$status"
  }
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  tmp_dir=$(mktemp -d)

  if [[ "$version" != "$AKEN_VERSION" ]]; then
    curl -fsSL --proto "$proto" --proto-redir "$proto" --tlsv1.2 \
      "$release_url/$version/install-mcp.sh" -o "$tmp_dir/install-mcp.sh" ||
      die 'Could not download install-mcp.sh.'
    local status=0
    bash "$tmp_dir/install-mcp.sh" --bin-dir "$bin_dir" || status="$?"
    exit "$status"
  fi

  local os machine asset expected
  os=$(uname -s)
  machine=$(uname -m)
  case "$os/$machine" in
    Linux/x86_64) asset=aken-mcp_linux_amd64; expected="$AKEN_SHA256_LINUX_AMD64" ;;
    Linux/aarch64|Linux/arm64) asset=aken-mcp_linux_arm64; expected="$AKEN_SHA256_LINUX_ARM64" ;;
    Darwin/arm64) asset=aken-mcp_darwin_arm64; expected="$AKEN_SHA256_DARWIN_ARM64" ;;
    *) die "Unsupported platform: $os/$machine (supported: Linux amd64/arm64 and macOS arm64)." ;;
  esac
  curl -fsSL --proto "$proto" --proto-redir "$proto" --tlsv1.2 \
    "$release_url/$AKEN_VERSION/$asset" -o "$tmp_dir/$asset" ||
    die "Could not download $asset."
  if ! (cd "$tmp_dir" && printf '%s  %s\n' "$expected" "$asset" | "${checksum[@]}" -c -); then
    die 'Release checksum verification failed.'
  fi

  mkdir -p "$bin_dir"
  [[ ! -d "$bin_dir/aken-mcp" ]] || die "Install destination is a directory: $bin_dir/aken-mcp"
  install -m 0755 "$tmp_dir/$asset" "$bin_dir/aken-mcp"
  printf 'Installed aken-mcp %s to %s/aken-mcp\n' "$AKEN_VERSION" "$bin_dir"
  case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *)
      printf '\nAdd this line to your shell profile, then run it in this terminal:\n'
      # Keep PATH literal so it expands in the user's shell when they run the hint.
      # shellcheck disable=SC2016
      printf '  export PATH=%q:"$PATH"\n' "$bin_dir"
      ;;
  esac
  agent_available() {
    local executable
    executable=$(type -P "$1") || return 1
    [[ -f "$executable" && -x "$executable" ]]
  }
  if agent_available claude; then
    printf '\nRegister with Claude Code:\n  claude mcp add aken -- %q serve\n' "$bin_dir/aken-mcp"
  fi
  if agent_available codex; then
    printf '\nRegister with Codex CLI:\n  codex mcp add aken -- %q serve\n' "$bin_dir/aken-mcp"
  fi
  if agent_available cursor || agent_available cursor-agent; then
    printf '\nConnect Cursor: https://github.com/akenhq/aken/blob/main/docs/mcp.md#cursor\n'
  fi
  printf '\nAgent setup: https://github.com/akenhq/aken/blob/main/docs/mcp.md\n'
  exit 0
}
main "$@"
