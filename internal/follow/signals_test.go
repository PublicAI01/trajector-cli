package follow_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/follow"
)

func TestReader_SignalsAccumulateAcrossReads(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	writeFile(t, path, userLine(1))
	a.register(path, "")
	a.answer = follow.Stored
	a.noticed = drift.Signals{
		UnanchoredPathFields:           []string{"$.someNewPath"},
		AssistantLines:                 3,
		AssistantLinesWithoutMessageID: 1,
		NewTopLevelTypes:               []string{"mood-ring"},
	}
	a.reader.Advance(path)
	appendFile(t, path, userLine(2))
	a.noticed = drift.Signals{
		UnanchoredPathFields:      []string{"$.attachment.snapshot.newDir", "$.someNewPath"},
		IncompleteSegments:        1,
		AssistantLines:            2,
		MessagesWithBlockIndexGap: 1,
		AgentLines:                4,
		AgentLinesWithoutParent:   1,
		NewTopLevelTypes:          []string{"aurora"},
		NewLaunchSurfaces:         []string{"claude-holodeck"},
	}
	a.reader.Advance(path)

	got, err := a.registry.Signals(project)
	if err != nil {
		t.Fatal(err)
	}
	want := drift.Signals{
		UnanchoredPathFields:           []string{"$.attachment.snapshot.newDir", "$.someNewPath"},
		IncompleteSegments:             1,
		AssistantLines:                 5,
		AssistantLinesWithoutMessageID: 1,
		MessagesWithBlockIndexGap:      1,
		AgentLines:                     4,
		AgentLinesWithoutParent:        1,
		NewLaunchSurfaces:              []string{"claude-holodeck"},
		NewTopLevelTypes:               []string{"aurora", "mood-ring"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Signals = %+v, want %+v", got, want)
	}
	raw, err := os.ReadFile(filepath.Join(a.dir, project+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"signals":{"unanchored_path_fields":["$.attachment.snapshot.newDir","$.someNewPath"]`) {
		t.Errorf("registry file = %s, want a signals object with the field names", raw)
	}
}

func TestReader_KeepsWhatASegmentNoticedInTheWriteThatMovesItsCursor(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	line := assistantLine("m1", 0, "hello")
	writeFile(t, path, line)
	a.register(path, "")
	writes := registryWrites(t, filepath.Join(a.dir, project+".json"))
	a.answer = follow.Stored
	a.noticed = drift.Signals{AssistantLines: 1, AssistantLinesWithoutMessageID: 1}

	a.reader.Advance(path)

	if n := writes(); n != 1 {
		t.Errorf("registry written %d times for one segment, want once", n)
	}
	if f := a.entry(path); f.Offset != int64(len(line)) || f.NextSegment != 1 {
		t.Errorf("cursor = %+v, want it past the segment", f)
	}
	if got, _ := a.registry.Signals(project); !reflect.DeepEqual(got, a.noticed) {
		t.Errorf("Signals = %+v, want what the segment noticed", got)
	}
}

func TestReader_KeepsWhatAPausedReadNoticedAndLeavesTheCursor(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	writeFile(t, path, assistantLine("m1", 0, "hello"))
	a.register(path, "")
	writes := registryWrites(t, filepath.Join(a.dir, project+".json"))
	a.answer = follow.Paused
	a.noticed = drift.Signals{IncompleteSegments: 1}

	if a.reader.Advance(path) {
		t.Error("Advance of a paused read = true, want the project's reading stopped")
	}

	if n := writes(); n != 1 {
		t.Errorf("registry written %d times for a paused read, want once", n)
	}
	if f := a.entry(path); f.Offset != 0 || f.NextSegment != 0 || f.ReadAt != "" {
		t.Errorf("cursor = %+v, want it left where it was", f)
	}
	if got, _ := a.registry.Signals(project); got.IncompleteSegments != 1 {
		t.Errorf("Signals = %+v, want the incomplete segment counted", got)
	}
}

func TestReader_DropsWhatAReadNoticedWhenItsRecordsAreNotStored(t *testing.T) {
	for _, answer := range []follow.Storing{follow.Held, follow.NotStored} {
		a := newAdvancing(t)
		path := mainPath(t)
		writeFile(t, path, assistantLine("m1", 0, "hello"))
		a.register(path, "")
		writes := registryWrites(t, filepath.Join(a.dir, project+".json"))
		a.answer = answer
		a.noticed = drift.Signals{AssistantLines: 1, AssistantLinesWithoutMessageID: 1}

		a.reader.Advance(path)

		if n := writes(); n != 0 {
			t.Errorf("registry written %d times for a read answered %d, want not at all: the read that stores these lines notices them", n, answer)
		}
		if got, _ := a.registry.Signals(project); got.Any() {
			t.Errorf("Signals after a read answered %d = %+v, want none", answer, got)
		}
	}
}

func TestRegistry_SignalsOfAnUnknownProjectAreNone(t *testing.T) {
	got, err := follow.Open(t.TempDir()).Signals(project)
	if err != nil || got.Any() {
		t.Errorf("Signals = %+v, %v; want none and no error", got, err)
	}
}

func TestReader_NoticingNothingWritesNoSignals(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	writeFile(t, path, userLine(1))
	a.register(path, "")
	a.answer = follow.Stored

	a.reader.Advance(path)

	raw, _ := os.ReadFile(filepath.Join(a.dir, project+".json"))
	if strings.Contains(string(raw), "signals") {
		t.Errorf("registry file = %s, want no signals key when nothing was noticed", raw)
	}
}

func TestRegistry_UnregisterDropsTheSignals(t *testing.T) {
	a := newAdvancing(t)
	path := mainPath(t)
	writeFile(t, path, userLine(1))
	a.register(path, "")
	a.answer = follow.Stored
	a.noticed = drift.Signals{AssistantLines: 1, AssistantLinesWithoutMessageID: 1}
	a.reader.Advance(path)

	if err := a.registry.Unregister(project); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.registry.Signals(project); got.Any() {
		t.Errorf("Signals after Unregister = %+v, want none", got)
	}
}
