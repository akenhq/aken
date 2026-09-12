# One-shot artifact

Draft: to be implemented in phase 1.

## Purpose

One-shot mode uploads one encrypted artifact. The local MCP reads it by range.

## Layout

An artifact consists of a manifest and fixed-size chunks of 65536 bytes. The
last chunk may be shorter.

## Keys

Derive two separate 32-byte keys from the content root with HKDF-SHA256 and salt
`aken/blob/v1`. Use `manifest-key` as the manifest key's `info` label and
`chunk-key` as the chunk key's `info` label.

## Chunk encryption

Use AES-256-GCM. A chunk nonce is four zero bytes followed by the chunk index as
a big-endian uint64. The associated data is the canonical header:

```text
version (uint8 = 1) || chunk_index (uint64) || chunk_count (uint32)
```

Encode the integers big-endian. The manifest uses the manifest key, an all-zero
nonce, and associated data `version || 0 || chunk_count`, with the same field
widths and encoding as the chunk header.

## Manifest contents

The manifest is encrypted JSON. It contains sources and their line counts, the
redaction summary as counts by category, the chunk count and chunk size, and
the SHA-256 of every ciphertext chunk.

## Ranged fetch

The relay API accepts chunk index ranges. The MCP can fetch the manifest and
only the chunks needed for a request; it never needs the whole artifact.

## TTL

Proposed: a default TTL of 4 hours and a cap of 24 hours, enforced by the relay.
These values are proposals for phase 1.

## Open items

- Define the manifest JSON schema and empty-artifact representation.
- Define chunk index origin, range bounds and the wire layout.
- Define upload completion and retry rules that prevent nonce reuse.
- Set blob size and chunk count caps in phase 1.
- Add manifest and chunk test vectors before implementation.
