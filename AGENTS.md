# Repository Guidelines

## Project structure and module organization

Aken is a Go collector, local MCP server, and relay service. Entry points live in `cmd/aken`, `cmd/aken-mcp`, and `cmd/aken-relay`. Implementation packages live in `internal/`; shared wire-protocol code and relay handlers live in `protocol/` and `relay/`. Redaction defaults are in `rules/default.json`. Specifications, fixtures, and conformance tests live in `spec/`; user documentation lives in `docs/`. Installer scripts and tests live in `packaging/`.

## Build, test, and development commands

Use Go 1.27.1 (`go.mod`) and golangci-lint v2.13.2. Run commands from the repository root:

- `make build`: build all three binaries into `bin/`.
- `./bin/aken-relay serve --store memory`: run the in-memory relay at `127.0.0.1:7788`.
- `make test`: run all Go tests with `go test ./...`.
- `make lint vet`: run golangci-lint and Go vet.
- `make check`: run lint, vet, tests, dependency-budget checks, reproducible builds, and installer tests.
- `make test-install`: run installer tests; for shell changes, also run `shellcheck -S style packaging/*.sh`, as CI does.

## Coding style and naming conventions

Use standard Go formatting with tabs and LF line endings. `.golangci.yml` configures `gofmt`, `goimports`, standard linters, and `gosec`. Use lowercase package names, exported `MixedCaps` identifiers, and unexported `mixedCaps` identifiers. Start every Go file with `// SPDX-License-Identifier: Apache-2.0`. Prefer the standard library and follow [DEPENDENCIES.md](DEPENDENCIES.md) before adding modules.

## Testing guidelines

Use Go's standard `testing` package. Place `*_test.go` files beside implementation files and name tests `TestBehavior`. Use descriptive `t.Run` subtests for cases. End-to-end tests live in `internal/e2e`; conformance tests live in `spec/conformance`. Run focused checks with `go test ./protocol -run TestVectors`. No numeric coverage threshold is configured. For protocol changes, update the specification, implementation, and `spec/vectors/` together.

## Commit and pull request guidelines

History uses concise descriptive subjects, sometimes with phase prefixes; no strict Conventional Commits pattern is established. Sign off commits with `git commit -s` (DCO). For work beyond a small fix, open an issue before implementation. Keep PRs focused, explain what changed and why, and complete [.github/PULL_REQUEST_TEMPLATE.md](.github/PULL_REQUEST_TEMPLATE.md). Run `make check` before submitting; justify new modules and update the dependency budget.

## Security constraints

Preserve [THREAT-MODEL.md](THREAT-MODEL.md) invariants: no collector listener or shell jobs, server-side redaction before encryption, no LLM in that path, and no relay access to plaintext content. Never commit secrets, customer data, internal hostnames, or personal data.
