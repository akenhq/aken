# Verify a release

## Verify the script

Download `install.sh` and `install.sh.sigstore.json` from the same release.
Replace `<tag>` with the release tag:

```sh
curl -fsSL "https://github.com/akenhq/aken/releases/download/<tag>/install.sh" -o install.sh
curl -fsSL "https://github.com/akenhq/aken/releases/download/<tag>/install.sh.sigstore.json" -o install.sh.sigstore.json
```

Install [cosign](https://github.com/sigstore/cosign/releases), then verify:

```sh
cosign verify-blob \
  --bundle install.sh.sigstore.json \
  --certificate-identity "https://github.com/akenhq/aken/.github/workflows/release.yml@refs/tags/<tag>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  install.sh
```

If verification fails, stop. Read the verified script, then run that local
copy without selecting a different version:

```sh
less install.sh
sudo bash install.sh
```

For once mode, download `run.sh` and `run.sh.sigstore.json` from the same
release and substitute those names in the verification command.
For the local MCP, use `install-mcp.sh` and `install-mcp.sh.sigstore.json`,
then run the verified copy with `bash install-mcp.sh` as your normal user.
All three rendered scripts are signed release assets and are listed in
`SHA256SUMS`. The source templates in `packaging/` are unrendered and refuse
to run.

## Verify a release by hand

### Download

Download the assets for your operating system and architecture from the
[releases page](https://github.com/akenhq/aken/releases), together with
`SHA256SUMS` and `SHA256SUMS.sigstore.json`. Keep them in one download directory.

| Platform | Collector | Local MCP | Relay |
|---|---|---|---|
| Linux amd64 | `aken_linux_amd64` | `aken-mcp_linux_amd64` | `aken-relay_linux_amd64` |
| Linux arm64 | `aken_linux_arm64` | `aken-mcp_linux_arm64` | `aken-relay_linux_arm64` |
| macOS arm64 | `aken_darwin_arm64` | `aken-mcp_darwin_arm64` | `aken-relay_darwin_arm64` |

In the commands below, replace `<tag>` with the release tag you downloaded.

### Verify the signature

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

### Verify the checksum

Run this command in your download directory. If verification fails, stop.

```sh
sha256sum -c --ignore-missing SHA256SUMS
```

### Verify the build attestation

With the GitHub CLI (`gh`) installed, run this command for the Linux amd64
collector, or substitute the asset you downloaded:

```sh
gh attestation verify aken_linux_amd64 --repo akenhq/aken
```

Attestations exist only for releases made after the repository became public.

### Reproduce the build

Optional, and the strongest check: install Go 1.27.1, then clone the repository
and run the same commands as the release workflow:

```sh
git clone https://github.com/akenhq/aken.git
cd aken
git checkout <tag>
make release sums VERSION=<tag>
```

Compare the binary entries in `dist/SHA256SUMS` with the same entries in the
published `SHA256SUMS` from your download directory. Their hashes must match.
The published file also lists the rendered scripts and SBOMs.

## Install on the server by hand

After verification, run the following commands as root in the download
directory on your Debian or Ubuntu server. Set `ARCH=amd64` for x86_64 or
`ARCH=arm64` for aarch64 in that shell before you run them.

Install the collector with root ownership so the collector user cannot replace it:

```sh
install -d -o root -g root -m 0755 /usr/local/libexec
install -o root -g root -m 0755 "aken_linux_${ARCH}" /usr/local/libexec/aken
```

Install the launcher as `/usr/local/bin/aken`. This is `packaging/launcher.sh` at
the release tag, the same text the install script writes:

```sh
cat > /usr/local/bin/aken <<'AKEN_LAUNCHER'
#!/bin/sh
# Aken launcher. install.sh installs this file as /usr/local/bin/aken and the
# collector as /usr/local/libexec/aken. Started as root, it switches to the
# unprivileged aken user, with that user's groups, a clean environment, no
# capabilities and no_new_privs, and then runs the collector. Started as any
# other user, it runs the collector as that user. The collector refuses root.
set -eu
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
collector=/usr/local/libexec/aken
if [ "$(id -u)" -ne 0 ]; then
  exec "$collector" "$@"
fi
if ! getent passwd aken >/dev/null 2>&1; then
  printf '%s\n' 'aken: the aken user does not exist; run install.sh first' >&2
  exit 1
fi
exec /usr/bin/setpriv --reset-env --reuid=aken --regid=aken --init-groups \
  --inh-caps=-all --no-new-privs -- "$collector" "$@"
AKEN_LAUNCHER
chmod 0755 /usr/local/bin/aken
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

Optionally, create `/etc/aken/rules.json` as root for site-specific redaction
rules and make it readable by `aken`. See [Redaction](redaction.md) for the
format and how to check your rules before uploading.

## Install the MCP by hand

Verify your MCP asset as above. Create `~/.local/bin` if needed, then install
the macOS arm64 asset, or substitute the Linux asset for your architecture:

```sh
mkdir -p ~/.local/bin
install -m 0755 aken-mcp_darwin_arm64 ~/.local/bin/aken-mcp
```

Add the binary directory to your PATH, including in your shell profile:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Then [connect your agent](mcp.md#install) and [join a session](mcp.md#join-a-session).
