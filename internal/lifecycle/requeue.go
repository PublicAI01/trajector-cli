package lifecycle

import (
	"errors"
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// RequeueRejected moves quarantined batches back into the spool so the
// next flush uploads them again.
func (m *Machine) RequeueRejected(batchID string, all bool, io IO) error {
	ids := []string{batchID}
	if all {
		rejected, err := upload.ListRejected(m.deps.Layout.RejectedDir())
		if err != nil {
			return err
		}
		if len(rejected) == 0 {
			fmt.Fprintln(io.Out, "No rejected batches; nothing to requeue.")
			return nil
		}
		ids = ids[:0]
		for _, b := range rejected {
			ids = append(ids, b.BatchID)
		}
	}

	sp, err := m.spoolUnbounded()
	if err != nil {
		return err
	}
	// One failing batch must not strand the ones behind it: every batch
	// is attempted, and whatever could not move is reported at the end.
	var failed []error
	requeued := 0
	for _, id := range ids {
		rej, moved, err := upload.Requeue(m.deps.Layout.RejectedDir(), sp, id)
		requeued += moved
		if err != nil {
			if moved > 0 {
				fmt.Fprintf(io.Out, "Requeued %d rawcall(s) from batch %s before it failed.\n", moved, id)
			}
			failed = append(failed, err)
			continue
		}
		fmt.Fprintf(io.Out, "Requeued %d rawcall(s) from batch %s%s.\n", moved, id, rejectionSuffix(rej))
	}
	if requeued > 0 {
		fmt.Fprintln(io.Out, "They will upload with the next flush; run `trajector upload --force` to try now.")
	}
	return errors.Join(failed...)
}

// rejectionSuffix names the recorded reason on the line a command
// prints about one quarantined batch, so requeue and discard describe
// the same record the same way.
func rejectionSuffix(rej upload.Rejection) string {
	if rej.Details == "" {
		return ""
	}
	return fmt.Sprintf(" (rejected as: %s)", rej.Details)
}
