package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/routing"
)

func TestASectionReadsWhatIsBrokenFirst(t *testing.T) {
	s := &section{name: "Spool"}
	s.linef("2.0 GiB of 2.0 GiB used.")
	s.warnf("something to watch")
	s.problem("recording is stopped", "the spool is full", "trajector upload --force")
	s.linef("a second plain fact")
	s.warnf("a second thing to watch")

	var b bytes.Buffer
	s.render(&b, Style{})
	want := "\nSpool\n" +
		"  error: recording is stopped\n" +
		"    why:  the spool is full\n" +
		"    fix:  trajector upload --force\n" +
		"  warning: something to watch\n" +
		"  warning: a second thing to watch\n" +
		"  2.0 GiB of 2.0 GiB used.\n" +
		"  a second plain fact\n"
	if got := b.String(); got != want {
		t.Errorf("section =\n%q\nwant\n%q", got, want)
	}
	if s.errors() != 1 {
		t.Errorf("errors = %d, want 1", s.errors())
	}
}

func TestProblemFixPrintsTheCommandOnALineOfItsOwn(t *testing.T) {
	f := &Findings{}
	f.ProblemFix("recording is paused everywhere", "this device is signed out", "trajector login")
	f.Detail("Pairing is what lifts it.")
	f.OK("capture spool writable")

	var b bytes.Buffer
	f.Render(&b, Style{})
	want := "  error: recording is paused everywhere\n" +
		"    why:  this device is signed out\n" +
		"    fix:  trajector login\n" +
		"      Pairing is what lifts it.\n" +
		"  ok: capture spool writable\n"
	if got := b.String(); got != want {
		t.Errorf("findings =\n%q\nwant\n%q", got, want)
	}
	if f.Problems() != 1 {
		t.Errorf("problems = %d, want 1", f.Problems())
	}
}

// The one sentence a surface with a single line prints and the three
// lines a surface with room lays out are the same two halves joined,
// for every pause this build knows.
func TestPauseFixIsTheCommandTheReasonNames(t *testing.T) {
	for _, reason := range routing.AllPauseReasons() {
		why, fix := reason.Why(), reason.Fix()
		if why == "" || len(fix) == 0 {
			t.Fatalf("%s: why = %q, fix = %q, want both stated", reason, why, fix)
		}
		want := why + "; run `" + strings.Join(fix, "`, then `") + "`"
		if explained := reason.Explain(); explained != want {
			t.Errorf("%s: explained as %q, want %q", reason, explained, want)
		}
		if strings.Contains(why, "run ") || strings.Contains(why, "`") {
			t.Errorf("%s: why = %q, want no command in it", reason, why)
		}
		for _, command := range fix {
			if strings.ContainsAny(command, "`()") {
				t.Errorf("%s: fix line %q, want a command a user can copy whole", reason, command)
			}
		}
		lineWhy, lineFix := pauseWhyFixFor(ProjectStatus{PauseReason: reason})
		if lineWhy != why || lineFix != fix[0] {
			t.Errorf("%s: status lays it out as %q / %q, want %q / %q", reason, lineWhy, lineFix, why, fix[0])
		}
	}
}
