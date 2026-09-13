# Install and verify

## Install with one command

Run this on your Linux server:

```sh
curl -fsSL https://aken.dev/install.sh | sudo bash
```

The canonical GitHub URL is:

```sh
curl -fsSL https://github.com/akenhq/aken/releases/latest/download/install.sh | sudo bash
```

You install `aken` on the server and `aken-mcp` on your own machine. Both
binaries come from the same signed release. Install the MCP using the
[steps below](#install-the-mcp-on-your-machine).

The install script requires root. It selects the Linux amd64 or arm64 binary
for your architecture, downloads it from its release, and checks its SHA-256
hash against the hash embedded in the script. It installs `/usr/local/bin/aken`
as root with mode `0755`.

The script creates the unprivileged `aken` user and `/var/lib/aken` state
directory with mode `0700`, owned by `aken`. It adds `aken` to `adm` and
`systemd-journal` where those groups exist. It prints the version and the
`sudo -u aken aken collect --help` hint. The collector itself refuses root.

The script does not run cosign or offer `--skip-signature-check`. The install
script is the trust root of the install path; verify it with
`cosign verify-blob` if you do not trust the host that served it. See
[Verify the script](#verify-the-script).

To select a release, replace `<tag>` with its tag:

```sh
curl -fsSL https://aken.dev/install.sh | sudo bash -s -- --version <tag>
```

`--version` fetches that release's own `install.sh` and runs it with the
remaining arguments. Verify the selected release's script if you need to
check its signature before execution.

## Run once

To download the collector, collect logs, and remove the temporary binary:

```sh
curl -fsSL https://aken.dev/run.sh | sudo bash -s -- collect --unit nginx --since 1h
```

To open a live session instead:

```sh
curl -fsSL https://aken.dev/run.sh | sudo bash -s -- serve
```

The canonical script URL is
`https://github.com/akenhq/aken/releases/latest/download/run.sh`.
`run.sh` selects once mode by default; `install.sh --once` selects the same mode.

Once mode downloads and checks the binary against the embedded hash in a
`/tmp/aken-once.XXXXXX` temporary directory. It runs the collector with
`--state-dir`, removes the temporary directory on exit even if the collector
fails, and returns the collector's exit code.

The local copy stays. After `collect`, the script prints
`Local copy: <state>/runs/`; after `serve`, it prints
`Local copy: <state>/sessions/`.

| How you start it | Run identity | State directory |
|---|---|---|
| With root through sudo and a non-root `SUDO_USER` | `nobody`, primary group `nogroup`; supplementary `adm` and `systemd-journal` where they exist | Temporary `state/` during the run; copied to `<home>/.local/state/aken` on exit, with `<home>` from the passwd database |
| With root and no non-root `SUDO_USER` | Same as above | Temporary `state/` during the run; copied to `/root/.local/state/aken` on exit |
| As a normal user, without sudo | Your user and existing groups | `${XDG_STATE_HOME:-$HOME/.local/state}/aken` |

Root once mode uses `setpriv` with `--reuid=nobody`, `--regid=nogroup`,
`--groups=<adm,systemd-journal that exist>`, `--inh-caps=-all`, and
`--no-new-privs`. State is staged in the temporary directory with mode `0700`,
owned by `nobody`. The invoking user's home is untouched while the collector
runs. On exit, any staged state is copied to the destination above, with
ownership `SUDO_UID:SUDO_GID`, or `0:0` when invoked directly as root, and
directory mode `0700`. Other daemons running as the shared `nobody` identity
can read the staged copy during the run. Normal-user mode does not use `setpriv`.

The collector's own `Local copy:` line names the staged path inside the
temporary directory; the script's final `Local copy:` line names where the copy
ended up. Once mode changes nothing outside its temporary directory and the invoking
user's own state directory. It creates no user and changes no group membership.
It keeps the local audit copy, including
original values in the placeholder mapping. It cannot prevent journal entries
from sshd or sudo.

The scripts require `curl`, `sha256sum`, `mktemp`, and `install`. Install mode
also requires `useradd`, `usermod`, and `getent`; root once mode requires `setpriv`
and `getent`. Once mode reads collector input from `/dev/tty`
so the approval screen can work while the script arrives through a pipe.
Run it from a terminal.

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
release and substitute those names in the verification command. Both rendered
scripts are signed release assets and are listed in `SHA256SUMS`. The source
copy at `packaging/install.sh` is unrendered and refuses to run.

## Verify a release by hand

### Download

Download the assets for your operating system and architecture from the
[releases page](https://github.com/akenhq/aken/releases), together with
`SHA256SUMS` and `SHA256SUMS.sigstore.json`. Keep them in one download directory.

| Platform | Collector | Local MCP | Dev relay |
|---|---|---|---|
| Linux amd64 | `aken_linux_amd64` | `aken-mcp_linux_amd64` | `aken-devrelay_linux_amd64` |
| Linux arm64 | `aken_linux_arm64` | `aken-mcp_linux_arm64` | `aken-devrelay_linux_arm64` |
| macOS arm64 | `aken_darwin_arm64` | `aken-mcp_darwin_arm64` | `aken-devrelay_darwin_arm64` |

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

Optionally, create `/etc/aken/rules.json` as root for site-specific redaction
rules and make it readable by `aken`. See [Redaction](redaction.md) for the
format and how to check your rules before uploading.

## Install the MCP on your machine

Verify your MCP asset as above. Create `~/.local/bin` if needed, then install
the macOS arm64 asset, or substitute the Linux asset for your architecture:

```sh
mkdir -p ~/.local/bin
install -m 0755 aken-mcp_darwin_arm64 ~/.local/bin/aken-mcp
```

Homebrew and npm come later.

## Run

Run a smoke test as the `aken` user. Replace `<unit>` with a journald unit:

```sh
sudo -u aken aken collect --dry-run --unit <unit>
```

This collects, redacts, and shows the review screen without uploading.
See [Live sessions](serve.md) to open a session, [Collect logs](collect.md)
to send an artifact, and [Docker logs](docker.md) to configure container logging.
