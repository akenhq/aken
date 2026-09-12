# Aken

Let your coding agent investigate production without giving it production access.

A small collector runs on your server as an unprivileged user, makes outbound
HTTPS requests only, and runs jobs from a fixed catalog. Logs are redacted on the
server, shown to you, then encrypted and uploaded to a zero-knowledge relay with
a short TTL. A local MCP server on your machine gives your coding agent search,
tail and context tools over that artifact. The agent never gets a shell, an SSH
key or a network path to your server.

**Status:** phase 0. The binaries build, print help and refuse to do anything
else. The protocol and the code have not been professionally reviewed.

## What is in this repository

| Path | Contents |
|---|---|
| `cmd/aken` | Collector command stub; refuses to collect as root |
| `cmd/aken-mcp` | Local MCP command stub |
| `cmd/aken-devrelay` | Development relay command stub |
| `protocol/` | Token format and key derivation using only the standard library |
| `spec/` | Protocol specifications and token test vectors |
| `internal/` | Shared build version information |
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

## Security

Read [SECURITY.md](SECURITY.md) for reporting vulnerabilities and
[THREAT-MODEL.md](THREAT-MODEL.md) for the trust boundaries and risks.
Redaction is defence in depth, not a guarantee.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) and the dependency budget in
[DEPENDENCIES.md](DEPENDENCIES.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
