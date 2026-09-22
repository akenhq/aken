# Security

## Reporting a vulnerability

You can use [GitHub private vulnerability reporting](https://github.com/akenhq/aken/security/advisories/new)
or email [security@aken.dev](mailto:security@aken.dev). Do not open a public issue
for a vulnerability. Include the version, platform, reproduction steps, and impact.

## What to expect

One maintainer works part time. The maintainer acknowledges reports within
7 days and provides a fix or a plan within 30 days for anything that lets an
agent read outside its scope or run anything, or lets the relay read or forge
content. You can request credit in the release notes. There is no bounty.

## Scope

This repository is in scope: the collector, local MCP, protocol package, dev
relay, redaction rules, and spec. The hosted relay at relay.aken.dev is in scope;
report it through the same channels.

## Supported versions

Only the latest release is supported.

## Review status

The protocol and the code have not been professionally reviewed. You can send
reviews and findings through the reporting channels above.

## Verifying releases

You can follow [Verify a release](docs/verify.md) to verify a release before installing it.
