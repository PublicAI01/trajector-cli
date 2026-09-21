package consent

// AgreementVersion identifies the agreement text below. Bumping it
// makes every earlier acceptance stale: capture pauses until the user
// reconfirms, so recorded consent always matches the current terms.
const AgreementVersion = "2026-09-21"

// AgreementText is shown in full before the explicit yes/no prompt.
// It states the actual client behavior and must be kept truthful to
// it. The text, AgreementVersion, and PRIVACY.md describe the same
// terms and change together; a test pins the three to each other.
const AgreementText = `Trajector Data Contribution Agreement (` + AgreementVersion + `)

This agreement describes what the trajector client does on your
machine.

1. What is collected. After you enable a project, coding data from
   that project is collected from three sources: (a) the API requests
   your coding agent sends from that project, forwarded through a
   local proxy and recorded verbatim — the full request (system
   prompt, tools, messages, thinking configuration) and the full
   response (including usage details); (b) the session files
   Claude Code writes on your machine for that project. The session
   files contain things the proxy cannot see: your tool results,
   including the contents of the files that were read and the changes
   that were written, your working directory and git branch, and the
   full conversations of subagents; and (c) the state of that
   project's git repository, read with git when a session starts,
   when it ends, and after a shell command that makes a commit. What
   is recorded there is the commit that is checked out, the commit
   before it, the branch name, and the list of paths that changed
   between two commits with the identifiers git prints beside them.
   Only paths and identifiers are recorded from git, never the
   content of any file, and trajector never writes to your
   repository.

   When you enable a project, trajector also collects, once, the
   session files that project had already written before you enabled
   it, and starts reading them right away. Enable tells you how many
   there are and how old the oldest one is. Enabling with --no-earlier
   leaves those files alone: they are never registered and never
   read. After that it does not scan backwards again unless you ask
   it to.

   Only enabled projects are collected. Traffic from other projects
   never reaches the proxy, and the paths to their session files are
   never constructed, so those files are never opened.

2. What never leaves your machine. Credential headers (Authorization,
   x-api-key) are never written to disk. Before upload, every record
   is masked locally for secrets such as API keys, tokens, passwords,
   and personal data. In the records that come from the session
   files, the few fields whose value is, by construction, the
   directory the session ran in are replaced with a placeholder: the
   field stays, its value goes. Tool results, however, are kept as
   observed: their text may contain file paths from your machine, and
   trajector does not rewrite it, because rewriting it would destroy
   the data itself. Every other path a record holds is uploaded the
   same way, as observed. Unredacted data does not leave your
   machine.

3. What the data is used for. Uploaded records are combined into
   datasets that are sold or licensed to third parties. You receive
   rewards for contributions that are delivered. What a record is
   worth depends on what it contains: a record that came from only
   one of the sources above may be rewarded less than one that came
   from more of them. It also depends on whether the local proxy
   witnessed the call the record is of. A call is witnessed when the
   proxy handled its request and its response on your machine. A
   call trajector read from the session file while the session was
   running, as it does for a project enabled without the proxy, may
   also count as witnessed; a call not witnessed — every session a
   project had before you enabled it, and every session whose file
   was read only after it ended — is rewarded at a lower rate than a
   witnessed one. The tokens are counted in full either way; only
   the amount is reduced. The rates themselves are set by trajector
   and are not fixed by this agreement.

4. Third-party relays. If this project routes traffic through a
   non-official base URL, its records are marked as third-party
   origin. Reward terms are the same regardless of origin.

5. Optional client settings. During enable, trajector may offer an
   optional Claude Code setting. If you accept, trajector writes that
   setting to the project's local Claude Code configuration, records
   what was there before, and restores it exactly when you disable
   the project or uninstall. If you decline, nothing else changes.

6. Revocation. Disabling a project stops collection immediately,
   revokes its token, and deletes its local unuploaded data. You may
   additionally request deletion of uploaded data that has not yet
   been delivered. Data already delivered and rewarded is covered by
   an irrevocable license. Trajector deletes only its own copies: the
   session files Claude Code writes on your machine are yours, and
   trajector never modifies or deletes them — not on disable, not on
   uninstall.

By answering yes you accept these terms for this device.`
