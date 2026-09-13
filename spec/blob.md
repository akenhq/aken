# One-shot artifact

Normative, phase 1. Blob format version 1.

## Purpose and layout

An artifact contains one encrypted manifest and between 1 and 128 encrypted
chunks. The relay stores opaque ciphertext. Readers must authenticate the
manifest and chunks before using their plaintext.

Plaintext chunks must contain 1048576 bytes, except the last chunk, which must
contain between 1 and 1048576 bytes. Chunk indices start at zero. The chunk count
must equal `ceil(total_bytes / 1048576)`. Empty artifacts are invalid. Total
plaintext must not exceed 134217728 bytes (128 MiB).

The 1 MiB chunk size replaces the draft's 65536 bytes to reduce requests per
artifact; ranged reads were dropped for v0 and can be added without a format change.

## Keys

Senders must derive separate 32-byte keys using HKDF-SHA256, with the content root
from [token.md](token.md) as the input secret and the UTF-8 bytes of `aken/blob/v1`
as the salt. The labels are UTF-8 strings without a terminating zero byte.

| Key | HKDF info | Output bytes |
|---|---|---|
| Manifest | `manifest-key` | 32 |
| Chunk | `chunk-key` | 32 |

## Encryption

Chunks must use AES-256-GCM under the chunk key. The ciphertext consists of the
encrypted plaintext followed by a 16-byte authentication tag. The nonce is not
stored with the ciphertext.

The chunk nonce has exactly 12 bytes:

| Byte offsets | Value |
|---|---|
| 0..3 | Zero |
| 4..11 | Chunk index, unsigned 64-bit integer, big-endian |

The associated data has exactly 13 bytes:

| Byte offsets | Value |
|---|---|
| 0 | Blob version, `0x01` |
| 1..8 | Chunk index, unsigned 64-bit integer, big-endian |
| 9..12 | Chunk count, unsigned 32-bit integer, big-endian |

The manifest must use AES-256-GCM under the manifest key, a 12-byte zero nonce,
and the same associated-data layout with index zero and the artifact's chunk
count. Manifest plaintext is UTF-8 JSON. Readers must use the exact received
ciphertext for authentication; JSON serialization does not affect decryption.

`SealChunk` must reject counts outside 1..128, indices at or above the count,
empty plaintext, plaintext above 1048576 bytes, and short non-last chunks with
`protocol: blob size out of range`. `OpenChunk` must reject the same invalid
indices and counts and ciphertext lengths outside 17..1048592 with that error.
Any chunk decryption failure must return `protocol: blob authentication failed`.

`SealManifest` must reject empty plaintext and plaintext longer than 1048560
bytes with `protocol: blob size out of range`. Any manifest decryption failure
must return `protocol: blob authentication failed`. Authentication errors must
not include plaintext, ciphertext, or the underlying cryptographic error.

## Plaintext

Sources must be concatenated in manifest order. Every line must end with `\n`.
A source starts at its byte `offset` and spans `bytes` bytes. Source ranges must
be contiguous and cover `[0, total_bytes)`. Chunk boundaries may split lines or
sources. Line numbers within a source start at one.

## Manifest JSON

The manifest must contain these fields:

| Field | JSON type | Meaning |
|---|---|---|
| `version` | integer | Must be 1 |
| `created_at` | string | Artifact creation time, RFC 3339 |
| `collector` | string | Collector name and version |
| `chunk_size` | integer | Must be 1048576 |
| `chunk_count` | integer | Number of chunks, 1..128 |
| `total_bytes` | integer | Total plaintext bytes |
| `chunks_sha256` | array of strings | SHA-256 of each ciphertext chunk, in index order; 64 lowercase hexadecimal characters per hash |
| `sources` | array of objects | At least one source, in plaintext order |
| `redaction` | object | Redaction counts for the artifact |

Each source has these fields:

| Field | JSON type | Meaning |
|---|---|---|
| `name` | string | Source name, such as `unit:nginx.service`, `container:api`, or `file:/var/log/app.log` |
| `kind` | string | `unit`, `container`, or `file` |
| `target` | string | Unit name, container name, or absolute file path |
| `since` | string, optional | RFC 3339 UTC start time for a journald source |
| `until` | string, optional | RFC 3339 UTC end time for a journald source |
| `offset` | integer | First byte's offset in the artifact plaintext |
| `bytes` | integer | Source byte length |
| `lines` | integer | Source line count |
| `lines_redacted` | integer | Source lines with at least one replacement |
| `note` | string, optional | Selection note, such as `last 500 lines` |

The `redaction` object has these fields:

| Field | JSON type | Meaning |
|---|---|---|
| `lines_redacted` | integer | Artifact lines with at least one replacement |
| `by_category` | object | Category name to a count object |
| `flags` | integer | Distinct high-entropy strings that matched no rule and are not hex ids or UUIDs |
| `rules` | integer | Active rules in the run |

A category count object has integer fields `values` (distinct original values)
and `lines` (lines with a replacement in this category). Category names must be
`secret`, `token`, `jwt`, `key`, `ip`, `email`, `phone`, `name`, or `address`.
An empty `by_category` object is valid. Placeholders match
`<(secret|token|jwt|key|ip|email|phone|name|address)#[1-9][0-9]*>`.

Manifest validation must check the version, chunk size, hash count, chunk count
against total bytes, lowercase hash encoding, presence of sources, contiguous
source coverage, source kinds, and category names. Errors must not include
chunk hashes or source contents. Readers must verify each ciphertext chunk's
SHA-256 against the manifest and authenticate it before using the plaintext.

## Size caps

| Item | Maximum bytes |
|---|---|
| Artifact plaintext | 134217728 |
| Chunk plaintext | 1048576 |
| Chunk ciphertext | 1048592 |
| Manifest plaintext | 1048560 |
| Manifest ciphertext | 1048576 |

## Uploads and nonce reuse

Senders must use a fresh token for every artifact: one token, one artifact.
They must not encrypt different plaintext under the same key and nonce. Separate
manifest and chunk keys allow both to use index zero.

Senders must create a session with the final chunk count, upload every chunk,
and upload the manifest last. A stored manifest means every chunk is stored and
the upload is complete. The relay must not replace a stored chunk or manifest.

A retry must resend identical ciphertext. A `409 already_exists` means the
object is stored; the client treats this as success for a chunk upload. The
client must still return manifest conflicts to the caller. Transport errors and
5xx responses allow up to three attempts, with delays of 500 ms and then 2 s.
The client must not retry 4xx responses.

## TTL

The TTL defaults are 4 hours (14400 seconds), with a cap of 24 hours (86400
seconds). A zero or absent requested TTL selects the default. The relay must
reject requests above the cap and treat an expired session as missing. The
expiry is set when the session is created; uploads and reads must not extend it.

## Test vectors

[vectors/blob-v1.json](vectors/blob-v1.json) fixes key derivation, chunk encryption,
and manifest encryption for three content roots and plaintext lengths: 1,
1048576, and 1049576 bytes. Each plaintext byte at index `i` is `(7*i + 3) mod 251`.
These binary encryption inputs exercise chunk boundaries, not log-line layout.

Small chunks contain full ciphertext hex. Full-size chunks contain the SHA-256
and first 32 ciphertext bytes. Each `manifest_json` string is exactly the compact
JSON encrypted by its vector. [protocol/blob_test.go](../protocol/blob_test.go)
regenerates the plaintext, checks the keys and ciphertext values, and decrypts
chunks and manifests.
