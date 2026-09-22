# Install

| On the server | On your machine |
|---|---|
| Install the collector. | Install the MCP and connect your agent. |
| Start: `sudo aken serve`. | Join: `aken-mcp join`. |
| Approve jobs in the terminal. | Ask the agent to investigate. |

Both script installers download a release binary and check its SHA-256 hash against
an embedded hash. To verify a script's signature before running it, see
[Verify the script](verify.md#verify-the-script).

## Try it without installing

On your server, download the collector, collect logs, and remove the temporary binary:

```sh
curl -fsSL https://aken.dev/run.sh | sudo bash -s -- collect --unit nginx --since 1h
```

On your server, open a live session instead:

```sh
curl -fsSL https://aken.dev/run.sh | sudo bash -s -- serve
```

The local audit copy remains after the temporary binary is removed. See
[Run-once details](#run-once-details) for permissions and state paths.
Install the MCP on your machine below to use the results with your agent.

## Install the collector on your server

Run this on your Linux server:

```sh
curl -fsSL https://aken.dev/install.sh | sudo bash
```

The canonical GitHub URL is:

```sh
curl -fsSL https://github.com/akenhq/aken/releases/latest/download/install.sh | sudo bash
```

## Install the MCP on your machine

With Node 18 or later, install on Linux (x64 or arm64) or macOS arm64
(Apple Silicon):

```sh
npm install -g aken-mcp
```

The package runs no install scripts. Its platform-specific optional dependency
contains the same binary as the signed GitHub release asset.
Then [connect your agent](mcp.md#install) and [join a session](mcp.md#join-a-session).

To check the binary, run `aken-mcp version` and verify that version's
`SHA256SUMS` as described in [Verify a release by hand](verify.md#verify-a-release-by-hand).
Print the installed binary's path, replacing `<os>-<arch>` with `linux-x64`,
`linux-arm64`, or `darwin-arm64`:

```sh
(cd "$(npm root -g)/aken-mcp" && node -p "require.resolve('@akenhq/mcp-<os>-<arch>/bin/aken-mcp')")
```

Hash the printed path with `sha256sum` (on macOS, use `shasum -a 256`):

```sh
sha256sum "<binary-path>"
```

Compare the hash with the `aken-mcp_<os>_<arch>` line in the verified
`SHA256SUMS`; the `x64` package matches the `amd64` release asset.

Without Node, run the script installer as your normal user:

```sh
curl -fsSL https://aken.dev/install-mcp.sh | bash
```

The canonical GitHub URL is:

```sh
curl -fsSL https://github.com/akenhq/aken/releases/latest/download/install-mcp.sh | bash
```

The script selects the binary for your platform, checks its embedded SHA-256
hash, and installs `aken-mcp` into `~/.local/bin`. It requires Bash, `curl`,
`mktemp`, `install`, and either `sha256sum` or `shasum`. No sudo is needed.

If the script prints a PATH instruction, add that line to your shell profile
and run it in your current terminal. It detects agent commands on PATH and
prints registration commands for Claude Code and Codex CLI when found,
using the full binary path. If it finds `cursor` or `cursor-agent`, it links
to Cursor setup. It does not register the MCP automatically.

Follow the printed instructions for your agent, or see
[Connect your agent](mcp.md#install) if it was not detected.
Then [join a session](mcp.md#join-a-session) using the token from your server.

To select a release or installation directory, replace `<tag>` with a release tag:

```sh
curl -fsSL https://aken.dev/install-mcp.sh | bash -s -- --version <tag> --bin-dir "$HOME/.local/bin"
```

`--bin-dir` requires an absolute path. `--version` downloads that release's
own `install-mcp.sh`, which carries its own hashes. Verify the selected
release's script if you need to check its signature before execution.
For manual installation, see [Install the MCP by hand](verify.md#install-the-mcp-by-hand).

## First run

On the server, run a smoke test. Replace `<unit>` with a journald unit:

```sh
sudo aken collect --dry-run --unit <unit>
```

The `aken` command switches to the `aken` user before the collector starts.
This collects and displays redacted content without uploading.
See [Live sessions](serve.md) to open a session, [Collect logs](collect.md)
to send an artifact, and [Docker logs](docker.md) to configure container logging.

## Upgrade

On the server, run the same collector installer again:

```sh
curl -fsSL https://aken.dev/install.sh | sudo bash
```

On your machine, update the npm package:

```sh
npm install -g aken-mcp@latest
```

If you installed without Node, rerun the MCP installer on your machine:

```sh
curl -fsSL https://aken.dev/install-mcp.sh | bash
```

The MCP session file survives either upgrade. Restart your agent's MCP process
to use the new binary. See the [release notes](https://github.com/akenhq/aken/releases)
for changes.

## Uninstall

On the server, stop any running collector, then remove the binaries and the
`aken` user:

```sh
curl -fsSL https://aken.dev/install.sh | sudo bash -s -- --uninstall
```

This keeps `/var/lib/aken` (local audit copies and placeholder mappings) and
`/etc/aken` (configuration). To delete those too, run this on the server instead:

```sh
curl -fsSL https://aken.dev/install.sh | sudo bash -s -- --uninstall --purge
```

The equivalent manual commands on the server are:

```sh
sudo rm -f /usr/local/bin/aken /usr/local/libexec/aken
if getent passwd aken >/dev/null; then sudo userdel aken; fi
```

To purge the saved state and configuration manually, run this on the server:

```sh
sudo rm -rf /var/lib/aken /etc/aken
```

On your machine, remove the registration for the agent you use:

```sh
claude mcp remove aken
```

For Codex CLI, run `codex mcp remove aken`. For Cursor, remove the `aken`
entry from `mcpServers` in your project's `.cursor/mcp.json` or your global
`~/.cursor/mcp.json`.

If you installed with npm, remove the package on your machine:

```sh
npm uninstall -g aken-mcp
```

If you used the script installer with its default directory, remove the binary
on your machine instead:

```sh
rm ~/.local/bin/aken-mcp
```

If you selected another `--bin-dir`, remove `aken-mcp` from that directory.
To delete the saved session, including its token and keys, run this on your
Linux machine:

```sh
rm -r ~/.config/aken
```

On macOS, use this path instead:

```sh
rm -r "$HOME/Library/Application Support/aken"
```

Removing these files does not delete content already shared in an agent transcript.

## Collector installer details

The install script requires root. It selects the Linux amd64 or arm64 binary
for your architecture, downloads it from its release, and checks its SHA-256
hash against the hash embedded in the script. It installs the collector as
`/usr/local/libexec/aken` and a launcher as `/usr/local/bin/aken`, both owned
by root with mode `0755`.

The launcher is a shell script of a dozen lines. Started as root, it switches
to the `aken` user with `setpriv`: that user's groups from the group database,
a clean environment with `HOME`, `USER`, and `LOGNAME` set for `aken` and `TERM`
kept, no capabilities, and `no_new_privs`. Then it runs the collector. Started
as any other user, it runs the collector as that user, so `sudo -u aken aken`
keeps working. The collector itself refuses root. The launcher's text is in
[Install on the server by hand](verify.md#install-on-the-server-by-hand).

The script creates the unprivileged `aken` user and `/var/lib/aken` state
directory with mode `0700`, owned by `aken`. It adds `aken` to `adm` and
`systemd-journal` where those groups exist. It prints the version through
the launcher, lists what it created, and shows commands to start and uninstall.

The script does not run cosign or offer `--skip-signature-check`. The install
script is the trust root of the install path; verify it with
`cosign verify-blob` if you do not trust the host that served it. See
[Verify the script](verify.md#verify-the-script).

To select a release, replace `<tag>` with its tag:

```sh
curl -fsSL https://aken.dev/install.sh | sudo bash -s -- --version <tag>
```

`--version` fetches that release's own `install.sh` and runs it with the
remaining arguments. Verify the selected release's script if you need to
check its signature before execution.

## Run-once details

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
also requires `useradd`, `usermod`, `getent`, and `setpriv`; root once mode
requires `setpriv` and `getent`. Once mode reads collector input from `/dev/tty`
so the approval screen can work while the script arrives through a pipe.
Run it from a terminal.

[Verify the script](verify.md#verify-the-script).

[Verify a release by hand](verify.md#verify-a-release-by-hand).

[Install on the server by hand](verify.md#install-on-the-server-by-hand).

[Install the MCP by hand](verify.md#install-the-mcp-by-hand).
