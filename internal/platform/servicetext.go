package platform

import (
	"strings"
	"unicode"
)

// maxServiceTextRunes bounds one piece of service-supplied text. The
// limit is generous for a sentence and small enough that no single
// message can scroll the rest of a status report off the screen.
const maxServiceTextRunes = 400

// SafeServiceText makes a string the service chose safe to print on a
// terminal line.
//
// The service can put arbitrary text in a notice or an upgrade message,
// and the client prints those words next to its own. Printed verbatim
// they are not just text: escape sequences move the cursor, repaint
// earlier lines, and set colors, so a service — or anything that can
// answer as one — could forge output the user would read as the
// client's own report. Bidi and zero-width formatting characters do the
// same to a single line without any escape at all.
//
// So only graphic runes survive. Everything else — control characters
// including ESC, line and paragraph separators, formatting characters —
// becomes a space, runs of whitespace collapse to one, and the result
// is one line, trimmed and length-capped. A message that needed a line
// break loses its shape; that is the correct trade against a message
// that can draw anywhere on the screen.
func SafeServiceText(s string) string {
	// Invalid encoding is dropped rather than replaced: a run of U+FFFD
	// is noise the user cannot act on.
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsGraphic(r) {
			return r
		}
		return ' '
	}, s)
	s = strings.Join(strings.Fields(s), " ")

	runes := []rune(s)
	if len(runes) > maxServiceTextRunes {
		// The ellipsis says the text was cut, so a truncated sentence is
		// not read as the whole of what the service said.
		return string(runes[:maxServiceTextRunes]) + "…"
	}
	return s
}
