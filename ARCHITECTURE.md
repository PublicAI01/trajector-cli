# Architecture

Trajector is a single Go binary with three jobs: route an enabled project's
Claude Code API traffic through a local proxy, record those exchanges
verbatim as a best-effort sidecar, and upload redacted batches. This page
describes how the pieces fit; the package-by-package map is in the
[README](README.md#layout).

```
Claude Code ──HTTP──▶ 127.0.0.1:41100 (trajector proxy) ──▶ upstream API
   ▲    settings.local.json:                │
   │      ANTHROPIC_BASE_URL ─┐             ├──▶ spool (rawcalls, local)
   └── session hooks ─────────┘             │        │ redact → batch
        (start proxy on demand)             │        ▼
                                            │    trajector service (HTTPS)
                                            └──▶ healthz / selfcheck
```

## The consent boundary is the routing layer

`trajector enable` mints a random per-project token, records it in a routing
table, and injects `ANTHROPIC_BASE_URL=http://127.0.0.1:41100/t/<token>`
into the project's own `.claude/settings.local.json`. The proxy records an
exchange only when its token resolves to an active grant. Traffic without a
valid token — disabled projects, revoked tokens, bare paths — is forwarded
untouched and never recorded. Privacy does not depend on a filter being
correct; unauthorized capture has no code path.

Enable is transactional: settings injection, routing grant, consent record,
and a `.gitignore` check either all land, verified by an end-to-end
self-check against the live proxy (no upstream call, nothing billed), or
every touched file is restored byte-for-byte.

## Forwarding is sacred

The proxy is a transparent reverse proxy first. Recording runs inside one
guarded region: any failure there — disk full, malformed stream, internal
error — is counted and dropped, and the forwarded bytes are unaffected.
Streaming responses pass through unbuffered; capture reads a tee, so what
the client and upstream see does not depend on whether recording succeeds
or is even enabled.

The port is fixed at 41100 and loopback-only, and there is no fallback
port: every injected settings file names this address, so a proxy that
cannot bind either finds a healthy sibling already serving (the normal
concurrent-start outcome) or fails loudly. A foreign process on the port is
reported, never fought — enable refuses to inject, and session hooks warn.

## Nothing runs permanently

There is no daemon. Session hooks injected by enable run
`trajector hook ensure-proxy` at session start and prompt submit; any CLI
touchpoint does the same. If a healthy proxy of the current version is
listening, that is a no-op; a stale-version proxy is asked to drain
in-flight requests and hand the port over; nothing listening starts a
supervisor that restarts a crashed proxy with backoff. Idle for 30 minutes,
the proxy exits on its own.

## Capture

Only `POST /v1/messages` with a 2xx response is eligible. SSE responses are
reassembled client-side into the equivalent non-streaming JSON object,
preserving thinking signatures verbatim. A stream that cannot be reassembled
— unknown event types, malformed frames, truncation — degrades to the raw
stream text with a `garbled` mark; an interrupted response is kept as far as
it got. Records are never repaired or rewritten: model, signatures, and
usage are observed truth.

Each record is wrapped in a versioned envelope (schema_version 1) carrying
the request id, timestamps, project hash, upstream origin
(official/third-party), and format hints, then written atomically into a
day-partitioned spool with owner-only permissions, a sidecar index, and a
bounded quota. A full spool stops recording — loudly, via status and doctor —
and never evicts.

## Redaction, batching, upload

The uploader lives inside the proxy process — the machine's only resident
part, and its only flusher. On thresholds (10 MiB or 24 hours, adjustable by
the service handshake), records are redacted (secret masking that preserves
JSON structure, ordering, tool-call pairing, and signatures), packed with
same-session records adjacent for compression, zstd-compressed, and uploaded
with a client-generated idempotency key.

The key rules are strict because they guard against double counting and
data loss:

- The batch id is persisted before the first network attempt and reused
  until acknowledged, so a retry is recognizable as a retry and can never
  be ingested as new data.
- Only a 2xx that **echoes the batch id** deletes local records. Anything
  else keeps them: auth failures and server errors retry, a version gate
  (426) pauses automatic uploads until upgrade, rate limiting honors
  Retry-After (capped at one hour). Any other 4xx quarantines the batch
  locally — visible in status and doctor, recoverable with
  `trajector doctor requeue`, deleted only by consent withdrawal.

## Self-healing and observability

- `trajector status` is the read-only dashboard: pairing, project consent,
  proxy health, capture counters, spool watermark, uploads, quarantine, and
  service notices.
- `trajector doctor` diagnoses and repairs what is safely trajector's own:
  injected settings, session hooks, the discovery hint, a moved upstream.
  Consent disagreements and foreign port holders are reported with the
  command that resolves them, never guessed at.
- `trajector doctor bundle` produces the only diagnostic artifact, generated
  and sent by the user.
- The proxy serves `/trajector/healthz` (identity, uptime, counters) and a
  per-token selfcheck used by enable.

## Platform

One codebase serves macOS, Linux (including WSL2, no systemd required), and
Windows. Platform differences converge in two leaf packages: user-directory
layout and the token store (OS keyring with an owner-only file fallback for
headless machines). Claude Code and trajector must run on the same side of a
WSL boundary; doctor points this out.

## Invariants worth naming

- Recording failures never interrupt forwarding.
- Observed truth is never rewritten.
- Credential headers are never written to disk.
- Unredacted data never leaves the machine.
- An injected base URL implies an active token and both session hooks;
  enable rolls back to exactly the prior bytes on any failure.
- Acknowledgement is the only deletion trigger; a batch id is never reused
  for different content after an ack.
- The proxy binds loopback only and forwards only to configured upstreams.
