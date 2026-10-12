#!/bin/sh
# Coverage gate. Thresholds are ratchets: they may be raised, never
# lowered.
set -eu

REPO_MIN=87
CORE_MIN=85
# Core packages (capture and SSE reassembly, redaction, the rewrite of
# image and document blocks, routing, spool, batches, upload, consent
# lifecycle, the lines left for the next command (notice), session-file
# following, observing a git repository, the reading of one session
# line and the shape checks on what it reads,
# what the surfaces render, the served proxy, and atomic file writes and
# locks; selfupdate, which installs a newer release over this one) are
# added here as they land. A package joins once its own tests
# hold it above the floor without a knob that exists only for the tests.
CORE_PACKAGES="internal/apiproxy internal/batch internal/capture internal/mediablock internal/claudesettings internal/drift internal/envelope internal/follow internal/follow/discover internal/fsatomic internal/gitsnapshot internal/proxyserve internal/routing internal/selfupdate internal/sessionline internal/spool internal/lifecycle internal/notice internal/redact internal/report internal/upload"

go test -race -timeout 30m -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...

fail=0
total=$(go tool cover -func=coverage.out | awk '/^total:/ { sub(/%/, "", $3); print $3 }')
echo "repo coverage: ${total}% (minimum ${REPO_MIN}%)"
if awk "BEGIN { exit !($total < $REPO_MIN) }"; then
	echo "coverage gate: repo coverage ${total}% is below ${REPO_MIN}%" >&2
	fail=1
fi

# The core floors measure each package against its own tests. One
# invocation covers all of them: without -coverpkg each package is
# instrumented for its own test binary alone, so the combined profile
# carries the same per-package numbers that one run per package would
# produce, without the serial rebuilds.
set --
for pkg in $CORE_PACKAGES; do set -- "$@" "./$pkg"; done
go test -timeout 30m -covermode=atomic -coverprofile=coverage.pkg.out "$@"

module=$(go list -m)
for pkg in $CORE_PACKAGES; do
	pct=$(awk -v prefix="${module}/${pkg}/" '
		NR > 1 && index($1, prefix) == 1 && substr($1, length(prefix) + 1) !~ /\// {
			stmts += $2
			if ($3 > 0) covered += $2
		}
		END { printf "%.1f", stmts ? covered * 100 / stmts : 0 }
	' coverage.pkg.out)
	echo "core package ${pkg}: ${pct}% (minimum ${CORE_MIN}%)"
	if awk "BEGIN { exit !($pct < $CORE_MIN) }"; then
		echo "coverage gate: ${pkg} coverage ${pct}% is below ${CORE_MIN}%" >&2
		fail=1
	fi
done

exit $fail
