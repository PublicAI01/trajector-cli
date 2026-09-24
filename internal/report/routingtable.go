package report

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"

	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// The routing table records which projects are enabled. When it exists
// and cannot be read, which projects those are is unknown, and unknown
// is not "none enabled": no token resolves, so traffic is forwarded and
// nothing is recorded, on every project of the device at once.
//
// No command of trajector's ends it. The file is the only record of
// the grants, so moving or rewriting it is the user's decision, and a
// fix line, which carries a command and nothing else, would send the
// user to a command that cannot succeed: every command that writes the
// table must first read it. The steps are stated under the why, in the
// order they run.
const (
	tableUnreadableHeadline = "recording is stopped: the routing table could not be read"
	tableUnreadableMoveStep = "Move that file aside yourself; no command of trajector's moves or rewrites it."
	tableUnreadableNextStep = "Then run `trajector enable` in each project you enabled."
)

// An injection of trajector's writes over the ANTHROPIC_BASE_URL a
// project keeps in its own settings file, and the grant is then the only
// copy of that value. With the grant gone, nothing on this device can
// tell a relay from the official endpoint, so the user puts the value
// back before enable runs. The routing-table steps, enable's refusal,
// and doctor's finding all state this from here.
const (
	restoreRelayStep    = "For a project that sends its traffic through a relay: write the relay's URL back to ANTHROPIC_BASE_URL in its .claude/settings.local.json. An earlier copy of the routing table records it as that project's `upstream`."
	restoreOfficialStep = "For a project that uses the official endpoint: delete that ANTHROPIC_BASE_URL line."
)

// BaseURLRestoreSteps are the steps, in order, that put back what an
// injection without a grant wrote over.
func BaseURLRestoreSteps() []string {
	return []string{restoreRelayStep, restoreOfficialStep}
}

// tableUnreadableWhy names the file and the failure. A failure that
// carries the path already is stated without it, so the path is not
// printed twice on one line.
func tableUnreadableWhy(e *routing.UnreadableError) string {
	cause := e.Err
	if pathErr, ok := errors.AsType[*fs.PathError](cause); ok {
		cause = pathErr.Err
	}
	return fmt.Sprintf("reading %s failed (%v), so which projects are enabled is unknown and none is recorded", e.Path, cause)
}

// tableUnreadableFact is the why for a surface that carries one value
// per fact, empty when the table was read.
func tableUnreadableFact(e *routing.UnreadableError) string {
	if e == nil {
		return ""
	}
	return tableUnreadableWhy(e)
}

// tableUnreadableProblem is the one line every surface states for a
// routing table that cannot be read.
func tableUnreadableProblem(e *routing.UnreadableError) line {
	return line{
		severity: severityError,
		text:     tableUnreadableHeadline,
		why:      tableUnreadableWhy(e),
		details:  slices.Concat([]string{tableUnreadableMoveStep}, BaseURLRestoreSteps(), []string{tableUnreadableNextStep}),
	}
}

// TableUnreadable writes that line for a command that could not run
// because the table could not be read: what is left to its caller is
// the exit code.
func TableUnreadable(w io.Writer, style Style, e *routing.UnreadableError) {
	render(w, style, []line{tableUnreadableProblem(e)}, layout{})
}

// doctorTable records it among a doctor run's findings. doctor repairs
// nothing about it, for the reason the steps above state.
func doctorTable(f *Findings, st ProjectStatus) {
	if st.TableUnreadable != nil {
		f.take(tableUnreadableProblem(st.TableUnreadable))
	}
}
