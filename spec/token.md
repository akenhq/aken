# Token v1

Normative.

## Purpose

The collector generates one token on the server and shows it once to the human.
The human carries it to the local MCP out of band. This token bootstraps the
session identifier, relay credential and content keys.

## Format

A token must contain 32 random bytes from the operating system CSPRNG. Its
copy-and-paste form is the exact prefix `akn1_` followed by those bytes encoded
with RFC 4648 base32, lowercase, without padding. The body alphabet is `a-z` and
`2-7`. The body has 52 characters. The complete token has 57 characters.

For example, a secret of 32 zero bytes encodes as:

```text
akn1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
```

This example has no entropy and must not be used for a real session.

## Parsing rules

1. Remove leading and trailing ASCII whitespace: space, tab, carriage return and
   line feed. Nothing else is normalised.
2. Require the exact prefix `akn1_`. The comparison is case-sensitive.
3. Require a body of exactly 52 characters from the alphabet `a-z` and `2-7`.
   Uppercase anywhere is rejected.
4. Decode the body as RFC 4648 base32 without padding. Require exactly 32 bytes.
5. Re-encode the 32 bytes as described under Format and require equality with the
   trimmed input. This rejects non-canonical trailing bits.

Every failure is one and the same error, and errors must never echo any part of
the input. The Go implementation returns `ErrInvalidToken` unwrapped and
unadorned for every parsing failure.

## Derivation

Use HKDF-SHA256. The input secret is the 32 token bytes. The salt is the bytes of
`aken/token/v1`. Derive each output separately with the following `info` label:

| Output | `info` label | Length | Purpose |
|---|---|---|---|
| `SessionID` | `session-id` | 16 bytes | Public identifier of the session at the relay |
| `RelayCredential` | `relay-credential` | 32 bytes | Bearer credential both ends present to the relay; the relay stores only its SHA-256 |
| `ContentRoot` | `content-root` | 32 bytes | Root of all content keys; never leaves the collector or the MCP |

The distinct `info` labels make the three outputs independent. The Go
implementation uses `crypto/hkdf.Key` with `crypto/sha256.New`.

## Where values may go

- `session_id` is public. It appears in relay URLs and terminals as 32 lowercase
  hexadecimal characters.
- `relay_credential` goes to the relay in the `Authorization` header. The relay
  must store only `SHA-256(relay_credential)` and compare in constant time.
- The content root must never leave the collector or the MCP.
- The token itself must never appear in logs, in the local audit copy, or on the
  relay. `Token.String()` returns `akn1_[redacted]`; `Token.GoString()` returns
  `protocol.Token{[redacted]}`. Call `Encode()` explicitly to obtain the token.

## Security notes

A generated token has 256 bits of entropy. Brute force against the relay is not
a concern, so the relay needs no lockout logic for token guessing. The token is
single-use and short-lived. The collector must invalidate it when the session
ends.

A token pasted into an agent chat has been in a transcript. Later phases must
account for that exposure.

## Versioning

A new prefix means a new token format. A v1 parser must reject every prefix
other than `akn1_`. A short-code PAKE variant would get its own prefix.

## Test vectors

See [vectors/token-v1.json](vectors/token-v1.json) for valid encodings, derived
values and invalid inputs. The protocol tests check these values directly.
