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
machine.** Known limitation: masking applies to values only — a secret
placed in a JSON key position is not masked, because keys are structure
and the pass never rewrites them.

## Settings we ask you to change

Some Claude Code settings make the data you contribute more complete, and so
more valuable to us. That is our reason for asking, and we say so every time
we ask.

**By default we ask; we do not act.** trajector tells you what to change and
leaves the change to you. What `trajector enable` asks you is the one
exception, and it is described below.

### What we may ask you to change

A setting qualifies only if all six of these hold:

1. **It costs you nothing.** Your token usage is identical with it on or off.
2. **It does not degrade your own use of Claude Code.**
3. **You can change it back yourself**, at any time, without us.
4. **Its effect is visible to you** — not something only we can observe.
5. **Declining costs you nothing.** Recording, compensation, and everything
   trajector does are identical whether you accept or decline.
6. **Its effect does not reach past what you already consented to.** A setting
   that would change Claude Code's behaviour in projects you never enabled
   does not qualify.

A setting that fails any one of these is never asked for.

### How we may ask

- **Never blocking.** A recommendation appears only in the output of a command
  you ran yourself, or as a session notice that does not interrupt you. There
  is nothing you must answer in order to continue.
- **One refusal ends it.** Decline an item and trajector stops bringing it up.
  It stays listed in `trajector status`, where you can change your mind, and
  appears nowhere else.

### What we tell you, every time

Before you decide, for each setting:

- which key changes, and to what
- what it does for you
- what it does for us — why we want it
- how far it reaches: this project only, or every project on this machine
- how to undo it
- what happens if you decline

### The one exception: what `trajector enable` asks you

`trajector enable` asks you about each qualifying setting and writes the ones
you accept. This is the only place trajector writes a setting it does not need
in order to function, and it is allowed only when all of these hold:

- **the answer we suggest is yes — unless your own configuration already says
  otherwise.** A setting you have explicitly turned off elsewhere is suggested
  as no, and says what it is set to now, what accepting would change it to, and
  that leaving it alone costs you nothing.
- every setting states all six disclosures above, on screen, at the moment you
  answer
- **declining is as visible and as easy as accepting**, and the question cannot
  go by without being seen
- every setting stays visible and changeable afterwards in `trajector status`
- **the change can be undone exactly** — restored to what it was before we
  wrote it. A setting we cannot undo exactly is never asked about here, only
  suggested for you to apply yourself.
- **it is asked only where a person can answer.** With no interactive session —
  a script, a pipeline, or any flag that answers for you — nothing is asked and
  nothing is written, because none of the conditions above can be met when
  nobody is reading.

Nothing here fixes the shape of the question. A prompt, a list, or anything
else is allowed as long as every condition holds; a shape that cannot meet one
of them is not.

### Settings added later

When a later release adds a setting, you hear about it once through the
non-blocking path above, whether or not you have already run `enable`. It
follows the same rule: one refusal ends it.

## What leaves your machine

Redacted records are packed into compressed batches (by default when 10 MiB
or 24 hours accumulate) and uploaded over HTTPS to the trajector service,
authenticated by your device pairing token. The upload destination can be
changed only through `config.json` in your user config directory (field
`platform_url`, https required off-loopback) — never through an environment
variable, so nothing a repository ships can redirect your uploads. A
non-default destination is announced at `enable` and at proxy start; it is
never silent. Each batch carries a client-side
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
- `trajector doctor discard <batch-id>` (or `--all`): deletes a
  quarantined batch's records from this machine for good, after asking
  you to confirm. It is a local deletion only — nothing is sent anywhere
  and nothing already uploaded is affected.
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
