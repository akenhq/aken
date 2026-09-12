# Install and verify

## What you are installing

You install `aken` on the server and `aken-mcp` on your own machine. Both
binaries come from the same signed release. The collector runs as the
unprivileged user `aken` and refuses to start as root.

## Download

Download the assets for your operating system and architecture from the
[releases page](https://github.com/akenhq/aken/releases), together with
`SHA256SUMS` and `SHA256SUMS.sigstore.json`. Keep them in one download directory.

| Platform | Collector | Local MCP | Dev relay |
|---|---|---|---|
| Linux amd64 | `aken_linux_amd64` | `aken-mcp_linux_amd64` | `aken-devrelay_linux_amd64` |
| Linux arm64 | `aken_linux_arm64` | `aken-mcp_linux_arm64` | `aken-devrelay_linux_arm64` |
| macOS arm64 | `aken_darwin_arm64` | `aken-mcp_darwin_arm64` | `aken-devrelay_darwin_arm64` |

In the commands below, replace `<tag>` with the release tag you downloaded.

## Verify the signature

Install [cosign](https://github.com/sigstore/cosign/releases), then run this
command in your download directory:

```sh
cosign verify-blob \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity "https://github.com/akenhq/aken/.github/workflows/release.yml@refs/tags/<tag>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
```

This proves that the checksum file was produced by this repository's release
workflow, at that tag, on GitHub's runners. If verification fails, stop.

## Verify the checksum

Run this command in your download directory. If verification fails, stop.

```sh
sha256sum -c --ignore-missing SHA256SUMS
```

## Verify the build attestation

With the GitHub CLI (`gh`) installed, run this command for the Linux amd64
collector, or substitute the asset you downloaded:

```sh
gh attestation verify aken_linux_amd64 --repo akenhq/aken
```

Attestations exist only for releases made after the repository became public.

## Reproduce the build

Optional, and the strongest check: install Go 1.27.1, then clone the repository
and run the same commands as the release workflow:

```sh
git clone https://github.com/akenhq/aken.git
cd aken
git checkout <tag>
make release sums VERSION=<tag>
```

Compare `dist/SHA256SUMS` with the published `SHA256SUMS` from your download
directory. The files must match.

## Install on the server by hand

After verification, run the following commands as root in the download
directory on your Debian or Ubuntu server. Set `ARCH=amd64` for x86_64 or
`ARCH=arm64` for aarch64 in that shell before you run them.

Install the binary with root ownership so the collector user cannot replace it:

```sh
install -o root -g root -m 0755 "aken_linux_${ARCH}" /usr/local/bin/aken
```

Create the dedicated system user if it does not exist, with a home directory
and no login shell:

```sh
if ! getent passwd aken >/dev/null; then
  useradd --system --user-group --home-dir /var/lib/aken --create-home --shell /usr/sbin/nologin aken
fi
```

Restrict access to the state directory to its owner:

```sh
chmod 0700 /var/lib/aken
```

Give the collector user ownership of its state directory:

```sh
chown aken:aken /var/lib/aken
```

Add the user to the groups that exist to give it read access to journald and
the system logs:

```sh
for g in adm systemd-journal; do
  if getent group "$g" >/dev/null; then
    usermod -aG "$g" aken
  fi
done
```

## Or use the install script

Download `packaging/install.sh` from the same tag, read it, then run it:

```sh
curl -fsSL --proto '=https' --tlsv1.2 \
  "https://raw.githubusercontent.com/akenhq/aken/<tag>/packaging/install.sh" \
  -o install.sh
less install.sh
sudo bash install.sh --version <tag>
```

The script checks Linux, architecture, and required tools; downloads the
release; verifies its signature and checksum; installs the binary; and sets
up the `aken` user, state directory permissions, and log group memberships.
It prints the version and the command to run as `aken`.

The script refuses to run as non-root. Without cosign it stops, unless you
pass `--skip-signature-check`; that option prints a warning and trusts the
downloaded checksum file alone, so use it only after you verify the release by
hand. Checksums are always checked. The script never touches systemd, cron,
`/etc`, or the PATH. Running it again upgrades the binary and keeps the existing
user and state directory, applying the same permissions and group memberships.
It is a convenience, not a trust shortcut.

## Install the MCP on your machine

Verify your MCP asset as above. Create `~/.local/bin` if needed, then install
the macOS arm64 asset, or substitute the Linux asset for your architecture:

```sh
mkdir -p ~/.local/bin
install -m 0755 aken-mcp_darwin_arm64 ~/.local/bin/aken-mcp
```

Homebrew and npm come later.

## Run

Run the collector as the `aken` user:

```sh
sudo -u aken aken collect --help
```

In phase 0, that is all it does.
