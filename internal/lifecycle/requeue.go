package lifecycle

import (
	"errors"
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// RequeueRejected moves quarantined batches back into the spool so the
// next flush uploads them again.
func (m *Machine) RequeueRejected(batchID string, all bool, io IO) error {
	ids, err := m.quarantinedBatches(batchID, all, "requeue", io)
	if err != nil || len(ids) == 0 {
		return err
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
				fmt.Fprintf(io.Out, "Requeued %d record(s) from batch %s before it failed.\n", moved, id)
			}
			failed = append(failed, err)
			continue
		}
		fmt.Fprintf(io.Out, "Requeued %d record(s) from batch %s%s.\n", moved, id, rejectionSuffix(rej))
	}
	if requeued > 0 {
		fmt.Fprintln(io.Out, "They will upload with the next flush; run `trajector upload --force` to try now.")
	}
	return errors.Join(failed...)
}

// quarantinedBatches resolves which batches a quarantine command acts
// on: the one the user named, or every batch held. It returns no ids
// when there is nothing held, having already said so under the verb of
// the command that asked, so no two commands can describe an empty
// quarantine differently.
func (m *Machine) quarantinedBatches(batchID string, all bool, verb string, io IO) ([]string, error) {
	if !all {
		return []string{batchID}, nil
	}
	rejected, err := upload.ListRejected(m.deps.Layout.RejectedDir())
	if err != nil {
		return nil, err
	}
	if len(rejected) == 0 {
		fmt.Fprintf(io.Out, "No rejected batches; nothing to %s.\n", verb)
		return nil, nil
	}
	ids := make([]string, 0, len(rejected))
	for _, b := range rejected {
		ids = append(ids, b.BatchID)
	}
	return ids, nil
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
