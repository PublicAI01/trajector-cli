package platform_test

import (
	"strings"
	"testing"
	"unicode"

	"github.com/PublicAI01/trajector-cli/internal/platform"
)

func TestSafeServiceTextKeepsOrdinaryProse(t *testing.T) {
	for _, s := range []string{
		"Upload format 0.1.x is no longer accepted.",
		"请在 9 月 1 日前升级到 0.2.0。",
		"Contact support@example.com — quote ticket #4821.",
	} {
		if got := platform.SafeServiceText(s); got != s {
			t.Errorf("SafeServiceText(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestSafeServiceTextDisarmsTextThatCouldDrawOnTheTerminal(t *testing.T) {
	// Each of these, printed verbatim, does something other than put
	// characters on one line: it repaints the line, moves the cursor,
	// breaks the line in two, or reverses what the eye reads. Any of
	// them lets the service forge output the user reads as the client's
	// own report.
	cases := []struct {
		name string
		in   string
	}{
		{"ansi color", "urgent \x1b[31mupgrade\x1b[0m now"},
		{"cursor home and clear", "upgrade\x1b[H\x1b[2Jtrajector: everything is fine"},
		{"carriage return overwrite", "upgrade required\rnothing to do here"},
		{"newline forges a second line", "upgrade\nStatus: OK, nothing to do"},
		{"line separator", "upgrade\u2028Status: OK"},
		{"bidi override", "upgrade \u202eelbadaerru\u202c"},
		{"zero width space", "upgrade\u200b required"},
		{"bell", "upgrade\a\a\a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := platform.SafeServiceText(c.in)
			for _, r := range got {
				if r != ' ' && !unicode.IsGraphic(r) {
					t.Fatalf("SafeServiceText(%q) = %q, which still holds %U", c.in, got, r)
				}
			}
			if strings.ContainsAny(got, "\x1b\r\n\a\u2028\u202e\u202c\u200b") {
				t.Fatalf("SafeServiceText(%q) = %q, want the dangerous runes gone", c.in, got)
			}
			if strings.Contains(got, "  ") || got != strings.TrimSpace(got) {
				t.Fatalf("SafeServiceText(%q) = %q, want one trimmed line without runs of spaces", c.in, got)
			}
			if !strings.Contains(got, "upgrade") {
				t.Fatalf("SafeServiceText(%q) = %q, want the words still readable", c.in, got)
			}
		})
	}
}

func TestSafeServiceTextMakesAHiddenRuneInsideAWordVisible(t *testing.T) {
	// A zero-width character becomes a space rather than nothing, so the
	// word visibly breaks. Dropping it would repair the disguise: the
	// user would read an ordinary word and never learn one was hidden
	// inside what the service sent.
	if got := platform.SafeServiceText("up\u200bgrade"); got != "up grade" {
		t.Fatalf("SafeServiceText = %q, want the seam to show", got)
	}
}

func TestSafeServiceTextCapsWhatOneMessageCanOccupy(t *testing.T) {
	// Without a cap, one message scrolls the rest of a status report off
	// the screen — the same forgery by other means.
	got := platform.SafeServiceText(strings.Repeat("x", 10_000))
	if n := len([]rune(got)); n > 500 {
		t.Fatalf("SafeServiceText kept %d runes, want a message that cannot fill the screen", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("SafeServiceText = %q, want a mark that it was cut", got)
	}
}

func TestSafeServiceTextDropsInvalidEncoding(t *testing.T) {
	got := platform.SafeServiceText("upgrade \xff\xfe now")
	if got != "upgrade now" {
		t.Fatalf("SafeServiceText = %q, want the bad bytes gone and the words left", got)
	}
}

func TestSafeServiceTextOfNothingIsNothing(t *testing.T) {
	// Callers test the result for emptiness to decide whether to print a
	// line at all; text that was only escapes must not print a blank one.
	for _, s := range []string{"", "   ", "\x1b", "\n\t\r", "\u200b\u202e"} {
		if got := platform.SafeServiceText(s); got != "" {
			t.Errorf("SafeServiceText(%q) = %q, want empty", s, got)
		}
	}
}
