# Collect logs

## What it does

`aken collect` creates one artifact in five steps:

1. Read the sources you select.
2. Redact values with deterministic rules on the server.
3. Show the redacted content for your review in the server terminal.
4. Encrypt and upload the artifact when you choose **send**.
5. Print the session token so you can join from your own machine.

The token appears once, on the terminal. The collector does not store it
anywhere. Keep it out of agent chats, shell history, and tickets.

## Permissions

Run the collector as the unprivileged `aken` user. It refuses root. Follow
[Install and verify](install.md) to give that user membership in `adm` and
`systemd-journal` where those groups exist.

File sources must be under `/var/log` or a directory you add with `--allow DIR`.
The collector reads them through `os.Root` and refuses symlinks that leave the
allowed directories. The user must also have permission to read the files.

Journal sources use `journalctl` with a fixed argument list and no shell.
Container sources require Docker's [journald driver](docker.md).

## Sources

Select at least one source. Each source flag can repeat.

| Flag | What it reads | How the window applies |
|---|---|---|
| `--unit NAME` | journald unit, for example nginx or nginx.service | `--since` to `--until` |
| `--container NAME` | Docker containers using journald: an exact name, a short name matching names that start with `NAME` followed by `.`, `_`, or `-` (including Swarm task names), or a 12- or 64-character hex ID; several matching names become several sources | `--since` to `--until` |
| `--file PATH` | plain text file; absolute path under /var/log or a --allow directory | No timestamp filtering within the file; `--tail` limits lines |
| `--glob PATTERN` | files matching a glob; every match must be under an allowed directory | Skips files last modified before `--since`; `--tail` limits lines |

The fixed read order is units, containers, files, then globs. The same canonical
file path is read once, even when selected more than once. Quote glob patterns
so the collector receives the pattern.

Source names are `unit:<name>`, `container:<name>`, and `file:<absolute path>`.
Files selected by `--glob` also use `file:` names.

