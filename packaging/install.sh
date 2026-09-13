#!/usr/bin/env bash
# Install the verified collector as root, or run it once and remove the binary.
# Read this script before running it. AKEN_RELEASE_URL is a test hook.
# --version fetches that release's script without verification; it carries its own hashes.
main() {
  set -euo pipefail
  umask 022
  AKEN_VERSION='@VERSION@'
  AKEN_DEFAULT_MODE='@MODE@'
  AKEN_SHA256_LINUX_AMD64='@SHA256_LINUX_AMD64@'
  AKEN_SHA256_LINUX_ARM64='@SHA256_LINUX_ARM64@'

  usage() {
    cat <<'USAGE'
Usage: install.sh [--version vX.Y.Z] [--once] [-- ] [collector arguments]

  --version TAG   Install that release: fetch that release's own install.sh and run it with the same arguments.
  --once          Run the collector once and delete it afterwards (run.sh does this by default).
  --help
USAGE
  }

  die() {
    printf '%s\n' "$*" >&2
    exit 1
  }

  [[ "$AKEN_VERSION" != @* ]] ||
    die 'install.sh: this copy is not rendered; download it from a release: https://github.com/akenhq/aken/releases'

  local VERSION="$AKEN_VERSION" mode="$AKEN_DEFAULT_MODE"
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --help) usage; exit 0 ;;
      --version)
        if [[ "$#" -lt 2 || "$2" == --* || -z "$2" ]]; then
          usage >&2
          exit 2
        fi
        VERSION="$2"
        shift 2
        ;;
      --once) mode=once; shift ;;
      --) shift; break ;;
      -*) usage >&2; exit 2 ;;
      *) break ;;
    esac
  done
  [[ "$VERSION" =~ ^v[0-9][A-Za-z0-9._-]*$ ]] || die "Invalid release tag: $VERSION"
  if [[ "$mode" == install ]]; then
    if [[ "$#" -ne 0 ]]; then usage >&2; exit 2; fi
  elif [[ "$#" -eq 0 || ( "$1" != collect && "$1" != serve ) ]]; then
    usage >&2
    exit 2
  fi

  local command
  for command in curl sha256sum mktemp install; do
    command -v "$command" >/dev/null 2>&1 || die "Required command not found: $command"
  done
  local release_url="${AKEN_RELEASE_URL:-https://github.com/akenhq/aken/releases/download}"
  local proto='=https'
  if [[ "$release_url" == http://127.0.0.1:* || "$release_url" == http://127.0.0.1/* ]]; then
    proto='=http,https'
  fi
  local TMP_DIR='' state='' dest='' owner='' copy_kind='' copied=''
  # Called through the EXIT trap.
  # shellcheck disable=SC2317
  cleanup() {
    local status="$?" parent
    trap - EXIT
    if [[ -n "$owner" && -n "$TMP_DIR" && -d "$TMP_DIR/state" && -n "$(find "$TMP_DIR/state" -mindepth 1 -printf x -quit)" ]]; then
      # The home directory must already exist; only the two directories below it are created, owned by the user.
      for parent in "${dest%/state/aken}" "${dest%/aken}"; do
        if [[ ! -e "$parent" ]]; then
          install -d -o "${owner%%:*}" -g "${owner##*:}" -m 0755 "$parent" || status=1
        fi
      done
      mkdir -p "$dest" &&
        cp -a "$TMP_DIR/state/." "$dest/" &&
        chown -R "$owner" "$dest" &&
        chmod 0700 "$dest" && copied=1 || status=1
    fi
    if [[ -n "$TMP_DIR" ]]; then
      rm -rf -- "$TMP_DIR" || status=1
    fi
    if [[ -n "$copy_kind" && ( -z "$owner" || -n "$copied" ) ]]; then
      printf 'Local copy: %s/%s/\n' "${dest:-$state}" "$copy_kind"
    fi
    exit "$status"
  }
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  if [[ "$mode" == once && "$(id -u)" -eq 0 ]]; then
    command -v getent >/dev/null 2>&1 || die 'Required command not found: getent'
    local invoking_home=/root
    if [[ -n "${SUDO_USER:-}" && "$SUDO_USER" != root ]]; then
      invoking_home=$(getent passwd "$SUDO_USER" | cut -d: -f6) || invoking_home=''
      [[ -n "$invoking_home" ]] || die "Cannot resolve the home directory of $SUDO_USER."
      owner="${SUDO_UID}:${SUDO_GID}"
    else
      owner=0:0
    fi
    dest="$invoking_home/.local/state/aken"
  fi

  if [[ "$VERSION" != "$AKEN_VERSION" ]]; then
    local tmp script_fd
    local -a remaining=()
    TMP_DIR=$(mktemp -d)
    tmp="$TMP_DIR/install.sh"
    curl -fsSL --proto "$proto" --proto-redir "$proto" --tlsv1.2 \
      "$release_url/$VERSION/install.sh" -o "$tmp" || die 'Could not download install.sh.'
    if [[ "$mode" == once ]]; then remaining+=(--once); fi
    remaining+=(-- "$@")
    # Keep the downloaded script open while removing its temporary directory before exec.
    exec {script_fd}< "$tmp"
    rm -rf -- "$TMP_DIR"
    TMP_DIR=''
    exec bash -s -- "${remaining[@]}" <&"$script_fd"
  fi

  local uid OS MACHINE ARCH expected
  uid=$(id -u)
  if [[ "$mode" == install ]]; then
    [[ "$uid" -eq 0 ]] ||
      die 'install.sh must run as root (sudo). The collector itself never runs as root.'
    for command in useradd usermod getent; do
      command -v "$command" >/dev/null 2>&1 || die "Required command not found: $command"
    done
  elif [[ "$uid" -eq 0 ]]; then
    for command in setpriv getent; do
      command -v "$command" >/dev/null 2>&1 || die "Required command not found: $command"
    done
  fi
  OS=$(uname -s)
  [[ "$OS" == Linux ]] || die "Unsupported operating system: $OS"
  MACHINE=$(uname -m)
  case "$MACHINE" in
    x86_64) ARCH=amd64; expected="$AKEN_SHA256_LINUX_AMD64" ;;
    aarch64) ARCH=arm64; expected="$AKEN_SHA256_LINUX_ARM64" ;;
    *) die "Unsupported architecture: $MACHINE" ;;
  esac
  if [[ "$mode" == once ]]; then
    TMP_DIR=$(mktemp -d /tmp/aken-once.XXXXXX)
  else
    TMP_DIR=$(mktemp -d)
  fi
  curl -fsSL --proto "$proto" --proto-redir "$proto" --tlsv1.2 \
    "$release_url/$AKEN_VERSION/aken_linux_${ARCH}" -o "$TMP_DIR/aken_linux_${ARCH}" ||
    die "Could not download aken_linux_${ARCH}."
  if ! (cd "$TMP_DIR" && printf '%s  %s\n' "$expected" "aken_linux_${ARCH}" | sha256sum -c -); then
    die 'Release checksum verification failed.'
  fi

  if [[ "$mode" == install ]]; then
    (
      cd "$TMP_DIR"
      install -o root -g root -m 0755 "aken_linux_${ARCH}" /usr/local/bin/aken
    )
    if ! getent passwd aken >/dev/null; then
      useradd --system --user-group --home-dir /var/lib/aken --create-home --shell /usr/sbin/nologin aken
    fi
    chmod 0700 /var/lib/aken
    chown aken:aken /var/lib/aken
    local g
    for g in adm systemd-journal; do
      if getent group "$g" >/dev/null; then
        usermod -aG "$g" aken
      fi
    done
    /usr/local/bin/aken version
    printf '%s\n' 'Run the collector as the aken user, never as root:' \
      '  sudo -u aken aken collect --help'
    exit 0
  fi

  local -a runner=()
  if [[ "$uid" -eq 0 ]]; then
    state="$TMP_DIR/state"
    install -d -o nobody -g nogroup -m 0700 "$state"
    chmod 0711 "$TMP_DIR"
    install -o root -g root -m 0755 "$TMP_DIR/aken_linux_${ARCH}" "$TMP_DIR/aken"
    local group groups=''
    for group in adm systemd-journal; do
      if getent group "$group" >/dev/null; then
        groups+="${groups:+,}$group"
      fi
    done
    runner=(setpriv --reuid=nobody --regid=nogroup --inh-caps=-all --no-new-privs)
    if [[ -n "$groups" ]]; then
      runner+=("--groups=$groups")
    else
      runner+=(--clear-groups)
    fi
  else
    state="${XDG_STATE_HOME:-$HOME/.local/state}/aken"
    mkdir -p "$state"
    chmod 0700 "$state"
    install -m 0755 "$TMP_DIR/aken_linux_${ARCH}" "$TMP_DIR/aken"
  fi
  copy_kind=runs
  if [[ "$1" == serve ]]; then copy_kind=sessions; fi
  local status=0 tty_fd
  if { exec {tty_fd}< /dev/tty; } 2>/dev/null; then
    "${runner[@]}" "$TMP_DIR/aken" "$@" --state-dir "$state" <&"$tty_fd" || status="$?"
  else
    "${runner[@]}" "$TMP_DIR/aken" "$@" --state-dir "$state" || status="$?"
  fi
  exit "$status"
}
main "$@"
