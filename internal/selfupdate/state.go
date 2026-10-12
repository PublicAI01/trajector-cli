package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/semver"
)

// State is this machine's upgrade bookkeeping, as the last upgrade that
// held the lock left it. Only a lock holder writes it; anyone may read
// it with LoadState.
type State struct {
	// Installed is the version the last upgrade installed. It is written
	// when the install is committed, so it names what is on disk rather
	// than what some running process announces.
	Installed string `json:"installed,omitempty"`
	// Unusable maps each release that does not start on this machine to
	// what running it did instead. A version at or below the installed
	// one is dropped: the machine is past it.
	Unusable map[string]string `json:"unusable,omitempty"`
	// Error is how the last run failed, on one line with no URL query,
	// or empty when it did not fail.
	Error string `json:"error,omitempty"`
}

// stateMode keeps the state file private to the user, like everything
// else trajector stores.
const stateMode = 0o600

// LoadState reads the state file at path. A file that is not there is a
// machine that has not upgraded yet, which is the zero State.
//
//exportsurface:keep test support: tests read the state file back with the reader that sits beside the writer
func LoadState(path string) (State, error) {
	data, err := fsatomic.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("selfupdate: reading %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("selfupdate: reading %s: %w", path, err)
	}
	return s, nil
}

// record writes how one run under the lock ended: the version it
// installed, a release that did not start, and its error or none.
// Entries at or below the version installed when the run ends are
// dropped from Unusable.
func record(in Installation, out Outcome, runErr error) error {
	return updateState(in.State, func(s *State) {
		current := in.Version
		if out.Kind == Upgraded {
			s.Installed = out.To
			current = out.To
		}
		if unusable, ok := errors.AsType[*UnusableError](runErr); ok {
			if s.Unusable == nil {
				s.Unusable = map[string]string{}
			}
			s.Unusable[unusable.Version] = unusable.Reason
		}
		maps.DeleteFunc(s.Unusable, func(version, _ string) bool {
			order, ok := semver.Compare(version, current)
			return ok && order <= 0
		})
		s.Error = ""
		if runErr != nil {
			s.Error = errorLine(runErr)
		}
	})
}

// updateState rewrites the state file through change.
//
// Fields this build does not know are carried over untouched: a newer
// build's fields survive this one's write, so a build that runs after
// a newer one has used the file — the build that is still running while
// it installs its successor, or one put back by hand — does not erase
// them. A file this build cannot parse is replaced rather than kept: it
// is a note no reader can use, and refusing to write over it would
// stop the bookkeeping for good.
func updateState(path string, change func(*State)) error {
	return fsatomic.Update(path, stateMode, func(old []byte) ([]byte, error) {
		var fields map[string]json.RawMessage
		var s State
		if json.Unmarshal(old, &fields) != nil || json.Unmarshal(old, &s) != nil {
			fields, s = nil, State{}
		}
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
		change(&s)

		known, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		set := map[string]json.RawMessage{}
		if err := json.Unmarshal(known, &set); err != nil {
			return nil, err
		}
		// A field this build knows and has emptied is gone from set; it
		// has to go from the file too.
		for _, name := range stateFields() {
			delete(fields, name)
		}
		maps.Copy(fields, set)
		return json.MarshalIndent(fields, "", "  ")
	})
}

// stateFields is every JSON name State writes, read off its tags so a
// field added to State is one this build knows without a second list.
var stateFields = sync.OnceValue(func() []string {
	t := reflect.TypeFor[State]()
	names := make([]string, 0, t.NumField())
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		names = append(names, name)
	}
	return names
})

// urlQuery matches the query or fragment of a URL inside an error. A
// release asset is served through redirects to signed URLs whose query
// is a credential for that download; it has no place in a file that
// status prints.
var urlQuery = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s?#"']*)[?#][^\s"']*`)

// errorLine is err as the state file keeps it: one line, with every URL
// cut at its query.
func errorLine(err error) string {
	line := strings.Join(strings.Fields(err.Error()), " ")
	return urlQuery.ReplaceAllString(line, "$1")
}
