# Redaction

## What redaction promises

The collector applies deterministic rules on the server before encryption.
No LLM takes part in redaction. Redaction is defence in depth and can miss
sensitive data.

Use the review screen to catch what the rules missed. Everything the agent
reads goes to the LLM provider, including any sensitive values left in the
artifact.

## Placeholders

A replacement looks like `<ip#3>` or `<secret#1>`. The grammar is:

```text
<(secret|token|jwt|key|ip|email|phone|name|address)#[1-9][0-9]*>
```

Numbers count distinct original values within each category in first-seen
order. The same value gets the same placeholder within a run. Do not use
placeholder numbers to correlate separate runs.

Rules apply per line in file order. A later rule cannot match a span already
replaced. The placeholder mapping stays local on the server.

In a [live session](serve.md), placeholders stay consistent across every
result of that session. The mapping lives in the session's local copy under
`<state-dir>/sessions/` and is updated after each result that adds values.
It contains original sensitive values and stays on the server.

## Categories

| Category | Default | Coverage |
|---|---|---|
| `secret` | On | Recognized service credentials, URL passwords, and sensitive assignments |
| `token` | On | Bearer tokens |
| `jwt` | On | JWT-shaped strings |
| `key` | On | Private key blocks, handled by the engine |
| `ip` | On | IPv4 and IPv6 addresses |
| `email` | On | Email addresses |
| `phone` | Off | International phone number pattern; enable it with a custom rule |
| `name` | No default rule | Available for custom rules |
| `address` | No default rule | Available for custom rules |

## Default rules

`rules/default.json` is embedded into the collector. These rules run in the
order shown. Examples are synthetic strings that match the patterns, not
usable credentials.

| Name | Category | What it matches | Example |
|---|---|---|---|
| `aws-access-key-id` | `secret` | Access key IDs with the configured prefixes | `AKIAIOSFODNN7EXAMPLE` (the documented example key) |
| `github-token` | `secret` | `ghp`, `gho`, `ghu`, `ghs`, or `ghr` token prefixes and `github_pat_` tokens | `ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789` |
| `slack-token` | `secret` | `xox[abprs]-` followed by at least 10 letters, digits, or hyphens | `xoxb-0123456789` |
| `stripe-key` | `secret` | `sk_` or `rk_`, then `live_` or `test_`, then at least 20 letters or digits | `sk_test_` followed by 20 or more letters or digits |
| `google-api-key` | `secret` | `AIza` followed by 35 letters, digits, underscores, or hyphens | `AIzaABCDEFGHIJKLMNOPQRSTUVWXYZ012345678` |
| `sendgrid-key` | `secret` | `SG.` followed by two dot-separated segments of at least 16 allowed characters | `SG.ABCDEFGHIJKLMNOP.0123456789ABCDEF` |
| `jwt` | `jwt` | Three segments, the first two starting with `eyJ` | `eyJabcde.eyJfghij.abcdefgh` |
| `bearer-token` | `token` | Token after case-insensitive `bearer` and whitespace; capture group 1 | `Bearer ABCDEFGHIJKLMNOP` |
| `basic-auth-header` | `secret` | Base64 credentials after case-insensitive `basic` and whitespace; capture group 1 | `Authorization: Basic dXNlcjpwYXNzd29yZDEyMw==` |
| `basic-auth-url` | `secret` | Password in a URL (user part optional); capture group 1 | `https://user:examplepass@example.invalid/` |
| `generic-assignment` | `secret` | Values after sensitive field names, also inside longer names such as DB_PASSWORD or accessToken; capture group 1 | `api_key=ABCDEFGHIJKLMNOP` |
| `ipv4` | `ip` | IPv4 addresses | `203.0.113.5` |
| `ipv6` | `ip` | Compressed and full eight-group IPv6 addresses, as whole colon-separated tokens | `2001:db8::1` |
| `email` | `email` | Email address pattern | `user@example.invalid` |
| `phone` | `phone` | International phone number pattern; disabled by default | `+1-202-555-0123` |

`generic-assignment` recognizes `api_key`, `api_secret`, `secret`, `token`,
`password`, `passwd`, `pwd`, `passphrase`, `credential`, `credentials`, `auth`,
`authorization`, `access_key`, `private_key`, `signing_key`, `encryption_key`,
`client_secret`, `session_id`, and `cookie`, without regard to case. Underscores
in these names can be hyphens or omitted. A name can have a prefix of letters,
digits, underscores, or hyphens, as in `DB_PASSWORD` or `accessToken`, and a
suffix of `key`, `id`, `hash`, `value`, `str`, or `string`, optionally separated
by an underscore or hyphen. Values must have at least eight characters;
whitespace, quotes, backticks, commas, semicolons, ampersands, and brackets end
the value.

The IPv6 rule covers `fe80::1ff:fe23:4567:890a`, `::1`, and the `::ffff:`
part of `::ffff:192.0.2.1` too. It does not match times such as `12:30:45`,
MAC addresses such as `aa:bb:cc:dd:ee:ff`, or `2026-09-12T14:39:28`.

The rule claims a whole run of colon-separated hex groups, and the engine keeps
an `ip` match only when it parses as an address. A run that is not an address
stays unchanged, such as the fourteen-group `MAC=` field of iptables and UFW
log lines or a key fingerprint like `MD5:aa:bb:cc:dd:ee:ff:00:11:...`. No part
of such a run is read as an address. The `::` inside a name such as
`Net::HTTP` is not matched either. After an unbracketed address, a five-digit
port stays visible: `2001:db8::1:54321` becomes `<ip#1>:54321`.

