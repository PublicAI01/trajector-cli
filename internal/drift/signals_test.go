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
		NewAttachmentTypes:               []string{"holo-sketch"},
		NewSystemSubtypes:                []string{"weather-note"},
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
		NewAttachmentTypes:                  []string{"mood-board"},
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
		NewAttachmentTypes:                  []string{"holo-sketch", "mood-board"},
		NewSystemSubtypes:                   []string{"weather-note"},
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

func TestEverySignalsFieldIsSummedOrMergedByAdd(t *testing.T) {
	signals := reflect.TypeFor[drift.Signals]()
	for i := range signals.NumField() {
		field := signals.Field(i)
		t.Run(field.Name, func(t *testing.T) {
			var first, second, want drift.Signals
			switch field.Type.Kind() {
			case reflect.Int:
				valueIn(&first, i).SetInt(1)
				valueIn(&second, i).SetInt(2)
				valueIn(&want, i).SetInt(3)
			case reflect.Slice:
				if field.Type.Elem().Kind() != reflect.String {
					t.Fatalf("no rule for a slice of %s; teach this test the kind", field.Type.Elem().Kind())
				}
				valueIn(&first, i).Set(reflect.ValueOf([]string{"first-name"}))
				valueIn(&second, i).Set(reflect.ValueOf([]string{"second-name"}))
				valueIn(&want, i).Set(reflect.ValueOf([]string{"first-name", "second-name"}))
			default:
				t.Fatalf("no rule for a %s field; teach this test the kind", field.Type.Kind())
			}
			if got := first.Add(second); !reflect.DeepEqual(got, want) {
				t.Errorf("Add = %+v, want %+v", got, want)
			}
		})
	}
}

func valueIn(s *drift.Signals, field int) reflect.Value {
	return reflect.ValueOf(s).Elem().Field(field)
}
