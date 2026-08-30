package proxylife

import "github.com/PublicAI01/trajector-cli/internal/semver"

// ReuseReason is the takeover rule in one sentence. Every surface that
// reports a differing-version proxy left serving prints this spelling.
const ReuseReason = "only a strictly older release is replaced"

// supersedes reports whether a proxy announcing version ours may take
// over a running proxy announcing version holder: only when both are
// semantic versions and holder's is strictly older. An equal or newer
// holder is reused, and a pair with no defined order — either side a
// dev or otherwise non-semver build — is never acted on: two
// coexisting builds that each replaced the other would trade the port
// on every session start, cutting live streams at each turn. It is
// unexported because the decision it answers belongs to Verdict, not
// to the callers of one: a surface asks a verdict whether it is
// Serving or Replaceable and never compares versions itself.
func supersedes(ours, holder string) bool {
	order, ok := semver.Compare(holder, ours)
	return ok && order < 0
}
