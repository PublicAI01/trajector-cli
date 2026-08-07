# Privacy and data flow

This page describes everything trajector does with data on your machine and
what leaves it. The client is fully open source; every statement here can be
checked against the code in this repository.

## What is collected

Nothing, until you opt a project in. `trajector enable`, run inside a
project, shows the data agreement and requires an explicit yes. Only after
that does the project's Claude Code API traffic flow through the local proxy.

For an enabled project, the proxy records **successful `POST /v1/messages`
exchanges, verbatim**: the full request (system prompt, tools, messages) and
the full response (content, thinking signatures, usage), plus the HTTP
status, timestamps, API version headers, and a **hash** of the project's
root path. Streamed responses are reassembled into the equivalent
non-streaming object; when reassembly fails, the raw stream text is kept and
the record is marked `garbled` instead of being dropped or repaired.

Records are observed facts and are never rewritten: model identifiers,
thinking signatures, and usage figures stay exactly as the API produced
them.

## What is never collected

- **Projects you did not enable.** Consent is enforced structurally: only
  requests carrying an enabled project's token are recorded. Everything else
  is forwarded untouched — recording it is not a code path that exists.
- **Credentials.** `Authorization`, `x-api-key`, and other credential
  headers are never written to disk, in any file, in any state.
- **Project paths.** Stored records and uploads identify a project only by a
  hash of its root path.
- **Telemetry.** There is no separate reporting channel. A handful of
  counters (records captured, stream reassembly failures, spool usage) ride
  along inside data uploads you already consented to; no upload, no
  counters. The one-time project discovery hint is computed locally and
  never touches the network.
- **Diagnostics, unless you send them.** `trajector doctor bundle` writes an
  archive you can inspect and attach to a report yourself. It contains
  identities, counters, and timestamps — never captured records, credentials,
  or clear-text tokens. Nothing reports home on its own.

## What happens on your machine

Recorded calls wait in a local spool, in files and directories readable only
by your user account (0600/0700), under a bounded disk quota (2 GiB by
default). A full spool stops recording; it never evicts captured data.

Before anything is uploaded, records pass a **local redaction pass** that
masks secrets — API keys, tokens, passwords, and other credential-shaped
strings — and personally identifying strings — email addresses and phone
numbers — while preserving JSON structure, message order, tool-call
pairing, and thinking signatures. **Unredacted data never leaves your
machine.**

## What leaves your machine

Redacted records are packed into compressed batches (by default when 10 MiB
or 24 hours accumulate) and uploaded over HTTPS to the trajector service,
authenticated by your device pairing token. The upload destination can be
changed only through `config.json` in your user config directory (field
`platform_url`, https required off-loopback) — never through an environment
variable, so nothing a repository ships can redirect your uploads. Each batch carries a client-side
idempotency key, so a retried upload can never be counted twice. Local
records are deleted **only after** the service acknowledges the batch by
echoing that key; any other answer leaves your data in place for retry.

Contributed data is sold to third-party buyers and contributors are
compensated. That fact is public by design; buyer identity and commercial
terms are confidential, the sale itself is not. Records captured through a
third-party base URL (a relay you configured yourself) are labelled as
third-party origin; reward terms are the same regardless of origin.

## Revoking consent and deleting data

- `trajector disable` (per project): removes the settings injection, revokes
  the project token, and deletes the project's local unuploaded records —
  both the spool and any upload-rejected quarantine.
- `trajector disable --purge`: additionally asks the service to delete the
  project's uploaded but not yet delivered data.
- `trajector logout`: revokes the device token and pauses recording
  everywhere; forwarding is unaffected. Logging in again resumes.
- `trajector uninstall`: removes every injection, stops the proxy, and
  optionally deletes all local data. Run it before deleting the binary —
  removing the binary alone does not clean up the settings injections.

Data that has already been delivered and compensated is licensed under the
data agreement you accepted at enable time; deletion requests reach
everything before that point.

## Questions

Open an issue in this repository, or see [SECURITY.md](SECURITY.md) for
reporting vulnerabilities privately.
