# Use the local MCP

## Install

Install and verify `aken-mcp` on your own machine using
[Install and verify](install.md#install-the-mcp-on-your-machine).
Ensure the agent can find `aken-mcp` on its PATH.

## Join a session

After the collector opens a [live session](serve.md) or uploads an artifact,
run this on your machine and paste the token at the prompt. The prompt does
not echo the token:

```sh
aken-mcp join
```

Without a token argument, `join` reads one line from stdin. You can also pass
the token as an argument:

```sh
aken-mcp join akn1_...
```

An argument can enter your shell history. Prefer pasting at the prompt and
keep the token out of agent chats and tickets. Anyone with a one-shot token
can read its artifact until it expires. A live token authenticates the key
exchange, so protect it before joining too.

If the collector used a non-default relay, pass the same URL when joining:

```sh
aken-mcp join --relay <URL>
```

The token is validated and the session checked with the relay. A successful
join for a one-shot artifact prints:

```text
Joined session <sid>. The artifact expires at <t>.
```

For a live session, the MCP exchanges authenticated keys with the collector
and prints:

```text
Joined live session <sid> via cli. It expires at <t>.
```

Both CLI join and the chat `join` tool refuse a token whose session ID matches
the current saved live session, even after expiry, with
`this token was already used to join a live session; run aken serve again and join its new token`.
The CLI prefixes this message with `aken-mcp: ` and exits 1; chat join returns
it as a tool error. A missing or malformed session file does not block joining.

If the collector MAC fails verification, `join` exits 1 with:

```text
aken-mcp: join rejected: bad authentication
```

If the relay answers 404, `join` exits 1 with:

```text
aken-mcp: no artifact for this token on <relay>: it expired, was deleted, or the upload did not finish
```

The MCP stores the session at `~/.config/aken/session.json` on Linux and
`~/Library/Application Support/aken/session.json` on macOS. The directory is mode `0700` and the file is mode `0600`.
Unlike the collector's local copy, this file contains the token. A one-shot
session uses this version 2 form:

```json
{"version": 2, "mode": "blob", "token": "akn1_...", "relay": "https://relay.aken.dev", "session_id": "<32 hex>", "expires_at": "<RFC 3339>", "joined_at": "<RFC 3339>", "joined_via": "cli"}
```

A live session also stores the content root and sequence counters:

```json
{"version": 2, "mode": "session", "token": "akn1_...", "relay": "https://relay.aken.dev", "session_id": "<32 hex>",
 "expires_at": "<RFC 3339>", "joined_at": "<RFC 3339>", "joined_via": "cli",
 "content_root": "<base64url 32 bytes>", "next_job_seq": 1, "next_result_seq": 1}
```

`joined_via` is `cli` or `chat`. Joining overwrites the session file. Version 1
files load as `blob`; saves use version 2. Blob files have no content root or
sequence counters.

The MCP persists the job counter before posting and the result counter as
soon as it receives a result. Restarting `aken-mcp serve` continues the
sequence. Protect the live session file: its content root derives the keys
that decrypt the session's messages.

Check the current session, mode, and expiry:

```sh
aken-mcp status
```

To delete either kind of session on the relay and forget the local session:

```sh
aken-mcp end
```

Ending the session deletes the session file. For a live session, `end` prints
`Ended live session <sid>; the collector's aken serve stops on its next poll.`
and the collector ends with `aken: the session was ended on the relay`. This does not remove the collector's local copy
or content already present in an agent transcript.

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

## Prompts that work

After joining a session and connecting the MCP, try one of these prompts:

- `Use the aken MCP to check the nginx journal for errors in the last hour. Propose a plan for any follow-up reads.`
- `Check the aken MCP: summarise what the collected log covers and list the error lines.`
- `In the aken artifact, find every 429 or rate limit line, group them by the proxy placeholder, and show the first occurrence with context.`
- `Use the aken tools to reconstruct what happened to job <id> between 11:30 and 11:40; quote line numbers.`

Replace `<id>` with the job ID you want to investigate. The agent sees redacted
values as placeholders such as `<ip#3>` or `<secret#1>`. Within an artifact or
across every result of one live session, the same placeholder refers to the
same original value, so the agent can correlate occurrences without recovering
the value. For artifact tools, `lines_redacted` counts returned lines containing
placeholders, not replaced values.

At connection time, the server sends workflow guidance. For artifacts, start
with `summary`, then `sources`; use RE2 `search` with before/after context,
`context` around a
line number, and `read` for exact ranges of at most 500 lines, continuing with
`next_from`. The guidance also explains that tools are read-only, line numbers
start at 1 per source, responses end with JSON metadata, and the human must
collect again after the expiry reported by `summary`.

For live sessions, the guidance explains that tools run on the server as
typed jobs under the terminal approval level. Page with `from` or `cursor`.
Prefer `search_files` and `journal` with `regex` over reading whole files.
Every result uses the same placeholders across the session.

## Live session tools

Live tools submit catalog jobs to the collector. At level 1, approve each job
or plan in the server terminal. Level 0 preapproves catalog v1 jobs within the
path scope. See [Live sessions](serve.md#levels) for result review at each level.

| Tool | Parameters (JSON Schema types) | Behaviour |
|---|---|---|
| `list_dir` | `path` string required | One job |
| `read_file` | `path` required; `from`, `to` integers, defaults 1 and from+499; at most 500 lines | One job |
| `search_files` | `glob`, `regex` required; optional `since`; `before`, `after` integers 0..50; `max` integer 1..200 default 50; optional `cursor` | One `search` job |
| `tail_file` | `path` required; `n` integer 1..500 default 100 | One `tail` job |
| `journal` | `unit` required; `since` default 1h, `until` default now; optional `regex`; `tail` integer 1..500; `max` integer 1..500 default 200; optional `cursor` | One job; `tail` excludes `regex`, `max`, and `cursor` |
| `docker_logs` | `container` required; otherwise the same parameters as `journal` | One job |
| `systemctl_status` | `unit` required | One job |
| `ps` | none | One job |
| `df` | none | One job |
| `plan` | `jobs`: array of `{name, params}` objects, 1..40 | One plan message; returns every result in job order |
| `result` | `id` required | Waits for a job result after an earlier call timed out |

Paths, globs, regexes, times, cursors, units, containers, and IDs are strings.
The [catalog](serve.md#the-catalog) lists commands, output shapes, and limits.
Every result carries at most 256 KiB of line text. Continue `read_file` with
`next` as `from`; pass `next` as `cursor` for `search_files`, `journal`, or
`docker_logs`.

Tool output contains the lines followed by one JSON metadata line with
`id`, `status`, `lines`, `lines_redacted`, `flags`, and `next`, for example:

```json
{"id":"j7","status":"ok","lines":200,"lines_redacted":12,"flags":0,"next":"201"}
```

`lines_redacted` counts lines changed by redaction; `flags` counts strings to
inspect. Neither count establishes that the result is free of sensitive data.

### Plan

Call `plan` with catalog names, including `search` and `tail` rather than their
MCP names `search_files` and `tail_file`. For example:

```json
{
  "jobs": [
    {"name": "journal", "params": {"unit": "nginx.service", "since": "1h", "regex": "error"}},
    {"name": "tail", "params": {"path": "/var/log/nginx/error.log", "n": 100}}
  ]
}
```

A plan cannot contain another plan. At level 1, one approval covers all its
jobs. Denying the plan gives every job a `denied` result. The MCP assigns IDs
`j<seq>` to jobs and `j<seq>.<n>` to jobs inside a plan. It returns each result
in job order with a heading in this form:

```text
## <n> <name> <status>
```

### Timeouts and errors

Each call waits for its result for up to 120 seconds. If it times out, it
returns this tool error:

```text
awaiting approval or still running in the server terminal; call result with id <id>
```

Call `result` with the reported ID to wait for that result:

```json
{"id": "j7"}
```

Results are cached by ID. `denied`, `rejected`, and `error` results return
`isError: true` with `status` and `error` so the agent can see the reason.
A result envelope that fails authentication or sequence checks is dropped
with a line on stderr.

Artifact and live tools are both registered. Calling an artifact tool in a
live session returns:

```text
this is a live session: use read_file, search_files, tail_file, journal, docker_logs, systemctl_status, ps, df or plan
```

Calling a live tool in a one-shot session returns:

```text
this session is one-shot: use sources, summary, search, tail, read or context
```

## Tools

These artifact tools read one artifact. Prefer `search` and `context` over
reading whole sources. Use the names returned by `sources` for the `source` parameter.
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
For live sessions, it joins with `via: chat`. The success message ends with
`This token has been in the chat transcript.` A chat join forces the collector
to level 1 even when you started it with `--level 0`.

## Sandbox the agent

Deny `ssh` in Claude Code permissions; Aken does not close the agent's own SSH access.
Keep `~/.ssh` outside the agent's sandbox so it cannot read your SSH keys.
Keep `SSH_AUTH_SOCK` out of that sandbox so it cannot use your SSH agent.

## What the agent never gets

Through Aken, the agent gets no shell, no SSH key, and no network path to the
server. It gets read-only artifact tools or typed live jobs under the session
approval level. Everything it reads goes to its LLM provider. Content already
shared in chat can remain there after the session expires.
