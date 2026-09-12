# Persistent session envelope

Draft: to be implemented in phase 2.

## Purpose

The relay sees a plaintext envelope and ciphertext for each job or result. The
envelope is all the relay enforces policy on. Job names, parameters and result
contents remain encrypted.

## Fields

| Field | Type | Meaning |
|---|---|---|
| `version` | uint8 | Protocol version, value 1 |
| `session_id` | 16 bytes | Public session identifier |
| `seq` | uint64 | Per-direction sequence; starts at 1 and increments by one |
| `class` | uint8 | 1 read, 2 write, 3 exec |
| `payload_len` | uint32 | Ciphertext length in bytes |

## Canonical encoding

Concatenate the fields in the order above. Encode integers big-endian. The
result is exactly 31 bytes and forms the AEAD associated data.

On the wire, the envelope is JSON with those five fields plus the base64
ciphertext. The receiver rebuilds the canonical bytes from the JSON fields and
authenticates the ciphertext against them. Any change the relay makes to a
field fails authentication.

## AEAD

Use AES-256-GCM from the Go standard library. Derive one 32-byte key per direction
from the session content root with HKDF-SHA256, salt `aken/session/v1`:

| Direction | `info` label |
|---|---|
| MCP to collector | `job-key` |
| Collector to MCP | `result-key` |

The nonce is four zero bytes followed by `seq` as a big-endian uint64. Sequence
numbers must be unique per key, so nonces never repeat.

## Receiver rules

The receiver must reject a message if authentication fails or if `seq` is not
exactly the next expected value. Replays, reordering and gaps are rejected.
The collector must reject a job if its decrypted catalog class differs from
the envelope class. A rejected job must never be shown for approval.

## Persistent sessions

For long sessions, an X25519 exchange through the relay replaces the
token-derived content root. A MAC under a token-derived key authenticates the
exchange. The exchange details are phase 2 work.

## Open items

- Define the authenticated key exchange transcript and its derivation labels.
- Define JSON representations for binary fields and integer bounds.
- Define sequence exhaustion and session recovery rules without reusing nonces.
- Add envelope and authentication test vectors before implementation.
