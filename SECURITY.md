# Security policy

Trajector captures API traffic on contributors' machines, so it deserves —
and expects — adversarial review. If you find a vulnerability, thank you for
reporting it responsibly.

## Reporting a vulnerability

Please report vulnerabilities **privately** through GitHub's security
advisory form for this repository: **Security → Report a vulnerability**.
Do not open a public issue for anything that could put contributors' data
or credentials at risk before a fix ships.

A useful report includes the client version (`trajector version`), the
platform, reproduction steps, and — if relevant and you are comfortable
sharing it — a `trajector doctor bundle` archive, which contains
diagnostics only (no captured data, no credentials; review it yourself
before attaching).

We will acknowledge reports as quickly as we can, keep you informed while a
fix is developed, and credit you in the advisory unless you prefer
otherwise.

## Scope

Especially interesting areas, in rough order of impact:

- Redaction bypasses: any way secrets survive the masking pass and leave
  the machine.
- Consent-boundary violations: any way a project that was never enabled, or
  was disabled, gets recorded.
- Credential exposure: any code path that writes credential headers or the
  device token where it does not belong.
- The local proxy: it must bind loopback only and forward only to
  configured upstreams; anything that makes it reachable from outside the
  machine or turns it into an open relay.
- Injection handling: the settings files trajector writes into projects,
  and the session hooks it installs.

The server side of the service is not in this repository; reports about it
are still welcome through the same channel and will be routed.

## Supported versions

Security fixes target the latest release. There are no long-term support
branches; upgrading is always the remediation path, and the service can
refuse uploads from versions with known-dangerous defects while keeping the
data safe locally.

## Verifying what you run

Releases ship with checksums and build provenance attestations so you can
verify a downloaded binary was built from this repository's source — see
the README for the verification commands.
