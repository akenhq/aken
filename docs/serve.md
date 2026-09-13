# Live sessions

## What it does

`aken serve` handles a live session in five steps:

1. Open a session on the relay and print a token in the server terminal.
2. Pair with the local MCP when you enter the token on your machine.
3. Ask you to approve each job or plan at level 1, or use your level 0 preapproval.
4. Run the typed jobs, redact their output, keep a local copy, and send encrypted results.
5. End on Ctrl-C, relay loss, or expiry and keep the local copy.

The collector handles one job or plan at a time, in order. Catalog v1 contains
only read jobs. For a single reviewed artifact, use [Collect logs](collect.md).

## Permissions

Run the collector as the unprivileged `aken` user. It refuses root. Follow
[Install and verify](install.md) to give that user membership in `adm` and
`systemd-journal` where those groups exist.

File jobs must stay under `/var/log` or a directory you add with `--allow DIR`.
You can repeat `--allow`. The collector resolves canonical paths through
`os.Root` and refuses symlinks that leave an allowed root. The user must also
have permission to read the files.

Command-backed jobs use fixed argument lists and no shell. The collector
selects `/usr/bin/<name>` or `/bin/<name>`, whichever exists, without a PATH
lookup. Container logs come from journald; see [Docker logs](docker.md).

## Start a session

On the server, run:

```sh
sudo -u aken aken serve
```

The opening block has this layout. The scope, redaction overrides, counts,
and times illustrate a session with extra configuration. The token is a
placeholder:

```text
aken serve: session open on https://relay.aken.dev, level 1, expires 2026-09-13T22:00:00Z
Scope    /var/log, /srv/app/logs
Local    /var/lib/aken/sessions/20260913T140000Z-1a2b3c4d
Redaction   14 rules (12 default, 2 from /etc/aken/rules.json); kept: 10.0.0.5; off: email

Session token. Paste it into `aken-mcp join` on your machine, not into the agent chat:

  akn1_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

Waiting for the local MCP to join. Ctrl-C ends the session.
```

On your machine, run this and paste the token at the prompt:

```sh
aken-mcp join
```

Keep the token out of agent chats, shell history, and tickets. The collector
prints it once and does not store it. See [Use the local MCP](mcp.md) to
connect your agent. A successful terminal join prints this on the server:

```text
Joined via cli at 14:01:12Z. Waiting for jobs.
```

If the MCP's authentication fails, the collector keeps waiting and prints:

```text
aken: join rejected: bad authentication
```

Set redaction overrides when you open the session:

| Flag | Meaning |
|---|---|
| `--keep VALUE` | Leave this exact value unchanged; repeatable and shown when the session opens |
| `--keep-category C` | Disable a category for the session; repeatable: secret token jwt key ip email phone name address |
| `--rules FILE` | Extra rules file; defaults to `/etc/aken/rules.json` when it exists |

Kept values and disabled categories can leave sensitive data visible to the
agent and its provider. Redaction is defence in depth and can miss data.
See [Redaction](redaction.md) for rules and placeholders.

## Levels

Choose the level with `--level N` when you start the session:

| Level | What you approve | What is sent automatically |
|---|---|---|
| `1` (default) | Each job or plan on one approval screen | Redacted results without strings to inspect; flagged results pause for send or drop |
| `0` | Starting the session preapproves every catalog v1 job within the path scope | Every redacted result, including flagged results; the summary shows the flag count |

A session joined through the chat tool runs at level 1 even if you started
it with `--level 0`. The collector prints:

```text
Joined via chat at <t>: the token has been in a transcript, so this session runs at level 1.
```

Both levels require a terminal. Level 0 still validates jobs and paths before
running them and keeps a local audit copy of every result.

## The approval screen

At level 1, inspect the requested reads in the server terminal. A plan has
1..40 jobs and cannot contain another plan. This example shows the layout;
`!` marks a sensitive path. The `.env` row requires `/srv/app` in the scope:

