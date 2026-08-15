package fsatomic_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/repotest"
)

// plainReadFiles lists the non-test files whose os.ReadFile calls are
// all aimed at paths WriteFile never replaces, so the Windows rename
// collision that fsatomic.ReadFile absorbs cannot occur there.
var plainReadFiles = map[string]bool{
	// The user's Claude settings belong to programs outside this
	// codebase, which replace them plainly. The project .gitignore no
	// longer belongs here: EnsureGitIgnored replaces it by rename since
	// 2026-08-15, so every reader of it goes through ReadFile.
	"internal/claudesettings/claudesettings.go": true,
	// The user config file has no writer in this codebase.
	"internal/cli/cli.go": true,
	// Lock files are created exclusively and removed, never replaced.
	"internal/fsatomic/fsatomic.go": true,
	// /proc/version is provided by the kernel.
	"internal/lifecycle/doctor.go": true,
	// This module's own sources sit in the checkout, where nothing in
	// this codebase writes them.
	"internal/harness/repotest/repotest.go": true,
}

// Reads of a path that WriteFile replaces must come through ReadFile:
// an os.ReadFile crossing the replacing rename fails spuriously on
// Windows. Every other non-test os.ReadFile must sit in a file listed
// above as reading only never-replaced paths.
func TestReadsOfReplacedPathsComeThroughReadFile(t *testing.T) {
	// The pattern is assembled at run time so this table is not a finding.
	pattern := "os." + "ReadFile("

	var hits []string
	listed := map[string]bool{}
	repotest.Lines(t, func(l repotest.Line) {
		if l.File.Test() || !strings.Contains(l.Text, pattern) {
			return
		}
		if plainReadFiles[l.File.Rel] {
			listed[l.File.Rel] = true
			return
		}
		hits = append(hits, l.String())
	})
	if len(hits) > 0 {
		t.Errorf("reads of paths that fsatomic replaces must use fsatomic.ReadFile; use it, or list the file in plainReadFiles if every path it reads is never replaced:\n%s",
			strings.Join(hits, "\n"))
	}
	for rel := range plainReadFiles {
		if !listed[rel] {
			t.Errorf("plainReadFiles lists %s, which no longer reads any file plainly; remove the entry", rel)
		}
	}
}
