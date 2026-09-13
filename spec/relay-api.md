# Relay API

Normative, phase 1. HTTP API v0, protocol version 1.

## Principles

The relay stores opaque artifact ciphertext and session metadata. It is
untrusted for content integrity: readers must authenticate artifacts as defined
in [blob.md](blob.md). The relay must enforce the caps below. It must not provide
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

JSON requests and responses use `Content-Type: application/json`. Ciphertext
requests and responses use `Content-Type: application/octet-stream`.

`CreateSessionRequest` has these fields:

| Field | JSON type | Meaning |
|---|---|---|
| `ttl_seconds` | integer | Requested lifetime in seconds; zero or absent selects 14400 |
| `chunk_count` | integer | Final chunk count, 1..128 |

TTL above 86400 seconds must return 400 `ttl_too_long`. Negative TTL and invalid
chunk counts must return 400 `bad_request`. The dev relay caps creation JSON at
2097152 bytes and returns 413 `too_large` above that size.

`SessionInfo` has these fields:

| Field | JSON type | Meaning |
|---|---|---|
| `session_id` | string | 32 lowercase hexadecimal characters |
| `expires_at` | string | Absolute expiry, RFC 3339, UTC |
| `chunk_count` | integer | Final chunk count set at creation |
| `chunks_stored` | integer | Number of distinct chunks stored |
| `manifest_stored` | boolean | Whether the upload is complete |

## Caps

The relay must enforce and advertise these values in `Info.caps`:

| JSON field | Value | Unit |
|---|---|---|
| `manifest_bytes` | 1048576 | Ciphertext bytes (1 MiB) |
| `chunk_bytes` | 1048592 | Ciphertext bytes |
| `chunk_count` | 128 | Chunks |
| `ttl_default_seconds` | 14400 | Seconds (4 hours) |
| `ttl_max_seconds` | 86400 | Seconds (24 hours) |

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
and delete its state. The dev relay also sweeps expired sessions every 30 seconds.
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
| 404 | `not_found` | Missing object, invalid session ID, or credential failure |
| 405 | `method_not_allowed` | Known path with an unsupported method |
| 409 | `already_exists` | Session or object already stored |
| 409 | `chunks_missing` | Manifest uploaded before all chunks |
| 413 | `too_large` | Body exceeds its cap |
| 426 | `unsupported_version` | Missing or unsupported protocol version |

Unknown paths must return 404 `not_found`. Known paths accept only the methods
listed in the endpoint table, including no implicit HEAD support.

Clients must use HTTPS except for loopback development relays. The Go client
accepts HTTP only for `localhost`, IP addresses in `127.0.0.0/8`, or `::1`. A base
URL must not contain userinfo, a query, a fragment, or a path other than empty
or `/`.

The client must make at most three attempts on transport errors or 5xx responses,
waiting 500 ms and then 2 s while respecting cancellation. It must not retry
4xx responses. `PutChunk` maps 409 `already_exists` to success. Other non-2xx
responses become `RelayError`; unparseable error bodies use `http_<status>` as
the code. JSON responses are limited to 2097152 bytes; ciphertext responses are
limited to 1048592 bytes.

## Hosted relays

Per-IP rate limits and abuse controls are hosted-relay policy outside this spec.
Hosted relays return HTTP 429 when those controls reject a request. The dev relay
has no accounts, rate limits, persistence, or TLS.

## Phase 2

These paths are reserved without definitions in v0:

| Method and path |
|---|
| `POST .../jobs` |
| `GET .../jobs` |
| `POST .../results` |
| `GET .../results` |
| `POST .../join` |

## Conformance

Run `go test ./spec/conformance/ -count=1` for the in-process dev relay. Set
`AKEN_RELAY_URL` to run the same suite against another relay. Each test uses a
fresh token and deletes its session afterward.

## Reference implementation

The [`relay` package](../relay/) holds the request handling used by the dev relay
and hosted relays. A hosted relay supplies a `Store` and its own limits through
`Options.Caps`. Run the conformance suite against the relay to check that its
store behaves as this specification requires.
