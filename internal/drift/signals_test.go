package drift_test

import (
	"reflect"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/drift"
)

func TestSignalsAddSumsCountsAndMergesNamesWithoutRepeats(t *testing.T) {
	first := drift.Signals{
		UnanchoredPathFields:             []string{"$.someNewPath"},
		AssistantLines:                   3,
		AssistantLinesWithoutMessageID:   1,
		AssistantLinesWithEmptyReasoning: 2,
		NewTopLevelTypes:                 []string{"mood-ring"},
	}
	second := drift.Signals{
		UnanchoredPathFields:                []string{"$.attachment.snapshot.newDir", "$.someNewPath"},
		IncompleteSegments:                  1,
		AssistantLines:                      2,
		AssistantLinesWithEmptyReasoning:    1,
		AssistantLinesMissingResponseFields: 2,
		MessagesWithBlockIndexGap:           1,
		AgentLines:                          4,
		AgentLinesWithoutParent:             1,
		NewLaunchSurfaces:                   []string{"claude-holodeck"},
		NewTopLevelTypes:                    []string{"aurora"},
	}
	want := drift.Signals{
		UnanchoredPathFields:                []string{"$.attachment.snapshot.newDir", "$.someNewPath"},
		IncompleteSegments:                  1,
		AssistantLines:                      5,
		AssistantLinesWithoutMessageID:      1,
		AssistantLinesWithEmptyReasoning:    3,
		AssistantLinesMissingResponseFields: 2,
		MessagesWithBlockIndexGap:           1,
		AgentLines:                          4,
		AgentLinesWithoutParent:             1,
		NewLaunchSurfaces:                   []string{"claude-holodeck"},
		NewTopLevelTypes:                    []string{"aurora", "mood-ring"},
	}
	if got := first.Add(second); !reflect.DeepEqual(got, want) {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
	if got := second.Add(first); !reflect.DeepEqual(got, want) {
		t.Errorf("Add the other way round = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(first.UnanchoredPathFields, []string{"$.someNewPath"}) {
		t.Errorf("the signals added to = %+v, want them left as they were", first)
	}
}

func TestSignalsAddingNothingToNothingFindsNothing(t *testing.T) {
	got := drift.Signals{}.Add(drift.Signals{})
	if got.Any() || got.UnanchoredPathFields != nil || got.NewTopLevelTypes != nil {
		t.Errorf("Add = %+v, want nothing found and no empty lists", got)
	}
}

func TestSignalsCountingLinesIsNotAFinding(t *testing.T) {
	counted := drift.Signals{AssistantLines: 12, AgentLines: 5}
	if counted.Any() || counted.Stop() || counted.Alerts() || counted.Logs() {
		t.Errorf("Signals = %+v, want lines counted beside nothing found to report nothing", counted)
	}
}
