package gitsnapshot_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/gitsnapshot"
)

func TestObserveReasonDecidesWhichMomentsAreObservedAndWhatTheyAreComparedAgainst(t *testing.T) {
	head := strings.Repeat("a", 40)
	parent := strings.Repeat("b", 40)
	earlier := strings.Repeat("c", 40)
	position := gitsnapshot.Position{Branch: "main", Head: head, Parent: parent}

	tests := []struct {
		name           string
		event          string
		command        string
		reportedCommit bool
		lastSeen       string
		wantTrigger    string
		wantBase       string
		wantRecord     bool
	}{
		{
			name:        "a session opening is compared against the commit last seen",
			event:       "SessionStart",
			lastSeen:    earlier,
			wantTrigger: envelope.TriggerSessionStart,
			wantBase:    earlier,
			wantRecord:  true,
		},
		{
			name:        "the first observation of a project has nothing to compare against",
			event:       "SessionStart",
			wantTrigger: envelope.TriggerSessionStart,
			wantRecord:  true,
		},
		{
			name:        "a session opening at the commit last seen is compared against nothing",
			event:       "SessionStart",
			lastSeen:    head,
			wantTrigger: envelope.TriggerSessionStart,
			wantRecord:  true,
		},
		{
			name:        "a session closing is compared against the commit last seen",
			event:       "SessionEnd",
			lastSeen:    earlier,
			wantTrigger: envelope.TriggerSessionEnd,
			wantBase:    earlier,
			wantRecord:  true,
		},
		{
			name:           "a host that states the tool made a commit is compared against that commit's own parent",
			event:          "PostToolUse",
			command:        "git commit -m x",
			reportedCommit: true,
			lastSeen:       earlier,
			wantTrigger:    envelope.TriggerGitOperation,
			wantBase:       parent,
			wantRecord:     true,
		},
		{
			name:           "a host that states the tool made a commit is believed although the head has not moved",
			event:          "PostToolUse",
			command:        "",
			reportedCommit: true,
			lastSeen:       head,
			wantTrigger:    envelope.TriggerGitOperation,
			wantBase:       parent,
			wantRecord:     true,
		},
		{
			name:        "a host that states nothing leaves the command the tool was given",
			event:       "PostToolUse",
			command:     "cd repo && git commit -m x",
			lastSeen:    earlier,
			wantTrigger: envelope.TriggerCommandMatch,
			wantBase:    parent,
			wantRecord:  true,
		},
		{
			name:     "a command that commits but left the head where it was states nothing",
			event:    "PostToolUse",
			command:  "git commit -m x",
			lastSeen: head,
		},
		{
			name:     "a command that commits nothing",
			event:    "PostToolUse",
			command:  "git status",
			lastSeen: earlier,
		},
		{
			name:     "an event this client observes on no schedule of its own",
			event:    "UserPromptSubmit",
			lastSeen: earlier,
		},
		{
			name:     "an event no session named",
			lastSeen: earlier,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			why, ok := gitsnapshot.ObserveReason(tt.event, tt.command, tt.reportedCommit)
			var base string
			if ok {
				base, ok = why.Compare(position, tt.lastSeen)
			}
			if ok != tt.wantRecord {
				t.Fatalf("observed = %v, want %v", ok, tt.wantRecord)
			}
			if !tt.wantRecord {
				return
			}
			if why.Trigger != tt.wantTrigger {
				t.Errorf("trigger = %q, want %q", why.Trigger, tt.wantTrigger)
			}
			if base != tt.wantBase {
				t.Errorf("base = %q, want %q", base, tt.wantBase)
			}
		})
	}
}
