# Aken

Let your coding agent investigate production without giving it production access.

A small collector runs on your server as an unprivileged user and makes
outbound HTTPS requests only. It redacts logs on the server, encrypts them,
and sends them through a relay with a short TTL. A local MCP server on your
machine gives your coding agent typed read jobs in a live session or tools
over a one-shot artifact. The agent gets no shell, SSH key, or network path
to your server through Aken.

**Status:** phase 2, live sessions and one-shot mode. `aken serve` handles
catalog read jobs with terminal approvals at level 1 or preapproval at level 0.
`aken collect` uploads one reviewed, redacted, encrypted artifact.
`aken-mcp` supports both modes. The protocol and the code have not been
professionally reviewed.

## What is in this repository

| Path | Contents |
|---|---|
| `cmd/aken` | Collector |
| `cmd/aken-mcp` | Local MCP |
| `cmd/aken-relay` | Relay with memory, directory, or R2 storage |
| `protocol/` | Tokens, artifacts, and relay client |
| `relay/` | Relay API v0 request handling used by `aken-relay` and other relays |
| `internal/` | Collector, redaction, MCP, and relay service packages |
| `rules/` | Default redaction rules |
| `spec/` | Specifications, vectors, and the conformance suite |
| `docs/` | User docs |
| `.github/workflows/` | CI checks and signed, reproducible releases |

## Build

Requires Go 1.27.1, pinned in `go.mod`, and golangci-lint v2.13.2 for `make lint`.
Go's automatic toolchain selection downloads the required toolchain when the
local Go is older.

```sh
make build
make check
```

`make build` puts the three binaries in `bin/`. `make check` runs lint, vet,
tests, the dependency check and the reproducibility check.

## Install and verify a release

See [docs/install.md](docs/install.md) to install and verify a release.

## Quick start

On the server, install the collector:

```sh
curl -fsSL https://aken.dev/install.sh | sudo bash
```

On your own machine, install the local MCP (Linux or Apple Silicon macOS)
with Node 18 or later:

```sh
npm install -g aken-mcp
```

Without Node, use the [MCP installer](docs/install.md#install-the-mcp-on-your-machine).
Register the MCP with Claude Code:

```sh
claude mcp add aken -- aken-mcp serve
```

For Codex CLI or Cursor, see [Use the local MCP](docs/mcp.md#install).
See [Install and verify](docs/install.md) for signature verification,
version selection, and the collector's run-once option.

Open a live session on the server. The `aken` command switches to the
unprivileged `aken` user before the collector starts:

```sh
sudo aken serve
```

On your machine, run this and paste the printed token at the prompt:

```sh
aken-mcp join
```

Then ask Claude Code:

> Use the aken MCP to check the nginx journal for errors in the last hour. Propose a plan for any follow-up reads.

Approve jobs or plans in the server terminal. The default level 1 sends
redacted results automatically unless they have strings to inspect; those
pause for send or drop. See [Live sessions](docs/serve.md) for levels and scope.

For a one-shot artifact, run this on the server instead:

```sh
sudo aken collect --unit nginx --since 1h
```

Review the redacted content, then choose **send** to encrypt and upload it.
Join its printed token with `aken-mcp join` on your machine. Then ask the
agent to summarise the collected log and list its error lines.

Check the session mode and expiry:

```sh
aken-mcp status
```

## Docs

- [Install and verify](docs/install.md)
- [Live sessions](docs/serve.md)
- [Collect logs](docs/collect.md)
- [Run your own relay](docs/relay.md)
- [Docker logs](docs/docker.md)
- [Redaction](docs/redaction.md)
- [Use the local MCP](docs/mcp.md), including Claude Code, Codex CLI, and Cursor

## Security

Read [SECURITY.md](SECURITY.md) for reporting vulnerabilities and
[THREAT-MODEL.md](THREAT-MODEL.md) for the trust boundaries and risks.
Redaction is defence in depth and can miss sensitive data.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) and the dependency budget in
[DEPENDENCIES.md](DEPENDENCIES.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
