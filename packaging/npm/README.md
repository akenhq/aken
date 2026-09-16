# aken-mcp

`aken-mcp` is the local MCP server for [Aken](https://aken.dev). It gives
your coding agent redacted, reviewed access to server logs. See the
[Aken repository](https://github.com/akenhq/aken) for the collector and protocol.

## Install

With Node 18 or later, run:

```sh
npm install -g aken-mcp
```

## Register with Claude Code

```sh
claude mcp add aken -- aken-mcp serve
```

For Codex CLI and Cursor, see
[Use the local MCP](https://github.com/akenhq/aken/blob/main/docs/mcp.md#install).

## Join a session

Run this on your machine and paste the token from your server at the prompt:

```sh
aken-mcp join
```

## Supported platforms

Linux x64 and arm64, and macOS arm64 (Apple Silicon).

## How this package works

This package runs no install scripts. The binary comes from the
`@akenhq/mcp-<os>-<arch>` optional dependency and is the same file as the signed
GitHub release asset. See
[Install and verify](https://github.com/akenhq/aken/blob/main/docs/install.md#install-the-mcp-on-your-machine)
to check it against the release checksums.