Container IDs of exactly 12 or 64 hex characters match `CONTAINER_ID` or
`CONTAINER_ID_FULL`, respectively, and use the ID as the source target.
Other names are resolved before reading, using the same journalctl binary with
`--no-pager -q -F CONTAINER_NAME`. An exact name takes precedence over prefix
matches. Swarm task names have the form `<stack>_<service>.<slot>.<task id>`.
Each matched name is read with `CONTAINER_NAME=<full name>` and becomes a
`container:<full name>` source, sorted by name. If no name matches, collection
stops before reading and reports up to 20 sorted known names; if the journal
has no container logs, the error points to the journald driver setup in
[Docker logs](docker.md#check-it).

| Selection flag | Meaning |
|---|---|
| `--since T` | start of the window for journald sources; --glob skips files last modified before T (default 1h) |
| `--until T` | end of the window for journald sources (default now) |
| `--tail N` | keep only the last N lines of each file source (default 0, everything) |
| `--allow DIR` | extra directory that --file and --glob may read from (repeatable; /var/log is always allowed) |

## Time values

Use these formats for `T` in `--since` and `--until`. Quote values containing
spaces.

| Format | Examples |
|---|---|
| Duration before now | `30m`, `2h`, `3d` |
| RFC 3339 time | `2026-09-12T10:00:00Z` |
| Local time | `2026-09-12 10:00`, `2026-09-12 10:00:00` |
| Local date | `2026-09-12` |

## A first run

On the server, collect the last two hours from a unit and a container:

```sh
sudo -u aken aken collect --unit nginx --container api --since 2h
```

The review screen has this layout. The numbers, window, file source, and
redaction overrides below illustrate a separate run:

```text
aken collect: review before anything leaves this machine

Sources
  unit:nginx.service              1842 lines   2026-09-12T13:04:11Z to 2026-09-12T14:04:11Z
  container:api                    932 lines   2026-09-12T13:04:11Z to 2026-09-12T14:04:11Z
  file:/var/log/app/error.log       40 lines   last 40 lines

Redaction   14 rules (12 default, 2 from /etc/aken/rules.json); kept: 10.0.0.5; off: email
  ip          32 values in 1204 lines
  secret       1 value  in    1 line
  jwt          0
  key          0
  token        0
  email        off
  1210 of 2814 lines changed

Flags   3 strings to inspect: unit:nginx.service lines 88, 401, 1733; 2 hex ids or hashes not listed. Press f to view the lines to inspect.

Upload   2814 lines, 412.3 KiB, 1 chunk, TTL 4h, relay https://relay.aken.dev
Local    /var/lib/aken/runs/ (kept 30 days; includes the placeholder mapping)

[s] send   [v] view everything   [f] view flagged lines   [a] abort
>
```

Choose **view everything** to inspect the artifact and **view flagged lines**
to inspect possible missed values other than hex IDs or UUIDs. Choose **send**
only when you are ready to share the reviewed content. Choose **abort** to stop
without uploading.

On **send**, the collector prints progress to stdout before the final block:

```text
Creating the session on <relay>...
Uploading chunk <i> of <n> (<size so far> of <total>)...
Uploading the manifest...
```

There is one upload progress line per chunk; sizes use KiB or MiB.
A successful run ends with this layout; the token below is a placeholder:

```text
Uploaded 1 chunk (412.3 KiB). The artifact expires at 2026-09-12T18:04:11Z.
Local copy: /var/lib/aken/runs/20260912T140411Z-1a2b3c4d

Session token. Paste it into `aken-mcp join` on your machine, not into the agent chat:

  akn1_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

When the relay is not the default, join with: aken-mcp join --relay <URL>
```

On your machine, [join the session](mcp.md#join-a-session) at the terminal.

## The review screen

| Section | What to check |
|---|---|
| **Sources** | Selected sources, line counts, journal windows, and file tail notes |
| **Redaction** | Active rules, kept values, disabled categories, distinct replaced values, and changed lines |
| **Flags** | High-entropy strings to inspect, with source and line numbers; a separate count of hex IDs or hashes |
| **Upload** | Lines, size, chunks, lifetime, and destination relay |
| **Local** | Local copy directory and retention, including the placeholder mapping |

A line can contain replacements from more than one category. Category line
counts therefore need not add up to the total changed-line count.

With no flags, the line is `Flags   none`. Otherwise, it starts with
`Flags   <n> strings to inspect` (`string` for one). When there are strings to
inspect, it adds `: <source> lines <line numbers>`, listing up to 20 line
numbers per source and appending ` and <k> more` for additional lines.
When there are hex IDs or hashes, it appends `; <m> hex ids or hashes not listed`
(`id` for one). The line ends with `. Press f to view the lines to inspect.`
when there are strings to inspect, or just `.` otherwise.

Here, "hex ids or hashes" means flagged strings that are entirely hexadecimal
digits of one case, 16 to 64 characters long, or UUIDs with `8-4-4-4-12`
hexadecimal groups. They are counted separately because their shape resembles
IDs or hashes; their lines are not listed for inspection. The manifest's
`redaction.flags` counts only distinct strings to inspect. Press `f` to show
only lines with strings to inspect, or `v` to view everything.

The viewer prints one screen at a time (the terminal height, or 40 lines when
it cannot be read) as `<source>:<line> | <text>`. Flagged
lines use `!` instead of `|`. At `-- more: Enter, q to stop --`, press Enter
for more lines or `q` to stop viewing.

The gutter (`123 |`, `123 !`) is display only and is not part of the upload.
The viewer shows control characters, invalid bytes and bidirectional-text controls as escapes such as \x1b or \u{202e}; the upload keeps the original bytes.
Redaction can miss sensitive data; see [Redaction](redaction.md).

Interactive input requires stdin to be a terminal. A real run without one
exits 1 with `aken: the review screen needs a terminal`.

## Dry run

Collect, redact, and review without uploading:

```sh
sudo -u aken aken collect --dry-run --unit nginx --since 1h
```

The first line is `aken collect --dry-run: nothing will be uploaded`. The
Upload line says `would upload`, and the choices are:

```text
[v] view everything   [f] view flagged lines   [q] quit
```

Dry runs write no local copies and prune none. Without a terminal, a dry run
prints the screen and exits 0.

## Limits

| Flag or limit | Value |
|---|---|
| Artifact size | At most 128 MiB of redacted text |
| `--ttl D` | artifact lifetime on the relay (default 4h, maximum 24h) |
| Empty input | Zero lines is a failure |

Narrow `--since` for journal sources or use `--tail N` for large files.
The relay enforces its caps too.

## Local copies

The collector creates the following directory before upload starts, with
mode `0700`:

```text
<state-dir>/runs/<YYYYMMDDTHHMMSSZ>-<first 8 hex of session id>/
```

Each file is written once, then set to mode `0400`.

| File | Contents |
|---|---|
| `artifact.txt` | Exactly the plaintext bytes that were encrypted and uploaded |
| `manifest.json` | The plaintext manifest, indented |
| `mapping.json` | Placeholder to original value, such as `{"<ip#1>": "203.0.113.5"}` |
| `run.json` | `session_id`, `relay`, `created_at`, `expires_at`, `chunk_count`, and `argv` |

`mapping.json` holds the original values, including sensitive values removed
from the artifact. Protect the directory as you protect the source logs.
The collector does not write the token into any of these files.

| Flag | Meaning |
|---|---|
| `--state-dir DIR` | where local copies go (default /var/lib/aken when writable, else $XDG_STATE_HOME/aken or ~/.local/state/aken) |
| `--retention D` | delete local copies older than this before the run (default 720h; 0 keeps everything) |

Before a real run, the collector removes every directory under `runs/` whose
name starts with a timestamp older than `--retention`. The default is 30 days.
Relay expiry does not remove the local copy. Dry runs do not prune it.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | failure or refusal |
| `2` | usage |
| `3` | aborted at the review screen (collector only) |

No source flag is a usage error. Running as root, reading zero lines, and
exceeding the artifact size limit are failures or refusals.

## Relay

`--relay URL` selects the relay base URL. The default is
`https://relay.aken.dev`. Use HTTPS except for a development relay on loopback,
where plain HTTP is allowed. For the default development relay address:

```sh
sudo -u aken aken collect --unit nginx --relay http://127.0.0.1:7788
```

Use the same `--relay URL` with `aken-mcp join` on your machine. A loopback
address refers to the machine running each command.
