package report

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// line is one line of a report: what it says, how much it asks of the
// reader, and — where one command ends it — the why and the fix it is
// laid out in. Every surface states a problem in these parts, so a user
// who learns the shape on one surface reads the others without learning
// a second one.
type line struct {
	severity severity
	text     string
	// why is the one sentence that says what went wrong, and fix the
	// one command that ends it. The command stands alone on its own
	// line so it can be copied whole, and nothing but the command is on
	// that line.
	why string
	fix string
	// details carry what follows the fix line. A remedy of several
	// commands puts the first in fix, because that line is copied
	// whole, and the rest here in the order they run.
	details []string
}

// lines accumulates what one surface established, in the order it
// established it. How the lines are read is decided at render time, so
// no call site has to know where its fact will end up.
type lines struct {
	all []line
}

// add states a fact of one severity that names no command.
func (l *lines) add(sev severity, format string, a ...any) {
	l.all = append(l.all, line{severity: sev, text: fmt.Sprintf(format, a...)})
}

// take appends a line composed elsewhere, so a problem more than one
// surface states stays one value rather than one spelling each.
func (l *lines) take(one line) { l.all = append(l.all, one) }

// problemFix states something that stopped working in the three parts
// the user acts on: what is wrong, why it is wrong, and the one command
// that ends it. The headline carries no command, so the two never state
// it twice.
func (l *lines) problemFix(headline, why, fix string) {
	l.take(line{severity: severityError, text: headline, why: why, fix: fix})
}

// warnFix states something to watch that still records and that one
// command ends. It takes the same three parts as a problem: a warning
// with a command and no reason for it sends the user to a command
// without saying what it repairs.
func (l *lines) warnFix(headline, why, fix string) {
	l.take(line{severity: severityWarning, text: headline, why: why, fix: fix})
}

// detail attaches a follow-up line to the line stated last.
func (l *lines) detail(format string, a ...any) {
	last := &l.all[len(l.all)-1]
	last.details = append(last.details, fmt.Sprintf(format, a...))
}

// count is how many lines carry one severity, which is what an exit
// code is read from.
func (l *lines) count(sev severity) int {
	n := 0
	for _, one := range l.all {
		if one.severity == sev {
			n++
		}
	}
	return n
}

// layout is how one surface reads its lines: whether what is broken is
// read first, and whether every line opens with a marker or only the
// ones that ask for something.
type layout struct {
	sorted    bool
	markEvery bool
}

// render writes every line in the layout its surface reads them in. The
// sort is stable, so lines that ask the same amount keep the order the
// facts were established in. Where only what asks for something is
// marked, a line that asks for nothing carries no marker at all: a
// screen where every line is marked has no marked line.
func render(w io.Writer, style Style, all []line, lay layout) {
	if lay.sorted {
		slices.SortStableFunc(all, func(a, b line) int {
			return a.severity.rank() - b.severity.rank()
		})
	}
	for _, one := range all {
		marker := ""
		if lay.markEvery || one.severity == severityError || one.severity == severityWarning {
			marker = style.marker(one.severity)
		}
		fmt.Fprintf(w, "  %s%s\n", marker, one.text)
		if one.why != "" {
			fmt.Fprintf(w, "    why:  %s\n", one.why)
		}
		if one.fix != "" {
			fmt.Fprintf(w, "    fix:  %s\n", one.fix)
		}
		for _, detail := range one.details {
			fmt.Fprintf(w, "      %s\n", detail)
		}
	}
}

// section accumulates one heading's lines. The lines are written in the
// order the facts are established and read in the order of what they
// ask for, which is why the sort happens here and not at each call
// site.
type section struct {
	name string
	lines
}

// linef states a fact that asks for nothing.
func (s *section) linef(format string, a ...any) { s.add(severityNote, format, a...) }

// warnf states something to watch that still records.
func (s *section) warnf(format string, a ...any) { s.add(severityWarning, format, a...) }

// problem states something that stopped working, with the one sentence
// that says why and the one command that ends it.
func (s *section) problem(headline, why, fix string) { s.problemFix(headline, why, fix) }

// errors counts the lines that stopped something working, which is what
// the exit code is read from.
func (s *section) errors() int { return s.count(severityError) }

// render writes the heading and every line under it, what is broken
// first. status marks only the lines that ask for something: most of
// what it prints is a plain fact about the device.
func (s *section) render(w io.Writer, style Style) {
	fmt.Fprintf(w, "\n%s\n", s.name)
	render(w, style, s.all, layout{sorted: true})
}

// trimPeriod drops a trailing period from a headline. A three-line
// problem ends its headline without one: the why below it is the
// sentence, and the headline is its title.
func trimPeriod(s string) string { return strings.TrimSuffix(s, ".") }
