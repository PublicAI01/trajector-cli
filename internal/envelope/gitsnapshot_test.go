package envelope_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

const (
	fixtureHead   = "d0cf90f327430f11f8a68493a58f402fa11d7c9e"
	fixtureParent = "4d0071c7e54967da4d11e6a397df9844797bdf81"
)

func fixtureChange(path string) envelope.Change {
	return envelope.Change{
		Path:    path,
		Status:  "M",
		OldBlob: "3c0ea41d1e4894bce3492660e412d145e972fde8",
		NewBlob: "2394262ce26c44c0fe958c60825b1ef29d94f397",
	}
}

func fixtureGitSnapshot(changed []envelope.Change) envelope.GitSnapshot {
	return envelope.NewGitSnapshot(
		fixtureSessionID, "PostToolUse", envelope.TriggerGitOperation, fixtureCapture(),
		"master", fixtureHead,
		envelope.CommitOrNone(fixtureParent), envelope.CommitOrNone(fixtureParent),
		changed,
	)
}

func TestGitSnapshotSerializesEveryContractFieldInOrder(t *testing.T) {
	snap := fixtureGitSnapshot([]envelope.Change{fixtureChange("src/app.ts")})
	data, err := snap.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":"3","source":"hook","record_kind":"git_snapshot","record_id":"` + snap.RecordID +
		`","session_id":"` + fixtureSessionID + `","hook_event":"PostToolUse","trigger":"git_operation","capture":` +
		`{"client_version":"0.2.0","timestamp":"2026-09-10T09:00:00.000000000Z","project_id_hash":"a4935b31d2ff72636fb53f77bb80a37fe44f9e113820330ebae207b70108a58e","injection":"proxy"},` +
		`"branch":"master","head":"` + fixtureHead + `","parent":"` + fixtureParent + `","base":"` + fixtureParent + `",` +
		`"changed":[{"path":"src/app.ts","status":"M","old_blob":"3c0ea41d1e4894bce3492660e412d145e972fde8","new_blob":"2394262ce26c44c0fe958c60825b1ef29d94f397"}],` +
		`"changed_count":1,"truncated":false}`
	if string(data) != want {
		t.Errorf("serialized git snapshot:\n got %s\nwant %s", data, want)
	}
}

