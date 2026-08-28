package platform

import (
	"net/url"
	"strings"
	"unicode"
)

// maxServiceURLLen bounds a service-supplied URL in bytes. It is
// generous for any address a service would hand out and small enough
// that one cannot fill a terminal.
const maxServiceURLLen = 512

// SafeServiceURL returns a service-supplied URL when it is one the
// client can print and the user can open, and empty when it is not.
//
// It exists because SafeServiceText must not be used here. That function
// caps its result and appends an ellipsis, which is right for a sentence
// and wrong for an address: a truncated URL is a link that opens
// nothing, which is worse than no link at all — the user follows it,
// lands nowhere, and has no way to tell whether the instruction or the
// address was the problem. So this one never truncates. A URL that does
// not fit, does not parse, or is not an absolute https address is
// discarded whole, and the caller falls back to wording of its own.
//
// The remaining checks are about what the printed line can be made to
// look like. Userinfo is rejected because it lets an address read as one
// host while resolving to another, and non-graphic runes are rejected
// because they are what makes a printed line able to lie about itself —
// the same reason SafeServiceText disarms them, reached here by
// refusing rather than rewriting, since a URL cannot survive rewriting.
func SafeServiceURL(s string) string {
	if s == "" || len(s) > maxServiceURLLen {
		return ""
	}
	if strings.ToValidUTF8(s, "") != s {
		return ""
	}
	for _, r := range s {
		if !unicode.IsGraphic(r) || unicode.IsSpace(r) {
			return ""
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	if u.Scheme != "https" || u.Host == "" || u.Opaque != "" || u.User != nil {
		return ""
	}
	return s
}