```text
Job j7 from the agent: plan of 4 reads

  1  read_file   /var/log/nginx/error.log  lines 1-200
  2  tail        /var/log/app/app.log  last 100 lines
  3  journal     unit nginx.service  2026-09-13T13:00:00Z to 2026-09-13T14:00:00Z  first 200 lines
  4  read_file ! /srv/app/.env  lines 1-50

[a] approve   [d] deny   [v] view params
>
```

For a single job, the first line is `Job j7 from the agent: read_file` and
the list has one row. Search rows show the glob, regex, and file count, such
as `12 files`, followed by `; first: a, b, c and 9 more`.

| Input | Action |
|---|---|
| `a` | Approve the job or every job in the plan |
| `d` | Deny the job or every job in the plan |
| `v` | Show the raw parameters and return to the prompt |

The collector marks these sensitive paths:

- Base names `.env`, names starting with `.env.`, `shadow`, and `gshadow`.
- Suffixes `.pem`, `.key`, `.p12`, `.pfx`, `.kdbx`, and `.keystore`.
- Names starting with `id_` or ending with `_rsa`, `_ed25519`, `_ecdsa`, or `_dsa`.
- Paths with a `.ssh`, `.gnupg`, or `.aws` component.

The marker asks you to inspect the path. It does not add the path to the
scope or redact its contents. Unknown jobs, invalid parameters, paths outside
the scope, and class mismatches are rejected before approval.

## Results

The collector redacts output with one engine for the whole session. The same
original value gets the same placeholder across results. It caps each result,
writes the local audit copy, then encrypts and sends the result.

Each result has one rolling summary line:

```text
14:02:07Z  read_file /var/log/nginx/error.log  200 lines sent, 12 redacted (ip 3 values), 0 flags
```

At level 1, strings to inspect pause the result before sending. These are the
same flags shown by `f` in collect, excluding hex IDs or hashes:

```text
14:02:07Z  tail /var/log/app/app.log  100 lines, 2 strings to inspect
  app.log:41 ! <line>
  app.log:87 ! <line>
[s] send   [d] drop
>
```

Enter `s` to send or `d` to drop. Dropping sends a `denied` result with error
`dropped after review`. At level 0, the summary names the flag count and does
not wait. A result with no flags can still contain sensitive data.

Denied and rejected results have a summary with the reason, for example:

```text
14:02:07Z  read_file /etc/shadow  rejected: outside the scope
```

| Status | Meaning |
|---|---|
| `ok` | The job ran and returned output |
| `denied` | You declined the job or dropped its result after review |
| `rejected` | The collector refused the job before approval |
| `error` | An approved job failed, for example because a file was missing or a command failed |

Each result contains at most 256 KiB of line text. The collector cuts at a
line boundary and sets `next` when output remains. For `read_file`, continue
with that line number as `from`. For `search_files`, `journal`, and
`docker_logs`, pass `next` unchanged as `cursor`. The catalog also limits
lines, matches, and directory entries as listed below.

A job that fails envelope authentication or sequence checks gets no result
and is not shown for approval. The collector prints:

```text
aken: dropped a job: <error>
```

## The catalog

Catalog v1 jobs are all class read (1). The MCP calls catalog `search` through
`search_files` and catalog `tail` through `tail_file`. Other job names match
their MCP tool names. Use catalog names inside a `plan`.

