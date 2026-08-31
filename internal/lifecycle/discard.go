package lifecycle

import (
	"errors"
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// DiscardRejected deletes quarantined batches from this machine. It is
// the terminal half of the pair requeue opens: requeue refuses a batch
// whose records no longer read back as rawcalls, and those records have
// no other way out of the quarantine. The deletion cannot be undone, so
// a caller that has not already decided (confirmed false) is asked
// here, before anything is removed.
func (m *Machine) DiscardRejected(batchID string, all, confirmed bool, io IO) error {
	ids := []string{batchID}
	if all {
		rejected, err := upload.ListRejected(m.deps.Layout.RejectedDir())
		if err != nil {
			return err
		}
		if len(rejected) == 0 {
			fmt.Fprintln(io.Out, "No rejected batches; nothing to discard.")
			return nil
		}
		ids = ids[:0]
		for _, b := range rejected {
			ids = append(ids, b.BatchID)
		}
	}
	if !confirmed {
		yes, _ := askYesNo(io, discardPrompt(ids), false)
		if !yes {
			fmt.Fprintln(io.Out, "Nothing was discarded.")
			return nil
		}
	}

	// One failing batch must not strand the ones behind it: every batch
	// is attempted, and whatever could not be deleted is reported at the
	// end.
	var failed []error
	discarded := 0
	for _, id := range ids {
		rej, deleted, err := upload.Discard(m.deps.Layout.RejectedDir(), id)
		discarded += deleted
		if err != nil {
			failed = append(failed, err)
			continue
		}
		fmt.Fprintf(io.Out, "Discarded %d rawcall(s) from batch %s%s.\n", deleted, id, rejectionSuffix(rej))
	}
	if discarded > 0 {
		fmt.Fprintf(io.Out, "Deleted %d quarantined rawcall(s) from this machine; they cannot be recovered.\n", discarded)
	}
	return errors.Join(failed...)
}

// discardPrompt is the one question discard asks, so the single-batch
// and every-batch forms cannot describe the same deletion differently.
func discardPrompt(ids []string) string {
	what := "batch " + ids[0]
	if len(ids) > 1 {
		what = fmt.Sprintf("all %d quarantined batches", len(ids))
	}
	return fmt.Sprintf("Discard %s? The rawcalls are deleted from this machine for good. [y/N]: ", what)
}
