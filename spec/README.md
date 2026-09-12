# Protocol specification

Draft.

## Scope

This specification covers the wire protocol shared by the collector, the local
MCP and relays.

| File | Status | Scope |
|---|---|---|
| [token.md](token.md) | Normative | Token encoding, parsing and key derivation |
| [envelope.md](envelope.md) | Draft, phase 2 | Persistent job and result envelopes |
| [blob.md](blob.md) | Draft, phase 1 | One-shot encrypted artifacts |
| [relay-api.md](relay-api.md) | Draft, v0 | Relay requests, authentication and caps |

## Status

The overall protocol is a draft. Each file states whether it is normative or a
draft. Only the token format and derivation are implemented in phase 0. The
protocol has not been professionally reviewed. A public call for review is open
through [GitHub issues](https://github.com/akenhq/aken/issues).

## Versioning

The token prefix carries the token format version. Every envelope carries a
protocol version. A relay advertises the versions it accepts.

## Conventions

“Must”, “must not” and “should” have their plain English meanings. Draft text
records the intended design; its open items must be resolved before implementation.

## Test vectors

[vectors/token-v1.json](vectors/token-v1.json) contains four valid tokens with
secrets and derived outputs, ten invalid tokens, and the KDF labels, salt and
output lengths. [protocol/token_test.go](../protocol/token_test.go) reads this
file to check the implementation. CI's secret scanner treats vector-shaped values
in this directory as public test data; `.gitleaks.toml` at the repository root
says exactly which shapes, and nothing else is exempt.

## Proposing a change

Open an issue first. Then submit a pull request that changes the specification,
the code and the test vectors together.
