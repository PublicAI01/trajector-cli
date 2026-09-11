// Package drift reads the structured fields of session file lines and
// reports what no longer matches the shape this build was written
// against. It decides nothing about the lines beyond their shape, and
// it never reads free text: a path or a type name that appears inside
// message text, a tool result, or a command string is content, and
// content is where earlier attempts at this kind of check produced
// false alarms.
//
// Every finding belongs to one of three groups, told apart by what
// happens once the finding is made:
//
//   - Stop. The line cannot be masked the way this build masks it, so
//     it must not leave the device: a field holding an absolute path
//     that the anchored list does not know, or a line cut short of its
//     newline. Reading pauses until a build that covers the shape is
//     installed.
//   - Alert. The line was masked and stored as it was, but a field the
//     record is later read by is missing or inconsistent: an assistant
//     line without a message id, block indexes that do not run
//     0..N-1 within one message, an agent line naming nothing it was
//     started by. Reading goes on; the count is shown and logged.
//   - Log. A value this build had not seen before, in a field whose
//     values are an open set: a launch surface, an attachment type, a
//     system subtype, a top-level line type. Nothing changes; the
//     value is logged so it is known.
//
// The lists of known values in this package are a snapshot of what
// Claude Code 2.1.26x wrote. They exist only to tell a new value from
// a known one for logging, never to refuse a line: a value outside a
// list is recorded, not judged.
//
// Whoever adds a field or a line kind to what is read must place its
// check in one of the three groups above at the same time, and say
// why there: the groups are defined by what a wrong shape costs, and a
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
	"encoding/json"
	"fmt"
	"sort"

	"github.com/PublicAI01/trajector-cli/internal/redact"
)

// The top-level keys a scan reads. launchSurfaceKey is the key under
// which Claude Code records how it was started.
const (
	typeKey          = "type"
	subtypeKey       = "subtype"
	messageKey       = "message"
	attachmentKey    = "attachment"
	blockIndexKey    = "apiBlockIndex"
	sidechainKey     = "isSidechain"
	launchSurfaceKey = "entrypoint"

	typeAssistant  = "assistant"
	typeSystem     = "system"
	typeAttachment = "attachment"
)

// agentParentKeys are the fields by which an agent's lines are attached
// to what started the agent, any one of which is enough: agentId
// names the agent, and is what its metadata file and the tool result
// in the file that started it both carry; toolUseId names the tool use
// that started it; parentAgentId names the agent that started a nested
// one; parentSessionId is a forked agent's reference to the session it
// forked from. Today only the first appears on the lines themselves
// and the others in the metadata file beside them; all four are
// accepted so that a key moving onto the lines does not read as a
// line without a parent.
var agentParentKeys = []string{"agentId", "toolUseId", "parentAgentId", "parentSessionId"}

// requiredResponseFields lists the fields every assistant line must
// carry under message for the record to stand on its own. The list is
// not settled; while it is empty the check never reports.
var requiredResponseFields []string

// Scan reads the structured fields of the complete lines in lines and
// reports what does not match the shape this build expects. It never
// reads free text. Every line must be a JSON object, as a reader
// guarantees; a line that is not is an error, not a finding.
func Scan(lines []byte) (Signals, error) {
	var r Signals
	if !bytes.HasSuffix(lines, []byte("\n")) {
		r.IncompleteSegments = 1
	}
	s := scanner{
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
	paths       map[string]bool
	blockIndex  map[string][]int
	launch      map[string]bool
	attachments map[string]bool
	subtypes    map[string]bool
	types       map[string]bool
}

func (s *scanner) line(r *Signals, line []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(line, &top); err != nil || top == nil {
		return fmt.Errorf("not a JSON object")
	}
	fields, err := redact.AbsolutePathFields(line)
	if err != nil {
		return err
	}
	for _, f := range fields {
		s.paths[f] = true
	}

	typ := stringField(top[typeKey])
	if typ != "" && !knownTopLevelTypes[typ] {
		s.types[typ] = true
	}
	if v, ok := top[launchSurfaceKey]; ok {
		if surface := stringField(v); surface != "" && !knownLaunchSurfaces[surface] {
			s.launch[surface] = true
		}
	}
	switch typ {
	case typeAssistant:
		s.assistant(r, top)
	case typeSystem:
		if sub := stringField(top[subtypeKey]); sub != "" && !knownSystemSubtypes[sub] {
			s.subtypes[sub] = true
		}
	case typeAttachment:
		var att struct {
			Type string `json:"type"`
		}
		decode(top[attachmentKey], &att)
		if att.Type != "" && !knownAttachmentTypes[att.Type] {
			s.attachments[att.Type] = true
		}
	}

	var sidechain bool
	decode(top[sidechainKey], &sidechain)
	if sidechain || stringField(top["agentId"]) != "" {
		r.AgentLines++
		parented := false
		for _, key := range agentParentKeys {
			if stringField(top[key]) != "" {
				parented = true
				break
			}
		}
		if !parented {
			r.AgentLinesWithoutParent++
		}
	}
	return nil
}

func (s *scanner) assistant(r *Signals, top map[string]json.RawMessage) {
	r.AssistantLines++
	var message map[string]json.RawMessage
	decode(top[messageKey], &message)
	id := stringField(message["id"])
	if id == "" {
		r.AssistantLinesWithoutMessageID++
	}
	for _, key := range requiredResponseFields {
		if _, ok := message[key]; !ok {
			r.AssistantLinesMissingResponseFields++
			break
		}
	}
	if id == "" {
		return
	}
	var index int
	if raw, ok := top[blockIndexKey]; ok && json.Unmarshal(raw, &index) == nil {
		s.blockIndex[id] = append(s.blockIndex[id], index)
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

func stringField(raw json.RawMessage) string {
	var s string
	decode(raw, &s)
	return s
}

func decode(raw json.RawMessage, v any) {
	if raw != nil {
		_ = json.Unmarshal(raw, v)
	}
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
