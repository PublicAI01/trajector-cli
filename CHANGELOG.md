# Changelog

All notable changes to trajector are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.3.2] - 2026-09-20

### Changed

- A segment whose shape is new to this build is now held on this
  machine instead of pausing recording everywhere. It is kept where
  nothing uploads it, reading goes on from the next segment, and
  `status` and `doctor` say how many segments are held and for how
  many sessions. A later build that reads a held segment cleanly moves
  it back for upload when `doctor` runs, and `trajector forget
  <session-id>` deletes what is held for that session. Recording still
  pauses everywhere when a read contradicts itself.
- `doctor` lifts a redaction pause without waiting for another build,
  once this build has read the session files again and found nothing
  it cannot redact. A pause an older build set is still lifted after
  the upgrade, as before.

### Fixed

- A slash command typed as a prompt — `/exit` written to a
  `queue-operation` or a `system` / `local_command` line — no longer
  reads as a session file shape this build cannot redact, so it no
  longer pauses recording device-wide. The value is what the user
  typed, not where the session ran, and the same text stands in the
  user line it came from.

## [0.3.1] - 2026-09-20

### Added

- Session files are read while the session runs. `enable` installs one
  more session hook, run when the model stops and when a batch of tools
  finishes; it tells the resident process which file just gained lines,
  and the process reads that one file at once. When no resident process
  is up the hook falls back to what 0.3.0 did: a one-shot reader that
  brings the process up on its way out. `doctor` adds the hook to an
  injection made before it existed.
- The resident process looks at the files of running sessions on its
  own every five minutes, for a session whose hooks are disabled, a
  process that started after the session did, or a session killed
  before its last hook ran. A file is hot while the session's process
  runs or a hook named it within the last day; a cold file is never
  looked at until a hook names it again. The registry keeps both facts
  beside the cursor; nothing new is stored elsewhere.
- Records read from session files leave the machine on thresholds of
  their own — 1 MiB or five minutes, adjustable by the service
  handshake — and at once when a session ends or its process is gone
  and its file stopped growing.
- The data agreement and `PRIVACY.md` say that a call read from the
  session file while the session was running counts as witnessed. The
  agreement version moves to 2026-09-20, so `enable` asks once more.

### Changed

- The resident process now counts a running session as being in use:
  it exits after 30 minutes with neither traffic forwarded nor a
  session file gaining lines, and waits two hours instead while a
  session's process is still running. It never stays up for good.

## [0.3.0] - 2026-09-19

### Added

- A second source of records: the session files Claude Code writes on
  your machine. `trajector enable` now installs session hooks (session
  start, prompt submit, session end). Each hook registers the
  session's own files and starts a one-shot reader that appends what
  the files gained since the last run to the spool and exits — nothing
  runs permanently and nothing is watched. Files are read only for
  projects you enabled; a project that was never enabled has no path
  trajector could even construct. At enable time trajector also finds
  the project's earlier session files (up to a fixed limit, without
  listing any other project's directory) and tells you how many it
  found and how old the oldest is.
- `trajector enable --no-proxy` records a project from its session files
  only. The hooks stand, no base URL is injected, and Claude Code's
  `/remote-control` stays available inside the project. `enable`
  states which shape it installed, and `status` and `doctor` say which
  shape a project records in.
- Before injecting, `enable` judges from the configuration readable on
  this machine whether Claude Code will load trajector's hooks at all
  (hooks disabled in a settings layer, managed settings, a workspace
  not yet trusted, a project on a Windows drive mounted into WSL). It
  says what it found and what to do; it never guesses past what it can
  read.
- Session file contents pass the same redaction as recorded calls, and
  first have the fields that name the session's directory on disk
  replaced with a placeholder. Every other path is uploaded as observed;
  `PRIVACY.md` says so in exactly those terms.
- Recording pauses device-wide when a build meets session file lines
  whose shape its redaction does not cover, so nothing unmasked is ever
  stored. `status` names the reason, `trajector upgrade` tells you the
  next step while the pause stands, and `trajector doctor` lifts the
  pause once a different build has read the files.
- `trajector forget [session-id]` deletes one session's not-yet-uploaded
  records from this machine — from both spool slots and from any
  quarantined batch — and reports one total. It defaults to the current
  session.
- `trajector status` shows the second source per project: how many files
  are registered and when they were last read, what is waiting to
  upload by kind (`rawcall`, `segment`, `session snapshot`, `git snapshot`),
  what the reader
  noticed about the files' shape (counts and field names only, never a
  path or a session id), and how many assistant lines carried no
  reasoning — with a pointer to `showThinkingSummaries` when that
  setting is off. `doctor` additionally walks the project's session
  files and reports sessions that were written without a hook of
  trajector's noticing them; `doctor bundle` carries the same findings.
- `enable` and `status` say what a call the local proxy did not witness
  is worth, before anything of yours is uploaded: a call is witnessed
  when the proxy handled its request and its response on your machine,
  so the sessions a project had before it was enabled, and any session
  that runs while the proxy does not, are rewarded at a lower rate than
  witnessed ones. The tokens are counted in full either way. The data
  agreement, `PRIVACY.md` and the README say the same. The client
  states the rule and no figure: the current rates are published at
  <https://docs.publicai.io/publicai-documentation/publicai-trajector-cli/rewards>,
  and the agreement carries no address of its own.
