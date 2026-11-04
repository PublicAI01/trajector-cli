package consent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/PublicAI01/trajector-cli/internal/consent"
)

func TestWindowsProjectIdentityIgnoresCaseSlashesAndShortNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Project with spaces")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := consent.CanonicalRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	variants := []string{strings.ToLower(dir), filepath.ToSlash(dir), filepath.Join(dir, ".")}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 32768)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	variants = append(variants, windows.UTF16ToString(buf[:n]))
	for _, variant := range variants {
		got, err := consent.CanonicalRoot(variant)
		if err != nil || got != canonical || consent.ProjectIDHash(got) != consent.ProjectIDHash(canonical) {
			t.Fatalf("CanonicalRoot(%q) = %q, %v; want %q", variant, got, err, canonical)
		}
	}
}
