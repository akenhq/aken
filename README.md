# Aken

Let your coding agent investigate production without giving it production access.

A small collector runs on your server as an unprivileged user, makes outbound
HTTPS requests only. Logs are redacted on the server, shown to you, then
encrypted and uploaded to a zero-knowledge relay with a short TTL. A local MCP server on your machine gives your coding agent search,
tail and context tools over that artifact. The agent never gets a shell, an SSH
key or a network path to your server.

**Status:** phase 1, one-shot mode. `aken collect` uploads one redacted,
encrypted artifact, and `aken-mcp` serves read-only tools over it. Persistent
sessions, approvals, and the job catalog are later phases. The protocol and
the code have not been professionally reviewed.

## What is in this repository

| Path | Contents |
|---|---|
| `cmd/aken` | Collector |
| `cmd/aken-mcp` | Local MCP |
| `cmd/aken-devrelay` | In-memory relay for development |
| `protocol/` | Tokens, artifacts, and relay client |
| `relay/` | Relay API v0 request handling shared by the dev relay and hosted relays |
| `internal/` | Collector, redaction, MCP, and dev relay packages |
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

After [installing and verifying](docs/install.md), run this on the server:

```sh
sudo -u aken aken collect --unit nginx --since 1h
```

The review screen runs in the server terminal. Review the redacted content,
then choose **send** to encrypt and upload it.

On your machine, run this and paste the printed token at the prompt:

```sh
aken-mcp join
```

Register the MCP with Claude Code:

```sh
claude mcp add aken -- aken-mcp serve
```

Then ask Claude Code:

> Check the aken MCP: summarise what the collected log covers and list the error lines.

Check the session and its expiry:

```sh
aken-mcp status
```

## Docs

- [Install and verify](docs/install.md)
- [Collect logs](docs/collect.md)
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
