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
	// The Claude settings files no longer belong here either: this
	// codebase replaces them by rename through editFile's fsatomic.Update,
	// so the premise this entry rested on — that only programs outside
	// this codebase write them, and plainly — was never true of
	// trajector's own writes. The injected hooks run trajector on every
	// prompt, so a read racing one of our renames is the normal case.
	// The project .gitignore left this list on 2026-08-15 for the same
	// reason. 2026-09-13.
	//
	// The user config file has no writer in this codebase.
	"internal/cli/cli.go": true,
	// Lock files are created exclusively and removed, never replaced.
	"internal/fsatomic/fsatomic.go": true,
	// /proc/version is provided by the kernel.
	"internal/report/doctor.go": true,
	// Session files belong to Claude Code, which writes them plainly;
	// nothing in this codebase writes them at all.
	"internal/follow/read.go": true,
	// The log of what reading noticed is only ever appended to, by the
	// one package that owns it, and the harness reads that same file.
	"internal/drift/log.go":                   true,
	"internal/harness/proxytest/readerlog.go": true,
	// This module's own sources sit in the checkout, where nothing in
	// this codebase writes them.
	"internal/harness/repotest/repotest.go": true,
	// The shared contract fixtures are read-only test data kept outside
	// this repository; nothing here writes them.
	"internal/harness/conformance/conformance.go": true,
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
