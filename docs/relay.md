# Run your own relay

## What it does

`aken-relay` serves relay API v0 for collectors and local MCPs. The relay sees
session envelopes, addresses, sizes, and ciphertext. It never receives content
or session tokens. It adds per-IP limits, a capacity guard for directory and R2
stores, JSON request logs, expiry sweeps, and `/healthz`.

## Get the binary

Download `aken-relay_<os>_<arch>` from the
[releases page](https://github.com/akenhq/aken/releases). Follow
[Install and verify](install.md#download) to verify it, then name it `aken-relay`
and put it on your PATH. You can also build from the repository root:

```sh
make build
./bin/aken-relay version
```

## Choose storage

For memory storage, run:

```sh
aken-relay serve --store memory
```

The default listener is `127.0.0.1:7788`. Memory sessions are lost on exit.
For directory storage, choose a private data directory:

```sh
aken-relay serve --store dir --data-dir ./relay-data
```

The directory store keeps session metadata and ciphertext on disk. It creates
directories with mode `0700` and files with mode `0600`. Blob sessions survive
restarts. Live session joins and queues stay in memory with every store;
a restart ends those sessions.

For R2, set `AKEN_R2_ACCOUNT_ID`, `AKEN_R2_BUCKET`, `AKEN_R2_ACCESS_KEY_ID`, and
`AKEN_R2_SECRET_ACCESS_KEY` to your account, bucket, and access credentials, then run:

```sh
aken-relay serve --store r2
```

Sweeps run at startup and every ten minutes. The directory and R2 capacity
guard uses the last successful sweep's live count. Memory storage only sweeps
expired sessions. Expired sessions are also rejected on access.

## Flags

Run `aken-relay serve --help` for usage. All limits must be positive.

| Flag | Default | Meaning |
|---|---|---|
| `--listen ADDR` | `127.0.0.1:7788` | HTTP listen address |
| `--store memory\|dir\|r2` | `memory` | Storage backend |
| `--data-dir PATH` | None | Required with `dir`; rejected with other stores |
| `--behind-cloudflare` | `false` | Trust `CF-Connecting-IP` for client addresses |
| `--requests-per-minute N` | `600` | Request rate per IP, with a burst of 60 |
| `--creates-per-hour N` | `10` | Session creation rate and burst per IP |
| `--max-live-sessions N` | `200` | Capacity from the last directory or R2 sweep |
| `--allowance-mode off\|observe\|enforce` | `off` | Anonymous allowance mode |
| `--allowance-servers N` | `2` | Distinct server addresses per developer address |
| `--allowance-window DURATION` | `24h` | Rolling allowance window |

## Anonymous allowance

A server address is the address that created a session. A developer address
is a different address using that session. IPv4 addresses count individually;
IPv6 addresses share a key within their `/64` prefix. Unknown addresses and
requests from the creator's address do not count.

`off` records no allowance state. `observe` allows requests and logs each extra
server once while its pair remains in the window. `enforce` returns HTTP 429
`server_limit` when a developer tries to use another server beyond the cap.
The response includes `Retry-After` in seconds. Existing pairs can keep working;
each use renews their window. Failed first requests do not add pairs.

The allowance table stays in memory and resets on restart. For enforcement, run:

```sh
aken-relay serve --allowance-mode enforce --allowance-servers 2 --allowance-window 24h
```

## TLS and client addresses

For remote clients, put the listener behind a TLS reverse proxy or Cloudflare
tunnel. Clients permit plain HTTP only on loopback. Without
`--behind-cloudflare`, limits use the connection's peer address.

With `--behind-cloudflare`, the relay trusts `CF-Connecting-IP`. Only the
trusted proxy or tunnel should be able to reach that listener. The relay does
not trust `X-Forwarded-For`. TLS terminates outside the relay.

## Health and clients

`GET /healthz` returns `200` with body `ok` and bypasses rate limits:

```sh
curl http://127.0.0.1:7788/healthz
```

Use the same relay URL for the collector and MCP. For a local relay:

```sh
sudo aken serve --relay http://127.0.0.1:7788
aken-mcp join --relay http://127.0.0.1:7788
```

You can also pass `--relay URL` to `aken collect`. A loopback address refers to
the machine running each command. For separate machines, replace the local URL
with your relay's HTTPS URL. The default without `--relay` is `https://relay.aken.dev`.

## Delete an R2 session

With the four R2 environment variables set, replace `SESSION_ID` with the
32-character lowercase hexadecimal session ID:

```sh
aken-relay admin delete-session SESSION_ID
```

This command works only with R2. It succeeds even if the session is already gone.
