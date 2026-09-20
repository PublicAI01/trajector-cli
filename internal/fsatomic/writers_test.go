package fsatomic_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/repotest"
)

// plainWriteFiles lists the non-test files outside fsatomic that name a
// plain write, rename or remove, each with the reason the file it
// touches is one this codebase alone owns at that moment.
var plainWriteFiles = map[string]bool{
	// The proxy publishes its admin token through fsatomic and only ever
	// unlinks after that: the stale publications of earlier instances,
	// and its own on the way out. A reader that finds one gone reads it
	// as no proxy to talk to.
	"internal/apiproxy/apiproxy.go": true,
	// Unregistering drops the whole registry file a project owns; a
	// project with no registry file is an unregistered project.
	"internal/follow/follow.go": true,
	// Rolling a project file back to "it did not exist" is an unlink.
	// The other half of the same rollback, restoring content, goes
	// through fsatomic.
	"internal/lifecycle/snapshot.go": true,
	// Installing a binary is the rename of a staged sibling this package
	// created and fsatomic wrote, and on Windows the running image must
	// be renamed aside first. Both steps move a whole file that only
	// this package names, and the cleanup unlinks the same siblings.
	"internal/selfupdate/install.go":         true,
	"internal/selfupdate/install_other.go":   true,
	"internal/selfupdate/install_windows.go": true,
	// A spool record is written once and afterwards only deleted, so a
	// walk that meets the gap another handle's delete left treats it as
	// vanished. The sweeps unlink only this spool's own aged temps and
	// the probe that tests whether the directory is writable.
	"internal/spool/spool.go":   true,
	"internal/spool/read.go":    true,
	"internal/spool/records.go": true,
	"internal/spool/held.go":    true,
	// Deleting a stored secret unlinks it; a secret is replaced by
	// writing the new one, never by editing the file in place.
	"internal/tokenstore/file.go": true,
	// A quarantined record leaves the store by being unlinked once it is
	// read back or discarded, and the sweep unlinks only aged temps.
	"internal/upload/rejected.go": true,
	// Clearing the pending marker is unlinking it: its absence is the
	// state "nothing is pending", which every reader already reads.
	"internal/upload/state.go": true,
}

// A path another process can read while it is being replaced must be
// written through fsatomic, which stages a sibling, flushes it, and
// renames it into place. Production code outside fsatomic may name a
// plain write, rename or remove only where the file it touches is one
// this codebase alone owns at that moment, and every such file is
// listed above with that reason.
//
// Tests and internal/harness are not the subject. A test owns the tree
// it writes and no second process reads it, and the harness stages
// fixtures deliberately — putting files where production code will find
// them is what it is for. The rule is about what ships.
func TestWritesOfSharedPathsGoThroughFsatomic(t *testing.T) {
	// The patterns are assembled at run time so this table is not a finding.
	patterns := []string{"WriteFile(", "Rename(", "Remove("}
	for i, name := range patterns {
		patterns[i] = "os." + name
	}

	var hits []string
	listed := map[string]bool{}
	repotest.Lines(t, func(l repotest.Line) {
		if l.File.Test() ||
			strings.HasPrefix(l.File.Rel, "internal/harness/") ||
			strings.HasPrefix(l.File.Rel, "internal/fsatomic/") {
			return
		}
		if !slices.ContainsFunc(patterns, func(p string) bool { return strings.Contains(l.Text, p) }) {
			return
		}
		if plainWriteFiles[l.File.Rel] {
			listed[l.File.Rel] = true
			return
		}
		hits = append(hits, l.String())
	})
	if len(hits) > 0 {
		t.Errorf("a file another process can read while it is replaced must be written through fsatomic; use it, or list the file in plainWriteFiles with the reason nothing else names the file it touches:\n%s",
			strings.Join(hits, "\n"))
	}
	for _, rel := range slices.Sorted(maps.Keys(plainWriteFiles)) {
		if !listed[rel] {
			t.Errorf("plainWriteFiles lists %s, which no longer writes, renames or removes any file itself; remove the entry", rel)
		}
	}
}
