// Package drift reads the structured fields of session file lines and
// reports what no longer matches the shape this build was written
// against. It decides nothing about the lines beyond their shape, and
// it never reads free text: a path or a type name that appears inside
// message text, a tool result, or a command string is content, and
// content is where earlier attempts at this kind of check produced
// false alarms.
//
// Every finding belongs to one of four groups, told apart by what
// happens once the finding is made:
//
//   - Stop. The reader contradicted itself: a line cut short of its
//     newline, which a reader never hands over. Reading pauses until a
//     build that covers the shape is installed.
//   - Quarantine. The line holds a field naming where the session ran
//     that the anchored list does not know, so this build cannot mask
//     it. The segment is kept on this machine and never uploaded;
//     reading goes on.
//   - Alert. The line was masked and stored as it was, but a field the
//     record is later read by is missing or inconsistent: an assistant
//     line without a message id, block indexes that do not run
//     0..N-1 within one message, an agent line naming nothing it was
//     started by. Reading goes on; the count is shown and logged.
//   - Log. A value this build had not seen before, in a field whose
//     values are an open set: a launch surface, an attachment type, a
//     system subtype, a top-level line type. Here too the assistant
//     lines whose reasoning field holds nothing: a setting of Claude
//     Code's decides whether that field is filled, so the count says
//     how the sessions ran and not that a line is wrong. Nothing
//     changes; the value or the count is logged so it is known.
//
// The lists of known values in this package are a snapshot of what
// Claude Code 2.1.26x wrote. They exist only to tell a new value from
// a known one for logging, never to refuse a line: a value outside a
// list is recorded, not judged.
//
// Whoever adds a field or a line kind to what is read must place its
// check in one of the four groups above at the same time, and say why
// there: the groups are defined by what a wrong shape costs, and a
// check that is not placed is a check that is not run.
//
// What a scan found is one value, Signals, and this package holds the
// only spelling of it: a store keeps that value and sums it across
// scans without naming its fields, a surface prints it by those same
// names, and the log of what reading noticed, which this package also
// writes and reads, carries it as it is. A check added here is
// therefore a field here and a sentence where it is printed.
//
// Two kinds of run answer two different questions. The fixtures under
// testdata are frozen and synthetic; a run over them proves the code
// does what it says. A run over the real session files on a
// developer's device, opted into with the localcorpus build tag,
// proves the files still have the shape the code expects. Neither run
// proves the other's claim: fixtures cannot notice that the upstream
// format moved, and the local files cannot show that a check reports
// what it should when the shape is wrong.
package drift

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/PublicAI01/trajector-cli/internal/redact"
	"github.com/PublicAI01/trajector-cli/internal/sessionline"
)

// The line kinds a scan treats apart from the rest.
const (
	typeAssistant  = "assistant"
	typeSystem     = "system"
	typeAttachment = "attachment"
)

// parented reports whether an agent line names what started the agent.
// Any one of the four references is enough: AgentID names the agent,
// and is what its metadata file and the tool result in the file that
// started it both carry; ToolUseID names the tool use that started it;
// ParentAgentID names the agent that started a nested one;
// ParentSessionID is a forked agent's reference to the session it
// forked from. Today only the first appears on the lines themselves
// and the others in the metadata file beside them; all four are
// accepted so that a reference moving onto the lines does not read as
// a line without a parent.
func parented(f sessionline.Fields) bool {
	return f.AgentID != "" || f.ToolUseID != "" || f.ParentAgentID != "" || f.ParentSessionID != ""
}

// requiredResponseFields lists the fields every assistant line must
// carry under message for the record to stand on its own. The list is
// not settled; while it is empty the check never reports.
var requiredResponseFields []string

