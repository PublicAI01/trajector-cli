# Contributing

Thanks for looking under the hood — that is the point of this repository
being open.

## Building and testing

Go (version per `go.mod`) is the only requirement:

```sh
go build ./...
go test ./...            # the full suite; -race is worth the time
./scripts/check_coverage.sh   # the CI coverage gate, runnable locally
```

Tests drive the system at its three seams — the CLI in a sandboxed HOME, the
proxy over real HTTP against a fake upstream, and the uploader against a
fake service. Inside those seams there are no mocks: tests assert observable
behavior (exit codes, output, files on disk, bytes on the wire, documented
on-disk formats), never implementation details. New code should follow that
shape, tests first where practical.

## Gates

CI enforces, and you can run locally:

- `gofmt -l .` — clean formatting.
- `go vet ./...`
- `./scripts/check_vocabulary.sh` — see below.
- `./scripts/check_coverage.sh` — repo ≥ 80%, core packages ≥ 85%. The
  thresholds are ratchets: they may be raised, never lowered.

## Vocabulary and comments

The client's domain language is deliberately small: rawcall, capture,
spool, batch, consent, redaction, envelope. Service-side business concepts
do not belong in this codebase — in identifiers, comments, strings, or
logs — and the vocabulary check enforces a denylist. Prefer neutral wording
(integrity, observed-truth).

Comments state constraints and invariants the code cannot express ("recording
failures must never interrupt forwarding"), not narration. Keep them
self-contained; do not reference documents a reader here cannot open.

## Pull requests

- Plain, descriptive commit messages; English identifiers and comments.
- One concern per PR where possible; include the tests that pin the new
  behavior.
- Anything touching redaction, routing, credential handling, or upload
  acknowledgement gets extra scrutiny — see [SECURITY.md](SECURITY.md) for
  why, and open an issue first for larger changes.
