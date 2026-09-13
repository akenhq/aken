# Dependency budget

## Why

The collector is third-party code on someone's production server. A person
should be able to read the whole executor in an afternoon. Every module makes
that longer and adds a supply-chain party.

## Budget per binary

| Binary | Allowed outside the standard library | Status in phase 1 |
|---|---|---|
| `aken` (collector) | `golang.org/x/term` and its dependency `golang.org/x/sys` | Terminal handling for the review screen; nothing else outside the standard library |
| `aken-mcp` | Official MCP SDK `github.com/modelcontextprotocol/go-sdk` v1.7.0 and its transitive modules, plus `golang.org/x/term` | SDK with stdio transport; modules listed below |
| `aken-devrelay` | Nothing | Standard library only |

`go.mod` pins `golang.org/x/term` to v0.46.0. The indirect modules in
`go.mod` are:

| Module | Version |
|---|---|
| `github.com/google/jsonschema-go` | v0.4.3 |
| `github.com/segmentio/asm` | v1.1.3 |
| `github.com/segmentio/encoding` | v0.5.4 |
| `github.com/yosida95/uritemplate/v3` | v3.0.2 |
| `golang.org/x/oauth2` | v0.35.0 |
| `golang.org/x/sync` | v0.20.0 |
| `golang.org/x/sys` | v0.48.0 |
| `golang.org/x/time` | v0.15.0 |

Why the SDK: the MCP runs on the developer's machine, not on the server. A
maintained official implementation of a moving protocol is safer than a
hand-written one. The audit path below covers the server binary.

## Enforced

`make depcheck` covers `aken`, allowing only `golang.org/x/term` and
`golang.org/x/sys` outside the standard library and this module, and
`aken-devrelay`, allowing only the standard library and this module. CI runs
it on every push. When you change the budget, update the target and this file
in the same pull request.

## Adding a module

State the reason in your pull request. Prefer the standard library. Pin the
version. Read the module before you add it; a module that pulls in a tree of
modules does not fit the budget.

## Toolchain

`go.mod` pins the Go version. Dependabot proposes updates weekly for modules
and GitHub Actions. Every action is pinned to a commit.

## Audit path

The server binary consists of these files today:

- `cmd/aken/main.go`
- `internal/buildinfo/buildinfo.go`
- `internal/collect/*.go`
- `internal/serve/*.go`
- `internal/screen/*.go`
- `internal/source/*.go`
- `internal/redact/*.go`
- `rules/rules.go`
- `rules/default.json`
- `protocol/token.go`
- `protocol/blob.go`
- `protocol/session.go`
- `protocol/jobs.go`
- `protocol/relayapi.go`
- `protocol/relayclient.go`

Test files are excluded.

As you grow the collector, keep this list current. It is the list a reviewer reads.
