#!/bin/sh
# Coverage gate. Thresholds are ratchets: they may be raised, never
# lowered.
set -eu

REPO_MIN=87
CORE_MIN=85
# Core packages (capture and SSE reassembly, redaction, routing, spool,
# upload, consent lifecycle, and what the surfaces render) are added here
# as they land. A package joins once its own tests hold it above the
# floor without a knob that exists only for the tests.
CORE_PACKAGES="internal/apiproxy internal/capture internal/envelope internal/routing internal/spool internal/lifecycle internal/redact internal/report internal/upload"

go test -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...

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
core_paths=$(for pkg in $CORE_PACKAGES; do printf './%s ' "$pkg"; done)
go test -covermode=atomic -coverprofile=coverage.pkg.out $core_paths >/dev/null

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
