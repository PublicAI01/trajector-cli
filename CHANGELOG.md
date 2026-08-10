# Changelog

All notable changes to trajector are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `install.sh`: a one-line install for macOS and Linux that picks the
  right archive, verifies it against the release's checksums, and
  refuses to install anything unverified. Windows gets printed manual
  instructions, including how to clear the download mark.
- `trajector upgrade`: replaces this binary with the newest published
  release. The archive is verified before anything is replaced, a
  failed upgrade leaves the working binary untouched, and an
  installation a package manager owns is handed back to that manager.
- Every surface that reports the service asking for a newer client —
  `upload`, `status`, `doctor` — now names `trajector upgrade`, and
  relays what the service said about the refusal when it said anything.
  A required version number alone cannot explain why or by when; the
  sentence beside it can. The diagnostic bundle carries it too.

### Security

- Free text the service supplies — the upgrade explanation and the
  handshake notice — is stripped of anything that could draw on a
  terminal before it is stored or printed: escape sequences, carriage
  returns, line breaks, and invisible or direction-changing characters
  all become spaces, and the result is one capped line. Printed beside
  our own output, such text could otherwise forge a line the user
  reads as the client's own report.

## [0.1.0] - 2026-08-09

First release: local capture proxy with lazy lifecycle, consent-gated
routing, verbatim capture with streaming reassembly, local redaction,
spooling with a bounded quota, acknowledged idempotent batch uploads,
enable/disable/logout/uninstall lifecycle, status and doctor (including
quarantine requeue and diagnostic bundles), and the release pipeline.

Hardened ahead of the tag:

- The proxy port's holder is trusted only after it answers an
  admin-token challenge, with one token published per listen address.
  Management calls that act on the port wait out a sibling still
  starting up instead of blaming it, and each rides a connection of its
  own, so a takeover never leaves a dead pooled connection behind.
- Only a strictly older release is asked to drain and hand the port
  over, and the flush a proxy runs on its way out is bounded so the
  port is released inside its successor's wait; whatever that flush did
  not upload stays spooled and uploads later under the same batch id.
- Upload attempts get a time budget sized to the batch, unreadable
  records are set aside instead of stalling every upload, and
  `trajector doctor discard` deletes a quarantined batch for good — the
  terminal counterpart to `trajector doctor requeue`.
- `trajector status` says why recording stopped — a full or otherwise
  unwritable spool — in the same words doctor uses, and the dashboard
  stays whole when a store cannot be read.
- State files are replaced and read atomically on every platform,
  Windows rename collisions included.