| Name | Parameters | What it runs | Output shape |
|---|---|---|---|
| `list_dir` | `path` | Lists a directory inside the scope | `<mode> <size> <mtime RFC 3339> <name>`, sorted by name; directories end in `/`; at most 500 entries; `next` is the next entry name |
| `read_file` | `path`; `from` default 1; `to` default from+499; at most 500 lines | Reads a file range inside the scope | `<line>: <text>`; `next` is the next line number when `to` was cut or lies beyond |
| `search` | Absolute `glob` inside scope; RE2 `regex`; optional `since`; `before`, `after` 0..50; `max` 1..200 default 50; optional `cursor` | Searches at most 200 files; `since` skips files modified before a duration or time as in collect | `<path>:<line>: <text>` for matches, `<path>:<line>- <text>` for context; opaque `next` |
| `tail` | `path`; `n` 1..500 default 100 | Reads the last N lines of a file inside the scope | `<line>: <text>` with the file's real line numbers |
| `journal` | `unit`; `since` default 1h; `until` default now; optional `regex`, `tail`, `max`, `cursor` | Reads the unit's journal window with `journalctl` | short-iso-precise lines; `next` is a decimal line offset into the window's output |
| `docker_logs` | `container`; otherwise the same parameters as `journal` | Reads container logs from journald | short-iso-precise lines; `next` is a decimal line offset into the window's output |
| `systemctl_status` | `unit` | `systemctl status --no-pager --lines=0 <unit>` | Command output; exit status 0..4 is `ok`, higher is `error` |
| `ps` | `{}` | `ps -eo pid,ppid,user,%cpu,%mem,rss,etimes,args --sort=-%cpu` | Command output, at most 500 lines |
| `df` | `{}` | `df -hP` | Command output |

For `journal` and `docker_logs`, `regex` selects matching lines. `max` is
1..500 lines per result, default 200. `tail` selects the last 1..500 lines of
the window and excludes `regex`, `max`, and `cursor`. Time values use the
formats in [Collect logs](collect.md#time-values).

## Local copy

The collector creates this directory with mode `0700`:

```text
<state-dir>/sessions/<YYYYMMDDTHHMMSSZ>-<first 8 hex of session id>/
```

| File | Contents |
|---|---|
| `session.json` | `session_id`, `relay`, `level`, `created_at`, `expires_at`, `joined_at`, `joined_via`, `ended_at`, `argv`; rewritten at join and exit |
| `jobs.log` | Append-only JSON lines with `time`, `seq`, `job_id`, `name`, `event`, `params`, `paths`, `status`, `lines`, `lines_redacted`, `flags`, `error`; events are `received`, `approved`, `denied`, `rejected`, `sent`, `dropped` |
| `results/<result seq>.txt` | Exactly the redacted lines of that result message; written once, mode `0400` |
| `mapping.json` | Placeholder to original value; rewritten through a temporary file and rename after every result that added values |

The token does not appear in these files. The mapping contains original
sensitive values. Protect the directory as you protect the source logs.

| Flag | Meaning |
|---|---|
| `--state-dir DIR` | Defaults to `/var/lib/aken` when writable, otherwise `$XDG_STATE_HOME/aken` or `~/.local/state/aken` |
| `--retention D` | Prunes copies under `sessions/` older than this before the session; default `720h` (30 days); `0` keeps everything |

Relay expiry does not remove the local copy.

## Ending a session

Press Ctrl-C in the server terminal, or run this on your machine:

```sh
aken-mcp end
```

The collector also ends on expiry or a relay 404 or 409, including when the
MCP ends the session or the relay loses it. It deletes the relay session,
ignores a deletion 404, writes the mapping, and sets `ended_at` in
`session.json`. The final line has this form:

```text
Session ended: 12 jobs, 10 sent, 1 denied, 1 rejected. Local copy: <dir>
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success, including ending with Ctrl-C |
| `1` | Failure or refusal, including relay termination or expiry |
| `2` | Usage error |

Running as root is a refusal. Without a terminal on stdin, serve exits 1 with:

```text
aken: serve needs a terminal
```

## Relay

`--relay URL` selects the relay base URL. The default is
`https://relay.aken.dev`. Use the same URL with `aken-mcp join --relay URL`.
`--ttl D` sets the session lifetime, default `8h`, maximum `24h`.

If the relay does not advertise live session support, the collector exits 1:

```text
aken: relay <url> does not support live sessions
```
