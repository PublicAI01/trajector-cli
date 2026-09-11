package follow_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/follow"
)

func TestRegistry_SignalsAccumulateAcrossReads(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	first := follow.Signals{
		UnanchoredPathFields:           []string{"$.someNewPath"},
		AssistantLines:                 3,
		AssistantLinesWithoutMessageID: 1,
		NewTopLevelTypes:               []string{"mood-ring"},
	}
	second := follow.Signals{
		UnanchoredPathFields:      []string{"$.attachment.snapshot.newDir", "$.someNewPath"},
		IncompleteSegments:        1,
		AssistantLines:            2,
		MessagesWithBlockIndexGap: 1,
		AgentLines:                4,
		AgentLinesWithoutParent:   1,
		NewTopLevelTypes:          []string{"aurora"},
		NewLaunchSurfaces:         []string{"claude-holodeck"},
	}
	for _, s := range []follow.Signals{first, second} {
		if err := r.AddSignals(project, s); err != nil {
			t.Fatal(err)
		}
	}

	got, err := r.Signals(project)
	if err != nil {
		t.Fatal(err)
	}
	want := follow.Signals{
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
	files, err := r.Files(project)
	if err != nil || len(files) != 0 {
		t.Errorf("Files = %v, %v; want a registry that holds signals and no files", files, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, project+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"signals":{"unanchored_path_fields":["$.attachment.snapshot.newDir","$.someNewPath"]`) {
		t.Errorf("registry file = %s, want a signals object with the field names", raw)
	}
}

func TestRegistry_SignalsOfAnUnknownProjectAreNone(t *testing.T) {
	got, err := follow.Open(t.TempDir()).Signals(project)
	if err != nil || got.Any() {
		t.Errorf("Signals = %+v, %v; want none and no error", got, err)
	}
}

func TestRegistry_AddingNothingWritesNoSignals(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	if err := r.AddSignals(project, follow.Signals{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, project+".json"))
	if strings.Contains(string(raw), "signals") {
		t.Errorf("registry file = %s, want no signals key when nothing was noticed", raw)
	}
}

func TestRegistry_UnregisterDropsTheSignals(t *testing.T) {
	r := follow.Open(t.TempDir())
	if err := r.AddSignals(project, follow.Signals{AssistantLines: 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.Unregister(project); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Signals(project); got.Any() {
		t.Errorf("Signals after Unregister = %+v, want none", got)
	}
}
