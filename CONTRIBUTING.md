# Contributing

## Before you start

For anything beyond a small fix, open an issue so you and the maintainer can
discuss the design before you write code.

## Setup

You need Go 1.27.1 and golangci-lint v2.13.2. `go.mod` pins Go 1.27.1;
a newer local Go downloads it automatically.

Run `make check` for lint, vet, tests, the dependency check, and the
reproducibility check. Run `make build` to put the binaries in `bin/`.

## Rules that are not up for discussion

No listener on the server, ever. No shell: jobs are typed functions. Redaction
runs on the server before encryption, with no LLM in the path. The relay never
sees content.

See [THREAT-MODEL.md](THREAT-MODEL.md) for the full list of invariants. A change
that breaks one is out of scope, not a trade-off.

## Dependencies

Follow [DEPENDENCIES.md](DEPENDENCIES.md). For a new module, state the reason
in your pull request and update that file.

## Protocol changes

Change `spec/`, the code, and the test vectors together in one pull request.

## Public from the first commit

Do not put secrets, customer data, internal hostnames, or personal data in
commits, issues, or code.

## Sign-off and license

Sign off your commits under the Developer Certificate of Origin with
`git commit -s`. Contributions are Apache-2.0. Start every Go file with
`// SPDX-License-Identifier: Apache-2.0`.

## Pull requests

Keep your pull request small, with one change. Run `make check` and resolve
failures before submitting. Fill in the checklist in the pull request template.
