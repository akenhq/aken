# Persistent sessions and envelopes v1

Normative, phase 2.

## Purpose

The relay sees envelope metadata and ciphertext for each job or result. Job
names, parameters and result contents must remain encrypted. Both endpoints
must authenticate the exchange and every received envelope; relay checks do
not replace endpoint checks.

## Authenticated exchange

The transcript label is the UTF-8 bytes of `aken/session/v1`, without a
terminator. The session ID is the 16 raw bytes derived from the token, not its
hexadecimal text. Public keys, private keys, MACs and derived keys are 32 bytes.
Concatenation below adds no separators or length prefixes.

1. Both endpoints derive the session ID, relay credential and exchange key from
   the token as defined in [token.md](token.md). The exchange key must never
   leave the endpoints.
2. The collector generates a fresh X25519 key pair. It creates a session with
   its public key and the collector MAC through [relay-api.md](relay-api.md).
3. The MCP reads the collector key and MAC from the session metadata. It must
   verify the collector MAC in constant time before accepting that key. It
   generates a fresh X25519 key pair and posts its public key and MCP MAC.
4. The collector obtains the join and must verify the MCP MAC in constant time
   before accepting the MCP key. The MCP must verify the collector MAC in the
   join response as well. A failed MAC must reject the exchange.
5. Each endpoint computes X25519 using its own private key and the peer's
   authenticated public key. It must reject an invalid or low-order peer key.
6. Each endpoint computes the transcript hash and derives the content root and
   direction keys below. The collector key must come first in both endpoints'
   transcript. The persistent-session content root replaces the token-derived
   blob content root for this session.

| Value | Definition |
|---|---|
| Collector MAC | HMAC-SHA256(exchange key, label ∥ `0x01` ∥ session ID ∥ collector public key) |
| MCP MAC | HMAC-SHA256(exchange key, label ∥ `0x02` ∥ session ID ∥ collector public key ∥ MCP public key) |
| Shared secret | X25519(own private key, peer public key) |
| Transcript hash | SHA-256(label ∥ session ID ∥ collector public key ∥ MCP public key) |
| Content root | HKDF-SHA256(shared secret, salt = transcript hash, info = `content-root`, length = 32) |
| Job key | HKDF-SHA256(content root, salt = label, info = `job-key`, length = 32) |
| Result key | HKDF-SHA256(content root, salt = label, info = `result-key`, length = 32) |

The Go implementation returns `ErrExchange` (`protocol: key exchange failed`)
unwrapped for key exchange failures. It must not expose the underlying crypto
error. Fixed private keys in `KeyPairFromPrivate` are for tests and vectors only.
Real sessions must use freshly generated key pairs.

## JSON envelope

| Field | JSON type | Meaning |
|---|---|---|
| `version` | integer | Must be 1 |
| `session_id` | string | Exactly 32 lowercase hexadecimal characters |
| `seq` | integer | Per-direction sequence, 1..4294967295 |
| `class` | integer | 1 read, 2 write, 3 exec |
| `payload` | string | Ciphertext encoded as standard padded base64, as in Go `encoding/json` for `[]byte` |

The payload must contain more than 16 bytes, including the 16-byte GCM tag.
Job ciphertext must be at most 65536 bytes. Result ciphertext must be at most
1048576 bytes. Phase 2 relays permit only class 1, even though envelope v1
represents classes 1 through 3. There is no JSON `payload_len` field; the
canonical length comes from the decoded payload.

## Canonical encoding

The canonical envelope is exactly 30 bytes. It is the AEAD associated data.
Integers must use big-endian encoding. The session ID must be decoded to raw
bytes before encoding.

| Offset | Length | Value |
|---|---|---|
| 0 | 1 | Version |
| 1 | 16 | Session ID |
| 17 | 8 | Sequence |
| 25 | 1 | Class |
| 26 | 4 | Ciphertext length, including the tag |

Implementations must reject fields outside the ranges above before constructing
canonical bytes. The canonical payload length must fit an unsigned 32-bit integer.

## Sealing and opening

Both directions must use AES-256-GCM with their distinct derived keys. The
nonce is four zero bytes followed by the sequence as a big-endian uint64.
The sender must calculate the ciphertext length as plaintext length plus 16
before constructing the associated data.

Each direction starts at sequence 1 and increments by one. A sender must not
reuse a sequence with different plaintext or metadata. Retransmission must use
the identical previously sealed envelope. After using sequence 4294967295
(`MaxSessionSeq`), the sender must end the session. It must not wrap or reset the
counter. Continuing requires a new session and fresh keys.

`SealJob` and `SealResult` must return `ErrEnvelope` for sequence zero, sequence
above the cap, class outside 1..3, empty plaintext, or plaintext longer than the
direction's ciphertext cap minus 16.

A receiver must check in this order:

1. Validate version, session ID syntax and equality to the expected session ID,
   class 1..3, sequence 1..4294967295, and payload length for the direction.
   Failure is `ErrEnvelope`.
2. Require the exact next expected sequence. Failure is `ErrSequence`.
3. Rebuild the canonical bytes and authenticate and decrypt with the direction
   key and nonce. Failure is `ErrSessionAuth`.

| Go error | Exact text |
|---|---|
| `ErrEnvelope` | `protocol: invalid envelope` |
| `ErrSequence` | `protocol: unexpected sequence number` |
| `ErrSessionAuth` | `protocol: session authentication failed` |

These errors must be unwrapped and must not include plaintext, ciphertext or
underlying crypto errors. Replays, reordering and gaps must be rejected. A job
that fails envelope checks must not be shown for approval and gets no result.
The collector must also reject a decrypted job whose catalog class differs
from the envelope class.

## Job and result plaintext

Plaintext must be a JSON object. A job has these fields:

| Field | Type | Meaning |
|---|---|---|
| `id` | string | MCP-chosen ID matching `^[A-Za-z0-9._-]{1,64}$` |
| `name` | string | Catalog name, or `plan` |
| `params` | object | Parameters for the named job; `{}` when none are needed |

Catalog v1 lists `list_dir`, `read_file`, `search`, `tail`, `journal`,
`docker_logs`, `systemctl_status`, `ps`, and `df` in that order. Every entry is
class read (1). A plan's params must contain `jobs`, an array of 1..40 jobs;
none may itself be a plan.

A result has these fields:

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Job ID |
| `status` | string | `ok`, `denied`, `rejected`, or `error` |
| `error` | string, optional | Explanation for a non-`ok` status; must not contain file contents or paths outside scope |
| `lines` | array of strings, optional | Redacted output lines without newlines |
| `next` | string, optional | Cursor for the next page when output was cut |
| `redaction` | object | `lines_redacted` integer, `by_category` map of category counts, and `flags` integer |

Category counts use the `values` and `lines` fields defined in
[blob.md](blob.md). `flags` counts strings to inspect in this result. `ok` means
the job ran; `denied` means the human declined it; `rejected` means the collector
refused it before approval; `error` means an approved job failed. Line text must
be at most 262144 bytes (`MaxResultPlaintext`) per result. The collector must
cut at a line boundary and set `next` when more output remains.

## Test vectors

[vectors/session-v1.json](vectors/session-v1.json) contains two fixed exchanges,
including a zero-secret example token. Each vector pins both private and public
keys, both MACs, the shared secret, transcript hash, content root, direction
keys, plaintexts, canonical envelopes and ciphertexts. Hexadecimal fields encode
raw bytes. These private keys and tokens are public test data and must not be
used in real sessions. [protocol/session_test.go](../protocol/session_test.go)
checks every derived value and seals and opens both directions against the file.
