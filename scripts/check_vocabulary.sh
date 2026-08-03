#!/bin/sh
# Vocabulary boundary check. The client's domain language is limited to
# rawcall / capture / spool / batch / consent / redaction / envelope;
# terms from outside that boundary must not appear anywhere in Go
# sources — comments, identifiers, strings, or test names. Use neutral
# wording (integrity, observed-truth) instead.
set -eu

pattern='buyer|purchaser|settlement|payout|monetiz|revenue|fraud|cheat'

hits=$(grep -rniE --include='*.go' "$pattern" . || true)
if [ -n "$hits" ]; then
	echo "vocabulary check: out-of-boundary terms found in Go sources:" >&2
	printf '%s\n' "$hits" >&2
	exit 1
fi
echo "vocabulary check: ok"
