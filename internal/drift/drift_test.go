package drift_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/redact"
)

// fixtureLocation is where the sessions in testdata ran.
var fixtureLocation = redact.SessionLocation{Home: "/home/jdoe", Project: "/srv/work/project-alpha"}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func scan(t *testing.T, name string) drift.Signals {
	t.Helper()
	r, err := drift.Scan(fixture(t, name), fixtureLocation)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return r
}

func TestScan_ReportsAPathFieldOutsideTheAnchoredList(t *testing.T) {
	cases := []struct {
		name string
		file string
		want []string
	}{
		{"fields the lists do not know, listed once each", "unanchored_path.jsonl", []string{"$.attachment.snapshot.newDir", "$.someNewPath"}},
		{"anchored and acknowledged fields report nothing", "known_values.jsonl", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := scan(t, tc.file)
			if !reflect.DeepEqual(r.UnanchoredPathFields, tc.want) {
				t.Errorf("UnanchoredPathFields = %v, want %v", r.UnanchoredPathFields, tc.want)
			}
			if r.Quarantine() != (tc.want != nil) {
				t.Errorf("Quarantine() = %v, want %v", r.Quarantine(), tc.want != nil)
			}
			if r.Stop() {
				t.Error("Stop() = true, want an unmaskable field to hold its own segment and not the device")
			}
		})
	}
}

func TestScan_IncompleteTrailingLine(t *testing.T) {
	complete := fixture(t, "known_values.jsonl")
	cut := complete[:len(complete)-1]
	cases := []struct {
		name  string
		lines []byte
		want  bool
	}{
		{"every line ends in a newline", complete, false},
		{"the last line is cut", cut, true},
		{"nothing at all", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := drift.Scan(tc.lines, fixtureLocation)
			if err != nil {
				t.Fatal(err)
			}
			if (r.IncompleteSegments > 0) != tc.want || r.Stop() != tc.want {
				t.Errorf("IncompleteSegments = %d, Stop() = %v, want a cut line = %v", r.IncompleteSegments, r.Stop(), tc.want)
			}
		})
	}
	t.Run("the cut line is not read", func(t *testing.T) {
		r, err := drift.Scan([]byte("{\"type\":\"user\"}\n{\"type\":\"assistant\",\"message\":{}"), fixtureLocation)
		if err != nil {
			t.Fatal(err)
		}
		if r.AssistantLines != 0 {
			t.Errorf("AssistantLines = %d, want the cut line left unread", r.AssistantLines)
		}
	})
}

func TestScan_CountsAssistantLinesWithoutMessageID(t *testing.T) {
	cases := []struct {
		name           string
		file           string
		lines, without int
	}{
		{"one of three carries no id", "missing_message_id.jsonl", 3, 1},
		{"every assistant line carries an id", "known_values.jsonl", 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := scan(t, tc.file)
			if r.AssistantLines != tc.lines || r.AssistantLinesWithoutMessageID != tc.without {
				t.Errorf("assistant lines = %d without id %d, want %d and %d", r.AssistantLines, r.AssistantLinesWithoutMessageID, tc.lines, tc.without)
			}
			if r.Alerts() != (tc.without > 0) {
				t.Errorf("Alerts() = %v, want %v", r.Alerts(), tc.without > 0)
			}
			if r.Stop() {
				t.Error("a missing id stopped the scan; it must only alert")
			}
		})
	}
}

func TestScan_BlockIndexGapWithinOneMessage(t *testing.T) {
	cases := []struct {
		name string
		file string
		want int
	}{
		{"a skipped and a repeated index count; a full run and a continued one do not", "block_index_gap.jsonl", 2},
		{"indexes that run from zero", "known_values.jsonl", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := scan(t, tc.file)
			if r.MessagesWithBlockIndexGap != tc.want {
				t.Errorf("MessagesWithBlockIndexGap = %d, want %d", r.MessagesWithBlockIndexGap, tc.want)
			}
			if r.Alerts() != (tc.want > 0) {
				t.Errorf("Alerts() = %v, want %v", r.Alerts(), tc.want > 0)
			}
		})
	}
	t.Run("lines without an index are not judged", func(t *testing.T) {
		r, err := drift.Scan([]byte(`{"type":"assistant","message":{"id":"msg_1"}}`+"\n"+`{"type":"assistant","message":{"id":"msg_1"}}`+"\n"), fixtureLocation)
		if err != nil {
			t.Fatal(err)
		}
		if r.MessagesWithBlockIndexGap != 0 {
			t.Errorf("MessagesWithBlockIndexGap = %d, want none for lines that carry no index", r.MessagesWithBlockIndexGap)
		}
	})
}