- A third source of records: the state of an enabled project's git
  repository, observed when a session opens, when it closes, and after
  a shell command that makes a commit. Each record states what `git`
  printed — the commit checked out, its first parent, the branch, and
  the paths that changed between two commits with the identifiers git
  prints beside them. **Only paths and identifiers, never the content
  of any file**, and nothing is written to your repository. `enable`
  installs one more session hook for it, and `doctor` adds that hook to
  an injection made before it existed.

### Changed

- The data agreement covers the second and third sources and its version
  is bumped. Recording pauses for existing users until the new terms are
  reconfirmed with `trajector enable`; forwarding is untouched. The
  version is the day the build carrying the terms was released, and
  `PRIVACY.md` names the version it describes.
- The data agreement says which paths are masked and which are uploaded
  as observed, in the terms `PRIVACY.md` already used: the few fields
  whose value is the directory a session ran in are replaced with a
  placeholder, and every other path a record holds goes up as observed.
  It previously claimed that every field identifying where the project
  lives on disk was masked, which is more than the client does.
- `status` states, for every enabled project, that sessions started
  under another spelling of the project's path — or under a directory
  name Claude Code was told to use instead — are stored under names
  this device does not compute and are not collected. Where a folder
  name trajector would derive could also belong to a different path on
  your machine, it leaves that whole folder alone and says which one
  and why.
- Uploads now use batch schema version 3: one batch carries recorded
  calls, session file segments, metadata snapshots and git observations
  together, and the batch and every record body in it declare one
  version.
- The word `record` now means any entry the spool stores and a batch
  carries; `rawcall`, `segment` and `snapshot` are its kinds. Counts in
  `upload`, `status`, `doctor`, `discard` and `forget` say which they
  count; a count over mixed kinds says "record(s)".
- On a second `enable`, an optional setting trajector already turned on
  is asked as `Keep it on? [Y/n]`. Every question about an optional
  setting now points the same way: yes means on.
- `trajector doctor` reads a project's recording shape from the grant
  written at enable time; the project's settings file is checked
  against it and repaired when it disagrees, never the other way round.
- `CLAUDE_CONFIG_DIR` is honoured wherever trajector reads or writes
  Claude Code's own files: the user-level settings that carry the
  discovery hook, the managed settings, and `remote-settings.json`.
  `doctor` removes a hook trajector left in `~/.claude/settings.json`
  on a machine where Claude Code reads another directory, and says so.
  The reason `doctor` gives for hooks that will not load names the
  settings layer that decided it, not a file path.
- `enable` rolls back through a ledger of every change it made, in
  reverse order: the routing grant, the consent record, the session
  file registry, the project's settings file and the `.gitignore` lines
  it appended. Accepting the agreement and lifting a reconfirmation
  pause are your answers, not files enable touched, and stand.
- A session file whose session leaves the project is retired in the
  registry rather than forgotten, so enabling the project again does
  not read it from the start or send the same record twice.
- The reader keeps a small diagnostic log of line shapes this build did
  not expect. It is bounded (1 MiB, trimmed to its newest half) and no
  longer grows on shapes the build already knows about.
- `trajector logout` states the device-wide pause in the same words
  `status` and `doctor` use: "Recording is paused everywhere", the
  reason, and the one command that ends it.
- `status`, `doctor`, `discard` and `forget` count every kind of record
  the spool holds from one declared list of kinds, so a kind this build
  stores is counted on every surface rather than only on the ones that
  remembered to name it. `forget` now also removes a record that
  declares a kind this build does not know but names your session.

### Fixed

- A device-wide pause now stops the session file reader as well as the
  proxy. Before, "Recording is paused everywhere" in `status` was true
  of recorded calls only.
- One `doctor` run no longer gives two answers about a project's shape:
  it could report that the proxy was not needed and then rewrite the
  base URL into the same project's settings.
- Counts printed as "rawcall(s)" over batches that also held session
  records now say "record(s)".
- A session file line whose object keys are themselves paths can no
  longer surface a path as a "field name" in `status` or in a
  diagnostic bundle.
- A batch left pending by an interrupted upload resumes only the
  records it named, even when a record in the other slot carries the
  same id, and finds them without rereading every record on this
  machine. An offline run used to reread the whole spool once a minute
  for as long as the batch stood, and one unreadable record anywhere
  could block the resend for good.
- A git observation is counted among the records waiting to upload.
  `status` left the kind out of the line, so a spool holding only
  observations was reported as having nothing waiting; `trajector
  forget` left observations behind in a quarantined batch, and
  `doctor requeue` reported such a batch stuck and kept it.
- Stopping the machine no longer kills a recording proxy outright. A
  reboot or a logout asks it to stop and gives it time to finish: live
  Claude Code sessions are drained, captures already complete are
  written, and the uploader runs its last flush.
- A base URL that carries its own credentials (`https://user:pass@relay`)
  works through trajector. The credentials were dropped on the way out,
  so such a relay answered 401 to everything the moment trajector stood
  in the path.
