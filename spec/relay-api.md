# Relay API

Normative, phase 2. HTTP API v0, protocol version 1.

## Principles

The relay stores opaque artifact ciphertext and session metadata, and queues
opaque job and result ciphertext. It is
untrusted for content integrity: readers must authenticate artifacts as defined
in [blob.md](blob.md) and envelopes as defined in [envelope.md](envelope.md). The relay must enforce the caps below. It must not provide
listing or enumeration endpoints.

## Version negotiation

Every request to a session endpoint must carry `Aken-Protocol: 1`. A missing or
unsupported version must receive HTTP 426 and this JSON structure:

```json
{"error":"unsupported_version","message":"protocol version not supported","protocol_versions":[1]}
```

`GET /v0/info` must accept requests without this header and return an `Info`
object. Its `protocol_versions` array lists supported integers, including 1;
its `caps` object contains the fields in the caps table.

## Authentication

Session requests must carry `Authorization: Bearer <credential>`, where
`<credential>` is the unpadded base64url encoding of exactly 32 credential bytes.
The prefix must be exactly `Bearer `.

The relay must store SHA-256 of the credential at session creation and compare
credential hashes in constant time. It must not store the raw credential.
Unknown sessions, expired sessions, missing or malformed authorization, and
wrong credentials must produce indistinguishable HTTP 404 `not_found` responses.
Errors must not expose tokens, credentials, or content.

Session handlers must check the protocol version first, the session ID syntax
second, and the credential third. Creation requires a well-formed credential;
an existing unexpired session returns 409 `already_exists` and is not replaced.

## Endpoints

The session base path is `/v0/sessions/{sid}`. A session ID must consist of exactly
32 lowercase hexadecimal characters. A chunk index is a zero-based unsigned
64-bit decimal integer. In the table, `...` means the session base path.

| Method and path | Request body | Success response | Endpoint errors |
|---|---|---|---|
| `GET /v0/info` | None | 200 `Info` JSON | |
| `PUT /v0/sessions/{sid}` | `CreateSessionRequest` JSON | 201 `SessionInfo` JSON | 400 `bad_request` for invalid JSON or chunk count, 400 `ttl_too_long`, 409 `already_exists` |
| `GET /v0/sessions/{sid}` | None | 200 `SessionInfo` JSON | 404 `not_found` |
| `DELETE /v0/sessions/{sid}` | None | 204, no body | 404 `not_found` |
| `PUT .../blob/chunks/{index}` | Ciphertext | 201, no body | 400 `bad_request` for invalid index or size, 409 `already_exists`, 413 `too_large` |
| `GET .../blob/chunks/{index}` | None | 200 ciphertext | 404 `not_found` |
| `PUT .../blob/manifest` | Ciphertext | 201, no body | 409 `chunks_missing`, 409 `already_exists`, 413 `too_large` |
| `GET .../blob/manifest` | None | 200 ciphertext | 404 `not_found` |
| `POST .../join` | `JoinRequest` JSON | 201 `JoinInfo` JSON | 400 `bad_request`, 409 `already_exists`, 409 `wrong_mode` |
| `GET .../join?wait=N` | None | 200 `JoinInfo` JSON; 204, no body, on timeout | 400 `bad_request`, 409 `wrong_mode` |
| `POST .../jobs` | `Envelope` JSON | 202, no body | 400 `bad_request`, 403 `class_not_allowed`, 409 `not_joined`, 409 `bad_sequence`, 409 `wrong_mode`, 413 `too_large`, 429 `queue_full` |
| `GET .../jobs?wait=N` | None | 200 `Messages` JSON; 204, no body, on timeout | 400 `bad_request`, 409 `wrong_mode` |
| `POST .../results` | `Envelope` JSON | 202, no body | Same as POST jobs, with the result cap |
| `GET .../results?wait=N` | None | 200 `Messages` JSON; 204, no body, on timeout | Same as GET jobs |

JSON requests and responses use `Content-Type: application/json`. Ciphertext
requests and responses use `Content-Type: application/octet-stream`.

`CreateSessionRequest` has these fields:

| Field | JSON type | Meaning |
|---|---|---|
| `ttl_seconds` | integer | Requested lifetime in seconds; zero or absent selects 14400 for blob mode or 28800 for session mode |
| `chunk_count` | integer | Blob mode: final chunk count, 1..128; session mode: must be absent or zero |
| `mode` | string, optional | `blob` (default) or `session` |
| `collector_key` | string | Required in session mode: collector X25519 public key, 32 bytes, unpadded base64url |
| `collector_mac` | string | Required in session mode: collector MAC, same encoding and length |

TTL above 86400 seconds must return 400 `ttl_too_long`. Negative TTL and invalid
chunk counts, modes or session keys must return 400 `bad_request`. The `relay` package caps creation JSON at
2097152 bytes and returns 413 `too_large` above that size.

`SessionInfo` has these fields:

