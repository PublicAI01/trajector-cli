package drift

import "sort"

// Signals is what a scan found, in the one form every reader of this
// package uses: what Scan returns, what a store keeps across scans,
// and what a surface prints. A store sums one scan into what earlier
// scans found with Add, and needs to know nothing about the fields it
// holds; a surface prints the fields by these names.
//
// Signals carries no path of the user's, no session id, and no text
// from a line: field names and counts are all of it. The field names
// arrive already printable, because the redaction pass that makes a
// name keeps a location out of it, and the remaining lists hold the
// words a line used to name its own shape.
//
// A count of lines seen is a denominator, not a finding: it says what
// the findings beside it are out of. The three group methods, and Any,
// answer for the findings alone.
type Signals struct {
	// UnanchoredPathFields lists, sorted and without repeats, the JSON
	// paths of fields whose whole value is an absolute path and that
	// neither the anchored list nor the acknowledged list knows.
	UnanchoredPathFields []string `json:"unanchored_path_fields,omitempty"`
	// IncompleteSegments counts the scans whose input did not end in a
	// newline. A reader only ever hands over complete lines, so this is
	// a contradiction when it is above zero; it is checked again here
	// because the cost of letting it through is a masked line that is
	// not JSON.
	IncompleteSegments int `json:"incomplete_segments,omitempty"`

	AssistantLines                 int `json:"assistant_lines,omitempty"`
	AssistantLinesWithoutMessageID int `json:"assistant_lines_without_message_id,omitempty"`
	// MessagesWithBlockIndexGap counts the message ids whose block
	// indexes, within the lines of one scan, repeat or skip a number. A
	// message may continue past those lines, so only what is decidable
	// from them counts: a run that starts above zero or ends early is
	// not a gap here.
	MessagesWithBlockIndexGap int `json:"messages_with_block_index_gap,omitempty"`
	// AssistantLinesMissingResponseFields counts the assistant lines
	// that lack a field the record is later read by. Which fields those
	// are is not settled, so requiredResponseFields is empty and no
	// line lacks one yet; the count belongs to the alert group, where a
	// line is stored as it was and the count is shown and logged.
	AssistantLinesMissingResponseFields int `json:"assistant_lines_missing_response_fields,omitempty"`

	AgentLines int `json:"agent_lines,omitempty"`
	// AgentLinesWithoutParent counts the agent lines that carry none of
	// the fields naming what started the agent.
	AgentLinesWithoutParent int `json:"agent_lines_without_parent,omitempty"`

	// The remaining lists hold values not on this build's known lists,
	// sorted and without repeats.
	NewLaunchSurfaces  []string `json:"new_launch_surfaces,omitempty"`
	NewAttachmentTypes []string `json:"new_attachment_types,omitempty"`
	NewSystemSubtypes  []string `json:"new_system_subtypes,omitempty"`
	NewTopLevelTypes   []string `json:"new_top_level_types,omitempty"`
}

// Stop reports a finding after which these lines must not be stored.
func (s Signals) Stop() bool {
	return len(s.UnanchoredPathFields) > 0 || s.IncompleteSegments > 0
}

// Alerts reports a finding to show and log while reading goes on.
func (s Signals) Alerts() bool {
	return s.AssistantLinesWithoutMessageID > 0 ||
		s.MessagesWithBlockIndexGap > 0 ||
		s.AssistantLinesMissingResponseFields > 0 ||
		s.AgentLinesWithoutParent > 0
}

// Logs reports a value seen for the first time, to log and nothing
// more.
func (s Signals) Logs() bool {
	return len(s.NewLaunchSurfaces) > 0 || len(s.NewAttachmentTypes) > 0 ||
		len(s.NewSystemSubtypes) > 0 || len(s.NewTopLevelTypes) > 0
}

// Any reports whether anything was found at all. Lines counted beside
// nothing found are not a finding: what a store keeps is what some
// scan found, and the lines it was out of.
func (s Signals) Any() bool { return s.Stop() || s.Alerts() || s.Logs() }

// Add reports what s and more found together: counts summed, name
// lists merged without repeats. It is the whole arithmetic of keeping
// signals across scans.
func (s Signals) Add(more Signals) Signals {
	s.UnanchoredPathFields = union(s.UnanchoredPathFields, more.UnanchoredPathFields)
	s.IncompleteSegments += more.IncompleteSegments
	s.AssistantLines += more.AssistantLines
	s.AssistantLinesWithoutMessageID += more.AssistantLinesWithoutMessageID
	s.MessagesWithBlockIndexGap += more.MessagesWithBlockIndexGap
	s.AssistantLinesMissingResponseFields += more.AssistantLinesMissingResponseFields
	s.AgentLines += more.AgentLines
	s.AgentLinesWithoutParent += more.AgentLinesWithoutParent
	s.NewLaunchSurfaces = union(s.NewLaunchSurfaces, more.NewLaunchSurfaces)
	s.NewAttachmentTypes = union(s.NewAttachmentTypes, more.NewAttachmentTypes)
	s.NewSystemSubtypes = union(s.NewSystemSubtypes, more.NewSystemSubtypes)
	s.NewTopLevelTypes = union(s.NewTopLevelTypes, more.NewTopLevelTypes)
	return s
}

// union merges two name lists into one sorted list without repeats,
// nil when both are empty.
func union(a, b []string) []string {
	if len(a)+len(b) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(a)+len(b))
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		seen[v] = true
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
