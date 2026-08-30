package lifecycle

import (
	"errors"
	"fmt"

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
		fmt.Fprintln(io.Out, "Not signed in; run `trajector login` first. Captured data is kept.")
	case upload.UpgradeRequired:
		fmt.Fprintf(io.Out, "Uploads are paused: the service requires trajector %s or newer (this is %s).\n", reply.MinClientVersion, m.deps.Version)
		if reply.UpgradeMessage != "" {
			fmt.Fprintf(io.Out, serviceSays+"\n", reply.UpgradeMessage)
		}
		// The kept-data line stands alone: a user reading a pause needs
		// to know nothing was lost before they read what to do about it.
		fmt.Fprintln(io.Out, "Captured data is kept.")
		fmt.Fprintln(io.Out, upgradeHint+retry(force, " Or retry with --force."))
	case upload.AuthorizationRequired:
		fmt.Fprintln(io.Out, authorizationPaused)
		if reply.AuthorizationMessage != "" {
			fmt.Fprintf(io.Out, serviceSays+"\n", reply.AuthorizationMessage)
		}
		fmt.Fprintln(io.Out, "Captured data is kept.")
		// --force is offered here where the upgrade pause offers an
		// install, because it is the recovery: the user fixes this in a
		// browser, nothing on this machine changes, and without --force
		// they would wait out a flush cycle for uploads that could go now.
		fmt.Fprintln(io.Out, authorizeHint(reply.AuthorizeURL)+retry(force, " Then retry with --force."))
	case upload.Deferred:
		fmt.Fprintln(io.Out, "The service asked to slow down; uploads resume automatically."+retry(force, " Use --force to try now."))
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
func retry(force bool, offer string) string {
	if force {
		return ""
	}
	return offer
}
