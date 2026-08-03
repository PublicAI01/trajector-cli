#!/bin/sh
# Coverage gate. Thresholds are ratchets: they may be raised, never
# lowered.
set -eu

REPO_MIN=75
CORE_MIN=85
# Core packages (capture and SSE reassembly, redaction, routing, spool,
# upload, consent lifecycle) are added here as they land.
CORE_PACKAGES="internal/apiproxy internal/capture internal/envelope internal/routing internal/spool internal/lifecycle internal/redact internal/upload"

go test -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...

percent() {
	go tool cover -func="$1" | awk '/^total:/ { sub(/%/, "", $3); print $3 }'
}

fail=0
total=$(percent coverage.out)
echo "repo coverage: ${total}% (minimum ${REPO_MIN}%)"
if awk "BEGIN { exit !($total < $REPO_MIN) }"; then
	echo "coverage gate: repo coverage ${total}% is below ${REPO_MIN}%" >&2
	fail=1
fi

for pkg in $CORE_PACKAGES; do
	go test -covermode=atomic -coverprofile=coverage.pkg.out "./$pkg" >/dev/null
	pct=$(percent coverage.pkg.out)
	echo "core package ${pkg}: ${pct}% (minimum ${CORE_MIN}%)"
	if awk "BEGIN { exit !($pct < $CORE_MIN) }"; then
		echo "coverage gate: ${pkg} coverage ${pct}% is below ${CORE_MIN}%" >&2
		fail=1
	fi
done

exit $fail