| Field | JSON type | Meaning |
|---|---|---|
| `session_id` | string | 32 lowercase hexadecimal characters |
| `expires_at` | string | Absolute expiry, RFC 3339, UTC |
| `chunk_count` | integer | Final chunk count set at creation |
| `chunks_stored` | integer | Number of distinct chunks stored |
| `manifest_stored` | boolean | Whether the upload is complete |
| `mode` | string | `blob` or `session` |
| `joined` | boolean | Whether a session-mode session has joined; false for blob mode |
| `collector_key` | string, optional | Collector public key from creation, unpadded base64url; omitted or empty for blob mode |
| `collector_mac` | string, optional | Collector MAC from creation, unpadded base64url; omitted or empty for blob mode |

Blob counts and `manifest_stored` must be zero and false for session mode. Key
and MAC encodings must be canonical unpadded base64url of exactly 32 bytes.

## Caps

The relay must enforce and advertise these values in `Info.caps`:

| JSON field | Value | Unit |
|---|---|---|
| `manifest_bytes` | 1048576 | Ciphertext bytes (1 MiB) |
| `chunk_bytes` | 1048592 | Ciphertext bytes |
| `chunk_count` | 128 | Chunks |
| `ttl_default_seconds` | 14400 | Seconds (4 hours) |
| `ttl_max_seconds` | 86400 | Seconds (24 hours) |
| `job_bytes` | 65536 | Ciphertext bytes per job |
| `result_bytes` | 1048576 | Ciphertext bytes per result |
| `queue_length` | 64 | Pending messages per direction |
| `session_ttl_default_seconds` | 28800 | Seconds (8 hours) |
| `wait_max_seconds` | 30 | Seconds per long poll |

After version and authentication checks, a chunk body above `chunk_bytes` must
return 413 `too_large` before index, size, or duplicate checks. Every non-last
chunk must be exactly 1048592 bytes. The last chunk must be between 17 and
1048592 bytes. Invalid PUT indices and wrong sizes must return 400 `bad_request`.
GET for an invalid index or unstored chunk must return 404 `not_found`.

## Completion and expiry

Clients must upload all chunks before the manifest. The relay must reject a
manifest with 409 `chunks_missing` until every chunk is stored. A stored manifest
therefore means the upload is complete. GET of an unstored manifest must return
404 `not_found`.

The relay must not replace stored chunks or manifests. Duplicate uploads must
return 409 `already_exists`. Retries must resend identical ciphertext; clients
must not reuse a token to encrypt a different artifact.

The relay must set expiry at session creation and must not extend it on reads
or uploads. At or after expiry, every access must treat the session as missing
and delete its state. `aken-relay` also sweeps expired sessions at startup and every ten minutes.
DELETE must remove the session, its credential hash, all chunks, and its manifest.

## Errors and client behavior

Errors use an `ErrorResponse` JSON object:

| Field | JSON type | Meaning |
|---|---|---|
| `error` | string | Exact error code below |
| `message` | string, optional | Human-readable explanation without sensitive input |

HTTP 426 additionally includes `protocol_versions: [1]`. The other status codes
and error strings are fixed:

| HTTP status | Error code | Meaning |
|---|---|---|
| 400 | `bad_request` | Invalid request, index, or chunk size |
| 400 | `ttl_too_long` | Requested TTL exceeds the cap |
| 403 | `class_not_allowed` | Envelope class is not 1 |
| 404 | `not_found` | Missing object, invalid session ID, or credential failure |
| 405 | `method_not_allowed` | Known path with an unsupported method |
| 409 | `already_exists` | Session or object already stored |
| 409 | `chunks_missing` | Manifest uploaded before all chunks |
| 409 | `not_joined` | Session has not joined |
| 409 | `bad_sequence` | Sequence differs from the next expected value and is not an identical replay of the last accepted envelope |
| 409 | `wrong_mode` | Endpoint does not apply to the session mode |
| 413 | `too_large` | Body or ciphertext exceeds its cap |
| 429 | `server_limit` | Client address exceeded the server allowance; retry after `Retry-After` seconds |
| 429 | `queue_full` | Direction already has 64 queued messages |
| 426 | `unsupported_version` | Missing or unsupported protocol version |

Unknown paths must return 404 `not_found`. Known paths accept only the methods
listed in the endpoint table, including no implicit HEAD support.

Clients must use HTTPS except for loopback relays. The Go client
accepts HTTP only for `localhost`, IP addresses in `127.0.0.0/8`, or `::1`. A base
URL must not contain userinfo, a query, a fragment, or a path other than empty
or `/`.

The client must make at most three attempts on transport errors or 5xx responses,
waiting 500 ms and then 2 s while respecting cancellation. It must not retry
4xx responses. `PutChunk` maps 409 `already_exists` to success. Other non-2xx
responses become `RelayError`; unparseable error bodies use `http_<status>` as
the code. Single-object JSON responses are limited to 2097152 bytes; blob ciphertext
responses are limited to 1048592 bytes. Queue responses may contain 64 envelopes
and must allow their base64 payloads plus JSON overhead. The Go client bounds
each queue response at `64 * (4 * ceil(ciphertext_cap / 3) + 1024)` bytes.

