# Trajector CLI

Trajector is an open-source command-line tool that turns your own Claude Code
coding sessions into compensated data contributions.

With your explicit per-project consent, it routes that project's Claude Code
API traffic through a local proxy (`127.0.0.1` only), records the raw
request/response data, masks secrets on your machine, and uploads the result.
Contributed data is sold to third-party buyers and contributors are
compensated — that fact is public by design; what stays confidential is buyer
identity and commercial terms, not the sale itself.

**Status: early development. Not yet functional.**

## How it works

`trajector enable` writes project-local Claude Code settings that route the
project's API traffic through a local reverse proxy bound to
`127.0.0.1:41100`. The proxy forwards requests verbatim to the configured
upstream and records them as a best-effort sidecar: any failure on the
recording side — disk full, malformed stream, internal error — never
interrupts forwarding, and streaming responses pass through unbuffered.

A per-project token is the consent boundary. Requests without a valid token
are forwarded but never recorded, so projects you have not enabled are
structurally incapable of being captured. Recorded calls wait in a local
spool with a bounded disk quota, secrets are masked on your machine, and
only redacted batches are uploaded. The proxy starts on demand, exits when
idle, and never runs as a permanent daemon.

Design commitments:

- Forwarding is sacred: capture failures never break your Claude Code session.
- Recording is unobservable: what the proxy sends upstream does not depend on
  whether the exchange is being recorded.
- Only projects you explicitly enable are captured; everything else connects
  straight to the upstream.
- Credential headers are never written to disk; unredacted data never leaves
  your machine.
- Consent is revocable at any time (`disable`, `logout`, `uninstall`).
- The client is fully open source, so everything it does on your machine can
  be audited in this repository.

## Layout

| Package | Responsibility |
|---|---|
| `internal/apiproxy` | The local proxy: forwards traffic, and records eligible calls inside one guarded region |
| `internal/proxylife` | The proxy's life outside the process that runs it: start, run, probe, stop |
| `internal/lifecycle` | Device and project consent: pairing, enable, disable, uninstall, session hooks |
| `internal/routing` | Which token routes where, and whether this exchange may be recorded |
| `internal/consent` | The durable record of what the user agreed to |
| `internal/capture` | Which calls are eligible, and reassembly of streamed responses |
| `internal/envelope` | What a stored rawcall is: written, classified, and read back |
| `internal/spool` | The bounded on-disk store between capture and upload |
| `internal/claudesettings` | Reading and writing Claude Code's own settings files |
| `internal/userdirs` | Where trajector's files live on this machine |
| `internal/platform` | The client for the trajector service API |
| `internal/tokenstore` | Small secrets, in the OS keyring or owner-only files |
| `internal/cli` | argv, exit codes, and what the user reads |
| `internal/harness` | Test doubles and sandboxes: a fake upstream, a fake service, a real proxy in a temp directory |
