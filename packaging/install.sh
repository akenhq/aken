#!/usr/bin/env bash
# This script verifies a release and installs the collector on Debian or Ubuntu.
# Read it before you run it. It needs root to create a user and write to
# /usr/local/bin. The collector itself never runs as root.
set -euo pipefail
umask 022

usage() {
  cat <<'EOF'
Usage: install.sh [--version vX.Y.Z] [--skip-signature-check]

  --version              Release tag to install. Default: the latest release.
  --skip-signature-check Trust the checksum file without verifying its Sigstore
                         signature. Prints a warning. Use only if you verified
                         the release by hand as described in docs/install.md.
EOF
}

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [ -n "$TMP_DIR" ]; then
    rm -rf -- "$TMP_DIR"
  fi
}

VERSION=''
SKIP_SIGNATURE_CHECK=0
TMP_DIR=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --help)
      usage
      exit 0
      ;;
    --version)
      if [ "$#" -lt 2 ] || [[ "$2" == --* ]] || [ -z "$2" ]; then
        usage >&2
        exit 2
      fi
      VERSION="$2"
      shift 2
      ;;
    --skip-signature-check)
      SKIP_SIGNATURE_CHECK=1
      shift
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

[ "$(id -u)" -eq 0 ] ||
  die 'install.sh must run as root (sudo). The collector itself never runs as root.'

OS=$(uname -s)
[ "$OS" = Linux ] || die "Unsupported operating system: $OS"

MACHINE=$(uname -m)
case "$MACHINE" in
  x86_64) ARCH=amd64 ;;
  aarch64) ARCH=arm64 ;;
  *) die "Unsupported architecture: $MACHINE" ;;
esac

for command in curl sha256sum install useradd usermod getent; do
  command -v "$command" >/dev/null 2>&1 || die "Required command not found: $command"
done

if [ -z "$VERSION" ]; then
  VERSION=$(
    curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 \
      https://api.github.com/repos/akenhq/aken/releases/latest |
      sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p'
  ) || die 'Could not resolve the latest release.'
  [ -n "$VERSION" ] || die 'Could not resolve the latest release: tag_name is empty.'
fi
[[ "$VERSION" =~ ^v[0-9][A-Za-z0-9._-]*$ ]] || die "Invalid release tag: $VERSION"

TMP_DIR=$(mktemp -d)
trap cleanup EXIT
cd "$TMP_DIR"

BASE_URL="https://github.com/akenhq/aken/releases/download/${VERSION}"
for asset in "aken_linux_${ARCH}" SHA256SUMS SHA256SUMS.sigstore.json; do
  curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 \
    "${BASE_URL}/${asset}" -o "$asset" || die "Could not download $asset."
done

if [ "$SKIP_SIGNATURE_CHECK" -eq 0 ]; then
  if ! command -v cosign >/dev/null 2>&1; then
    die 'cosign is required to verify the release signature;
install it from https://github.com/sigstore/cosign/releases;
or verify by hand as described in docs/install.md;
or rerun with --skip-signature-check to trust the checksum file alone.'
  fi
  cosign verify-blob \
    --bundle SHA256SUMS.sigstore.json \
    --certificate-identity "https://github.com/akenhq/aken/.github/workflows/release.yml@refs/tags/${VERSION}" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    SHA256SUMS || die 'Release signature verification failed.'
else
  printf '%s\n' 'WARNING: signature check skipped; trusting SHA256SUMS as downloaded.' >&2
fi

if ! grep " aken_linux_${ARCH}$" SHA256SUMS | sha256sum -c -; then
  die 'Release checksum verification failed.'
fi

install -o root -g root -m 0755 "aken_linux_${ARCH}" /usr/local/bin/aken

if ! getent passwd aken >/dev/null; then
  useradd --system --user-group --home-dir /var/lib/aken --create-home --shell /usr/sbin/nologin aken
fi
chmod 0700 /var/lib/aken
chown aken:aken /var/lib/aken

for g in adm systemd-journal; do
  if getent group "$g" >/dev/null; then
    usermod -aG "$g" aken
  fi
done

/usr/local/bin/aken version
printf '%s\n' 'Run the collector as the aken user, never as root:' \
  '  sudo -u aken aken collect --help'
