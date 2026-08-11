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

### Fixed

- `status` and `doctor` no longer tell a client that already meets the
  service's minimum version to upgrade. The service announces that
  minimum on every acknowledgement, so one successful upload used to
  leave a compliant build carrying the requirement and the instruction
  for good — and reading the very same two lines on the one occasion
  they meant something. Both surfaces now compare the two versions:
  a build that meets the minimum hears nothing about it, a build that
  is behind is told so and told what to run, and a pair no order covers
  — a development build, say — is stated without a remedy it cannot
  act on. A refusal the service explained is still relayed in full
  whatever the comparison says: while it stands, uploads really are
  stopped.
- `trajector upload --force` no longer answers a pause by suggesting
  `--force`. Neither pause the service can impose — a refusal or a
  request to slow down — is what `--force` bypasses; it bypasses this
  client's own upload thresholds. A run that already used it was being
  sent back to the switch it was holding down. The offer now appears
  only where it is the real next step: on a run that has not tried it.
- `trajector status` names the span its capture counts cover. They
  count one proxy's run, and the proxy exits when it goes idle, when a
  newer release takes the port, and with the machine — so calling them
  a day's work read low in exactly the direction a user takes for "it
  has stopped recording", and a restart could print a spool holding
  captures beside a count of zero. They are now labelled for the run
  whose uptime is on the line above, and no longer reset at midnight
  as well: two origins for one number left no span a reader could name.
- `trajector upgrade` no longer installs over a path that is not a
  file. What a rename does when a directory stands where the binary
  belongs differs between systems, and on Windows it moved the
  directory aside and left the new binary in its place. The refusal is
  now the program's own, before anything is written.
- A session started from a shell that configures its own base URL no
  longer has that relay replaced by the official endpoint. The hook
  that reconciles a project's upstream runs inside a session whose
  environment already carries our injection, which hid the shell's own
  setting from it; it now recognises that it cannot see the answer and
  keeps what the grant recorded, instead of guessing.

### Security

- The service's wording for a rejected batch is stripped of anything
  that could draw on a terminal, both where it arrives off the network
  and where it is read back from a quarantined batch's reason file.
  It is printed by `upload`, printed again by `doctor` among `ok:` and
  `problem:` lines, and carried in the diagnostic bundle, so it could
  otherwise forge a whole line the user reads as our own verdict. It is
  also cut without breaking a character.
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
