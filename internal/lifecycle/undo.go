package lifecycle

import "errors"

// enableLedger records what an install changed and how each change is
// taken back. An entry goes on the ledger at the step that makes the
// change, never after it: a change that can fail partway must already
// have its undo on the ledger when it does. A failed install replays
// the ledger in reverse, so the last change made is the first one taken
// back and a step the install never reached takes nothing back.
//
// The ledger is the whole of what a rollback needs. A flag threaded
// through the install and read again after the failure is a value an
// error return can drop; an entry already on the ledger cannot be.
type enableLedger struct {
	entries []func() error
}

// record puts one change on the ledger. The undo takes back only what
// this install did to that one artifact: state shared with concurrent
// processes is undone entry-wise through its own writer, never by
// restoring a whole file over a concurrent writer's work. An undo must
// also be safe to run when the change it belongs to failed partway or
// never landed at all.
func (l *enableLedger) record(undo func() error) {
	l.entries = append(l.entries, undo)
}

// undo replays the ledger in reverse. It keeps going after a failed
// entry so one unwritable artifact cannot strand the others, and
// reports everything that failed.
func (l *enableLedger) undo() error {
	var errs []error
	for i := len(l.entries) - 1; i >= 0; i-- {
		errs = append(errs, l.entries[i]())
	}
	return errors.Join(errs...)
}
