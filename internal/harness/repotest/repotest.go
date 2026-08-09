// Package repotest reads this module's own Go sources, for tests that
// lock a rule no compiler checks: a construction that must appear in
// exactly one place, a read that must go through one seam, a client
// every request must ride. One walk decides what counts as this
// module's source, so such a test states its rule and nothing else.
package repotest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// File is one Go source file of this module.
type File struct {
	// Path is where the file sits on disk.
	Path string
	// Rel is its slash-separated path from the module root, the
	// spelling a failure names it by.
	Rel string
}

// Test reports whether the file holds tests rather than the code they
// drive.
func (f File) Test() bool { return strings.HasSuffix(f.Rel, "_test.go") }

// Line is one line of one source file, in the spelling a failure names
// it by.
type Line struct {
	File File
	// N is the line's 1-based number.
	N    int
	Text string
}

func (l Line) String() string {
	return fmt.Sprintf("%s:%d: %s", l.File.Rel, l.N, strings.TrimSpace(l.Text))
}

// GoFiles lists every Go source file in this module. Dot directories
// and testdata trees hold no source of this module and are skipped.
func GoFiles(t *testing.T) []File {
	t.Helper()
	root := moduleRoot(t)
	var files []File
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, File{Path: path, Rel: filepath.ToSlash(rel)})
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		t.Fatal(err)
	}
	return files
}

// Lines calls visit for every line of every file GoFiles lists.
func Lines(t *testing.T, visit func(Line)) {
	t.Helper()
	for _, f := range GoFiles(t) {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		for i, text := range strings.Split(string(data), "\n") {
			visit(Line{File: f, N: i + 1, Text: text})
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
