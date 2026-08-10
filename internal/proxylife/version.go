package proxylife

import (
	"strconv"
	"strings"
)

// ReuseReason is the takeover rule in one sentence. Every surface that
// reports a differing-version proxy left serving prints this spelling.
const ReuseReason = "only a strictly older release is replaced"

// Supersedes reports whether a proxy announcing version ours may take
// over a running proxy announcing version holder: only when both are
// semantic versions and holder's is strictly older. An equal or newer
// holder is reused, and a pair with no defined order — either side a
// dev or otherwise non-semver build — is never acted on: two
// coexisting builds that each replaced the other would trade the port
// on every session start, cutting live streams at each turn.
func Supersedes(ours, holder string) bool {
	order, ok := Compare(holder, ours)
	return ok && order < 0
}

// Compare orders two release versions by semantic-version precedence,
// returning a negative number when a precedes b, zero when they are
// equal, and a positive number when a follows b. The second result is
// false when either side is not a semantic version — a dev build, say
// — and no order exists; the numeric result is meaningless then. This
// is the only version comparison in the repository: the port takeover
// rule and the choice of release to upgrade to answer from it.
func Compare(a, b string) (int, bool) {
	x, ok := parseVersion(a)
	if !ok {
		return 0, false
	}
	y, ok := parseVersion(b)
	if !ok {
		return 0, false
	}
	return x.compare(y), true
}

// semver is a parsed semantic version: the numeric core and the
// pre-release identifiers. Build metadata never affects precedence and
// is dropped at parse time.
type semver struct {
	core [3]uint64
	pre  []string
}

// parseVersion reads MAJOR.MINOR.PATCH with an optional leading "v",
// an optional pre-release, and optional build metadata. Anything else
// is not a semantic version and cannot be ordered.
func parseVersion(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v semver
	if i := strings.IndexByte(s, '-'); i >= 0 {
		for _, id := range strings.Split(s[i+1:], ".") {
			if !validIdentifier(id) {
				return semver{}, false
			}
			v.pre = append(v.pre, id)
		}
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	for i, p := range parts {
		n, ok := parseNumber(p)
		if !ok {
			return semver{}, false
		}
		v.core[i] = n
	}
	return v, true
}

func validIdentifier(id string) bool {
	if id == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '-') {
			return false
		}
	}
	return true
}

func parseNumber(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// compare orders v against o by semantic-version precedence: the
// numeric core first; then a pre-release sorts before its release;
// then pre-release identifiers left to right — numeric ones
// numerically and before alphanumeric ones, alphanumeric ones as ASCII
// strings — with a shorter identifier list before a longer one it
// prefixes.
func (v semver) compare(o semver) int {
	for i := range v.core {
		switch {
		case v.core[i] < o.core[i]:
			return -1
		case v.core[i] > o.core[i]:
			return 1
		}
	}
	switch {
	case len(v.pre) == 0 && len(o.pre) == 0:
		return 0
	case len(v.pre) == 0:
		return 1
	case len(o.pre) == 0:
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(o.pre); i++ {
		if c := compareIdentifier(v.pre[i], o.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.pre) < len(o.pre):
		return -1
	case len(v.pre) > len(o.pre):
		return 1
	}
	return 0
}

func compareIdentifier(a, b string) int {
	an, aNumeric := parseNumber(a)
	bn, bNumeric := parseNumber(b)
	switch {
	case aNumeric && bNumeric:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aNumeric:
		return -1
	case bNumeric:
		return 1
	}
	return strings.Compare(a, b)
}
