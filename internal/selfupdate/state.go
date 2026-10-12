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
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/semver"
)

// State is this machine's upgrade bookkeeping, as the last upgrade that
// held the lock left it. Only a lock holder writes it; anyone may read
// it with LoadState.
type State struct {
	// CheckedAt is when the background check last asked for the newest
	// release, and Result what it came to. The background check writes
	// both. A manual upgrade writes neither, but drops a Result it made
	// untrue: any, once it installed a release, and a failure, once it
	// ended without one.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Result    Result    `json:"result,omitempty"`
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
	// FinishPending is a version whose install was committed but whose
	// build has not yet said it finished it. It is written before the
	// commit and cleared once that build's finish succeeds, so a finish
	// that failed, or a machine that stopped before it ran, leaves it
	// here for that build to run again.
	FinishPending string `json:"finish_pending,omitempty"`
	// ToldManaged is the newest release the background check has
	// reported for an installation a package manager owns. That
	// release is not reported again: the user was told once how to
	// install it.
	ToldManaged string `json:"told_managed,omitempty"`
}

// Result is what the last background check came to.
type Result string

// The results the background check records. A build may read one it
// does not know, written by a newer build, and must still say when the
// check ran.
const (
	// ResultUpgraded installed a newer release; Installed names it.
	ResultUpgraded Result = "upgraded"
	// ResultNewest found this build already the newest release.
	ResultNewest Result = "newest"
	// ResultManaged found a newer release for an installation a package
	// manager owns, which trajector does not replace.
	ResultManaged Result = "managed"
	// ResultFailed could not finish; Error says why.
	ResultFailed Result = "failed"
)

// ManagerOf names the package manager that owns the binary at
// execPath, or empty when none does. It is what a surface names as the
// one that upgrades a managed installation.
func ManagerOf(execPath string) Manager { return packageManager(execPath) }

// stateMode keeps the state file private to the user, like everything
// else trajector stores.
const stateMode = 0o600

// LoadState reads the state file at path. A file that is not there is a
// machine that has not upgraded yet, which is the zero State.
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

// record writes how one manual run under the lock ended: the version
// it installed, a release that did not start, and its error or none.
// Entries at or below the version installed when the run ends are
// dropped from Unusable, and so is a background result the run made
// untrue (State.Result).
func record(in Installation, out Outcome, runErr error) error {
	installed := ""
	if out.Kind == Upgraded {
		installed = out.To
	}
	return updateState(in.State, func(s *State) {
		s.noteRun(in, installed, runErr)
		if installed != "" || (runErr == nil && s.Result == ResultFailed) {
			s.Result = ""
		}
	})
}

// noteRun is how one run under the lock ended, as record writes it:
// installed is the version it installed, or empty when it installed
// none, and runErr its error or nil.
func (s *State) noteRun(in Installation, installed string, runErr error) {
	current := in.Version
	if putBack, ok := errors.AsType[*PutBackError](runErr); ok {
		current = putBack.Version
	}
	if installed != "" {
		s.Installed = installed
		current = installed
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
