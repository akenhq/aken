# Dependency budget

## Why

The collector is third-party code on someone's production server. A person
should be able to read the whole executor in an afternoon. Every module makes
that longer and adds a supply-chain party.

## Budget per binary

| Binary | Allowed outside the standard library | Status in phase 0 |
|---|---|---|
| `aken` (collector) | `golang.org/x/crypto` for primitives the standard library lacks; `golang.org/x/term` for the terminal approval screens | Standard library only |
| `aken-mcp` | The two above plus `github.com/modelcontextprotocol/go-sdk` | Standard library only |
| `aken-devrelay` | Nothing | Standard library only |

## Enforced

`make depcheck` fails when `aken` or `aken-devrelay` link any package outside
the standard library and this module. CI runs it on every push. When you change
the budget, update the target and this file in the same pull request.

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

As you grow the collector, keep this list current. It is the list a reviewer reads.
