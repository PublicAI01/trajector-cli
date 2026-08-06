package lifecycle

import (
	"errors"
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// Upload triggers one flush through the resident proxy — the machine's
// one flusher — starting the proxy if nothing is listening. force
// bypasses the upload thresholds.
func (m *Machine) Upload(force bool, io IO) error {
	if err := m.proxy.Ensure(); err != nil {
		return err
	}
	reply, err := m.proxy.Flush(force)
	if err != nil {
		return err
	}
	if reply.Error != "" {
		if reply.Batches > 0 {
			fmt.Fprintf(io.Out, "Uploaded %d batch(es), %d rawcall(s) before failing.\n", reply.Batches, reply.Records)
		}
		return errors.New(reply.Error)
	}
	switch reply.Outcome {
	case upload.Uploaded:
		fmt.Fprintf(io.Out, "Uploaded %d batch(es), %d rawcall(s).\n", reply.Batches, reply.Records)
	case upload.Empty:
		fmt.Fprintln(io.Out, "Nothing to upload.")
	case upload.BelowThreshold:
		fmt.Fprintln(io.Out, "Below the upload thresholds; use --force to upload anyway.")
	case upload.Paused:
		fmt.Fprintln(io.Out, "Not signed in; run `trajector login` first. Captured data is kept.")
	case upload.UpgradeRequired:
		fmt.Fprintf(io.Out, "Uploads are paused: the service requires trajector %s or newer (this is %s).\n", reply.MinClientVersion, m.deps.Version)
		fmt.Fprintln(io.Out, "Captured data is kept. Upgrade trajector to resume, or retry with --force.")
	case upload.Deferred:
		fmt.Fprintln(io.Out, "The service asked to slow down; uploads resume automatically. Use --force to try now.")
	default:
		fmt.Fprintf(io.Out, "Flush finished: %s\n", reply.Outcome)
	}
	return nil
}