Private key blocks are built into the engine as category `key`. From a line
matching `-----BEGIN [A-Z ]*PRIVATE KEY-----` through a line matching
`-----END [A-Z ]*PRIVATE KEY-----`, the entire block becomes one line,
`<key#N>`. A block ends at the first END line within 64 lines; without one, only the BEGIN line is replaced. For example:

```text
-----BEGIN PRIVATE KEY-----
example key material
-----END PRIVATE KEY-----
```

`name` and `address` have no default rule. Add site rules if you need them.

## Your own rules

Create `/etc/aken/rules.json` as root and make it readable by `aken`, or select
another file with `--rules FILE`. The file has the same shape as the embedded
rules. This complete example redacts an API key in a query string:

```json
{
  "version": 1,
  "rules": [
    {"name": "app-api-key", "category": "secret", "regex": "[?&]key=([A-Za-z0-9]{16,})", "group": 1}
  ],
  "keep": []
}
```

For example, `?key=ABCDEFGHIJKLMNOP` becomes `?key=<secret#1>` when that is
the first distinct secret in the run. `group: 1` replaces only the key value.

| Top-level field | Meaning |
|---|---|
| `version` | Rules format version, `1` |
| `rules` | Rules in application order |
| `keep` | Exact values to leave unchanged; merged with the default kept values |

Each rule has these fields:

| Field | Meaning |
|---|---|
| `name` | Unique name matching `^[a-z0-9-]{1,64}$` |
| `category` | One of `secret`, `token`, `jwt`, `key`, `ip`, `email`, `phone`, `name`, `address` |
| `regex` | Pattern in Go RE2 syntax |
| `group` | Optional capture group to replace; default `0`, the whole match |
| `enabled` | Optional; default `true` |

A rule with the same `name` as a default replaces it. Other rules are appended.
`keep` values are unioned with the defaults: `127.0.0.1`, `0.0.0.0`, `::1`,
and `localhost`.

To disable a default rule, supply its replacement with `"enabled": false`.
For example, put this rule in the `rules` array to disable email redaction:

```json
{"name": "email", "category": "email", "regex": "\\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}\\b", "enabled": false}
```

Unknown fields, unknown categories, invalid regexes, and duplicate names stop
the run before any source is read. Check changes with `--dry-run` and inspect
the result before sending.

## Per-run overrides

| Flag | Meaning |
|---|---|
| `--keep VALUE` | never replace this exact value (repeatable; shown on the review screen) |
| `--keep-category C` | switch a category off for this run (repeatable): secret token jwt key ip email phone name address |
| `--rules FILE` | extra rules file (default /etc/aken/rules.json when it exists) |

For example, keep one address and disable email redaction for this run:

```sh
sudo aken collect --unit nginx --keep 10.0.0.5 --keep-category email
```

The review screen shows kept values after `kept:` and disabled categories
after `off:`. These values can remain visible to the agent and its provider.

## Flags

A yellow flag marks a high-entropy string left after redaction. It is a prompt
to inspect the value, not a replacement. Flagged lines carry a `!` gutter
marker in the viewer.

A string is flagged when it is a maximal run of `[A-Za-z0-9+/=_-]`, has at
least 20 characters, contains a letter and a digit, and has Shannon entropy
of at least 3.5 bits per character. Placeholders and kept values are excluded.
Flags count distinct strings and include source and line numbers.

Choose **view flagged lines** before sending. If a value is sensitive, abort,
add a rule, and run again. A flag does not redact the value. An absence of
flags does not establish that the artifact is free of sensitive data.

## Structured logging

Drop sensitive fields at the source. Configure your application's structured
logging to omit credentials and personal data before writing log records.
Use collector rules and review as additional controls.

## Local copies

Before uploading, the collector creates this directory with mode `0700`:

```text
<state-dir>/runs/<YYYYMMDDTHHMMSSZ>-<first 8 hex of session id>/
```

Files are written once and then set to mode `0400`.

| File | Contents |
|---|---|
| `artifact.txt` | Exactly the plaintext bytes encrypted and uploaded |
| `manifest.json` | The plaintext manifest, indented |
| `mapping.json` | Placeholder to original value, for example `{"<ip#1>": "203.0.113.5"}` |
| `run.json` | `session_id`, `relay`, `created_at`, `expires_at`, `chunk_count`, and `argv` |

The mapping contains original sensitive values. It stays on the server and
is not part of the upload. The collector does not store the token.

`--state-dir DIR` defaults to `/var/lib/aken` when writable, otherwise
`$XDG_STATE_HOME/aken` or `~/.local/state/aken`. Before a real run,
`--retention D` removes directories under `runs/` whose names start with a
timestamp older than that duration. The default is `720h` (30 days);
`0` keeps everything. Dry runs write nothing and prune nothing.

## Looking up original values

When the agent names a placeholder, such as `<ip#3>`, look up the original
value on the server with `aken reveal`. It reads the local copies only and
sends nothing anywhere. The agent and `aken-mcp` cannot do this lookup.

```sh
sudo aken reveal                                  # list local copies, newest first
sudo aken reveal 20260921T101500Z-1a2b3c4d        # every placeholder and its value
sudo aken reveal 1a2b3c4d '<ip#3>' email#1        # only these placeholders
```

The first argument is a local copy directory name or a session id prefix of
at least 8 hex characters. Placeholders may omit the angle brackets. Each
result is one line: the placeholder, a tab, and the value. Control
characters in values are escaped. A placeholder that is not in the mapping is
reported on stderr and the exit code is 1. `--state-dir DIR` has the same
default as for `collect` and `serve`.
