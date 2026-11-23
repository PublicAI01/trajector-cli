package lifecycle_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// registerGone registers a session file of root's project whose file
// is gone from a directory that is still there.
func (e *env) registerGone(root, rel string) {
	e.t.Helper()
	path := e.putSessionFile(rel, firstLine)
	e.registerFile(root, path, "")
	if err := os.Remove(path); err != nil {
		e.t.Fatal(err)
	}
}

// brokenRegistryElsewhere enables another project whose registry does
// not parse, so neither a migration nor a prune can read it.
func (e *env) brokenRegistryElsewhere() {
	e.t.Helper()
	e.enabledElsewhere("tok-broken", proxytest.WithProxy)
	broken := filepath.Join(e.layout().FollowDir(), "hash-tok-broken.json")
	if err := os.WriteFile(broken, []byte(`{"version":2,"files":`), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func TestFinishUpgradeRemovesTheEntriesOfSessionFilesThatAreGone(t *testing.T) {
	e := newEnv(t)
	e.enableProject()
	root := e.canonicalRoot()
	kept := e.putSessionFile("-work-sample/kept.jsonl", firstLine)
	e.registerFile(root, kept, "")
	e.registerGone(root, "-work-sample/gone.jsonl")
	e.enabledElsewhere("tok-other", proxytest.WithProxy)
	otherKept := e.putSessionFile("-work-other/kept.jsonl", firstLine)
	otherGone := filepath.Join(filepath.Dir(otherKept), "gone.jsonl")
	e.sandbox.LeaveEarlierRegistry("hash-tok-other", proxytest.EarlierRegistry{Files: []proxytest.EarlierEntry{{Path: otherKept}, {Path: otherGone}}})
	e.brokenRegistryElsewhere()

	if err := e.machine().FinishUpgrade(t.Context()); err != nil {
		t.Errorf("FinishUpgrade() = %v, want a registry it could not prune left as it is", err)
	}

	if paths := e.sandbox.RegisteredPaths(proxytest.ProjectIDHash(root)); !slices.Equal(paths, []string{kept}) {
		t.Errorf("registered = %q, want only the session file that is there", paths)
	}
	if paths := e.sandbox.RegisteredPaths("hash-tok-other"); !slices.Equal(paths, []string{otherKept}) {
		t.Errorf("registered in the migrated project = %q, want only the session file that is there", paths)
	}
}

func TestDoctorRemovesTheEntriesOfSessionFilesThatAreGoneAndSaysNothingOfIt(t *testing.T) {
	e := newEnv(t)
	e.enableProject()
	e.injectWithProxy()
	root := e.canonicalRoot()
	kept := e.putSessionFile("-work-sample/kept.jsonl", firstLine)
	e.registerFile(root, kept, "")
	e.brokenRegistryElsewhere()
	e.doctor()
	e.registerGone(root, "-work-sample/gone.jsonl")
	e.stdout.Reset()

	_, first := e.doctor()

	if paths := e.sandbox.RegisteredPaths(proxytest.ProjectIDHash(root)); !slices.Equal(paths, []string{kept}) {
		t.Errorf("registered = %q, want only the session file that is there", paths)
	}
	e.stdout.Reset()
	if _, again := e.doctor(); again != first {
		t.Errorf("doctor that removed an entry:\n%s\nwant it to say what a run with nothing to remove says:\n%s", first, again)
	}
}
