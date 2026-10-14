// Package sessionline reads one line of a Claude Code session file.
//
// A session file holds one JSON object per line. This package decides
// what is such a line and what is not, and it reads out of one the
// facts the format states in fixed positions. Every package that acts
// on a line goes through here, so the key names of the format are
// spelled once and a line that is not an object cannot be read as one
// anywhere.
//
// The facts are copied out as they were observed. This package
// classifies nothing, grades nothing, and fills nothing in: what a
// value means, and what to do about a line that carries it, belongs to
// the reader that asks.
package sessionline

import (
	"encoding/json"
	"strconv"
)

// Line is one line of a session file, established as a JSON object.
// It borrows the bytes it was parsed from: they must stay as they were
// for as long as the Line is used.
type Line struct{ raw []byte }

// Parse reports whether line is one line of a session file: a JSON
// object. Text that is no JSON, a JSON value of any other kind, and an
// empty line are not lines, and this is the one place that decides it.
// What a reader does with such a line — drop it, refuse the input,
// pass the bytes on untouched — is that reader's own policy; what no
// reader can do is read fields out of one.
func Parse(line []byte) (Line, bool) {
	i := 0
	for i < len(line) && isSpace(line[i]) {
		i++
	}
	if i == len(line) || line[i] != '{' || !json.Valid(line) {
		return Line{}, false
	}
	return Line{raw: line}, true
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// Text is the line as it was. Lines are stored byte for byte, so a
// reader that must work on the bytes themselves gets them unchanged.
func (l Line) Text() string { return string(l.raw) }

// Fields are the facts the format states in fixed positions on a line.
// Each is copied out as observed. A field the line does not carry, or
// carries in another shape than the format states, reads as absent:
// the line is kept as it is either way, and only a reader that steers
// on the field has anything to lose.
type Fields struct {
	// Type is the kind of the line.
	Type string
	// Subtype is the second name a system line carries.
	Subtype string
	// LaunchSurface is how Claude Code was started.
	LaunchSurface string
	// RelocatedCwd is the directory a session moved to.
	RelocatedCwd string
	// MessageID names the API message the line belongs to.
	MessageID string
	// AttachmentType is the kind of an attachment line's attachment.
	AttachmentType string
	// AgentID names the agent whose work the line records.
	AgentID string
	// ToolUseID, ParentAgentID and ParentSessionID are the references
	// an agent line carries back to what started the agent.
	ToolUseID       string
	ParentAgentID   string
	ParentSessionID string
	// Sidechain says the line was written for an agent rather than for
	// the session itself.
	Sidechain bool
	// BlockIndex is the line's place among the blocks of its message.
	// HasBlockIndex is false where the line carries no whole number
	// there, and BlockIndex is then meaningless.
	BlockIndex    int
	HasBlockIndex bool
	// Reasoning counts the blocks of the line's message that carry the
	// reasoning field as a string, by whether the string is empty.
	Reasoning ReasoningBlocks
}

// ReasoningBlocks counts the blocks of one message that carry the
// reasoning field as a string. Empty counts those whose string holds
// nothing, and WithText those whose string holds anything at all. A
// block without the field, or with a value of another shape, is in
// neither count. Only whether the string is empty is observed: the
// text itself is never read.
type ReasoningBlocks struct {
	Empty    int
	WithText int
}

// Add is the sum of r and more.
func (r ReasoningBlocks) Add(more ReasoningBlocks) ReasoningBlocks {
	return ReasoningBlocks{Empty: r.Empty + more.Empty, WithText: r.WithText + more.WithText}
}

// Fields reads the structural facts out of the line. The line is
// decoded once for all of them, so a reader that needs more than one
// keeps the value rather than asking again.
func (l Line) Fields() Fields {
	var doc struct {
		Type            string      `json:"type"`
		Subtype         string      `json:"subtype"`
		LaunchSurface   string      `json:"entrypoint"`
		RelocatedCwd    string      `json:"relocatedCwd"`
		AgentID         string      `json:"agentId"`
		ToolUseID       string      `json:"toolUseId"`
		ParentAgentID   string      `json:"parentAgentId"`
		ParentSessionID string      `json:"parentSessionId"`
		Sidechain       bool        `json:"isSidechain"`
		BlockIndex      wholeNumber `json:"apiBlockIndex"`
		Attachment      struct {
			Type string `json:"type"`
		} `json:"attachment"`
		Message struct {
			ID      string `json:"id"`
			Content []struct {
				Reasoning reasoningText `json:"thinking"`
			} `json:"content"`
		} `json:"message"`
	}
	// A field of the wrong shape stops that field and no other: the
	// decoder keeps going and reports the mismatch at the end, which is
	// the reading "absent" already means here.
	_ = json.Unmarshal(l.raw, &doc)
	f := Fields{
		Type:            doc.Type,
		Subtype:         doc.Subtype,
		LaunchSurface:   doc.LaunchSurface,
		RelocatedCwd:    doc.RelocatedCwd,
		MessageID:       doc.Message.ID,
		AttachmentType:  doc.Attachment.Type,
		AgentID:         doc.AgentID,
		ToolUseID:       doc.ToolUseID,
		ParentAgentID:   doc.ParentAgentID,
		ParentSessionID: doc.ParentSessionID,
		Sidechain:       doc.Sidechain,
	}
	f.BlockIndex, f.HasBlockIndex = doc.BlockIndex.value, doc.BlockIndex.found
	for _, block := range doc.Message.Content {
		switch block.Reasoning {
		case emptyText:
			f.Reasoning.Empty++
		case someText:
			f.Reasoning.WithText++
		case noText:
		}
	}
	return f
}

// reasoningText reads a JSON string for one fact about it: that it is
// there, and whether it holds nothing or something. The text of a
// string that holds something is never copied out, so a field read
// this way stays unread. A value of any other shape reads as noText,
// which is the reading "absent" has here, and never fails the decode.
type reasoningText int

const (
	noText reasoningText = iota
	emptyText
	someText
)

func (r *reasoningText) UnmarshalJSON(b []byte) error {
	switch {
	case string(b) == `""`:
		*r = emptyText
	case len(b) > 2 && b[0] == '"':
		*r = someText
	default:
		*r = noText
	}
	return nil
}

// wholeNumber reads a JSON number that counts. A value of any other
// shape — a fraction, a number in quotes, a null — leaves it unset,
// which is the reading "absent" has here, and never fails the decode.
type wholeNumber struct {
	value int
	found bool
}

func (n *wholeNumber) UnmarshalJSON(b []byte) error {
	if value, err := strconv.Atoi(string(b)); err == nil {
		n.value, n.found = value, true
	}
	return nil
}
