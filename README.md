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

The full behavior description lives in [PRIVACY.md](PRIVACY.md) (what data,
when, what processing, where it goes) and [ARCHITECTURE.md](ARCHITECTURE.md)
(how the pieces fit); vulnerabilities go through [SECURITY.md](SECURITY.md).

## Installation

On macOS and Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/PublicAI01/trajector-cli/main/install.sh | sh
```

The script picks the archive for your platform, verifies it against the
release's checksum file — refusing to install anything that does not match —
and puts `trajector` in `~/.local/bin`. Set `TRAJECTOR_INSTALL_DIR` to
install elsewhere, or `TRAJECTOR_VERSION=v0.1.0` to pin a release.

**On macOS, install with the command above rather than downloading the
archive in a browser.** Releases are not yet code-signed, and macOS marks
anything a browser downloaded; unpacking such an archive in Finder passes
that mark to the binary, and Gatekeeper then refuses to run it. A download
made by `curl` carries no mark. The same applies on Windows, where
SmartScreen acts on the equivalent mark.

Windows has no install script yet. Download
`trajector_<version>_windows_<arch>.zip` from the
[releases page](https://github.com/PublicAI01/trajector-cli/releases), then
in PowerShell:

```powershell
$archive = ".\trajector_<version>_windows_<arch>.zip"
Get-FileHash $archive -Algorithm SHA256
# compare against trajector_checksums.txt from the same release
Unblock-File $archive
Expand-Archive $archive -DestinationPath .\trajector
Unblock-File .\trajector\trajector.exe
```

`Expand-Archive` unpacks into a folder rather than the current directory, so
name the destination yourself; unblocking the archive before unpacking keeps
SmartScreen's mark off the binary.

Move `trajector.exe` somewhere on your `PATH`. Homebrew and Scoop are not
available yet.

Then pair the device and enable a project:

```sh
trajector login
cd your-project && trajector enable
```

### Updating

```sh
trajector upgrade
```

`upgrade` moves to the newest published release, verifying its checksum
before replacing anything: a download that fails verification leaves the
binary you have exactly as it was. Releases before 1.0.0 are published as
pre-releases and `upgrade` moves to them, which is what a beta wants. An
installation a package manager owns is handed back to that manager rather
than overwritten.

## Releases and verification

Every release is built by GitHub Actions from this repository and ships with
a checksum file and a build provenance attestation, so you can verify a
downloaded binary was produced from this source before running it:

```sh
shasum -a 256 -c --ignore-missing trajector_checksums.txt
gh attestation verify trajector_* --repo PublicAI01/trajector-cli
```

To uninstall, run `trajector uninstall` **before** deleting the binary:
deleting the binary alone leaves the settings injections behind.

## Layout

| Package | Responsibility |
|---|---|
| `internal/apiproxy` | The local proxy: forwards traffic, and records eligible calls inside one guarded region |
| `internal/proxylife` | The proxy as seen from outside its process: start, probe, flush, stop |
| `internal/lifecycle` | Device and project consent, and the composition root: pairing, enable, disable, uninstall, session hooks, serving the proxy |
| `internal/routing` | Which token routes where, and whether this exchange may be recorded |
| `internal/consent` | The durable record of what the user agreed to |
| `internal/capture` | Which calls are eligible, and reassembly of streamed responses |
| `internal/envelope` | What a stored rawcall is: written, classified, and read back |
| `internal/spool` | The bounded on-disk store between capture and upload |
| `internal/redact` | Masking secrets on this machine before anything is uploaded |
| `internal/batch` | A set of rawcalls prepared for one upload |
| `internal/upload` | Draining the spool to the service in acknowledged batches |
| `internal/claudesettings` | Reading and writing Claude Code's own settings files |
| `internal/userdirs` | Where trajector's files live on this machine |
| `internal/platform` | The client for the trajector service API |
| `internal/tokenstore` | The device pairing secret, in the OS keyring or owner-only files |
| `internal/fsatomic` | Atomic file writes, and updates serialized across processes |
| `internal/selfupdate` | Finding, verifying, and installing a newer published release over this one |
| `internal/cli` | argv, exit codes, and what the user reads |
| `internal/harness` | Test doubles and sandboxes: a fake upstream, a fake service, a real proxy in a temp directory |
| `internal/installscript` | Where `install.sh` is run end to end against a local release source |
