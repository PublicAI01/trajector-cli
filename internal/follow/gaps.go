package follow

// Ambiguity is a directory whose session files were not registered
// because Claude Code stores them under a name that at least one
// other real directory shares, so they cannot be attributed to one
// working directory.
type Ambiguity struct {
	// Dir is the directory under the project root that was skipped.
	Dir string `json:"dir"`
	// Name is the stored name Dir shares with the other directories.
	Name string `json:"name"`
	// Matches lists every real directory that stores under Name,
	// sorted. Dir is among them when it was found.
	Matches []string `json:"matches"`
}

// Gaps is what the search for a project's earlier session files
// could not cover. It is recorded with the registry so the outcome
// of the one search that ran can be shown afterwards without running
// another. A search states its outcome in this form directly: there
// is one shape for what was left uncovered, wherever it is held.
type Gaps struct {
	// Truncated reports that the project's directory tree was larger
	// than the search visits, so directories past its limit were not
	// looked at.
	Truncated bool `json:"truncated,omitempty"`
	// Ambiguous lists the directories skipped for a shared name.
	Ambiguous []Ambiguity `json:"ambiguous,omitempty"`
	// Unreadable lists the directories whose entries could not be
	// listed; directories below them were not looked at.
	Unreadable []string `json:"unreadable,omitempty"`
}

// Any reports whether anything was left uncovered.
func (g Gaps) Any() bool {
	return g.Truncated || len(g.Ambiguous) > 0 || len(g.Unreadable) > 0
}
