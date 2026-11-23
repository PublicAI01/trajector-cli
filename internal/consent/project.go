package consent

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// CanonicalRoot normalizes a project directory to the form used for
// identity: absolute, symlinks resolved, cleaned. Every caller that
// derives a project hash must go through this so the same project
// always maps to the same hash regardless of how its path was spelled.
func CanonicalRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}

// ProjectPosition reports where dir sits relative to a canonical
// project root, with forward slashes and "" at the root itself, and
// false when dir is outside the root or cannot be resolved. The two
// answers are kept apart because "at the root" and "not in the
// project" are the same empty string and opposite decisions.
//
// A project covers its root and every directory below it. dir is
// resolved the way a root is, so a link below the root that leads out
// of it is outside, and a directory that no longer exists is placed by
// the path as written.
func ProjectPosition(canonicalRoot, dir string) (subpath string, inside bool) {
	if dir == "" {
		return "", false
	}
	canonical, err := CanonicalRoot(dir)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(canonicalRoot, canonical)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	return filepath.ToSlash(rel), true
}

// ProjectIDHash derives the stable project identifier from a canonical
// root path. The hash — never the plaintext path — is what leaves the
// machine inside rawcall envelopes.
func ProjectIDHash(canonicalRoot string) string {
	sum := sha256.Sum256([]byte(canonicalRoot))
	return hex.EncodeToString(sum[:])
}
