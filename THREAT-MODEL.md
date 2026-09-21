# Threat model

Status: phase 2 implements live sessions with catalog read jobs and terminal
approvals, alongside one-shot mode. The mode sections describe current
behavior. The remaining design includes controls for later phases, including
exec and background mode. See [README.md](README.md) for what works today.

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
- Whoever runs a relay sees envelopes and ciphertext only, never content or tokens.
  A relay with the allowance on keeps, in memory for its window, which client
  addresses used sessions created from which server addresses.
  The relay must not be able to forge, replay, or reorder jobs or results.
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

## Live sessions (phase 2)

The collector opens a session and handles typed catalog v1 jobs. The relay
sees envelope versions, session IDs, sequence numbers, classes, ciphertext
sizes, and timing. It also sees the join: public keys, authentication MACs,
`via` (`cli` or `chat`), and the join time. Job names, parameters, and result
contents are encrypted. The relay does not receive the token or content keys.

The token derives an exchange key that authenticates fresh X25519 public
keys with HMAC-SHA256. Each endpoint verifies its peer's MAC. The X25519
shared secret and exchange transcript derive a content root and separate
job and result keys for AES-256-GCM. The content root is not derived from the
token alone. Someone who obtains only the token after the exchange cannot
use it to decrypt recorded session traffic without the private key material
or content root. Protect the token before joining: it authenticates the peer.
See [the envelope specification](spec/envelope.md).

Both endpoints check envelope authentication and the next expected sequence
number. The collector also checks the job's catalog class against the
class in the envelope before approval. Invalid envelopes are dropped without
running a job. Catalog v1 contains only read jobs; commands use fixed argv
arrays, no shell, and `/usr/bin` or `/bin` paths without a PATH lookup.

| Level | Preapproval and review |
|---|---|
| `1` (default) | No job is preapproved. The human approves a job or plan in the server terminal. That approval permits automatic sending after redaction, except that strings to inspect pause the result for send or drop. |
| `0` | Starting the session preapproves every catalog v1 job inside the path scope. Redacted results auto-send, including flagged results. The terminal shows a rolling summary and flag count. |

Only level 1 can grant a path outside the scope during a session, and only
through the approval screen, which names everything the answer grants before
the human gives it. `a` grants each row's directory until the session ends;
`o` grants each row's single name for that job alone, and is offered only when
every row can be served that way. Level 0 keeps the scope its preapproval was
defined by and rejects paths outside it.

Chat join requires the MCP's explicit `--allow-chat-join` opt-in. It puts the
token in the chat transcript and forces the collector to level 1 even when
started at level 0. Both levels require a terminal. Redaction and flags are
defence in depth and can miss sensitive data; level 1 job approval does not
mean the human inspected every output byte.

`via` is reported by the joiner and not authenticated, so the level-1 promotion covers the honest chat join, not a token holder with a modified client.

The collector refuses root and uses the same user and log groups as collect.
File jobs stay under `/var/log`, repeatable `--allow` roots, directories the
human added on the approval screen during the session, and single names
granted for one job. A granted name covers that one name and nothing under
it, and is opened through its parent directory, so `os.Root` still refuses a
link that leaves that parent and no sibling becomes readable. Canonical path
resolution through `os.Root` refuses escapes, including symlinks that leave
an allowed root. Search expands at most 200 files within scope.

A path outside the scope is a request, never a grant. At level 1 the approval
screen marks the row `+` and names both what a session grant would add and
what a one-job grant would add; denying adds nothing. A one-job grant is
revoked when the job or plan finishes, whatever its outcome. A path inside the
scope that resolves outside it is a link escaping the scope and is refused
before approval rather than offered, so a link planted under an allowed root
cannot become an invitation to widen the scope. The scope a session ran under,
and every grant made during it, are recorded in `session.json` and in
`jobs.log`.

The approval screen marks sensitive paths with `!`: `.env` and `.env.*`,
`shadow`, `gshadow`; suffixes `.pem`, `.key`, `.p12`, `.pfx`, `.kdbx`,
`.keystore`; names starting with `id_` or ending with `_rsa`, `_ed25519`,
`_ecdsa`, `_dsa`; and paths containing `.ssh`, `.gnupg`, or `.aws` components.
The marker does not expand the scope or redact the file's contents.

Every result has a local audit copy under
`<state-dir>/sessions/<YYYYMMDDTHHMMSSZ>-<first 8 hex of session id>/`, mode
`0700`. `jobs.log` records events as append-only JSON lines.
`results/<result seq>.txt` contains exactly the redacted lines of each result
message, written once with mode `0400`. `session.json` records session
metadata and is rewritten at join and exit. `mapping.json` holds original
values and is updated after results that add values. Placeholders stay
consistent across the session. These files contain no token; protect the
mapping as sensitive data.

The local MCP stores a version 2 session file at the same path and with the
same permissions as in one-shot mode. A live file includes the token, content
root, and sequence counters. Compromise of that file exposes the session's
content keys; token-only protection does not cover that case.

The default relay permits live sessions without an account in phase 2. The
TTL defaults to 8 hours and is capped at 24 hours. Ctrl-C, relay termination,
or expiry ends the collector session. The local audit copy remains;
`--retention` prunes old `sessions/` copies before a later session, default
`720h` (30 days), with `0` keeping everything.

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
| Symlink or path escapes in file jobs | Configured scope, canonical paths, and `os.Root` prevent escapes, including symlink races. The human approves resolved paths. A link that leaves the scope is refused, never offered as a directory to add. | Collector |
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

The install script is the trust root of the install path; verify it with
`cosign verify-blob` if you do not trust the host that served it. The script
checks the binary against an embedded hash; it does not run cosign itself.
See [Install and verify](docs/install.md#verify-the-script).

The installed `aken` command is a root-owned shell launcher of a dozen lines.
Started as root, it switches to the `aken` user with `setpriv`, with that
user's groups, a clean environment, no capabilities, and `no_new_privs`, and
then runs the collector; started by any other user, it runs the collector as
that user. It only lowers privilege, and the collector keeps refusing root.
As in run-once mode, the shell runs as root for the moment before the switch.

Run-once mode deletes the temporary directory on exit. With root, it stages
state there as `nobody`, then copies it to the invoking user's
`~/.local/state/aken` on exit, resolving the home from the passwd database,
or to `/root/.local/state/aken` without a non-root invoking user. The user's
home is untouched during the run. Other daemons running as the shared
`nobody` identity can read the staged copy. Without root it uses
`${XDG_STATE_HOME:-$HOME/.local/state}/aken`. Nothing changes outside the
temporary directory and the invoking user's own state directory; it creates
no user and changes no group membership. The local copy remains, including
the mapping of placeholders to original sensitive values. It cannot prevent
journal entries from sshd or sudo.

Alert fatigue can turn approvals into theatre. Batching, summaries, and entropy
flags help the human notice what matters.

## Review status

The protocol and the code have not been professionally reviewed. The substitutes
are a public spec with test vectors, a written self-review checklist, and this
statement. You can report findings through [SECURITY.md](SECURITY.md).
