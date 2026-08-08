package proxytest_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Test servers listen on ephemeral ports, so a pooled connection held by
// the process-wide client can resurface at an address a later test's
// server owns and fail with an EOF unrelated to that test. Every request
// a test sends itself must ride a client scoped to one test: Client, an
// Env helper, or a fake server's own client.
func TestTestRequestsNeverRideTheProcessWideConnectionPool(t *testing.T) {
	// Patterns are assembled at run time so this table is not a finding.
	shared := []string{"DefaultClient", "Get(", "Post(", "PostForm(", "Head("}
	for i, name := range shared {
		shared[i] = "http." + name
	}
	handRolled := "http." + "Client{"

	root := moduleRoot(t)
	harnessDir := filepath.Join("internal", "harness") + string(filepath.Separator)
	var hits []string
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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		isTest := strings.HasSuffix(path, "_test.go")
		if !isTest && !strings.HasPrefix(rel, harnessDir) {
			return nil
		}
		banned := shared
		if isTest {
			// Hand-rolling a client in a test either shares the
			// process-wide transport or leaves a pool nothing drops
			// with the test; only the harness builds clients.
			banned = append(append([]string(nil), shared...), handRolled)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, pattern := range banned {
				if strings.Contains(line, pattern) {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(line)))
				}
			}
		}
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Errorf("requests must go through a client scoped to the test (proxytest.Client, an Env helper, or a fake server's client):\n%s",
			strings.Join(hits, "\n"))
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
