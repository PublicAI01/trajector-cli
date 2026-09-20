package report

import (
	"io"
	"os"
	"strings"
)

// severity is how much a line asks of the reader. Three levels carry a
// marker — something is broken, something needs watching, something
// passed — and the two others are shades of them: a repair this run
// already made reads as passed, and a note asks for nothing at all.
// Every surface uses this one scale, so a user who learns it on
// `status` reads `doctor` without learning a second one.
type severity int

const (
	// severityOK is a check that passed.
	severityOK severity = iota
	// severityFixed is a repair this run already made.
	severityFixed
	// severityNote is something to read that asks for no action.
	severityNote
	// severityWarning is something to watch that still records.
	severityWarning
	// severityError is something the user must act on.
	severityError
)

// label is the word a line opens with. The words are fixed and never
// translated: a reader who does not read English still learns five
// words, and every surface spells them the same way.
func (s severity) label() string {
	switch s {
	case severityFixed:
		return "fixed"
	case severityNote:
		return "note"
	case severityWarning:
		return "warning"
	case severityError:
		return "error"
	default:
		return "ok"
	}
}

// rank orders the lines within one section: what is broken is read
// first, what needs watching next, and everything else keeps the order
// the surface wrote it in.
func (s severity) rank() int {
	switch s {
	case severityError:
		return 0
	case severityWarning:
		return 1
	default:
		return 2
	}
}

// Style is how much of a terminal a surface may use: a glyph before
// each marked line, and colour on the marker. Its zero value is what a
// pipe, a file, and every test get — plain ASCII with no escape
// sequences — so a surface that was handed no style still prints
// correctly.
type Style struct {
	// tty says a terminal is reading, which is what a glyph needs at
	// all; unicode says that terminal states a UTF-8 encoding, which
	// decides which glyph set it gets.
	tty     bool
	unicode bool
	color   bool
}

// noColorEnv, when set to anything at all, turns colour off. It is the
// cross-tool spelling of that choice, so trajector honours it rather
// than asking for one of its own.
const noColorEnv = "NO_COLOR"

// dumbTerminal is the terminal that states it renders no escape
// sequences.
const dumbTerminal = "dumb"

// localeEnv is read in the order POSIX resolves a locale in: the
// override first, then the character-class setting, then the default.
// The first one set decides, even when it is set to something this
// build does not recognize.
var localeEnv = []string{"LC_ALL", "LC_CTYPE", "LANG"}

// DetectStyle decides what w can show. Glyphs need both a terminal and
// a locale that states it encodes UTF-8: a terminal told to expect
// another encoding renders them as mojibake, which is worse than the
// ASCII marker it renders correctly. Colour needs a terminal that was
// not asked to go without.
func DetectStyle(w io.Writer, lookupEnv func(string) (string, bool)) Style {
	if !isTerminal(w) {
		return Style{}
	}
	term, _ := lookupEnv("TERM")
	_, noColor := lookupEnv(noColorEnv)
	return Style{
		tty:     true,
		unicode: utf8Locale(lookupEnv),
		color:   !noColor && term != dumbTerminal,
	}
}

// WithoutColor is this style with colour off, which is what --no-color
// leaves. The glyphs stay: they carry the severity where colour cannot,
// and a user who turned colour off did not ask to read less.
func (s Style) WithoutColor() Style {
	s.color = false
	return s
}

// isTerminal reports whether w is a character device. A surface writing
// anywhere else — a pipe, a file, a test buffer — gets plain text.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// utf8Locale reads whether the locale states a UTF-8 encoding. Both
// spellings the environment uses are accepted, in any case.
func utf8Locale(lookupEnv func(string) (string, bool)) bool {
	for _, key := range localeEnv {
		v, ok := lookupEnv(key)
		if !ok || v == "" {
			continue
		}
		v = strings.ToLower(v)
		return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
	}
	return false
}

// The escape sequences one marker is written in, and the one that ends
// it. Only marker writes them, and only for a style that says the
// terminal reads them.
const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiGreen  = "\x1b[32m"
)

// glyph is the one character that stands before a marked line. The
// UTF-8 set is used only where the locale states that encoding; the
// ASCII set says the same thing on every terminal.
func (s Style) glyph(sev severity) string {
	if !s.tty {
		return ""
	}
	switch sev {
	case severityError:
		if s.unicode {
			return "✗"
		}
		return "x"
	case severityWarning:
		return "!"
	case severityOK, severityFixed:
		if s.unicode {
			return "✓"
		}
		return "+"
	default:
		return ""
	}
}

// color wraps text in the severity's colour, or returns it unchanged
// where the terminal was not asked for colour. A note is never
// coloured: it asks for nothing, and colouring it would spend the
// reader's attention on it.
func (s Style) paint(sev severity, text string) string {
	if !s.color {
		return text
	}
	switch sev {
	case severityError:
		return ansiRed + text + ansiReset
	case severityWarning:
		return ansiYellow + text + ansiReset
	case severityOK, severityFixed:
		return ansiGreen + text + ansiReset
	default:
		return text
	}
}

// marker is what a line of this severity opens with, ending in a
// space: the glyph where a terminal shows one, then the word and its
// colon. A surface that writes an unmarked line asks for no marker
// rather than for an empty one.
func (s Style) marker(sev severity) string {
	marker := sev.label() + ":"
	if g := s.glyph(sev); g != "" {
		marker = g + " " + marker
	}
	return s.paint(sev, marker) + " "
}