- A large batch over a slow link is no longer cut off after 30 seconds
  whatever budget the upload had escalated to — which retried the same
  attempt forever and eventually filled the spool and stopped
  recording.
- `trajector upgrade` flushes the new binary to disk before it replaces
  the old one, so a crash during the upgrade can no longer leave an
  empty file where trajector was.
- A consent record that cannot be read pauses recording and says so,
  instead of being taken for an agreement that is current.
- The spool's own accounting of its size survives records deleted while
  it is being measured — by `trajector disable` in another process, or
  by the uploader — instead of undercounting and letting the quota stop
  binding.
- Claude Code's settings files are read the same way trajector writes
  them, so a read racing one of trajector's own writes no longer has
  `doctor` report an injection problem that does not exist and rewrite
  a settings file that was already correct.
- The service refusing this device's credential (401) or refusing it
  access (403) stops automatic uploads and says which it was;
  everything captured is kept. When the service will not accept this
  device's pairing token, `status` says so and tells you to run
  `trajector login`, and uploads resume at the next flush after you
  do, with no fixed wait. A 401 used to re-offer the whole batch every
  minute for the life of the process, and a 403 quarantined batches
  one a minute until the spool had walked into a store that is never
  retried automatically.
- In a git record the branch name is masked in the same pass as the
  changed paths, so a secret in a branch name is masked exactly as one
  in a path is. `PRIVACY.md` says which of a git record's fields are
  masked and which are uploaded as git printed them.
- `ARCHITECTURE.md` stated that records carry `schema_version 1`. They
  carry 3.
- A session's records no longer wait on disk for the next prompt. The
  flush the recording process runs as it stops now uploads whatever the
  spool holds instead of leaving what is under 10 MiB and newer than 24
  hours for a run that may be days away. A service that refused this
  build, this account or this device is still not asked again on the way
  out.

### Security

- A credential value that contains a `,`, `;` or `&` is masked to its
  end. Only the head was masked before, which left the rest of the
  password in a record that reads as redacted.
- A credential named by its key is masked in the JSON spelling too
  (`"db_password": "…"`), not only in `KEY=value`. A JSON document that
  arrives inside a string — a tool result, an MCP answer, a read of a
  `.json` config — was masked as structure and shipped in the clear as
  text.
- A credential that also appears as a bare array element — an argument
  vector, say — is masked there as well, under the verdict its own key
  established elsewhere in the same record.
- An AWS access key id is masked on its own. It was masked only when a
  secret access key stood within a few lines of it; an id in a sentence
  you typed, in the output of a command, or in the
  `aws_access_key_id = …` line of a credentials file was not. The
  documented `AKIA…EXAMPLE` placeholder is masked too.
- A Slack `xox…-` token is masked by its prefix alone, so a token whose
  body does not match a catalogued shape is masked all the same.

## [0.2.1] - 2026-08-31

### Added

- `trajector enable` asks about one optional Claude Code setting,
  `showThinkingSummaries`. Claude Code stopped generating these
  summaries by default in v2.1.89; turning the setting on shows you the
  model's reasoning again, and makes contributed records carry it
  instead of an empty field — which is why we ask. The question states,
  on screen before you answer: which key changes and to what, what it
  does for you, what it does for us, that it costs you nothing
  (reasoning is generated and billed either way), that it reaches this
  project only, how to undo it, and that declining changes nothing
  else. Accepting has trajector write the project's own
  `settings.local.json` and record what was there before; a value you
  set yourself is stated, left alone, and never recorded; a value you
  explicitly turned off elsewhere flips the suggested answer to no and
  names the settings layer it came from. Without an interactive session
  — a script, a pipeline — nothing is asked and nothing is written.
- `trajector disable` and `trajector uninstall` put an accepted setting
  back exactly as it was before trajector wrote it — the key deleted if
  it was absent, an explicit `false` restored — unless you edited it
  since: your later edit always wins. Declining ends the asking; the
  choice stays visible in `trajector status`, and rerunning `enable` is
  how you change your mind.
- `trajector status` closes the project section with the setting's
  state: a recommendation while it is off and never declined, one
  factual line once declined, and "on" marked as trajector's write or
  as your own. `doctor` says nothing about it — an optional setting
  left off is not a fault.
- `PRIVACY.md` publishes the rule all of this follows: what a setting
  must satisfy before trajector may ask for it, how trajector may ask
  (never blocking, one refusal ends it), what must be disclosed every
  time, and the conditions under which `enable` may write a setting at
  all.

### Changed

- The data agreement gains a clause covering these settings writes and
  their exact undo, and its version is bumped. Recording pauses for
  existing users until the new terms are reconfirmed with
  `trajector enable`; forwarding is untouched.

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

[Unreleased]: https://github.com/PublicAI01/trajector-cli/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/PublicAI01/trajector-cli/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/PublicAI01/trajector-cli/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/PublicAI01/trajector-cli/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/PublicAI01/trajector-cli/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/PublicAI01/trajector-cli/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/PublicAI01/trajector-cli/releases/tag/v0.1.0
