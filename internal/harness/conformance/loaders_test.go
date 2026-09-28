package conformance_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
)

// loaders names each loader by the directory under the fixture root it
// reads, and reports how many cases it read.
var loaders = []struct {
	dir  string
	load func(string) (int, error)
}{
	{"batches", func(dir string) (int, error) {
		c, err := conformance.Load(dir)
		return len(c), err
	}},
	{"segments", func(dir string) (int, error) {
		c, err := conformance.LoadSegments(dir)
		return len(c), err
	}},
	{"redaction", func(dir string) (int, error) {
		c, err := conformance.LoadRedaction(dir)
		return len(c), err
	}},
	{"blocks", func(dir string) (int, error) {
		c, err := conformance.LoadBlocks(dir)
		return len(c), err
	}},
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEveryLoaderFailsOnACaseDirectoryWithoutItsCaseFile(t *testing.T) {
	for _, l := range loaders {
		t.Run(l.dir, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, l.dir, "99-misspelt", "case.jsn"), `{"name":"99-misspelt"}`)
			if n, err := l.load(dir); err == nil {
				t.Fatalf("load = %d cases and no error: a case directory without its case file was passed over", n)
			}
		})
	}
}

func TestEveryLoaderLeavesFilesBesideTheCaseDirectoriesAlone(t *testing.T) {
	for _, l := range loaders {
		t.Run(l.dir, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, l.dir, "README.md"), "notes")
			if n, err := l.load(dir); err != nil || n != 0 {
				t.Fatalf("load = %d cases, %v; want none and no error", n, err)
			}
		})
	}
}
