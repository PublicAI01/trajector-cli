// Package tokenstore persists the device pairing secret. It prefers
// the operating system keyring and falls back to an owner-only file
// store when the keyring is unavailable, so headless hosts work
// without manual setup. Which backend holds the secret is decided in
// here: keyring availability is a platform fact, not a caller concern.
package tokenstore

import (
	"errors"
	"fmt"
	"os"
	"regexp"
)

// errNotFound reports that no secret is stored under the given name.
var errNotFound = errors.New("tokenstore: secret not found")

// BackendEnv set to "file" keeps trajector out of the OS keyring
// entirely. The keyring is a system-global resource, so test isolation
// and headless automation need a way to never touch it.
const BackendEnv = "TRAJECTOR_TOKEN_STORE"

// deviceTokenName is the single secret this store holds. Login writes
// it, logout clears it, and the uploader reads it.
const deviceTokenName = "device"

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

// Store persists this device's pairing secret.
type Store struct {
	backend backend
}

type backend interface {
	Save(name string, secret []byte) error
	Load(name string) ([]byte, error)
	Delete(name string) error
}

// Open returns the device's token store, with the file fallback rooted
// at dir.
func Open(dir string) *Store {
	if os.Getenv(BackendEnv) == "file" {
		return Files(dir)
	}
	return &Store{backend: &fallbackStore{primary: keyringStore{}, fallback: fileStore{dir: dir}}}
}

// Files is the file-only store rooted at dir, for hosts kept out of
// the keyring and for tests that must never touch the user's real one.
func Files(dir string) *Store { return &Store{backend: fileStore{dir: dir}} }

// DeviceToken reads the device pairing token. A missing or empty token
// is the signed-out state, not an error.
func (s *Store) DeviceToken() (token string, ok bool, err error) {
	secret, err := s.backend.Load(deviceTokenName)
	if errors.Is(err, errNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(secret) == 0 {
		return "", false, nil
	}
	return string(secret), true, nil
}

// SetDeviceToken stores the device pairing token.
func (s *Store) SetDeviceToken(token string) error {
	return s.backend.Save(deviceTokenName, []byte(token))
}

// ClearDeviceToken removes the device pairing token. A token that is
// already gone means signed out either way, so it clears cleanly.
func (s *Store) ClearDeviceToken() error {
	err := s.backend.Delete(deviceTokenName)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

// fallbackStore reads from both backends because a secret may have been
// saved while the keyring was in a different state (for example a
// headless session on a desktop machine).
type fallbackStore struct {
	primary  backend
	fallback backend
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
