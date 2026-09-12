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

func TestSignalsCallAShapeThisBuildExpectsToMeetExpected(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    drift.Signals
		want bool
	}{
		{
			name: "assistant lines whose reasoning field holds nothing",
			s:    drift.Signals{AssistantLines: 9, AssistantLinesWithEmptyReasoning: 6},
		},
		{
			name: "a value outside this build's lists",
			s:    drift.Signals{AssistantLines: 9, AssistantLinesWithEmptyReasoning: 6, NewTopLevelTypes: []string{"mood-ring"}},
			want: true,
		},
		{
			name: "an assistant line without a message id",
			s:    drift.Signals{AssistantLines: 9, AssistantLinesWithEmptyReasoning: 6, AssistantLinesWithoutMessageID: 1},
			want: true,
		},
		{
			name: "a field this build cannot mask",
			s:    drift.Signals{AssistantLinesWithEmptyReasoning: 6, UnanchoredPathFields: []string{"$.someNewPath"}},
			want: true,
		},
		{
			name: "nothing found",
			s:    drift.Signals{AssistantLines: 9},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.Unexpected(); got != tc.want {
				t.Errorf("Unexpected() = %v, want %v", got, tc.want)
			}
			if !tc.s.Any() && tc.s.AssistantLinesWithEmptyReasoning > 0 {
				t.Errorf("Any() = false for %+v, want the count still a finding a store keeps", tc.s)
			}
		})
	}
}
