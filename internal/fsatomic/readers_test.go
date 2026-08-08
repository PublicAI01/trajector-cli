package fsatomic_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// plainReadFiles lists the non-test files whose os.ReadFile calls are
// all aimed at paths WriteFile never replaces, so the Windows rename
// collision that fsatomic.ReadFile absorbs cannot occur there.
var plainReadFiles = map[string]bool{
	// The user's Claude settings and .gitignore belong to programs
	// outside this codebase, which replace them plainly.
	"internal/claudesettings/claudesettings.go": true,
	"internal/claudesettings/gitignore.go":      true,
	"internal/lifecycle/project.go":             true,
	// The user config file has no writer in this codebase.
	"internal/cli/cli.go": true,
	// Lock files are created exclusively and removed, never replaced.
	"internal/fsatomic/fsatomic.go": true,
	// /proc/version is provided by the kernel.
	"internal/lifecycle/doctor.go": true,
}

// Reads of a path that WriteFile replaces must come through ReadFile:
// an os.ReadFile crossing the replacing rename fails spuriously on
// Windows. Every other non-test os.ReadFile must sit in a file listed
// above as reading only never-replaced paths.
func TestReadsOfReplacedPathsComeThroughReadFile(t *testing.T) {
	// The pattern is assembled at run time so this table is not a finding.
	pattern := "os." + "ReadFile("

	root := moduleRoot(t)
	var hits []string
	listed := map[string]bool{}
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, pattern) {
				continue
			}
			if plainReadFiles[rel] {
				listed[rel] = true
				continue
			}
			hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
		}
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		t.Fatal(err)
	}
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

// moduleRoot walks up from this source file to the directory holding
// go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller position for this source file")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", file)
		}
		dir = parent
	}
}
