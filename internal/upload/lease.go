package upload

import (
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// A lease is one batch id pinned to the records it was offered with,
// alive from before the batch's first attempt until the one transition
// that ends it. The uploader's two invariants are enforced here and
// argued nowhere else:
//
//   - An acknowledgement is the only trigger that deletes records:
//     settle is the single transition that removes spool records without
//     preserving them elsewhere. quarantine moves records aside whole,
//     and release only drops the pin once nothing is left behind it.
//
//   - A batch id is never reused for different content: the id is
//     minted together with its record set (openLease), a retry resumes
//     the persisted pair verbatim (resumeLease), and every ending
//     transition clears the pair as one — no path hands a live id to
//     new records, so the service can treat a repeated id as a
//     repeated batch.
type lease struct {
	dir string
	p   pending
}

// openLease mints a fresh batch id for these records and persists the
// pair before anything is sent. Without the persisted lease a lost
// acknowledgement could be re-uploaded under a fresh id and ingested
// twice; better not to start.
func openLease(dir string, rawcalls []spool.Rawcall) (lease, error) {
	id, err := newBatchID()
	if err != nil {
		return lease{}, err
	}
	l := lease{dir: dir, p: pending{BatchID: id, RequestIDs: requestIDs(rawcalls)}}
	if err := savePending(dir, l.p); err != nil {
		return lease{}, fmt.Errorf("recording the batch before sending it: %w", err)
	}
	return l, nil
}

// resumeLease reloads the persisted lease, if any, so a retry offers
// the same id for the same records.
func resumeLease(dir string) (lease, bool, error) {
	p, ok, err := loadPending(dir)
	return lease{dir: dir, p: p}, ok, err
}

func (l lease) id() string { return l.p.BatchID }

func (l lease) requestIDs() []string { return l.p.RequestIDs }

// settle ends the lease on the service's acknowledgement — the only
// transition that deletes records.
func (l lease) settle(sp *spool.Spool, acked []string) error {
	uploaded := map[string]bool{}
	for _, id := range acked {
		uploaded[id] = true
	}
	if _, err := sp.DeleteWhere(func(id string) bool { return uploaded[id] }); err != nil {
		// The batch is acknowledged but its records are still on disk.
		// The lease must survive so the next flush retries under the same
		// id and the service ignores the duplicate.
		return fmt.Errorf("deleting acknowledged records: %w", err)
	}
	if err := l.clear(); err != nil {
		return fmt.Errorf("releasing the pending batch: %w", err)
	}
	return nil
}

// release ends a lease whose id no longer protects anything: every
// record behind it is already gone from the spool — withdrawn, or set
// aside whole — so no later flush could re-upload them under a fresh
// id.
func (l lease) release() error { return l.clear() }

// quarantine ends the lease by moving its records into the rejected
// store. moved reports whether they actually left the spool: false
// means nothing moved and the lease stands, so the next flush meets the
// same refusal under the same id; true with an error means the records
// are quarantined but the id is still pinned.
func (l lease) quarantine(rejectedDir string, sp *spool.Spool, rej Rejection, rawcalls []spool.Rawcall) (moved bool, err error) {
	if err := quarantine(rejectedDir, sp, rej, rawcalls); err != nil {
		return false, err
	}
	return true, l.clear()
}

// clear drops the persisted pair. Every ending transition funnels
// through here, so the lease file cannot outlive its lease or vanish
// while one stands.
func (l lease) clear() error { return clearPending(l.dir) }
