package lifecycle

import (
	"fmt"
	"math"

	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// RequeueRejected moves quarantined batches back into the spool so the
// next flush uploads them again. The user asked for this by name, so
// the spool quota does not apply: the quota bounds what recording may
// accumulate, not what already-captured data may return, and refusing
// here would strand records that can only leave through the spool.
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

	sp, err := spool.Create(m.deps.Layout.SpoolDir(), math.MaxInt64)
	if err != nil {
		return err
	}
	for _, id := range ids {
		rej, moved, err := upload.Requeue(m.deps.Layout.RejectedDir(), sp, id)
		if err != nil {
			if moved > 0 {
				fmt.Fprintf(io.Out, "Requeued %d rawcall(s) from batch %s before failing.\n", moved, id)
			}
			return err
		}
		line := fmt.Sprintf("Requeued %d rawcall(s) from batch %s", moved, id)
		if rej.Details != "" {
			line += fmt.Sprintf(" (rejected as: %s)", rej.Details)
		}
		fmt.Fprintln(io.Out, line+".")
	}
	fmt.Fprintln(io.Out, "They will upload with the next flush; run `trajector upload --force` to try now.")
	return nil
}
