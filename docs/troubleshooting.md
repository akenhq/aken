# Troubleshooting

Find the message you saw below. Run collector commands on your server and
MCP commands on your own machine. See [Install](install.md) for setup and
[Verify a release](verify.md) for signature and checksum checks.

## Collector refuses root

`aken: refusing to run as root. The installed aken command switches to the aken user for you: run sudo aken serve. ...`

You started the collector binary directly as root. Use the installed launcher:
`sudo aken serve` for a live session, or `sudo aken collect` with your source
flags for an artifact. If you downloaded a binary by hand, follow
[Install on the server by hand](verify.md#install-on-the-server-by-hand) to
install the launcher and create the unprivileged user.

## The aken user is missing

`aken: the aken user does not exist; run install.sh first`

The launcher is installed, but its dedicated user is missing. Rerun the
[collector installer](install.md#install-the-collector-on-your-server) as root
on the server, then run `sudo aken serve`.

## Collection needs a terminal

`aken: the review screen needs a terminal; use --dry-run to check a collection without one`

The review screen cannot read approval from a terminal. Run the collection
in an interactive server terminal. To check collection and redaction without
uploading, add `--dry-run` to the same command.

## Live sessions need a terminal

`aken: serve needs a terminal`

Live sessions need a terminal for approvals and status. Run `sudo aken serve`
in an interactive server terminal and keep it open while your agent works.

## The relay cannot be reached

`cannot reach the relay at <url>: ...`

The client could not connect to the relay. Check the URL, DNS, and outbound
HTTPS access from the machine that printed the error. If you self-host,
check that the relay and its TLS proxy are running. Use the same `--relay`
URL for the collector and `aken-mcp join`; a loopback address refers to each
machine separately. See [Health and clients](relay.md#health-and-clients).

## The URL is not an Aken relay

`<url> is not an Aken relay: it has no /v0/info`

The server at that URL does not expose the expected relay endpoint. Check
that `--relay` names the relay's base URL and that its proxy routes `/v0/info`
to Aken. If it runs an older relay, upgrade it. See [Run your own relay](relay.md).

## The relay rate limit was reached

`the relay's rate limit for this address was reached; retry in ...`

Requests from your address exceeded the relay's rate limit. Wait for the
reported interval before trying again. Machines sharing an outbound address
share that limit. See [Hosted relay](relay.md#hosted-relay) for service limits.

## The anonymous allowance was reached

`anonymous allowance reached: ...`

The relay returned `server_limit`: your developer address has used sessions
from too many distinct server addresses within the allowance window. Wait
for an unused pairing to age out, or [run your own relay](relay.md). Point both
the collector and MCP at it with `--relay`. See
[Anonymous allowance](relay.md#anonymous-allowance) for how the window works.

## The journal window is empty

`aken: no journal entries for <unit> in the window; ...`

The selected unit has no readable entries in that time window. Check the
unit name and journal access on the server with
`sudo -u aken journalctl -u <unit> --since '1 hour ago'`.
If the unit logged earlier, widen `--since`. See [Time values](collect.md#time-values).

## Container logs are missing

`no container logs in the journal; is the docker logging driver journald? ...`

Aken reads container logs through journald. Configure Docker's `journald`
logging driver and recreate the affected containers as described in
[Docker logs](docker.md). Then check journal access as the `aken` user.

## A file is not readable

`cannot read <path>: permission denied`

The collector's user lacks permission to read the file or traverse a parent
directory. On the server, check those permissions and grant the `aken` user
the needed read and directory traversal access. The installer adds `aken`
to `adm` and `systemd-journal` where those groups exist. Adding `--allow`
widens the path scope but does not grant filesystem permissions.

## A symlink leaves the allowed directories

`refusing <path>: it is a symlink that leaves the allowed directories (...)`

The path resolves outside `/var/log` and any directories you allowed. Use
the target's real path. If you intend to share that directory, add
`--allow DIR` when starting the collector. See [Permissions](collect.md#permissions).

## A live job is outside the scope

`rejected: outside the scope (/var/log); restart aken serve with --allow DIR to widen it`

The requested path is outside the session's allowed directories. If you
intend to share it, stop the session and restart on the server with
`sudo aken serve --allow DIR`, replacing `DIR` with the directory. On your
machine, run `aken-mcp join` and paste the new token. See [Live sessions](serve.md).

## The MCP has no session

`aken-mcp: no session; run aken-mcp join and paste the token from your server`

The MCP has no saved session. Start `sudo aken serve` on the server, then
run `aken-mcp join` on your machine and paste the token at its prompt.
Keep the token out of agent chats. See [Join a session](mcp.md#join-a-session).

## No artifact exists for the token

`aken-mcp: no artifact for this token on <relay>: ...; if the collector used another relay, join with aken-mcp join --relay <URL>`

The artifact expired, was deleted, did not finish uploading, or is on another
relay. Check the relay used by the collector. If it differs, join on your
machine with `aken-mcp join --relay <URL>`. If the artifact is gone, collect
and send it again on the server, then join with its new token.

## Join authentication failed

`aken-mcp: join rejected: bad authentication`

The live key exchange failed authentication. Check that you copied the token
from the intended server session. Stop that session, start `sudo aken serve`
again, and join its new token on your machine. If the failure repeats, stop
and check the collector, MCP, and relay setup before sharing data.

## A live token was already used

`this token was already used to join a live session; run aken serve again and join its new token`

The saved live session already used this token. To keep using that session,
check `aken-mcp status` instead of joining again. To start a new session,
stop the old collector, run `sudo aken serve` on the server, and join the new
token on your machine. Do not delete the saved session to reuse the token.

## The relay does not support live sessions

`aken: relay <url> does not support live sessions`

The relay lacks live-session support. Upgrade your relay or use one that
supports live sessions, with the same URL on the collector and MCP. You can
also use `sudo aken collect` for a one-shot artifact. See [Collect logs](collect.md).

## The agent cannot see the aken tools

The MCP may be unregistered, disconnected, or outside the agent's PATH.
On your machine, check `/mcp` in Claude Code, `codex mcp list` for Codex CLI,
or the `aken` entry in Cursor's `.cursor/mcp.json` or `~/.cursor/mcp.json`.
The command must start `aken-mcp` with the `serve` argument.

If your agent does not inherit your shell's PATH, use the full binary path
in its MCP registration, such as `/home/you/.local/bin/aken-mcp` for the
script install. Restart the MCP connection and check again. See
[Connect your agent](mcp.md#install) for registration commands and configuration.
