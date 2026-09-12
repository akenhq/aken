# Threat model

Status: phase 1 implements one-shot mode. Jobs, approvals, and persistent
sessions do not exist yet. The one-shot section describes current behavior;
the remaining design includes controls for later phases, including background
mode. See [README.md](README.md) for what works today.

## What Aken is

Aken has a collector on the server, a local MCP on the developer's machine,
and a hosted relay in between. Only the relay is hosted. The agent asks for
structured observations, and a small process on the server decides what leaves it.

## Assets

- Log and file contents on the server.
- The session token and every key derived from it.
- Job integrity: the collector runs only what a human approved.
- The server itself: Aken provides no way to change it by default.

## Parties

- The human operator owns the terminal on the server and the developer machine.
- The coding agent is untrusted: log content may prompt-inject it.
- The LLM provider sees everything the agent reads.
- The relay operator sees envelopes and ciphertext, nothing else, and must not
  be able to forge, replay, or reorder jobs or results.
- A network attacker can sit between any two parties.
- An attacker with the developer machine holds session keys and can read what
  the session reads until the session ends.

## One-shot mode (phase 1)

One artifact leaves the server: redacted lines encrypted with keys derived
from a fresh session token. The relay sees ciphertext, sizes, the session id,
and the TTL. It does not receive the plaintext or the token.

Anyone with the token can read the artifact until it expires. Keep the token
out of chats, shell history, and tickets. The collector prints it once in the
terminal and does not store it. The local MCP stores it on your machine in
`~/.config/aken/session.json` on Linux or `~/Library/Application Support/aken/session.json`
on macOS, with directory mode `0700` and file mode `0600`. Ending the session deletes the relay artifact and that file.

Before upload starts, the collector creates
`<state-dir>/runs/<YYYYMMDDTHHMMSSZ>-<first 8 hex of session id>/` with mode
`0700`. It writes each file once, then sets its mode to `0400`:

| File | Contents |
|---|---|
| `artifact.txt` | Exactly the plaintext bytes that were encrypted and uploaded |
| `manifest.json` | The plaintext manifest, indented |
| `mapping.json` | Placeholders mapped to original values, including sensitive data |
| `run.json` | `session_id`, `relay`, `created_at`, `expires_at`, `chunk_count`, and `argv` |

Local copies default to `/var/lib/aken` when writable, otherwise
`$XDG_STATE_HOME/aken` or `~/.local/state/aken`. They remain after relay expiry.
Before a real run, `--retention` prunes copies older than its duration; the
default is `720h` (30 days), and `0` keeps everything. Dry runs write and
prune nothing.

The collector runs unprivileged and refuses root. It runs `journalctl` with
a fixed argument list and no shell. It reads files only under `/var/log` and
`--allow` directories through `os.Root`, refusing symlinks that leave them.

The review screen shows exactly the plaintext bytes to be encrypted and sent.
Its line-number gutter is display only. Review can catch values the rules
missed; redaction remains defence in depth. Everything the agent reads goes
to the LLM provider.

## Threats and controls

| Threat | Control | Where enforced |
|---|---|---|
| Sensitive data reaches the LLM provider | Deterministic redaction before encryption, entropy flags, and a terminal confirmation screen provide defence in depth. No LLM takes part in redaction. | Collector |
| The relay reads content | End-to-end encryption; content keys stay on the collector and local MCP. The relay receives only envelopes and ciphertext. | Collector and local MCP |
| The relay forges, replays, reorders, or relabels jobs | Authenticated encryption binds the envelope as associated data. Authentication and the next expected sequence number are checked before approval; results use the same checks. | Collector for jobs; local MCP for results |
| The agent runs a command or changes state | Typed jobs, no shell, read-only by default. Exec requires a local config flag and a session start flag, both set on the server, then human approval for every call. | Collector and the human's terminal |
| Prompt injection through log content | Read-only defaults limit what an injected agent can do. Non-read-only jobs require approval for every call; exec also requires both local opt-ins. | Collector |
| A mislabelled exec job slips past relay policy | The collector rejects a decrypted job whose real class differs from its authenticated envelope class. Relay class policy is a circuit breaker; the collector is the enforcement point. | Collector; relay checks envelope class policy |
| An agent joins a session no human opened | The collector generates a short-lived, single-use token bound to its process. The human carries it to the local MCP. | Collector and local MCP |
| A token leaks | The human enters it outside the agent conversation by default. Chat joining requires explicit opt-in and exposes the token to the transcript. Tokens are short-lived and single-use, and never enter the relay, logs, or local audit copy. | Collector, local MCP, and human operator |
| Symlink or path escapes in file jobs | Configured scope, canonical paths, and `os.Root` prevent escapes, including symlink races. The human approves resolved paths. | Collector |
| Token brute force against the relay | Long random tokens of at least 128 bits, unguessable identifiers, token-derived relay credentials stored as hashes, no listing or enumeration endpoints, and rate limits. | Collector and local MCP derive credentials; relay checks credentials and limits |
| The relay becomes a log store or free file drop | Ephemeral blobs, payload and blob size caps, session lifetime caps, per-IP and per-account rate limits, abuse monitoring, and accounts for persistent sessions. Phase 1 allows 128 MiB per artifact, with a TTL default of 4 h and a cap of 24 h. | Relay |

## What Aken does not protect against

If the agent's own shell can run `ssh` to the server, that access exists without
Aken. You must sandbox the agent to close it; Aken does not close it for you.

Redaction can miss sensitive data. Rules, entropy flags, and the confirmation
screen are defence in depth. Aken does not protect a compromised server or a
compromised developer machine.

Everything the agent reads goes to the LLM provider. Redaction is the only
control on that path.

A collector inside tmux or screen can stay alive after the SSH window closes.
That is background mode with the same limits: L0 only, a hard lifetime limit,
and no escalation without attaching a human-owned terminal.

## Invariants

Every change must preserve these properties.

1. No listener on the server. The collector makes outbound HTTPS requests only.
   It has no listening socket, not even a Unix socket.
2. No shell. Jobs are typed functions in the collector's own code. Exec requires
   a local config flag and a start-time flag, both set on the server, and approval
   for each call. The default install cannot exec even in principle.
3. Redaction happens deterministically on the server, before encryption. No LLM
   takes part in redaction.
4. The bytes the human approves are exactly the bytes sent. No transformation
   happens after approval.
5. The relay cannot read content and is untrusted for integrity. It can drop or
   delay messages; it cannot read, forge, replay, or reorder messages.
6. Approvals happen only in a TTY the human owns. Background mode is L0 only:
   preapproved read-only job types. Escalation requires attaching a terminal.
7. An agent can only join a session a human opened. Tokens are generated on the
   server, short-lived, and single-use.
8. Blobs are ephemeral. The relay is not a log store.
9. Everything that leaves the server is kept in an append-only local copy.
10. The human approves resolved paths and argv arrays, never command strings.

## Residual risks

Redaction can miss sensitive data even with all controls in place. The relay
can drop or delay messages.

The collector is third-party code on a production machine. The design limits
that exposure with a small executor, minimal dependencies, reproducible signed
builds, an unprivileged user, and no listener.

Alert fatigue can turn approvals into theatre. Batching, summaries, and entropy
flags help the human notice what matters.

## Review status

The protocol and the code have not been professionally reviewed. The substitutes
are a public spec with test vectors, a written self-review checklist, and this
statement. You can report findings through [SECURITY.md](SECURITY.md).
