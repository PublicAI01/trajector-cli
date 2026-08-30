package lifecycle

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// Upload triggers one flush through the resident proxy — the machine's
// one flusher — starting the proxy if nothing is listening. force
// bypasses the upload thresholds.
//
// The reply's outcome decides before its error does, so a classified
// flush reads and exits the same on the first encounter, on repeats,
// and under --force. A pause the service asked for (upgrade required,
// deferral) is a working state, not a command failure; a rejected
// batch waits quarantined for the user, so it stays one.
func (m *Machine) Upload(force bool, io IO) error {
	if err := m.proxy.Ensure(); err != nil {
		return err
	}
	reply, err := m.proxy.Flush(force)
	if err != nil {
		return err
	}
	if reply.Batches > 0 {
		fmt.Fprintf(io.Out, "Uploaded %d batch(es), %d rawcall(s).\n", reply.Batches, reply.Records)
	}
	if n := setAsideUnreadable(reply.SetAside); n > 0 {
		fmt.Fprintf(io.Out, "Set aside %d unreadable rawcall(s); they were never sent. Run `trajector doctor` to inspect them.\n", n)
	}
	switch reply.Outcome {
	case upload.Uploaded:
		// the acknowledged-count line above is the whole report
	case upload.Empty:
		fmt.Fprintln(io.Out, "Nothing to upload.")
	case upload.BelowThreshold:
		fmt.Fprintln(io.Out, "Below the upload thresholds; use --force to upload anyway.")
	case upload.Paused:
		explainStanding(io, reply.Standing, reply.Standing.Remedy())
	case upload.UpgradeRequired:
		// The kept-data line stands alone: a user reading a pause needs
		// to know nothing was lost before they read what to do about it.
		explainStanding(io, reply.Standing,
			offer(reply.Standing.Remedy(), retry(force, " Or retry with --force.")),
			"Captured data is kept.")
	case upload.AuthorizationRequired:
		// --force is offered here where the upgrade pause offers an
		// install, because it is the recovery: the user fixes this in a
		// browser, nothing on this machine changes, and without --force
		// they would wait out a flush cycle for uploads that could go now.
		explainStanding(io, reply.Standing,
			offer(reply.Standing.Remedy(), retry(force, " Then retry with --force.")),
			"Captured data is kept.")
	case upload.Deferred:
		// The remedy is itself the --force offer, so a run that already
		// used it is told the pause and nothing else.
		explainStanding(io, reply.Standing, retry(force, reply.Standing.Remedy()))
	case upload.Rejected:
		return errors.New(reply.Error)
	default:
		if reply.Error != "" {
			return errors.New(reply.Error)
		}
		fmt.Fprintf(io.Out, "Flush finished: %s\n", reply.Outcome)
	}
	return nil
}

// explainStanding reports one pause the way `upload` reports it: what
// is true, the service's own words if it supplied any, whatever this
// command adds about the data it still holds, and last the remedy. The
// first three sentences come from the standing itself, so the three
// surfaces that report a pause cannot word one differently.
func explainStanding(io IO, s upload.Standing, remedy string, reassurance ...string) {
	fmt.Fprintln(io.Out, s.Explain())
	if s.Message != "" {
		fmt.Fprintf(io.Out, report.ServiceSays+"\n", s.Message)
	}
	for _, line := range reassurance {
		fmt.Fprintln(io.Out, line)
	}
	if remedy != "" {
		fmt.Fprintln(io.Out, remedy)
	}
}

// offer appends this command's own follow-up to a standing's remedy,
// and stands in for the remedy when the standing carries none.
func offer(remedy, follow string) string {
	return strings.TrimSpace(remedy + follow)
}

// setAsideUnreadable counts the rawcalls a flush set aside as
// unreadable, reading each rejection's cause rather than assuming every
// set-aside is one.
func setAsideUnreadable(rejections []upload.Rejection) int {
	n := 0
	for _, rej := range rejections {
		if rej.Cause == upload.CauseUnreadable {
			n += rej.Records
		}
	}
	return n
}

// retry returns the sentence offering --force, or nothing when this run
// already used it. Both pauses that offer it are reachable under
// --force: the service can refuse a forced attempt outright, and it can
// ask a forced attempt to slow down. Repeating the offer there sends the
// user back to the switch they just held down, which cannot move either
// pause — only the service can.
func retry(force bool, sentence string) string {
	if force {
		return ""
	}
	return sentence
}
