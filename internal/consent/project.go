package consent

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
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

// ProjectIDHash derives the stable project identifier from a canonical
// root path. The hash — never the plaintext path — is what leaves the
// machine inside rawcall envelopes.
func ProjectIDHash(canonicalRoot string) string {
	sum := sha256.Sum256([]byte(canonicalRoot))
	return hex.EncodeToString(sum[:])
}
