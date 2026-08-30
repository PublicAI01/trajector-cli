# Changelog

All notable changes to trajector are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.2.0] - 2026-08-30

### Added

- Uploads pause when the service refuses them until the account's data
  authorization is complete. Everything captured is kept, nothing is
  quarantined, automatic uploads stop instead of re-offering the batch
  every cycle, and `upload`, `status`, and `doctor` all point at the
  page the service names for completing it. This refusal can stand
  together with a required upgrade, and the surfaces name both.
- `status` and `doctor` can now see an upload backoff. When the service
  asks uploads to slow down, or an attempt runs out of time, the pause
  and its expiry are recorded beside the uploader's other state — so
  both surfaces say uploads are paused and until when, a proxy
  restarted inside the wait honours what is left of it, and the next
  acknowledged upload clears it.
- `doctor` explains every upload gate the same way — the required
  upgrade, the incomplete data authorization, an active backoff — one
  sentence for what is true and one for what ends it.
- `doctor` tells a batch the service refused apart from records this
  machine set aside because they no longer read back as rawcalls, and
  offers each kind only the way out that works: requeue or discard for
  a refusal, discard alone for unreadable records — which requeue now
  refuses up front instead of failing one record at a time.

### Changed

- One unreadable record no longer slows the batch it is in: packing
  answers for every record in one pass, uploads the readable ones, and
  reports everything set aside at once.
- After an attempt that ran out of time, `trajector upload` reports the
  resulting pause the same way every later flush does, and says the
  last attempt timed out — which is not the service asking to slow
  down, and no longer reads like it.
- Building from source embeds the version git describes instead of
  reporting `dev`, so the service's version gates see a real version.

### Fixed

- `install.sh` picks the same release `trajector upgrade` picks. It
  used to select a draft release when one named the highest version —
  failing the install against assets that do not exist — and to let a
  release candidate published after its finished version win on
  publication order. Both installers now rank releases by
  semantic-version precedence, skipping drafts, and one test drives
  both against the same release index so their answers cannot drift
  apart again.
- `enable` and `disable` against a configuration directory this user
  cannot write fail within a second with the real permission error,
  instead of retrying for thirty seconds and reporting a lock timeout.
- Ten batches of small robustness fixes across capture, redaction,
  forwarding, settings handling, and the lifecycle commands, produced
  by an unattended scan-fix-review pipeline with the full test suite
  and an independent review pass gating each batch.

## [0.1.1] - 2026-08-14

### Changed

- Releases carry macOS and Linux only. The Windows build is not
  published: the code compiles and is tested for it, but the end-to-end
  pass against the service has only been run on the other two, and an
  archive on the releases page reads as a platform we support. The
  0.1.0 Windows archives stay published — nothing that already runs
  stops running — but `trajector upgrade` on such a build now reports
  that this release's archive is not published and leaves the working
  binary in place, and `install.sh` sends Windows to WSL instead of to
  a download that would 404. Publishing resumes with the platform.

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

[Unreleased]: https://github.com/PublicAI01/trajector-cli/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/PublicAI01/trajector-cli/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/PublicAI01/trajector-cli/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/PublicAI01/trajector-cli/releases/tag/v0.1.0
