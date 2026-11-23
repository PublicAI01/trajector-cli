package consent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/consent"
)

func TestProjectPosition(t *testing.T) {
	base, err := consent.CanonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "project")
	elsewhere := filepath.Join(base, "elsewhere")
	for _, dir := range []string{filepath.Join(root, "apps", "api"), elsewhere} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name        string
		dir         string
		wantSubpath string
		wantInside  bool
	}{
		{name: "the root itself", dir: root, wantInside: true},
		{name: "a directory below the root", dir: filepath.Join(root, "apps", "api"), wantSubpath: "apps/api", wantInside: true},
		{name: "a directory below the root that does not exist", dir: filepath.Join(root, "gone", "deeper"), wantSubpath: "gone/deeper", wantInside: true},
		{name: "a path below the root spelled through its parent", dir: filepath.Join(root, "apps", "..", "apps"), wantSubpath: "apps", wantInside: true},
		{name: "a directory beside the root", dir: elsewhere},
		{name: "a directory whose name starts with the root's", dir: root + "-worktree"},
		{name: "the parent of the root", dir: base},
		{name: "no directory", dir: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subpath, inside := consent.ProjectPosition(root, tt.dir)
			if subpath != tt.wantSubpath || inside != tt.wantInside {
				t.Errorf("ProjectPosition(%q) = %q, %v; want %q, %v", tt.dir, subpath, inside, tt.wantSubpath, tt.wantInside)
			}
		})
	}
}

func TestProjectPosition_ALinkBelowTheRootThatLeadsOutOfItIsOutside(t *testing.T) {
	base, err := consent.CanonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "project")
	elsewhere := filepath.Join(base, "elsewhere")
	for _, dir := range []string{root, elsewhere} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "vendor")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Skipf("this system does not let the test make a link: %v", err)
	}

	if subpath, inside := consent.ProjectPosition(root, link); inside {
		t.Errorf("ProjectPosition(%q) = %q, inside; want outside: the link leads to %q", link, subpath, elsewhere)
	}
}