func TestGitSnapshotWritesParentAndBaseAsNullRatherThanLeavingThemOut(t *testing.T) {
	snap := envelope.NewGitSnapshot(
		fixtureSessionID, "SessionStart", envelope.TriggerSessionStart, fixtureCapture(),
		"main", fixtureHead, envelope.CommitOrNone(""), envelope.CommitOrNone(""), nil,
	)
	data, err := snap.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"parent":null`, `"base":null`, `"changed":[]`, `"changed_count":0`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("record %s does not state %s", data, want)
		}
	}
}

func TestGitSnapshotRecordIDNamesWhatWasObserved(t *testing.T) {
	snap := fixtureGitSnapshot(nil)
	want := envelope.GitSnapshotRecordID(fixtureSessionID, "PostToolUse", fixtureHead, fixtureCapture().Timestamp)
	if snap.RecordID != want {
		t.Errorf("record_id = %q, recomputed %q", snap.RecordID, want)
	}
	if !strings.HasPrefix(snap.RecordID, "gs_") || len(snap.RecordID) != len("gs_")+32 {
		t.Errorf("record_id = %q, want the documented shape", snap.RecordID)
	}
	// Each part of the identity moves the name, so two observations that
	// differ in any of them are two records.
	same := envelope.GitSnapshotRecordID(fixtureSessionID, "PostToolUse", fixtureHead, fixtureCapture().Timestamp)
	if same != snap.RecordID {
		t.Error("the same observation named twice got two names")
	}
	for _, other := range []string{
		envelope.GitSnapshotRecordID("other-session", "PostToolUse", fixtureHead, fixtureCapture().Timestamp),
		envelope.GitSnapshotRecordID(fixtureSessionID, "SessionEnd", fixtureHead, fixtureCapture().Timestamp),
		envelope.GitSnapshotRecordID(fixtureSessionID, "PostToolUse", fixtureParent, fixtureCapture().Timestamp),
		envelope.GitSnapshotRecordID(fixtureSessionID, "PostToolUse", fixtureHead, "2026-09-10T09:00:01.000000000Z"),
	} {
		if other == snap.RecordID {
			t.Error("two different observations got one name")
		}
	}
}

func TestGitSnapshotKeepsTheFirstOfTooManyChangesAndCountsThemAll(t *testing.T) {
	changed := make([]envelope.Change, envelope.MaxChanges+112)
	for i := range changed {
		changed[i] = fixtureChange(fmt.Sprintf("src/f%04d.ts", i))
	}
	snap := fixtureGitSnapshot(changed)
	if len(snap.Changed) != envelope.MaxChanges || !snap.Truncated {
		t.Errorf("carried %d changes, truncated=%v", len(snap.Changed), snap.Truncated)
	}
	if snap.ChangedCount != envelope.MaxChanges+112 {
		t.Errorf("changed_count = %d, want the count before the bound", snap.ChangedCount)
	}
	if snap.Changed[0] != changed[0] || snap.Changed[envelope.MaxChanges-1] != changed[envelope.MaxChanges-1] {
		t.Error("the changes kept are not the first of them, in order")
	}
}

func TestGitSnapshotAtTheBoundIsNotTruncated(t *testing.T) {
	changed := make([]envelope.Change, envelope.MaxChanges)
	for i := range changed {
		changed[i] = fixtureChange(fmt.Sprintf("f%04d.ts", i))
	}
	snap := fixtureGitSnapshot(changed)
	if snap.Truncated || snap.ChangedCount != envelope.MaxChanges || len(snap.Changed) != envelope.MaxChanges {
		t.Errorf("exactly the bound reported %d of %d, truncated=%v", len(snap.Changed), snap.ChangedCount, snap.Truncated)
	}
}

func TestGitSnapshotRoundTrips(t *testing.T) {
	snap := fixtureGitSnapshot([]envelope.Change{fixtureChange("a.txt")})
	data, err := snap.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	read, err := envelope.ParseGitSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	again, err := read.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Errorf("round trip:\n got %s\nwant %s", again, data)
	}
	if read.Parent == nil || *read.Parent != fixtureParent || read.Base == nil || *read.Base != fixtureParent {
		t.Errorf("read back parent/base = %v/%v", read.Parent, read.Base)
	}
}

func TestGitSnapshotParserRefusesOtherRecords(t *testing.T) {
	seg, _ := envelope.NewSegment(fixtureSessionID, "", 0, fixtureCapture(), "{}\n").Bytes()
	rawcall, _ := json.Marshal(map[string]any{"schema_version": "3", "source": "proxy"})
	future, _ := json.Marshal(map[string]any{"schema_version": "9", "source": "hook", "record_kind": "git_snapshot"})

	for _, data := range [][]byte{seg, rawcall, future, []byte("not json")} {
		if _, err := envelope.ParseGitSnapshot(data); err == nil {
			t.Errorf("%s parsed as a git snapshot", data)
		}
	}
}

func TestGitSnapshotDeclaresItsOwnKind(t *testing.T) {
	data, err := fixtureGitSnapshot(nil).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	kind, err := envelope.KindOf(data)
	if err != nil {
		t.Fatal(err)
	}
	if kind != envelope.KindGitSnapshot {
		t.Errorf("kind = %+v, want %+v", kind, envelope.KindGitSnapshot)
	}
}

func TestCommitOrNone(t *testing.T) {
	if got := envelope.CommitOrNone(""); got != nil {
		t.Errorf("CommitOrNone(\"\") = %v, want nothing", *got)
	}
	if got := envelope.CommitOrNone(fixtureHead); got == nil || *got != fixtureHead {
		t.Errorf("CommitOrNone(%q) = %v", fixtureHead, got)
	}
}