func TestScan_AgentLineWithoutAnyParentField(t *testing.T) {
	cases := []struct {
		name           string
		file           string
		lines, without int
	}{
		{"one of three names nothing it was started by", "agent_without_parent.jsonl", 3, 1},
		{"an agent line naming its agent", "known_values.jsonl", 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := scan(t, tc.file)
			if r.AgentLines != tc.lines || r.AgentLinesWithoutParent != tc.without {
				t.Errorf("agent lines = %d without parent %d, want %d and %d", r.AgentLines, r.AgentLinesWithoutParent, tc.lines, tc.without)
			}
			if r.Alerts() != (tc.without > 0) {
				t.Errorf("Alerts() = %v, want %v", r.Alerts(), tc.without > 0)
			}
		})
	}
	t.Run("each accepted key on its own", func(t *testing.T) {
		for _, key := range []string{"agentId", "toolUseId", "parentAgentId", "parentSessionId"} {
			r, err := drift.Scan([]byte(`{"type":"user","isSidechain":true,"`+key+`":"x"}`+"\n"), fixtureLocation)
			if err != nil {
				t.Fatal(err)
			}
			if r.AgentLines != 1 || r.AgentLinesWithoutParent != 0 {
				t.Errorf("%s: agent lines = %d without parent %d, want 1 and 0", key, r.AgentLines, r.AgentLinesWithoutParent)
			}
		}
	})
}

func TestScan_NewValuesAreListedNotJudged(t *testing.T) {
	r := scan(t, "new_values.jsonl")
	want := drift.Signals{
		NewLaunchSurfaces:  []string{"claude-holodeck"},
		NewAttachmentTypes: []string{"weather_report"},
		NewSystemSubtypes:  []string{"coffee_break"},
		NewTopLevelTypes:   []string{"mood-ring"},
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("Signals = %+v, want %+v", r, want)
	}
	if !r.Logs() || r.Stop() || r.Alerts() {
		t.Errorf("Logs() = %v, Stop() = %v, Alerts() = %v; want a new value to log and nothing more", r.Logs(), r.Stop(), r.Alerts())
	}
}

func TestScan_KnownValuesFindNothingButTheEmptyReasoningCount(t *testing.T) {
	r := scan(t, "known_values.jsonl")
	want := drift.Signals{AssistantLines: 2, AgentLines: 1, AssistantLinesWithEmptyReasoning: 1}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("Signals = %+v, want %+v", r, want)
	}
	if r.Stop() || r.Alerts() {
		t.Errorf("Stop() = %v, Alerts() = %v; want lines of the expected shape to neither stop nor alert", r.Stop(), r.Alerts())
	}
}

func TestScan_CountsAssistantLinesWhoseReasoningIsEmpty(t *testing.T) {
	r := scan(t, "empty_reasoning.jsonl")
	if r.AssistantLines != 3 || r.AssistantLinesWithEmptyReasoning != 1 {
		t.Errorf("assistant lines = %d with empty reasoning %d, want 3 and 1: a reasoning field holding text and a line without one are not counted",
			r.AssistantLines, r.AssistantLinesWithEmptyReasoning)
	}
	if !r.Logs() || r.Stop() || r.Alerts() {
		t.Errorf("Logs() = %v, Stop() = %v, Alerts() = %v; want an empty reasoning field logged and nothing more", r.Logs(), r.Stop(), r.Alerts())
	}
}

func TestScan_IgnoresFreeText(t *testing.T) {
	r := scan(t, "free_text.jsonl")
	if r.Any() {
		t.Errorf("Signals = %+v, want nothing found from paths and names spelled inside text", r)
	}
}

func TestScan_RefusesALineThatIsNotOneOfASessionFile(t *testing.T) {
	for _, line := range []string{`"a string"`, `[1,2]`, `{"cwd":"/srv/wo`, `null`} {
		_, err := drift.Scan([]byte(line+"\n"), fixtureLocation)
		if err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("Scan(%s) = %v, want an error naming line 1", line, err)
		}
	}
}

func TestScan_ResponseFieldProbeReportsNothingWhileItsListIsEmpty(t *testing.T) {
	line := []byte(`{"type":"assistant","uuid":"u1","message":{"id":"msg_1","role":"assistant","content":[]}}` + "\n")
	r, err := drift.Scan(line, fixtureLocation)
	if err != nil {
		t.Fatal(err)
	}
	if r.AssistantLines != 1 || r.AssistantLinesMissingResponseFields != 0 {
		t.Errorf("assistant lines = %d, missing response fields = %d; want 1 and 0 while no field is required", r.AssistantLines, r.AssistantLinesMissingResponseFields)
	}
	if r.Alerts() {
		t.Error("Alerts() = true for a bare assistant line, want false while no response field is required")
	}
}
