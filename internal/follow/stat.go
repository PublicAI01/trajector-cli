package follow

import "os"

// Stat is what the reader observes about a registered file before
// deciding how to continue.
type Stat struct {
	Exists bool
	Inode  uint64
	Size   int64
}

// StatFile observes path. A path that does not exist is an observation,
// not a failure. Inode is 0 where the platform has no stable file
// identity to report; a cursor compares inodes only when both sides
// know theirs.
func StatFile(path string) (Stat, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Stat{}, nil
	}
	if err != nil {
		return Stat{}, err
	}
	return Stat{Exists: true, Inode: inodeOf(info), Size: info.Size()}, nil
}

// Reaction is how reading must continue after observing a file.
type Reaction int

const (
	// Continue: read on from Offset.
	Continue Reaction = iota
	// Rewrite: the file is not the one the cursor describes. The
	// reader starts over from byte 0; ids already in MessageIDs are
	// not consumed again; NextSegment continues from its stored value
	// and is never reset.
	Rewrite
	// Vanished: the file is gone, and the cursor is void.
	Vanished
)

// React decides how reading of f continues given st. An inode of 0 on
// either side is unknown and takes no part in the decision.
func (f File) React(st Stat) Reaction {
	switch {
	case !st.Exists:
		return Vanished
	case f.Inode != 0 && st.Inode != 0 && st.Inode != f.Inode:
		return Rewrite
	case st.Size < f.Size:
		return Rewrite
	default:
		return Continue
	}
}

// Behind is how many bytes of st lie past f's cursor when reading
// continues, and 0 otherwise.
func (f File) Behind(st Stat) int64 {
	if f.React(st) != Continue {
		return 0
	}
	return st.Size - f.Offset
}
