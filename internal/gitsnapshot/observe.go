package gitsnapshot

import (
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

// commitCommand is the word a shell command carries when it is about to
// make a commit. It is the whole of the degraded path's evidence: a
// host that states nothing about what a tool did leaves only what the
// tool was asked to do, and what the repository looked like afterwards.
const commitCommand = "git commit"

// Reason is why one moment is observed, and how the observation is
// compared once the repository has been read. It is decided from what
// the hook stated alone, so the repository is opened only for a moment
// that can produce a record.
type Reason struct {
	// Trigger is what the record states about why it was made.
	// ObserveReason is the only thing that decides it.
	Trigger string
	// fromParent compares against HEAD's own first parent — the commit
	// just made — rather than against the commit this device last saw.
	fromParent bool
	// needsMovedHead makes the record conditional on HEAD no longer
	// being the commit last seen. It is what the degraded path has
	// instead of a statement from the host that a commit was made.
	needsMovedHead bool
}

// ObserveReason decides why this moment is observed, from the three
// things a session hook states: which event it ran for, the command a
// tool was given, and whether the tool's own answer said it made a
// commit. It answers false where no record is produced at all.
//
// A session opening or closing is always observed, and is compared
// against the commit this device last saw for the project — the one
// span no other trigger covers. After a tool ran, the tool's own
// statement that it made a commit is the first evidence; a host that
// states nothing leaves the command it was given, and then only a HEAD
// that is no longer the one last seen says a commit was made.
func ObserveReason(event, command string, reportedCommit bool) (Reason, bool) {
	switch event {
	case claudesettings.EventSessionStart:
		return Reason{Trigger: envelope.TriggerSessionStart}, true
	case claudesettings.EventSessionEnd:
		return Reason{Trigger: envelope.TriggerSessionEnd}, true
	case claudesettings.EventPostToolUse:
		switch {
		case reportedCommit:
			return Reason{Trigger: envelope.TriggerGitOperation, fromParent: true}, true
		case strings.Contains(command, commitCommand):
			return Reason{Trigger: envelope.TriggerCommandMatch, fromParent: true, needsMovedHead: true}, true
		}
	}
	return Reason{}, false
}

// Compare is the commit the changes are read against, and false when
// this reason turns out to produce no record after all. An empty commit
// is nothing to compare against — the first observation of a project,
// or a HEAD that has not moved since the last one — and the record then
// carries an empty list.
func (r Reason) Compare(position Position, lastSeen string) (base string, ok bool) {
	if r.needsMovedHead && position.Head == lastSeen {
		return "", false
	}
	if r.fromParent {
		return position.Parent, true
	}
	if lastSeen == position.Head {
		return "", true
	}
	return lastSeen, true
}
