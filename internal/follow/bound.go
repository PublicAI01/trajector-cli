package follow

import "github.com/PublicAI01/trajector-cli/internal/envelope"

// segmentBound is where the segment one read handed to the store ends.
// It is written into the entry before the store is asked, and taken
// out in the write that moves the cursor past the segment. While it
// stands and the cursor has not moved, the store may hold the segment
// already, and the store keeps
// the first segment it is given under an index: a later read from the
// same cursor that held more lines would lose them. So the next read
// of the entry takes the same bytes again and nothing past them, and
// what the file gained since is the segment after it.
//
// It is local state like the cursor, and holds positions only.
type segmentBound struct {
	// Offset is the cursor offset the read started from.
	Offset int64 `json:"offset"`
	// Segment is the index the segment was given.
	Segment int `json:"segment"`
	// End is the cursor offset the read reached.
	End int64 `json:"end"`
	// Inode is the file the read observed, and 0 when unknown.
	Inode uint64 `json:"inode,omitzero"`
	// Rewrite records that the read started over from byte 0.
	Rewrite bool `json:"rewrite,omitzero"`
}

// boundOf is the bound of the segment res holds, read from f as st
// observed it. ok is false for a read that produced no segment: there
// is nothing the store could hold under the cursor's next index.
func boundOf(f File, st Stat, res ReadResult) (b segmentBound, ok bool) {
	if len(res.Segments) == 0 {
		return segmentBound{}, false
	}
	return segmentBound{
		Offset:  f.Offset,
		Segment: res.Segments[0].SegmentIndex,
		End:     res.File.Offset,
		Inode:   st.Inode,
		Rewrite: res.Reaction == Rewrite,
	}, true
}

// standsFor reports whether b was left by a read from the cursor f
// holds now. A cursor that moved since has a different offset, or has
// passed the segment's index.
func (b segmentBound) standsFor(f File) bool {
	return b.Offset == f.Offset && b.Segment >= f.NextSegment
}

// holds reports whether the file st observed still holds the bytes b
// bounds. An inode of 0 on either side is unknown and takes no part.
func (b segmentBound) holds(st Stat) bool {
	return st.Exists && st.Size >= b.End && (b.Inode == 0 || st.Inode == 0 || st.Inode == b.Inode)
}

// readAgain reads the bytes b bounds once more, as the read that left b
// did: from the same start, under the same index, and to b.End however
// much the file gained since. That read kept a segment, so the session
// was inside when it judged the lines, and that judgment stands.
func (b segmentBound) readAgain(f File, st Stat, capture envelope.Capture, opts ReadOptions) (ReadResult, error) {
	f.NextSegment = b.Segment
	res := ReadResult{File: f, Reaction: Continue}
	if b.Rewrite {
		res.Reaction = Rewrite
	}
	return readLines(res, Stat{Exists: true, Inode: st.Inode, Size: b.End}, capture, opts, false)
}
