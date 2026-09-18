package envelope

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// A git snapshot is what one hook saw of the repository it ran in:
// which commit is checked out, which branch names it, and which paths
// differ between two commits. It carries no file content — only paths
// and the blob identifiers git printed beside them — and it states what
// git printed, never a reading of it.
const (
	sourceHook      = "hook"
	kindGitSnapshot = "git_snapshot"

	gitSnapshotRecordIDPrefix = "gs_"

	// MaxChanges bounds how many changed paths one record carries. A
	// record past the bound keeps the first MaxChanges of them and says
	// so, so one enormous commit cannot fill the spool by itself.
	MaxChanges = 500
)

// Triggers are the reasons this client observes a repository. The value
// travels as observed by the receiver: it is a closed set here and an
// open one there, so a reason a later client adds needs no agreement.
const (
	TriggerSessionStart = "session_start"
	TriggerSessionEnd   = "session_end"
	TriggerGitOperation = "git_operation"
	TriggerCommandMatch = "command_match"
)

// Change is one line of what git printed for a pair of commits: the
// path relative to the repository root, the status letter, and the blob
// identifiers on each side. Git spells an absent side as forty zeros
// and that spelling is kept, because replacing it would be a reading of
// what git said rather than a copy of it.
type Change struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	OldBlob string `json:"old_blob"`
	NewBlob string `json:"new_blob"`
}

// GitSnapshot is one observation of a repository at one moment. Every
// JSON tag is part of the documented contract and the field order is
// the serialized order. Parent and Base are pointers because the
// contract requires both keys to be present with a null value where
// there is no such commit; an absent key is not the same statement.
type GitSnapshot struct {
	SchemaVersion string `json:"schema_version"`
	Source        string `json:"source"`
	RecordKind    string `json:"record_kind"`
	RecordID      string `json:"record_id"`
	SessionID     string `json:"session_id"`
	// HookEvent is the event name the hook was handed, copied as it was
	// given.
	HookEvent string `json:"hook_event"`
	// Trigger is why this client observed the repository now.
	Trigger string  `json:"trigger"`
	Capture Capture `json:"capture"`
	Branch  string  `json:"branch"`
	Head    string  `json:"head"`
	// Parent is HEAD's first parent, null at a root commit.
	Parent *string `json:"parent"`
	// Base is the commit Changed was compared against, null when there
	// was nothing to compare with. Changed is then empty.
	Base    *string  `json:"base"`
	Changed []Change `json:"changed"`
	// ChangedCount is how many changes there were before the bound was
	// applied.
	ChangedCount int  `json:"changed_count"`
	Truncated    bool `json:"truncated"`
}

// NewGitSnapshot builds a git snapshot record and names it from its
// identity. Changed is bounded here rather than by the caller, so every
// record this client writes obeys the same bound and states the count
// it was cut from.
func NewGitSnapshot(sessionID, hookEvent, trigger string, capture Capture, branch, head string, parent, base *string, changed []Change) GitSnapshot {
	count := len(changed)
	truncated := count > MaxChanges
	if truncated {
		changed = changed[:MaxChanges]
	}
	if changed == nil {
		changed = []Change{}
	}
	return GitSnapshot{
		SchemaVersion: SchemaVersion,
		Source:        sourceHook,
		RecordKind:    kindGitSnapshot,
		RecordID:      GitSnapshotRecordID(sessionID, hookEvent, head, capture.Timestamp),
		SessionID:     sessionID,
		HookEvent:     hookEvent,
		Trigger:       trigger,
		Capture:       capture,
		Branch:        branch,
		Head:          head,
		Parent:        parent,
		Base:          base,
		Changed:       changed,
		ChangedCount:  count,
		Truncated:     truncated,
	}
}

// CommitOrNone is the pointer form of a commit that may not exist: nil
// where there is none, so the key is written with a null value rather
// than left out. The contract asks for the key either way.
func CommitOrNone(commit string) *string {
	if commit == "" {
		return nil
	}
	return new(commit)
}

// GitSnapshotRecordID names an observation from what it observed, so the
// same observation sent again after a crash carries the same id, and two
// observations of the same commit are told apart by when they were made.
func GitSnapshotRecordID(sessionID, hookEvent, head, timestamp string) string {
	h := sha256.New()
	h.Write([]byte(sessionID))
	h.Write([]byte{0})
	h.Write([]byte(hookEvent))
	h.Write([]byte{0})
	h.Write([]byte(head))
	h.Write([]byte{0})
	h.Write([]byte(timestamp))
	return gitSnapshotRecordIDPrefix + hex.EncodeToString(h.Sum(nil))[:recordIDHexLength]
}

// Restated returns the snapshot with its schema version set to the
// current one.
func (g GitSnapshot) Restated() GitSnapshot {
	g.SchemaVersion = SchemaVersion
	return g
}

// Bytes serializes the snapshot.
func (g GitSnapshot) Bytes() ([]byte, error) { return marshalRecord(g) }

// ParseGitSnapshot reads a stored git snapshot back, refusing any record
// that does not declare itself as one.
func ParseGitSnapshot(data []byte) (GitSnapshot, error) {
	var g GitSnapshot
	if err := json.Unmarshal(data, &g); err != nil {
		return GitSnapshot{}, fmt.Errorf("envelope: reading git snapshot: %w", err)
	}
	if err := checkVersion(g.SchemaVersion); err != nil {
		return GitSnapshot{}, err
	}
	if g.Source != sourceHook || g.RecordKind != kindGitSnapshot {
		return GitSnapshot{}, fmt.Errorf("envelope: record is %s/%s, not %s/%s", g.Source, g.RecordKind, sourceHook, kindGitSnapshot)
	}
	return g, nil
}
