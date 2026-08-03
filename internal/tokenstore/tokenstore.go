// Package tokenstore persists small secrets such as device tokens. It
// prefers the operating system keyring and falls back to an owner-only
// file store when the keyring is unavailable, so headless hosts work
// without manual setup.
package tokenstore

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrNotFound is returned when no secret is stored under the given name.
var ErrNotFound = errors.New("tokenstore: secret not found")

// Store persists named secrets.
type Store interface {
	Save(name string, secret []byte) error
	Load(name string) ([]byte, error)
	Delete(name string) error
}

// keyringService namespaces trajector entries in the OS keyring.
const keyringService = "trajector"

// validName guards against path traversal in the file backend; keyring
// entries use the same rule so a secret stays addressable when the
// active backend changes between runs.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func checkName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("tokenstore: invalid secret name %q", name)
	}
	return nil
}

// Open returns a store that keeps secrets in primary and transparently
// falls back to an owner-only file store rooted at dir whenever primary
// is unavailable, so a headless host works without manual setup.
func Open(dir string, primary Store) Store {
	return &fallbackStore{primary: primary, fallback: fileStore{dir: dir}}
}

// OSKeyring is the operating system's own credential store: Keychain,
// Secret Service, or Windows Credential Manager.
func OSKeyring() Store { return keyringStore{} }

// Files is the owner-only file store rooted at dir, for hosts with no
// keyring and for callers that must not touch the user's real one.
func Files(dir string) Store { return fileStore{dir: dir} }

// fallbackStore reads from both backends because a secret may have been
// saved while the keyring was in a different state (for example a
// headless session on a desktop machine).
type fallbackStore struct {
	primary  Store
	fallback Store
}

func (s *fallbackStore) Save(name string, secret []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	if err := s.primary.Save(name, secret); err == nil {
		// Remove any stale fallback copy so later loads cannot return
		// an outdated secret.
		_ = s.fallback.Delete(name)
		return nil
	}
	return s.fallback.Save(name, secret)
}

func (s *fallbackStore) Load(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	secret, err := s.primary.Load(name)
	if err == nil {
		return secret, nil
	}
	return s.fallback.Load(name)
}

func (s *fallbackStore) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	primaryErr := s.primary.Delete(name)
	fallbackErr := s.fallback.Delete(name)
	if primaryErr == nil || fallbackErr == nil {
		return nil
	}
	return fallbackErr
}
