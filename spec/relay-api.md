# Relay API

Draft: v0.

## Principles

The relay is zero-knowledge for content and untrusted for integrity. It sees
envelopes and ciphertext only. Every limit is a cap the relay enforces, not a
suggestion. There are no listing or enumeration endpoints.

## Version negotiation

Every request carries `Aken-Protocol: 1`. `GET /v0/info` returns
`{"protocol_versions":[1],"caps":{...}}`, with the caps below. An unsupported
version receives `426 Upgrade Required` with a JSON body naming the accepted
versions.

## Authentication

Clients send `Authorization: Bearer <relay_credential, base64url>`. The relay
stores only the SHA-256 of the credential, set when the session is created,
and compares in constant time. A wrong credential and a missing session both
return `404`.

## Endpoints

The session base path is `/v0/sessions/{session_id}`.

| Method and path | Purpose |
|---|---|
| `PUT /v0/sessions/{session_id}` | Create a session and set its credential hash |
| `POST /v0/sessions/{session_id}/join` | Join a session |
| `POST /v0/sessions/{session_id}/jobs` | Post an encrypted job and its envelope |
| `GET /v0/sessions/{session_id}/jobs` | Long-poll for jobs |
| `POST /v0/sessions/{session_id}/results` | Post an encrypted result and its envelope |
| `GET /v0/sessions/{session_id}/results` | Long-poll for results |
| `PUT /v0/sessions/{session_id}/blob` | Upload an encrypted manifest and chunks |
| `GET /v0/sessions/{session_id}/blob` | Fetch a chunk index range |
| `DELETE /v0/sessions/{session_id}` | End the session |

## Caps

The relay enforces these caps and reports them in `/v0/info`:

| Cap | Value |
|---|---|
| Job payload size | To be set in phase 1 |
| Result payload size | To be set in phase 1 |
| Blob size | To be set in phase 1 |
| Chunk count | To be set in phase 1 |
| Session TTL | To be set in phase 1; see the proposal in [blob.md](blob.md#ttl) |
| Per-IP rate limits | To be set in phase 1 |
| Per-account rate limits | To be set in phase 1 |

Size violations receive `413`. Rate-limit violations receive `429`.

## Long-poll

Timeouts are in the tens of seconds. The relay must hold nothing past the
session TTL.

## Open items

- Define request and response schemas, including cap field names.
- Define range syntax and manifest retrieval for blob reads.
- Define polling cursors, upload completion and session expiry responses.
- Set exact caps in phase 1.