## Hosted relays

Per-IP rate limits and abuse controls are hosted-relay policy outside this spec.
Hosted relays return HTTP 429 when those controls reject a request. `aken-relay`
provides per-IP limits and memory, directory, or R2 storage. TLS terminates
at a reverse proxy or tunnel.

A relay may cap how many distinct server addresses one client address pairs
with in a rolling window. The server address is the address that created the
session. Beyond the cap, the relay returns 429 `server_limit` with `Retry-After`.

## Join and queues

Blob endpoints on session-mode sessions must return 409 `wrong_mode`. Join,
jobs and results endpoints on blob-mode sessions must return 409 `wrong_mode`.
These checks follow version and credential checks.

`JoinRequest` has these fields:

| Field | Type | Meaning |
|---|---|---|
| `mcp_key` | string | MCP X25519 public key, canonical unpadded base64url of 32 bytes |
| `mcp_mac` | string | MCP MAC, same encoding and length |
| `via` | string | Must be `cli` or `chat` |

Invalid join JSON, keys, MACs or `via` must return 400 `bad_request`. The relay
must retain the first join. A second POST must return 409 `already_exists`.
The relay does not know the exchange key and must leave MAC verification to the
endpoints. `JoinInfo` has these fields:

| Field | Type | Meaning |
|---|---|---|
| `collector_key` | string | Key provided at creation |
| `collector_mac` | string | MAC provided at creation |
| `mcp_key` | string | Key provided at join |
| `mcp_mac` | string | MAC provided at join |
| `via` | string | `cli` or `chat` from the join |
| `joined_at` | string | Join time in UTC RFC 3339 |

POST jobs and results must check in this order:

1. Session mode; otherwise 409 `wrong_mode`.
2. Joined state; otherwise 409 `not_joined`.
3. JSON and envelope fields: version 1, matching session ID, class 1..3,
   sequence 1..4294967295, and payload longer than 16 bytes; otherwise
   400 `bad_request`.
4. Class equal to 1; otherwise 403 `class_not_allowed`.
5. Decoded ciphertext at or below the direction's cap; otherwise 413 `too_large`.
6. Sequence equal to the next expected value; otherwise 409 `bad_sequence`.
7. Queue length below 64; otherwise 429 `queue_full`.

The relay may bound the enclosing JSON request body at 2097152 bytes and return
413 `too_large` above that size. Ciphertext caps apply to decoded payload bytes,
not their base64 text. The two directions have separate counters starting at 1.
A POST repeating the last accepted sequence with an identical envelope and
byte-identical ciphertext must return 202 again without enqueueing it. This
exception precedes queue-length checks and applies even after delivery. A
changed payload at that sequence, or any older sequence, must be rejected.

GET join, jobs and results accepts an optional integer `wait` query parameter.
Absent or zero means no wait. Values outside 0..30 or invalid integers must
return 400 `bad_request`. A GET must wait at most that many seconds for a join
or at least one queued message. A join remains readable after GET. A queue GET
must return all pending messages in sequence order and remove them atomically.
Delivery is at most once. Concurrent polls must not receive the same message.
`Messages` has one field, `messages`, an array of envelopes defined in
[envelope.md](envelope.md). An empty poll must return 204 with no body.

Every mutation must wake waiting requests to check the state again. DELETE and
expiry must remove live state and wake waiters, which must receive 404
`not_found`. A poll must honor request cancellation. Expiry must not be extended
by a join, POST, or poll.

The relay must keep the join, queues, counters and last accepted envelopes in
memory. The Store keeps only the credential hash, expiry, mode, collector key
and collector MAC for session-mode sessions. Queues must not touch the Store.
A restart loses live sessions; later requests must return 404 `not_found` even
if the session metadata remains in the Store. Clients must end that session.

The Go client rounds wait durations down to whole seconds and clamps them to
0..30. Its default HTTP timeout remains 60 seconds. A 204 maps to no join or no
messages without an error. Existing transport and 5xx retries also apply to
these methods; a job or result POST retry must resend the same envelope.
A 404 from an older relay's join endpoint must remain a `RelayError` 404 so
callers can report that the relay does not support persistent sessions.

## Compatibility

v0.1 clients send no `mode` and keep working. Omitted mode means `blob`, with
the existing blob caps, four-hour default TTL, endpoints and upload behavior.
Both modes retain the 24-hour TTL cap, protocol header and bearer credential.

## Conformance

Run `go test ./spec/conformance/ -count=1` for the in-process memory and directory stores. Set
`AKEN_RELAY_URL` to run the same suite against another relay. Each test uses a
fresh token and deletes its session afterward.

## Reference implementation

The [`relay` package](../relay/) holds the request handling used by `aken-relay`
and other relays. A hosted relay supplies a `Store` and its own limits through
`Options.Caps`. Run the conformance suite against the relay to check that its
store behaves as this specification requires.
