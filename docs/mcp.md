# Use the local MCP

## Install

Install and verify `aken-mcp` on your own machine using
[Install and verify](install.md#install-the-mcp-on-your-machine).
Ensure the agent can find `aken-mcp` on its PATH.

## Join a session

After the collector uploads an artifact, run this on your machine and paste
the token at the prompt. The prompt does not echo the token:

```sh
aken-mcp join
```

Without a token argument, `join` reads one line from stdin. You can also pass
the token as an argument:

```sh
aken-mcp join akn1_...
```

An argument can enter your shell history. Prefer pasting at the prompt and
keep the token out of agent chats and tickets. Anyone with it can read the
artifact until it expires.

If the collector used a non-default relay, pass the same URL when joining:

```sh
aken-mcp join --relay <URL>
```

The token is validated and the session checked with the relay. A successful
join prints:

```text
Joined session <sid>. The artifact expires at <t>.
```

If the relay answers 404, `join` exits 1 with:

```text
aken-mcp: no artifact for this token on <relay>: it expired, was deleted, or the upload did not finish
```

The MCP stores the session at `~/.config/aken/session.json` on Linux and
`~/Library/Application Support/aken/session.json` on macOS. The directory is mode `0700` and the file is mode `0600`.
Unlike the collector's local copy, this file contains the token:

```json
{"version": 1, "token": "akn1_...", "relay": "https://relay.aken.dev", "session_id": "<32 hex>", "expires_at": "<RFC 3339>", "joined_at": "<RFC 3339>", "joined_via": "cli"}
```

`joined_via` is `cli` or `chat`. Joining overwrites the session file.

Check the current session and expiry:

```sh
aken-mcp status
```

To delete the artifact on the relay and forget the local session:

```sh
aken-mcp end
```

Ending the session deletes the session file. It does not remove the collector's
local copy or content already present in an agent transcript.

## Claude Code

Register the stdio MCP server:

```sh
claude mcp add aken -- aken-mcp serve
```

In Claude Code, use `/mcp` to check the connection.

## Codex CLI

Register the stdio MCP server:

```sh
codex mcp add aken -- aken-mcp serve
```

Alternatively, add this entry to `~/.codex/config.toml`:

```toml
[mcp_servers.aken]
command = "aken-mcp"
args = ["serve"]
```

Check the registration:

```sh
codex mcp list
```

## Cursor

Add this configuration to `.cursor/mcp.json` in your project or
`~/.cursor/mcp.json` globally. Merge the `aken` entry with any existing servers:

```json
{"mcpServers": {"aken": {"command": "aken-mcp", "args": ["serve"]}}}
```

## Tools

The tools read one artifact. Prefer `search` and `context` over reading whole
sources. Use the names returned by `sources` for the `source` parameter.
Line numbers start at 1 within each source.

| Tool | Parameters (JSON Schema types) | Lines returned | Metadata |
|---|---|---|---|
| `sources` | none | one per source: `name  kind  lines  since..until  note`, then `artifact expires <t>` | `{"sources": n}` |
| `summary` | none | created, expires, sources, total lines, lines redacted, by category, flags, rules | `{"lines": n, "lines_redacted": n, "flags": n}` |
| `search` | `regex` string required; `source` string optional (all when omitted); `before`, `after` integers 0..50 default 0; `max` integer 1..200 default 50; `cursor` string optional | `source:line: text` for matches, `source:line- text` for context; matched lines longer than 2000 bytes are cut at 2000 bytes with ` …[+N bytes]` | `{"matches": n, "lines_redacted": n, "truncated": bool, "cursor": "..."}` |
| `tail` | `source` required; `n` integer 1..500 default 100 | `line: text` | `{"lines": n, "lines_redacted": n, "first_line": n, "last_line": n}` |
| `read` | `source` required; `from` integer >= 1 required; `to` integer >= from required; at most 500 lines per call | `line: text` | `{"lines": n, "lines_redacted": n, "next_from": n}` (absent when done) |
| `context` | `source` required; `line` integer >= 1 required; `around` integer 0..200 default 20 | `line: text` | `{"lines": n, "lines_redacted": n, "first_line": n, "last_line": n}` |
| `join` (only with `--allow-chat-join`) | `token` required | `Joined session <sid>. The artifact expires at <t>. This token has been in the chat transcript.` | `{"session_id": "...", "expires_at": "..."}` |

Every tool result has two `text` content items: the lines, then a JSON object
on one line with the metadata in the table. `lines_redacted` counts returned
lines containing at least one placeholder. It does not count replaced values
or assert that every sensitive value was removed.

A response contains at most 200 KiB of line text. At the cap, the response
is truncated with a `cursor` or `next_from`. Pass a returned cursor unchanged;
a malformed cursor or one from another artifact is a tool error. Invalid
UTF-8 bytes are rendered as U+FFFD.

The MCP loads and decrypts the artifact on the first tool call that needs it.
It validates the manifest and verifies chunk hashes. The artifact stays in
memory for the life of the process or until `join` replaces the session.

Tool failures, such as an expired artifact, bad regex, unknown source, or bad
cursor, return `isError: true`.

## Chat join

By default, the MCP does not expose the `join` tool. To enable it, launch the
server with:

```sh
aken-mcp serve --allow-chat-join
```

For an agent-managed server, add `--allow-chat-join` after `serve` in its
configuration. The `join` tool accepts only `token`. It joins on the relay given
to `aken-mcp serve --relay URL` (default `https://relay.aken.dev`) and cannot
pick another relay.

The token will be in the chat transcript at the provider and in local session
logs. Use chat join only in cloud sandboxes where a terminal is impractical.

## Sandbox the agent

Deny `ssh` in Claude Code permissions; Aken does not close the agent's own SSH access.
Keep `~/.ssh` outside the agent's sandbox so it cannot read your SSH keys.
Keep `SSH_AUTH_SOCK` out of that sandbox so it cannot use your SSH agent.

## What the agent never gets

Through Aken, the agent gets no shell, no SSH key, and no network path to the
server. It gets read-only access to the artifact for the session's lifetime.
Everything it reads goes to its LLM provider. Content already shared in chat
can remain there after the artifact expires.