// Scan reads the structured fields of the complete lines in lines and
// reports what does not match the shape this build expects. It never
// reads free text. A line that is not one of a session file is an
// error, not a finding: a reader hands over no such line, so one here
// says the input did not come from a reader.
func Scan(lines []byte, loc redact.SessionLocation) (Signals, error) {
	var r Signals
	if !bytes.HasSuffix(lines, []byte("\n")) {
		r.IncompleteSegments = 1
	}
	s := scanner{
		location:    loc,
		paths:       map[string]bool{},
		blockIndex:  map[string][]int{},
		launch:      map[string]bool{},
		attachments: map[string]bool{},
		subtypes:    map[string]bool{},
		types:       map[string]bool{},
	}
	rest := lines
	for n := 1; ; n++ {
		nl := bytes.IndexByte(rest, '\n')
		if nl < 0 {
			break
		}
		line := rest[:nl]
		rest = rest[nl+1:]
		if err := s.line(&r, line); err != nil {
			return Signals{}, fmt.Errorf("drift: line %d: %w", n, err)
		}
	}
	r.UnanchoredPathFields = sorted(s.paths)
	r.MessagesWithBlockIndexGap = countGaps(s.blockIndex)
	r.NewLaunchSurfaces = sorted(s.launch)
	r.NewAttachmentTypes = sorted(s.attachments)
	r.NewSystemSubtypes = sorted(s.subtypes)
	r.NewTopLevelTypes = sorted(s.types)
	return r, nil
}

// scanner accumulates what one Scan sees across lines, so a value is
// listed once however many lines carry it, and a message's block
// indexes are judged together.
type scanner struct {
	// location is where the session whose lines these are ran. A field
	// naming a path is a finding only when it names that location; the
	// scan itself reads no environment, so what counts as the location
	// arrives with the lines.
	location    redact.SessionLocation
	paths       map[string]bool
	blockIndex  map[string][]int
	launch      map[string]bool
	attachments map[string]bool
	subtypes    map[string]bool
	types       map[string]bool
}

func (s *scanner) line(r *Signals, raw []byte) error {
	line, ok := sessionline.Parse(raw)
	if !ok {
		return fmt.Errorf("not a session line")
	}
	for _, name := range redact.AbsolutePathFields(line, s.location) {
		s.paths[name] = true
	}

	f := line.Fields()
	if f.Type != "" && !knownTopLevelTypes[f.Type] {
		s.types[f.Type] = true
	}
	if f.LaunchSurface != "" && !knownLaunchSurfaces[f.LaunchSurface] {
		s.launch[f.LaunchSurface] = true
	}
	switch f.Type {
	case typeAssistant:
		s.assistant(r, line, f)
	case typeSystem:
		if f.Subtype != "" && !knownSystemSubtypes[f.Subtype] {
			s.subtypes[f.Subtype] = true
		}
	case typeAttachment:
		if f.AttachmentType != "" && !knownAttachmentTypes[f.AttachmentType] {
			s.attachments[f.AttachmentType] = true
		}
	}

	if f.Sidechain || f.AgentID != "" {
		r.AgentLines++
		if !parented(f) {
			r.AgentLinesWithoutParent++
		}
	}
	return nil
}

func (s *scanner) assistant(r *Signals, line sessionline.Line, f sessionline.Fields) {
	r.AssistantLines++
	if f.MessageID == "" {
		r.AssistantLinesWithoutMessageID++
	}
	if f.EmptyReasoning {
		r.AssistantLinesWithEmptyReasoning++
	}
	for _, key := range requiredResponseFields {
		if !line.MessageHas(key) {
			r.AssistantLinesMissingResponseFields++
			break
		}
	}
	if f.MessageID == "" {
		return
	}
	if f.HasBlockIndex {
		s.blockIndex[f.MessageID] = append(s.blockIndex[f.MessageID], f.BlockIndex)
	}
}

// countGaps counts the messages whose block indexes repeat or skip a
// number between the lowest and the highest seen.
func countGaps(byMessage map[string][]int) int {
	gaps := 0
	for _, indexes := range byMessage {
		sort.Ints(indexes)
		for i := 1; i < len(indexes); i++ {
			if indexes[i] != indexes[i-1]+1 {
				gaps++
				break
			}
		}
	}
	return gaps
}

func sorted(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
