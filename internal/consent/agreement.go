package consent

// AgreementVersion identifies the agreement text below. Bumping it
// makes every earlier acceptance stale: capture pauses until the user
// reconfirms, so recorded consent always matches the current terms.
const AgreementVersion = "2026-08-31"

// AgreementText is shown in full before the explicit yes/no prompt.
// It states the actual client behavior and must be kept truthful to
// it.
const AgreementText = `Trajector Data Contribution Agreement (` + AgreementVersion + `)

This agreement describes what the trajector client does on your
machine.

1. What is collected. After you enable a project, API requests that
   your coding agent sends from that project are forwarded through a
   local proxy and recorded verbatim: the full request (system prompt,
   tools, messages, thinking configuration) and the full response
   (including usage details). Only enabled projects are recorded;
   traffic from other projects never reaches the proxy.

2. What never leaves your machine. Credential headers (Authorization,
   x-api-key) are never written to disk. Before upload, every record
   is masked locally for secrets such as API keys, tokens, passwords,
   and personal data. Unredacted data does not leave your machine.

3. What the data is used for. Uploaded records are combined into
   datasets that are sold or licensed to third parties. You receive
   rewards for contributions that are delivered.

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
   an irrevocable license.

By answering yes you accept these terms for this device.`
